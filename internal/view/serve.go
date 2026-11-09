package view

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/web/viewer"
)

// view отдаёт страницу, манифест, view.json или картинку альбома — после проверки входа. Альбом уже виден
// (опубликован, полон, не истёк): ServeHTTP спросил об этом у сервиса альбомов. Манифест и файл проверяются по
// снимку, из которого они и берутся (allow внутри сервиса): пароль, поставленный между проверкой и отдачей, не
// пропустит ответ.
func (h *Handler) view(c *call, k kind, name string, acc album.Access) {
	id := c.id
	allow := func(a album.Access) bool { return h.authorized(c.r, id, a) }
	pg := h.pagesOf(acc.Owner)
	switch k {
	case kIndex:
		if !allow(acc) {
			c.denied(k, acc)
			return
		}
		c.html(http.StatusOK, cachePrivate, pg.index)
	case kViewJSON:
		if !allow(acc) {
			c.denied(k, acc)
			return
		}
		c.send(http.StatusOK, "application/json", h.sandboxCSP, cachePrivate, pg.viewJSON)
	case kManifest:
		b, err := h.svc.PublishedManifest(id, allow)
		if err != nil {
			c.fail(err, k, acc)
			return
		}
		c.send(http.StatusOK, "application/json", h.sandboxCSP, cachePrivate, b)
	case kFile:
		h.image(c, name, acc, allow)
	}
}

// fail — ошибка сервиса альбомов: отказ входа — 401, прочее — svcError.
func (c *call) fail(err error, k kind, acc album.Access) {
	if errors.Is(err, album.ErrDenied) {
		c.denied(k, acc)
		return
	}
	c.svcError(err)
}

// image отдаёт файл страницы. Дескриптор открывает сервис альбомов под мьютексом альбома (имя есть в текущей
// published, вход проверен по тому же снимку); дальше отдача идёт из него, без блокировки: удаление альбома не
// оборвёт чтение (001-24).
func (h *Handler) image(c *call, name string, acc album.Access, allow func(album.Access) bool) {
	f, err := h.svc.OpenPublished(c.id, name, allow)
	if err != nil {
		c.fail(err, kFile, acc)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			c.err = errors.New("файл страницы закрыт с ошибкой")
		}
	}()
	// Caddy ответ не буферизует, а общий WriteTimeout сервера (120 с) мал для 15 МиБ на медленном канале.
	if err := http.NewResponseController(c.w).SetWriteDeadline(time.Now().Add(album.WriteBudget(f.Size))); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.err = errors.New("срок записи ответа не поставлен")
		c.svcError(c.err)
		return
	}
	h.common(c.w)
	hd := c.w.Header()
	hd.Set("Content-Type", f.Type) // из манифеста: ServeContent не гадает по имени и содержимому
	hd.Set("Content-Security-Policy", h.sandboxCSP)
	hd.Set("Cache-Control", cacheImage)
	// Имя файла — sha256 содержимого: это и есть сильный ETag, повторный запрос отвечается 304.
	hd.Set("Etag", `"`+strings.TrimSuffix(name, path.Ext(name))+`"`)
	http.ServeContent(c.w, c.r, "", time.Time{}, f)
}

// svcError переводит ошибку сервиса альбомов: «не найден» — единый 404, прочее — 500 без подробностей.
func (c *call) svcError(err error) {
	if errors.Is(err, album.ErrNotFound) {
		c.missing()
		return
	}
	c.err = err
	c.code = "internal"
	c.send(http.StatusInternalServerError, "text/plain; charset=utf-8", c.h.sandboxCSP, cacheNone, []byte("внутренняя ошибка\n"))
}

// asset отдаёт файл встроенной листалки по /viewer/<версия>/<файл>. Чужая версия, каталог, неизвестный файл —
// 404 тем же ответом. Содержимое под версией не меняется, поэтому immutable честный.
func (h *Handler) asset(c *call, rest string) {
	ver, file, ok := strings.Cut(rest, "/")
	if !ok || ver != h.version || file == "" || !fs.ValidPath(file) {
		c.missing()
		return
	}
	typ, known := assetTypes[path.Ext(file)]
	f, err := viewer.Assets.Open(file)
	if err != nil || !known {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.err = errors.New("файл листалки не открыт")
		}
		c.missing()
		return
	}
	defer func() { _ = f.Close() }() // встроенный файл: закрытие ничего не пишет
	rs, isSeeker := f.(io.ReadSeeker)
	if st, err := f.Stat(); err != nil || st.IsDir() || !isSeeker {
		c.missing()
		return
	}
	h.common(c.w)
	hd := c.w.Header()
	hd.Set("Content-Type", typ)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(c.w, c.r, "", time.Time{}, rs)
}

// assetTypes — типы файлов листалки по расширению. Таблица своя, а не системная: от базы типов машины ответ
// зависеть не должен. Файл с другим расширением не отдаётся.
var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".svg": "image/svg+xml",
	".txt": "text/plain; charset=utf-8",
}
