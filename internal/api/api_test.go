package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
)

// Conformance целиком (с /a/<id>/…) идёт в cmd/albums-svc: api и view не знают друг друга, а проверять
// контракт нужно на той же сборке, что и в работе (assemble). Здесь — только /v1.

func TestHealthLimits(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/v1/health", "", nil, nil) // без токена
	var h albums.Health
	if r.status != 200 || json.Unmarshal(r.body, &h) != nil || h.API != "v1" {
		t.Fatalf("health: %d %s", r.status, r.body)
	}
	if h.Limits != e.lim {
		t.Fatalf("пределы health %+v не те, по которым сервис режет запросы: %+v", h.Limits, e.lim)
	}
	if r.hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type: %q", r.hdr.Get("Content-Type"))
	}
	if r := e.do("POST", "/v1/health", "", nil, nil); r.status != 404 || r.errBody(t).Code != albums.CodeNotFound {
		t.Fatalf("POST /v1/health: %d %s", r.status, r.body)
	}
}

// Негодный id в пути после токена — 404 not_found тем же телом, что у несуществующего (001-26); без токена —
// 401 даже для негодного id (токен проверяется первым).
func TestV1BadID404(t *testing.T) {
	e := newEnv(t)
	ghost := "q" + strings.Repeat("a", 25) // по грамматике, но такого альбома нет
	_, want := e.notFoundBody(ghost)
	bad := []string{
		strings.Repeat("a", 22), strings.Repeat("a", 25), strings.Repeat("a", 27), "Zz" + strings.Repeat("q", 24),
		strings.Repeat("a", 25) + "1", strings.Repeat("a", 25) + "-", "-" + strings.Repeat("a", 25), strings.Repeat("a", 500),
		"a%00b", "..", "%2e%2e", "a b",
	}
	eps := []struct{ method, suffix string }{
		{"PUT", ""}, {"PATCH", ""}, {"DELETE", ""}, {"POST", "/publish"}, {"PUT", "/pages/1"},
	}
	for _, id := range bad {
		for _, ep := range eps {
			path := "/v1/albums/" + id + ep.suffix
			r := e.do(ep.method, path, e.tok1, strings.NewReader("{}"), map[string]string{"Content-Type": albums.TypeJPEG})
			if r.status != 404 {
				if r.status == 301 || r.status == 400 { // клиент/сервер отвергли путь ещё до маршрута — допустимо только 404
					t.Fatalf("%s %q: %d", ep.method, id, r.status)
				}
				t.Fatalf("%s %q: %d %s", ep.method, id, r.status, r.body)
			}
			if !bytes.Equal(r.body, want) {
				t.Fatalf("%s %q: тело 404 отличается от 404 несуществующего: %s", ep.method, id, r.body)
			}
			if strings.ContainsAny(id, "%. ") { // неканонический путь отвечает 404 ещё до токена
				continue
			}
			if r := e.do(ep.method, path, "", strings.NewReader("{}"), nil); r.status != 401 {
				t.Fatalf("без токена %s %q: %d", ep.method, id, r.status)
			}
		}
	}
}

// notFoundBody — байты ответа 404 на несуществующий (по грамматике) id.
func (e *env) notFoundBody(id string) (int, []byte) {
	e.t.Helper()
	r := e.do("DELETE", "/v1/albums/"+id, e.tok1, nil, nil)
	if r.status != 404 {
		e.t.Fatalf("несуществующий: %d %s", r.status, r.body)
	}
	return r.status, r.body
}

func TestWebPOff400(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 150)
	m := manifestOf("WebP", data, nil)
	m.Pages[0].Type = albums.TypeWebP
	r := e.json("POST", "/v1/albums", e.tok1, albums.CreateRequest{Manifest: m, ExpiresAt: time.Now().Add(time.Hour)})
	if r.status != 400 || r.errBody(t).Code != albums.CodeBadManifest {
		t.Fatalf("WebP в манифесте: %d %s", r.status, r.body)
	}
	cr := e.create(e.tok1, manifestOf("JPEG", data, nil))
	if r := e.json("PUT", "/v1/albums/"+cr.ID, e.tok1, albums.UpdateRequest{Manifest: m}); r.status != 400 {
		t.Fatalf("WebP в правке: %d %s", r.status, r.body)
	}
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeWebP, data); r.status != 400 {
		t.Fatalf("PUT WebP в JPEG-страницу: %d", r.status)
	}
}

