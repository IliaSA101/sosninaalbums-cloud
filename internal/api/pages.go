package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sync"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/imgcheck"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// slots — слоты одновременных запросов: на владельца и (если total > 0) всего. Для загрузки страниц (design
// §4) и для JSON-запросов с телом: украденный токен не займёт все соединения, память и диск параллельными телами.
type slots struct {
	mu       sync.Mutex
	perOwner int
	total    int // 0 — без общего предела
	n        map[string]int
	sum      int
	// onAcquire и onRelease — только для тестов: вызываются под мьютексом после выдачи и после возврата слота,
	// чтобы тест дождался их на стороне сервера (ответ и запись тела клиентом этого не гарантируют). В работе nil.
	onAcquire, onRelease func()
}

func newSlots(perOwner, total int) *slots {
	return &slots{perOwner: perOwner, total: total, n: map[string]int{}}
}

// acquire занимает слот владельца; false — слоты заняты. release вызывается ровно один раз.
func (s *slots) acquire(owner string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[owner] >= s.perOwner || (s.total > 0 && s.sum >= s.total) {
		return nil, false
	}
	s.n[owner]++
	s.sum++
	if s.onAcquire != nil {
		s.onAcquire()
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sum--
		if s.n[owner]--; s.n[owner] <= 0 {
			delete(s.n, owner)
		}
		if s.onRelease != nil {
			s.onRelease()
		}
	}, true
}

const copyChunk = 32 << 10

// putPage — PUT /v1/albums/{id}/pages/{n}. Порядок (design §4): токен и id уже проверены → альбом по
// индексу (нет, чужой, истёк — 404) → слот загрузки (429) → номер (400) и белая страница (400) → Content-Type
// (400) → Content-Length > page_bytes (413), известный и ≠ bytes (409) — оба до чтения → запас места (413) →
// тело через MaxBytesReader(bytes) во временный файл с sha256 → размер и хэш (409) → содержимое: сигнатура,
// маркеры, размеры (400) → финал под мьютексом альбома → 204. Файл уже есть — успех, повтор безопасен.
func (h *Handler) putPage(c *call, nStr string) {
	pages, err := h.svc.CurrentPages(c.owner, c.id)
	if err != nil {
		c.svcError(err)
		return
	}
	release, ok := h.slots.acquire(c.owner)
	if !ok {
		c.w.Header().Set("Retry-After", retryAfter)
		c.fail(http.StatusTooManyRequests, albums.CodeRateLimited, "слишком много одновременных загрузок")
		return
	}
	defer release()

	n, err := albums.ParsePageN(nStr, len(pages))
	if err != nil {
		c.fail(http.StatusBadRequest, albums.CodeBadPage, "номер страницы вне книги")
		return
	}
	pg := pages[n-1]
	if pg.Blank {
		c.fail(http.StatusBadRequest, albums.CodeBadPage, "белая страница: файла у неё нет")
		return
	}
	if ct, _, _ := mime.ParseMediaType(c.r.Header.Get("Content-Type")); ct != pg.Type {
		c.fail(http.StatusBadRequest, albums.CodeBadPage, "Content-Type не как в манифесте")
		return
	}
	if cl := c.r.ContentLength; cl > h.limits.PageBytes {
		c.fail(http.StatusRequestEntityTooLarge, albums.CodeTooLarge, "страница больше page_bytes")
		return
	} else if cl >= 0 && cl != pg.Bytes {
		c.fail(http.StatusConflict, albums.CodeHashMismatch, "размер тела не как в манифесте")
		return
	}
	// Файл уже на месте — повторный PUT идемпотентен и места не требует (запас не проверяем, ответ 204).
	if !h.svc.PageExists(c.owner, c.id, pg.File) {
		if err := h.svc.CheckSpace(pg.Bytes); err != nil {
			c.svcError(err)
			return
		}
	}
	if pg.Type != albums.TypeJPEG { // WebP включают отдельно (001-6): разбора его заголовка пока нет
		c.fail(http.StatusBadRequest, albums.CodeBadPage, "этот тип страниц не принимается")
		return
	}
	h.receive(c, pg)
}

// receive читает тело страницы, проверяет и принимает её.
func (h *Handler) receive(c *call, pg albums.Page) {
	if !c.extendDeadlines(pg.Bytes) {
		return
	}
	part, err := h.svc.NewPart()
	if err != nil {
		c.svcError(err)
		return
	}
	defer func() { // после CommitPage ничего не делает; при любом отказе удаляет .part
		if err := part.Abort(); err != nil {
			h.log.Warn("временный файл не удалён", slog.String(logx.KeyOp, "page"), slog.Any(logx.KeyErr, err))
		}
	}()

	sum, size, rerr, werr := readBody(part, http.MaxBytesReader(c.w, c.r.Body, pg.Bytes))
	var mbe *http.MaxBytesError
	switch {
	case werr != nil:
		c.svcError(album.DiskError(werr))
		return
	case errors.As(rerr, &mbe):
		c.fail(http.StatusConflict, albums.CodeHashMismatch, "тело длиннее, чем в манифесте")
		return
	case rerr != nil:
		c.fail(http.StatusBadRequest, albums.CodeBadPage, "тело страницы не прочитано")
		return
	case size != pg.Bytes || sum != pg.SHA256:
		c.fail(http.StatusConflict, albums.CodeHashMismatch, "размер или sha256 не как в манифесте")
		return
	}
	if err := imgcheck.Check(part, size, pg.W, pg.H); err != nil {
		if errors.Is(err, imgcheck.ErrBadPage) {
			c.fail(http.StatusBadRequest, albums.CodeBadPage, err.Error())
			return
		}
		c.svcError(err)
		return
	}
	if err := h.svc.CommitPage(c.owner, c.id, pg, part); err != nil {
		c.svcError(err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.WriteHeader(http.StatusNoContent)
}

// readBody копирует тело в часть и считает sha256. Ошибка чтения (rerr) и ошибка записи на диск (werr) — разные
// ответы: первая — вина клиента, вторая — сервера или нехватка места.
func readBody(dst *album.Part, src io.Reader) (sum string, size int64, rerr, werr error) {
	h := sha256.New()
	buf := make([]byte, copyChunk)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, err := dst.Write(buf[:k]); err != nil {
				return "", size, nil, err
			}
			h.Write(buf[:k]) // hash.Hash.Write не возвращает ошибку
			size += int64(k)
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), size, nil, nil
		}
		if err != nil {
			return "", size, err, nil
		}
	}
}
