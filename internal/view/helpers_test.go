package view

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

const (
	owner      = "p1"
	studioName = "Студия Тест"
	publicBase = "https://albums.test"
	clientIP   = "203.0.113.7"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// clock — часы теста: настенные и монотонные идут вместе, сторож часов молчит.
type clock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *clock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *clock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall, c.mono = c.wall.Add(d), c.mono+d
}

// env — настоящие хранилище и сервис альбомов во временном каталоге, обработчик view без сети.
type env struct {
	t   testing.TB
	dir string
	clk *clock
	svc *album.Service
	h   *Handler
	log *bytes.Buffer
	opt Options
	pw  *access.Passwords
	ck  *access.Cookies
}

var (
	keyPW     = []byte("ключ-пароля-для-теста-0123456789")
	keyCookie = []byte("ключ-cookie-для-теста-0123456789")
	keyLog    = []byte("ключ-журнала-для-теста-0123456789")
)

type envOpts struct {
	limits    *access.LimitConfig
	trusted   string
	pageBytes int64 // предел страницы сервиса альбомов; 0 — 1 МиБ
}

func newEnv(t testing.TB, mutate ...func(*envOpts)) *env {
	t.Helper()
	var eo envOpts
	for _, m := range mutate {
		m(&eo)
	}
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "data"), clk: &clock{wall: t0}, log: &bytes.Buffer{}}
	log := logx.New(e.log)
	tagger := logx.NewTagger(keyLog)
	st, err := store.Open(e.dir, store.Options{Clock: e.clk.Wall, Tagger: tagger, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.pw, e.ck = access.NewPasswords(keyPW), access.NewCookies(keyCookie)
	lim0 := albums.Limits{PageBytes: 1 << 20, Pages: 20, AlbumBytes: 8 << 20, TitleLen: 200, MaxDays: 366}
	if eo.pageBytes > 0 {
		lim0.PageBytes, lim0.AlbumBytes = eo.pageBytes, 4*eo.pageBytes
	}
	e.svc, err = album.New(st, album.Config{
		PublicBase: publicBase, UnpublishedTTL: 24 * time.Hour, Limits: lim0,
		Owners: map[string]album.OwnerLimits{owner: {QuotaBytes: 64 << 20, MaxAlbums: 100}},
	}, album.Options{Wall: e.clk.Wall, Mono: e.clk.Mono, Passwords: e.pw, Tagger: tagger, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	cfg := access.LimitConfig{
		PerAlbumIP: access.Rate{Count: 5, Window: 15 * time.Minute},
		PerIP:      access.Rate{Count: 20, Window: time.Hour},
		PerAlbum:   access.Rate{Count: 20, Window: 24 * time.Hour},
	}
	if eo.limits != nil {
		cfg = *eo.limits
	}
	lim, err := access.NewLimiter(cfg, access.LimiterOptions{Now: e.clk.Wall})
	if err != nil {
		t.Fatal(err)
	}
	e.opt = Options{PublicBase: publicBase, Owners: map[string]Owner{owner: {Studio: studioName}}, TrustedProxy: eo.trusted, Log: log, Tagger: tagger, Now: e.clk.Wall}
	e.h, err = New(e.svc, e.pw, e.ck, lim, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// blob — «страница»: уникальное содержимое по сиду (содержимое сервис альбомов не разбирает: это дело imgcheck).
func blob(seed int) []byte {
	b := make([]byte, 600)
	copy(b, []byte{0xFF, 0xD8, 0xFF, 0xE0})
	binary.BigEndian.PutUint64(b[8:], uint64(seed)+1)
	for i := 16; i < len(b); i++ {
		b[i] = byte(seed*7 + i)
	}
	return b
}

func pageOf(n int, data []byte) albums.Page {
	sum := sha256.Sum256(data)
	return albums.Page{N: n, Role: albums.RolePage, Type: albums.TypeJPEG, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), W: 16, H: 16}
}

// book — манифест из трёх страниц и одной белой (страница 2).
func book(title string, datas ...[]byte) albums.Manifest {
	m := albums.Manifest{Schema: 1, Generator: "test", Title: title, Book: albums.Book{PageMM: albums.Size{W: 100, H: 100}}}
	for i, d := range datas {
		if d == nil {
			m.Pages = append(m.Pages, albums.Page{N: i + 1, Role: albums.RolePage, Blank: true})
			continue
		}
		m.Pages = append(m.Pages, pageOf(i+1, d))
	}
	return m
}

// fixture — альбом, который создан в тесте: id, страницы и (для опубликованного) имя файла страницы 1.
type fixture struct {
	id    string
	datas [][]byte
	file  string // имя файла первой небелой страницы
}

var defaultDatas = [][]byte{blob(1), nil, blob(3), blob(4)}

// create создаёт альбом со страницами defaultDatas, срок — через двое суток.
func (e *env) create(title, password string) fixture {
	return e.createWith(title, password, defaultDatas)
}

// createWith — то же с заданными страницами.
func (e *env) createWith(title, password string, datas [][]byte) fixture {
	e.t.Helper()
	m := book(title, datas...)
	cr, err := e.svc.Create(owner, &albums.CreateRequest{Manifest: m, ExpiresAt: e.clk.Wall().Add(48 * time.Hour), Password: password})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return fixture{id: cr.ID, datas: datas}
}

// upload принимает страницы так же, как api: тело — во временный файл, финал — CommitPage.
func (e *env) upload(id string, datas ...[]byte) {
	e.t.Helper()
	for _, d := range datas {
		if d == nil {
			continue
		}
		part, err := e.svc.NewPart()
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := part.Write(d); err != nil {
			e.t.Fatal(err)
		}
		pages, err := e.svc.CurrentPages(owner, id)
		if err != nil {
			e.t.Fatal(err)
		}
		want := pageOf(0, d).SHA256
		done := false
		for _, p := range pages {
			if p.SHA256 == want {
				if err := e.svc.CommitPage(owner, id, p, part); err != nil {
					e.t.Fatalf("CommitPage: %v", err)
				}
				done = true
			}
		}
		if !done {
			e.t.Fatal("страницы нет в текущей редакции")
		}
	}
}

// published — создан, загружен и опубликован.
func (e *env) published(password string) fixture {
	e.t.Helper()
	return e.publishedWith(password, defaultDatas)
}

// publishedWith — то же с заданными страницами.
func (e *env) publishedWith(password string, datas [][]byte) fixture {
	e.t.Helper()
	f := e.createWith("Секретное название", password, datas)
	e.upload(f.id, f.datas...)
	if _, err := e.svc.Publish(owner, f.id); err != nil {
		e.t.Fatalf("Publish: %v", err)
	}
	f.file = e.fileName(f.id, 0)
	return f
}

// fileName — имя файла k-й небелой страницы из опубликованного манифеста.
func (e *env) fileName(id string, k int) string {
	e.t.Helper()
	b, err := e.svc.PublishedManifest(id, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	var m albums.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		e.t.Fatal(err)
	}
	for _, p := range m.Pages {
		if !p.Blank {
			if k == 0 {
				return p.File
			}
			k--
		}
	}
	e.t.Fatal("нет страницы")
	return ""
}

// response — ответ обработчика.
type response struct {
	status int
	hdr    http.Header
	body   []byte
}

type reqOpt func(*http.Request)

func withCookie(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: access.CookieName, Value: v}) }
}
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noIP() reqOpt                  { return func(r *http.Request) { r.Header.Del(ClientIPHeader) } }
func fromIP(ip string) reqOpt       { return withHeader(ClientIPHeader, ip) }

// do — запрос к обработчику; адрес клиента по умолчанию — как у Caddy.
func (e *env) do(method, target string, body io.Reader, opts ...reqOpt) response {
	e.t.Helper()
	return e.doTo(e.h, method, target, body, opts...)
}

func (e *env) doTo(h http.Handler, method, target string, body io.Reader, opts ...reqOpt) response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, "http://x"+target, body)
	if err != nil {
		e.t.Fatalf("запрос %q: %v", target, err)
	}
	req.Header.Set(ClientIPHeader, clientIP)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = res.Body.Close()
	return response{status: res.StatusCode, hdr: res.Header, body: b}
}