func TestJSONBody413(t *testing.T) {
	e := newEnv(t)
	cr := e.create(e.tok1, manifestOf("A", solid(t, 16, 16, 160), nil))
	huge := []byte(`{"manifest":{"title":"` + strings.Repeat("a", 2<<20) + `"}}`)
	for _, ep := range []struct{ method, path string }{
		{"POST", "/v1/albums"}, {"PUT", "/v1/albums/" + cr.ID}, {"PATCH", "/v1/albums/" + cr.ID},
	} {
		// Известная длина — отказ до чтения.
		r := e.do(ep.method, ep.path, e.tok1, bytes.NewReader(huge), map[string]string{"Content-Type": "application/json"})
		if r.status != 413 || r.errBody(t).Code != albums.CodeTooLarge {
			t.Fatalf("%s с Content-Length: %d %s", ep.method, r.status, r.body)
		}
		// Неизвестная длина — предел во время чтения.
		r = e.do(ep.method, ep.path, e.tok1, readerOnly{bytes.NewReader(huge)}, map[string]string{"Content-Type": "application/json"})
		if r.status != 413 {
			t.Fatalf("%s chunked: %d %s", ep.method, r.status, r.body)
		}
	}
	// Ровно один объект: хвост после него — 400.
	good, _ := json.Marshal(albums.Patch{RevokeSessions: true})
	r := e.do("PATCH", "/v1/albums/"+cr.ID, e.tok1, bytes.NewReader(append(good, []byte(`{"x":1}`)...)), nil)
	if r.status != 400 || r.errBody(t).Code != albums.CodeBadManifest {
		t.Fatalf("два объекта: %d %s", r.status, r.body)
	}
	for name, raw := range map[string]string{"не JSON": "{", "пусто": "", "массив": "[]", "число": "7", "null-страницы": `{"manifest":{"pages":"x"}}`} {
		r := e.do("POST", "/v1/albums", e.tok1, strings.NewReader(raw), nil)
		if r.status != 400 || r.errBody(t).Code != albums.CodeBadManifest {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
		if strings.Contains(string(r.body), "unexpected") || strings.Contains(string(r.body), "json:") {
			t.Fatalf("%s: текст разборщика в ответе: %s", name, r.body)
		}
	}
}

// readerOnly прячет длину: клиент отправляет chunked.
type readerOnly struct{ r *bytes.Reader }

func (r readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

func TestPagesLimit413(t *testing.T) {
	e := newEnv(t, func(c *album.Config) { c.Limits.AlbumBytes = 1 << 20 })
	data := solid(t, 16, 16, 170)
	many := manifestOf("Много", make([][]byte, e.lim.Pages+2)...) // белые страницы: предел числа страниц
	post := func(m albums.Manifest) *resp {
		return e.json("POST", "/v1/albums", e.tok1, albums.CreateRequest{Manifest: m, ExpiresAt: time.Now().Add(time.Hour)})
	}
	if r := post(many); r.status != 413 || r.errBody(t).Code != albums.CodeTooLarge {
		t.Fatalf("страниц больше предела: %d %s", r.status, r.body)
	}
	if r := e.json("PUT", "/v1/albums/"+e.create(e.tok1, manifestOf("A", data, nil)).ID, e.tok1, albums.UpdateRequest{Manifest: many}); r.status != 413 {
		t.Fatalf("то же при правке: %d %s", r.status, r.body)
	}
	// Сумма байт больше album_bytes: каждая страница в пределах page_bytes, вместе — больше.
	big := manifestOf("Тяжёлый", make([][]byte, 20)...)
	for i := range big.Pages {
		sum := bytes.Repeat([]byte{byte(i)}, 64)
		p := pageOf(i+1, append(sum, byte(i)), 16, 16)
		p.Bytes = e.lim.PageBytes
		big.Pages[i] = p
	}
	if int64(len(big.Pages))*e.lim.PageBytes <= e.lim.AlbumBytes {
		t.Fatal("тест не строит превышение суммы")
	}
	if r := post(big); r.status != 413 {
		t.Fatalf("сумма байт больше предела: %d %s", r.status, r.body)
	}
	// Страница больше page_bytes — не предел альбома, а негодный манифест.
	one := manifestOf("Страница", data, nil)
	one.Pages[0].Bytes = e.lim.PageBytes + 1
	if r := post(one); r.status != 400 || r.errBody(t).Code != albums.CodeBadManifest {
		t.Fatalf("страница больше page_bytes в манифесте: %d %s", r.status, r.body)
	}
}

func TestNonCanonicalPath404(t *testing.T) {
	e := newEnv(t)
	cr := e.create(e.tok1, manifestOf("A", solid(t, 16, 16, 180), nil))
	_, want := e.notFoundBody(strings.Repeat("q", 26))
	targets := []string{
		"/V1/albums", "/v1/Albums", "/v1//albums", "//v1/albums", "/v1/albums/", "/v1/%2e%2e/v1/albums", "/v1/%2E%2E/albums",
		"/v1/albums/../albums", "/v1/./albums", "/v1/albums/" + cr.ID + "/%2e%2e/" + cr.ID, "/v1/albums/%2e%2e%2falbums",
		"/v1/albums%2f" + cr.ID, "/v1/albums/" + cr.ID + "%2fpublish", "/v1/albums/" + cr.ID + "/./publish",
		"/v1/albums/" + cr.ID + "/pages/%31", "/v1/albums/" + cr.ID + "/pages/1/", "/v1/albums/" + cr.ID + "/publish/",
		"/v1/albums\\" + cr.ID, "/v1/албомы", "/v2/albums", "/", "/v1", "/v1/", "/a/" + cr.ID + "/",
	}
	for _, target := range targets {
		for _, method := range []string{"GET", "POST", "DELETE", "PUT"} {
			req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
			req.Header.Set("Authorization", "Bearer "+e.tok1)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d (ждали 404, без перенаправления)", method, target, rec.Code)
			}
			if rec.Header().Get("Location") != "" {
				t.Fatalf("%s %s: перенаправление", method, target)
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("%s %s: тело отличается: %s", method, target, rec.Body.String())
			}
		}
	}
	// Законный путь с тем же токеном работает.
	if r := e.do("GET", "/v1/albums", e.tok1, nil, nil); r.status != 200 {
		t.Fatalf("законный путь: %d", r.status)
	}
	// Альбом цел: ни один обход ничего не удалил.
	if got := e.svc.List(pOne); len(got) != 1 {
		t.Fatalf("альбомов %d", len(got))
	}
}

// Ошибки всегда {"error":{"code","message"}} с известным кодом; в тексте нет ни токена, ни id; 401 одинаков.
func TestErrorShape(t *testing.T) {
	e := newEnv(t)
	cr := e.create(e.tok1, manifestOf("A", solid(t, 16, 16, 190), nil))
	known := map[string]bool{
		albums.CodeBadManifest: true, albums.CodeBadPage: true, albums.CodeUnauthorized: true, albums.CodeNotFound: true,
		albums.CodeHashMismatch: true, albums.CodePagesMissing: true, albums.CodeTooLarge: true, albums.CodeRateLimited: true,
		albums.CodeInternal: true,
	}
	cases := []struct {
		name         string
		method, path string
		tok          string
		body         string
		status       int
	}{
		{"без токена", "GET", "/v1/albums", "", "", 401},
		{"чужой токен", "GET", "/v1/albums", "x" + e.tok1, "", 401},
		{"не Bearer", "GET", "/v1/albums", "", "", 401},
		{"негодный JSON", "POST", "/v1/albums", e.tok1, "{", 400},
		{"нет альбома", "DELETE", "/v1/albums/" + strings.Repeat("q", 26), e.tok1, "", 404},
		{"publish без страниц", "POST", "/v1/albums/" + cr.ID + "/publish", e.tok1, "", 409},
		{"метод", "DELETE", "/v1/albums", e.tok1, "", 404},
		{"неизвестный путь", "GET", "/v1/nope", e.tok1, "", 404},
	}
	var unauthorized [][]byte
	for _, tc := range cases {
		hdr := map[string]string{"Content-Type": "application/json"}
		if tc.name == "не Bearer" {
			hdr["Authorization"] = "Basic " + e.tok1
		}
		r := e.do(tc.method, tc.path, tc.tok, strings.NewReader(tc.body), hdr)
		if r.status != tc.status {
			t.Fatalf("%s: %d %s", tc.name, r.status, r.body)
		}
		eb := r.errBody(t)
		if !known[eb.Code] || eb.Message == "" {
			t.Fatalf("%s: форма ошибки: %+v", tc.name, eb)
		}
		if got := r.hdr.Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s: Content-Type %q", tc.name, got)
		}
		for _, secret := range []string{e.tok1, e.tok2, cr.ID} {
			if strings.Contains(string(r.body), secret) {
				t.Fatalf("%s: секрет в ответе", tc.name)
			}
		}
		if r.status == 401 {
			unauthorized = append(unauthorized, r.body)
		}
	}
	for _, b := range unauthorized[1:] {
		if !bytes.Equal(b, unauthorized[0]) {
			t.Fatalf("тела 401 различаются: %s и %s", b, unauthorized[0])
		}
	}
}

