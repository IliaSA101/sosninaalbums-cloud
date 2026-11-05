package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// statusWriter запоминает код и число байт ответа для журнала. Unwrap нужен http.ResponseController
// (индивидуальные сроки чтения и записи для PUT страницы).
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap отдаёт исходный писатель.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// call — один запрос: всё, что нужно ответить и записать в журнал.
type call struct {
	h     *Handler
	w     *statusWriter
	r     *http.Request
	route string
	owner string
	id    string
	code  string // код ошибки контракта, если ответ — ошибка
	err   error  // внутренняя причина 5xx (без имён файлов), только для журнала
	start time.Time
}

// finish — журнал запроса и защита от паники: паника в обработчике не должна ни ронять процесс, ни писать в
// журнал значение паники (в нём может быть путь или id).
func (c *call) finish() {
	if rec := recover(); rec != nil {
		if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			panic(rec) // штатный способ оборвать ответ: net/http его не логирует
		}
		c.err = errors.New("паника в обработчике")
		if c.w.status == 0 {
			c.fail(http.StatusInternalServerError, albums.CodeInternal, "внутренняя ошибка")
		}
	}
	status := c.w.status
	if status == 0 {
		status = http.StatusOK
	}
	attrs := []any{
		slog.String(logx.KeyRoute, c.route), slog.Int(logx.KeyStatus, status),
		slog.Int64(logx.KeyBytes, c.w.bytes), slog.Int64(logx.KeyMS, time.Since(c.start).Milliseconds()),
	}
	if c.code != "" {
		attrs = append(attrs, slog.String(logx.KeyCode, c.code))
	}
	if c.owner != "" {
		attrs = append(attrs, slog.String(logx.KeyOwner, c.owner))
	}
	if c.id != "" {
		attrs = append(attrs, c.h.tagger.Attr(c.id))
	}
	level := slog.LevelInfo
	if c.err != nil {
		level = slog.LevelError
		attrs = append(attrs, slog.Any(logx.KeyErr, c.err))
	}
	c.h.log.Log(c.r.Context(), level, "запрос", attrs...)
}

func (c *call) headers() {
	hd := c.w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
}

// json отвечает JSON с кодом status.
func (c *call) json(status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.err = errors.New("ответ не собран")
		c.fail(http.StatusInternalServerError, albums.CodeInternal, "внутренняя ошибка")
		return
	}
	c.headers()
	c.w.WriteHeader(status)
	if _, err := c.w.Write(b); err != nil {
		c.err = errors.New("ответ не записан клиенту")
	}
}

// fail отвечает ошибкой контракта {"error":{"code","message"}}. message — фиксированный текст без значений
// полей запроса.
func (c *call) fail(status int, code, message string) {
	c.code = code
	c.json(status, albums.ErrorBody{Error: albums.Error{Code: code, Message: message}})
}

// notFound — единственный 404: несуществующий, чужой, истёкший и отозванный альбом, неизвестный путь и id вне
// грамматики отвечают одними и теми же байтами.
func (c *call) notFound() {
	c.fail(http.StatusNotFound, albums.CodeNotFound, "альбом не найден")
}

// svcError переводит ошибку пакета album (и хранилища) в ответ контракта.
func (c *call) svcError(err error) {
	var (
		tl *album.TooLargeError
		bm *album.BadManifestError
		ms *album.MissingError
	)
	switch {
	case errors.Is(err, album.ErrNotFound):
		c.notFound()
	case errors.Is(err, album.ErrPageGone):
		c.fail(http.StatusConflict, albums.CodeHashMismatch, "страница больше не нужна альбому: запросите missing заново")
	case errors.As(err, &tl):
		c.fail(http.StatusRequestEntityTooLarge, albums.CodeTooLarge, tl.Reason)
	case errors.As(err, &bm):
		c.fail(http.StatusBadRequest, albums.CodeBadManifest, bm.Err.Error())
	case errors.As(err, &ms):
		c.code = albums.CodePagesMissing
		c.json(http.StatusConflict, albums.ErrorBody{Error: albums.Error{
			Code: albums.CodePagesMissing, Message: "не все страницы загружены", Missing: ms.Missing,
		}})
	default:
		c.err = err
		c.fail(http.StatusInternalServerError, albums.CodeInternal, "внутренняя ошибка")
	}
}