func (e *env) get(target string, opts ...reqOpt) response { return e.do("GET", target, nil, opts...) }

// login — POST /a/<id>/login с полем password.
func (e *env) login(id, password string, opts ...reqOpt) response {
	e.t.Helper()
	form := url.Values{"password": {password}}.Encode()
	return e.do("POST", "/a/"+id+"/login", strings.NewReader(form), opts...)
}

// sessionOf — значение cookie входа из ответа ("" — не выдана).
func sessionOf(t testing.TB, r response) string {
	t.Helper()
	res := http.Response{Header: r.hdr}
	for _, c := range res.Cookies() {
		if c.Name == access.CookieName {
			return c.Value
		}
	}
	return ""
}

// signIn — верный вход, возвращает cookie.
func (e *env) signIn(id, password string) string {
	e.t.Helper()
	r := e.login(id, password)
	v := sessionOf(e.t, r)
	if r.status != http.StatusSeeOther || v == "" {
		e.t.Fatalf("вход: %d, cookie %q", r.status, v)
	}
	return v
}

// tamper портит значение cookie гарантированно: меняет знак в середине последнего поля (подписи) на другой.
// Нельзя подменять последний знак base64: у подписи в 32 байта в нём значимы лишь 4 старших бита, и
// замена на «A» в 1/16 случаев возвращает исходную строку (тест становился нестабильным).
func tamper(s string) string {
	start := strings.LastIndexByte(s, '.') + 1
	i := start + (len(s)-start)/2
	repl := byte('A')
	if s[i] == repl {
		repl = 'B'
	}
	return s[:i] + string(repl) + s[i+1:]
}

// pageFile — путь файла страницы на диске (для порчи файлов в тестах неполного альбома).
func (e *env) pagePath(id, file string) string {
	return filepath.Join(e.dir, "owners", owner, "albums", id, file)
}

func (e *env) removeFile(id, file string) {
	e.t.Helper()
	if err := os.Remove(e.pagePath(id, file)); err != nil {
		e.t.Fatal(err)
	}
}
