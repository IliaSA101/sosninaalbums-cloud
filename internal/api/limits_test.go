package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
)

// Аудит Г2: `{"manifest":{"pages":[{},{},…]}}` в мегабайт разбирался в []Page целиком (≈ 200 МБ) до сверки с
// пределом. Теперь pages читается потоком и отказ наступает на limits.Pages+1-м элементе.
func TestJSONPagesMemory(t *testing.T) {
	e := newEnv(t)
	cr := e.create(e.tok1, manifestOf("A", solid(t, 16, 16, 1), nil))
	body := []byte(`{"manifest":{"pages":[` + strings.Repeat("{},", 340000) + `{}]}}`)
	if len(body) >= maxJSON {
		t.Fatalf("тело %d не укладывается в предел JSON", len(body))
	}
	for _, ep := range []struct{ method, path string }{{"POST", "/v1/albums"}, {"PUT", "/v1/albums/" + cr.ID}} {
		req := httptest.NewRequestWithContext(context.Background(), ep.method, ep.path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.tok1)
		rec := httptest.NewRecorder()
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		e.h.ServeHTTP(rec, req)
		runtime.ReadMemStats(&after)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: %d %s", ep.method, rec.Code, rec.Body.String())
		}
		if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
			t.Fatalf("%s: запрос с 340000 страниц выделил %d МБ (порог 16)", ep.method, got>>20)
		}
	}
}

// Предел страниц точен: ровно limits.Pages элементов разбираются (дальше — 400 по содержимому), на единицу
// больше — 413 без разбора.
func TestJSONPagesStreamBoundary(t *testing.T) {
	e := newEnv(t)
	post := func(n int) *resp {
		body := `{"manifest":{"schema":1,"pages":[` + strings.TrimSuffix(strings.Repeat("{},", n), ",") + `]}}`
		return e.do("POST", "/v1/albums", e.tok1, strings.NewReader(body), nil)
	}
	if r := post(e.lim.Pages); r.status != 400 || r.errBody(t).Code != albums.CodeBadManifest {
		t.Fatalf("ровно предел: %d %s", r.status, r.body)
	}
	if r := post(e.lim.Pages + 1); r.status != 413 {
		t.Fatalf("предел+1: %d %s", r.status, r.body)
	}
	// Известные поля в любом порядке, чужие — пропускаются, а манифест по-прежнему работает.
	m := manifestOf("A", solid(t, 16, 16, 2), nil)
	raw, _ := json.Marshal(map[string]any{
		"evil": map[string]any{"x": []int{1, 2}}, "password": "Пароль-1234", "manifest": m,
		"expires_at": time.Now().Add(time.Hour), "evil2": nil,
	})
	if r := e.do("POST", "/v1/albums", e.tok1, bytes.NewReader(raw), nil); r.status != 201 {
		t.Fatalf("порядок полей и чужие поля: %d %s", r.status, r.body)
	}
}

func TestSlotsGlobal(t *testing.T) {
	s := newSlots(1, 2)
	ra, ok := s.acquire("a")
	if !ok {
		t.Fatal("a")
	}
	if _, ok := s.acquire("a"); ok {
		t.Fatal("второй слот владельца выдан")
	}
	if _, ok := s.acquire("b"); !ok {
		t.Fatal("b")
	}
	if _, ok := s.acquire("c"); ok {
		t.Fatal("общий предел 2 превышен")
	}
	ra()
	rc, ok := s.acquire("c")
	if !ok {
		t.Fatal("слот не освободился")
	}
	rc()
	if len(s.n) != 1 || s.sum != 1 {
		t.Fatalf("учёт: %v, %d", s.n, s.sum)
	}
}

// slotWaiter возвращает ожидание «занято ровно n слотов» по счётчику сервера. Запись тела клиентом возвращается,
// когда его прочитал транспорт, а ответ уходит раньше, чем обработчик вернёт слот (defer release), поэтому ни
// «слот занят», ни «слот свободен» нельзя выводить из хода клиента. Условие проверяется по состоянию, а не по
// числу событий: слоты соседних запросов теста не сбивают счёт.
func slotWaiter(t *testing.T, s *slots) (waitSum func(n int)) {
	t.Helper()
	changed := make(chan struct{}, 1) // ёмкость 1: уведомление не теряется между проверкой и ожиданием
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	s.mu.Lock()
	s.onAcquire, s.onRelease = notify, notify
	s.mu.Unlock()
	return func(n int) {
		t.Helper()
		timeout := time.After(10 * time.Second) // страховка: провал вместо вечного зависания
		for {
			s.mu.Lock()
			sum := s.sum
			s.mu.Unlock()
			if sum == n {
				return
			}
			select {
			case <-changed:
			case <-timeout:
				t.Fatalf("за 10 с занятых слотов %d, ждали %d", sum, n)
			}
		}
	}
}

