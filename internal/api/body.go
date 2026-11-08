package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
)

// Чтение JSON-тел. Тело до 1 МиБ нельзя разбирать в []Page целиком: `{"pages":[{},{},…]}` в мегабайт даёт сотни
// мегабайт выделений до сверки с пределом (аудит Г2). Поэтому pages читается потоком, элемент за элементом, и
// отказ 413 наступает на limits.Pages+1-м элементе; остальное тело не разбирается.

var (
	errTooManyPages = errors.New("pages: больше предела")
	errNotObject    = errors.New("ожидался JSON-объект")
)

// acquireJSON занимает слот JSON-запроса (1 на владельца, 2 всего). Занято — 429 с Retry-After.
func (h *Handler) acquireJSON(c *call) (release func(), ok bool) {
	release, ok = h.json.acquire(c.owner)
	if !ok {
		c.w.Header().Set("Retry-After", retryAfter)
		c.fail(http.StatusTooManyRequests, albums.CodeRateLimited, "слишком много одновременных запросов")
	}
	return release, ok
}

// extendDeadlines продлевает сроки чтения тела и записи ответа под тело в n байт: общий ReadTimeout сервера
// (30 с) короток для мегабайта на медленном канале. Один механизм и для страниц, и для JSON. Сервер без
// поддержки (тестовый писатель) — молча без изменений; иная ошибка — false, ответ 500 уже дан.
func (c *call) extendDeadlines(n int64) bool {
	rc := http.NewResponseController(c.w)
	budget := album.ReadBudget(n)
	for _, set := range []func(time.Time) error{rc.SetReadDeadline, rc.SetWriteDeadline} {
		if err := set(time.Now().Add(budget + album.WriteExtra)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			c.err = errors.New("срок чтения тела не поставлен")
			c.fail(http.StatusInternalServerError, albums.CodeInternal, "внутренняя ошибка")
			return false
		}
	}
	return true
}

// jsonDecoder готовит чтение тела: предел maxJSON до чтения (по Content-Length, 413) и во время (MaxBytesReader),
// срок чтения по размеру. false — ответ уже дан.
func (c *call) jsonDecoder() (*json.Decoder, bool) {
	if c.r.ContentLength > maxJSON {
		c.fail(http.StatusRequestEntityTooLarge, albums.CodeTooLarge, "тело запроса больше предела")
		return nil, false
	}
	if !c.extendDeadlines(maxJSON) {
		return nil, false
	}
	return json.NewDecoder(http.MaxBytesReader(c.w, c.r.Body, maxJSON)), true
}

// jsonFail отвечает на ошибку чтения тела: предел (байт или страниц) — 413, прочее — 400 bad_manifest. nil — true.
func (c *call) jsonFail(err error) bool {
	if err == nil {
		return true
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || errors.Is(err, errTooManyPages) {
		c.fail(http.StatusRequestEntityTooLarge, albums.CodeTooLarge, "тело запроса или число страниц больше предела")
		return false
	}
	// Текст ошибки разборщика может цитировать содержимое запроса — клиенту идёт фиксированный.
	c.fail(http.StatusBadRequest, albums.CodeBadManifest, "тело запроса — не один JSON-объект нужного вида")
	return false
}

// noTrailing — после объекта в теле ничего нет.
func noTrailing(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("после объекта есть лишнее")
		}
		return err
	}
	return nil
}

// readJSON читает ровно один небольшой JSON-объект (PATCH) в v. false — ответ уже дан.
func (c *call) readJSON(v any) bool {
	dec, ok := c.jsonDecoder()
	if !ok {
		return false
	}
	err := dec.Decode(v)
	if err == nil {
		err = noTrailing(dec)
	}
	return c.jsonFail(err)
}

// readCreate читает тело POST /v1/albums, pages — потоком, не больше maxPages элементов.
func (c *call) readCreate(maxPages int) (*albums.CreateRequest, bool) {
	dec, ok := c.jsonDecoder()
	if !ok {
		return nil, false
	}
	var req albums.CreateRequest
	err := readObject(dec, map[string]func() error{
		"manifest":   func() error { return readManifest(dec, &req.Manifest, maxPages) },
		"expires_at": func() error { return dec.Decode(&req.ExpiresAt) },
		"password":   func() error { return dec.Decode(&req.Password) },
	})
	if err == nil {
		err = noTrailing(dec)
	}
	return &req, c.jsonFail(err)
}

// readUpdate читает тело PUT /v1/albums/{id}.
func (c *call) readUpdate(maxPages int) (*albums.UpdateRequest, bool) {
	dec, ok := c.jsonDecoder()
	if !ok {
		return nil, false
	}
	var req albums.UpdateRequest
	err := readObject(dec, map[string]func() error{
		"manifest": func() error { return readManifest(dec, &req.Manifest, maxPages) },
	})
	if err == nil {
		err = noTrailing(dec)
	}
	return &req, c.jsonFail(err)
}

func readManifest(dec *json.Decoder, m *albums.Manifest, maxPages int) error {
	return readObject(dec, map[string]func() error{
		"schema":    func() error { return dec.Decode(&m.Schema) },
		"generator": func() error { return dec.Decode(&m.Generator) },
		"title":     func() error { return dec.Decode(&m.Title) },
		"book":      func() error { return dec.Decode(&m.Book) },
		"pages":     func() error { return readPages(dec, &m.Pages, maxPages) },
	})
}

// readObject читает JSON-объект по ключам: известные поля разбирает fields[ключ], неизвестные пропускаются без
// разбора (RawMessage — только байты). null вместо объекта — пустой объект, как у encoding/json.
func readObject(dec *json.Decoder, fields map[string]func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errNotObject
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := kt.(string)
		if !ok {
			return errNotObject
		}
		if read := fields[strings.ToLower(key)]; read != nil {
			err = read()
		} else {
			var skip json.RawMessage
			err = dec.Decode(&skip)
		}
		if err != nil {
			return err
		}
	}
	_, err = dec.Token() // закрывающая «}»
	return err
}

// readPages читает массив страниц потоком; на maxPages+1-м элементе — errTooManyPages, остаток не разбирается.
func readPages(dec *json.Decoder, dst *[]albums.Page, maxPages int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		*dst = nil
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errNotObject
	}
	pages := []albums.Page{}
	for dec.More() {
		if len(pages) >= maxPages {
			return errTooManyPages
		}
		var p albums.Page
		if err := dec.Decode(&p); err != nil {
			return err
		}
		pages = append(pages, p)
	}
	*dst = pages
	_, err = dec.Token() // закрывающая «]»
	return err
}
