package view

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// statusWriter запоминает код и число байт ответа для журнала. Unwrap нужен http.ResponseController.
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

// call — один запрос: что ответить и что записать в журнал.
type call struct {
	h     *Handler
	w     *statusWriter
	r     *http.Request
	route string
	owner string
	id    string // только у существующего альбома: в журнал идёт его метка, не id
	code  string
	err   error // внутренняя причина (без путей и значений), только для журнала
	start time.Time
}

// finish — журнал запроса и защита от паники: паника в обработчике не роняет процесс и не пишет в журнал своё
// значение (в нём бывает путь или id).
func (c *call) finish() {
	if rec := recover(); rec != nil {
		if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			panic(rec) // штатный способ оборвать ответ: net/http его не логирует
		}
		c.err = errors.New("паника в обработчике")
		if c.w.status == 0 {
			c.svcError(c.err)
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