// heldJSON занимает слот JSON-запроса: тело POST держится открытым, пока тест не закроет его. Труба закрывается
// и в t.Cleanup (finish идемпотентен): после провала теста httptest.Server.Close иначе ждал бы соединение вечно.
func (e *env) heldJSON(tok string, waitSum func(int)) (finish func(*testing.T) *resp) {
	e.t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/v1/albums", pr)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	waitSum(0) // слот предыдущего запроса теста (create) возвращается после его ответа
	done := make(chan *resp, 1)
	go func() {
		res, err := e.srv.Client().Do(req)
		if err != nil {
			done <- &resp{status: -1}
			return
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		done <- &resp{status: res.StatusCode, body: b}
	}()
	var once sync.Once
	var got *resp
	finish = func(*testing.T) *resp {
		once.Do(func() {
			_ = pw.CloseWithError(io.ErrUnexpectedEOF)
			got = <-done
		})
		return got
	}
	e.t.Cleanup(func() { finish(nil) })
	if _, err := pw.Write([]byte("{")); err != nil {
		e.t.Fatal(err)
	}
	waitSum(1) // вернулась запись — ещё не значит, что сервер занял слот
	return finish
}

func TestJSONBusy429(t *testing.T) {
	e := newEnv(t)
	cr := e.create(e.tok1, manifestOf("A", solid(t, 16, 16, 3), nil))
	waitSum := slotWaiter(t, e.h.json)
	finish := e.heldJSON(e.tok1, waitSum)
	good := albums.CreateRequest{Manifest: manifestOf("B", solid(t, 16, 16, 4), nil), ExpiresAt: time.Now().Add(time.Hour)}
	for name, r := range map[string]*resp{
		"POST":  e.json("POST", "/v1/albums", e.tok1, good),
		"PUT":   e.json("PUT", "/v1/albums/"+cr.ID, e.tok1, albums.UpdateRequest{Manifest: good.Manifest}),
		"PATCH": e.json("PATCH", "/v1/albums/"+cr.ID, e.tok1, albums.Patch{RevokeSessions: true}),
	} {
		if r.status != 429 || r.errBody(t).Code != albums.CodeRateLimited || r.hdr.Get("Retry-After") != "5" {
			t.Fatalf("%s при занятом слоте: %d %s, Retry-After %q", name, r.status, r.body, r.hdr.Get("Retry-After"))
		}
	}
	// Запросы без тела и загрузка страниц слот JSON не занимают; чужой владелец не мешает.
	if r := e.do("GET", "/v1/albums", e.tok1, nil, nil); r.status != 200 {
		t.Fatalf("GET: %d", r.status)
	}
	if r := e.json("POST", "/v1/albums", e.tok2, good); r.status != 201 {
		t.Fatalf("другой владелец: %d %s", r.status, r.body)
	}
	if got := finish(t); got.status == 201 {
		t.Fatalf("оборванный запрос принят: %d", got.status)
	}
	waitSum(0) // обработчик оборванного запроса возвращает слот уже после ответа клиенту
	if r := e.json("POST", "/v1/albums", e.tok1, good); r.status != 201 {
		t.Fatalf("после освобождения слота: %d %s", r.status, r.body)
	}
}

// Повтор PUT уже лежащей страницы при нехватке места — 204 (идемпотентность под давлением диска); новая
// страница — 413. Свободное место подставное, реальный диск не задействован.
func TestPutPageNoSpaceIdempotent(t *testing.T) {
	e := newEnv(t, func(c *album.Config) { c.DiskReserve = 1 << 20 })
	e.avail.Store(1 << 30) // места достаточно
	one, two := solid(t, 16, 16, 5), solid(t, 16, 16, 6)
	cr := e.create(e.tok1, manifestOf("A", one, two))
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, one); r.status != 204 {
		t.Fatalf("первая страница: %d %s", r.status, r.body)
	}
	e.avail.Store(1) // места почти нет: меньше запаса
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, one); r.status != 204 {
		t.Fatalf("повтор при нехватке места: %d %s", r.status, r.body)
	}
	if r := e.putPage(e.tok1, cr.ID, 2, albums.TypeJPEG, two); r.status != 413 || r.errBody(t).Code != albums.CodeTooLarge {
		t.Fatalf("новая страница при нехватке места: %d %s", r.status, r.body)
	}
	e.noParts()
	e.avail.Store(1 << 30)
	if r := e.putPage(e.tok1, cr.ID, 2, albums.TypeJPEG, two); r.status != 204 {
		t.Fatalf("после освобождения места: %d %s", r.status, r.body)
	}
}