// Паника в обработчике — 500 той же формы, процесс жив, в журнале нет значения паники.
func TestErrorShapePanic(t *testing.T) {
	e := newEnv(t)
	bad := New(e.svc, (*access.Tokens)(nil), e.lim, Options{Log: e.h.log, Tagger: e.h.tagger}) // nil-токены: паника при проверке токена
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/albums", nil)
	req.Header.Set("Authorization", "Bearer "+e.tok1)
	bad.ServeHTTP(rec, req)
	var eb albums.ErrorBody
	if rec.Code != 500 || json.Unmarshal(rec.Body.Bytes(), &eb) != nil || eb.Error.Code != albums.CodeInternal {
		t.Fatalf("паника: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(e.log.String(), "nil pointer") || strings.Contains(e.log.String(), e.tok1) {
		t.Fatalf("значение паники или токен в журнале: %s", e.log.String())
	}
	if !strings.Contains(e.log.String(), `"status":500`) {
		t.Fatalf("паника не в журнале: %s", e.log.String())
	}
}

// Журнал запросов: токен, id, пароль и путь не попадают в него при полном прогоне; id — только 8 hex.
func TestLogNoTokenOrID(t *testing.T) {
	e := newEnv(t)
	const pw = "Пароль-для-журнала-77"
	data := solid(t, 16, 16, 200)
	r := e.json("POST", "/v1/albums", e.tok1, albums.CreateRequest{Manifest: manifestOf("Секретное название", data, nil), ExpiresAt: time.Now().Add(time.Hour), Password: pw})
	var cr albums.Created
	if r.status != 201 || json.Unmarshal(r.body, &cr) != nil {
		t.Fatalf("%d %s", r.status, r.body)
	}
	e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data)
	e.json("POST", "/v1/albums/"+cr.ID+"/publish", e.tok1, struct{}{})
	e.do("GET", "/v1/albums/"+cr.ID+"?token="+e.tok1, e.tok1, nil, nil)
	e.do("DELETE", "/v1/albums/"+cr.ID, e.tok1, nil, nil)
	e.do("DELETE", "/v1/albums/"+cr.ID, "x"+e.tok1, nil, nil)
	out := e.log.String()
	if !strings.Contains(out, `"route":"PUT /v1/albums/{id}/pages/{n}"`) {
		t.Fatalf("в журнале нет запроса страницы: %s", out)
	}
	for _, secret := range []string{e.tok1, e.tok2, cr.ID, pw, "Секретное название", "/v1/albums/" + cr.ID, "127.0.0.1"} {
		if strings.Contains(out, secret) {
			t.Fatalf("в журнале есть %q", secret[:min(len(secret), 8)])
		}
	}
}
