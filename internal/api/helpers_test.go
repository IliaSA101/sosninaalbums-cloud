package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

const (
	pOne = "p1"
	pTwo = "p2"
)

// env — настоящий store и сервис во временном каталоге, обработчик /v1 за httptest.
type env struct {
	t    *testing.T
	dir  string
	st   *store.Store
	svc  *album.Service
	h    *Handler
	srv  *httptest.Server
	log  *bytes.Buffer
	tok1 string // токен владельца p1
	tok2 string // токен владельца p2
	lim  albums.Limits
	// avail — подставное свободное место; 0 значит «реальное» (Statfs хранилища).
	avail atomic.Uint64
}

func newEnv(t *testing.T, mutate ...func(*album.Config)) *env {
	t.Helper()
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "data"), log: &bytes.Buffer{}}
	var entries []access.TokenEntry
	for _, o := range []struct {
		slug string
		tok  *string
	}{{pOne, &e.tok1}, {pTwo, &e.tok2}} {
		tok, sum, err := access.NewToken(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		*o.tok = tok
		entries = append(entries, access.TokenEntry{Owner: o.slug, Name: "тест", SHA256: sum})
	}
	tokens, err := access.NewTokens(entries)
	if err != nil {
		t.Fatal(err)
	}
	cfg := album.Config{
		PublicBase: "https://albums.test", UnpublishedTTL: 24 * time.Hour,
		Limits: albums.Limits{PageBytes: 256 << 10, Pages: 20, AlbumBytes: 8 << 20, TitleLen: 200, MaxDays: 366},
		Owners: map[string]album.OwnerLimits{
			pOne: {QuotaBytes: 64 << 20, MaxAlbums: 100},
			pTwo: {QuotaBytes: 64 << 20, MaxAlbums: 100},
		},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	e.lim = cfg.Limits
	log := logx.New(e.log)
	tagger := logx.NewTagger([]byte("ключ-журнала-для-теста-0123456789"))
	st, err := store.Open(e.dir, store.Options{Tagger: tagger, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := album.New(st, cfg, album.Options{
		Passwords: access.NewPasswords([]byte("ключ-пароля-для-теста-0123456789")), Tagger: tagger, Log: log,
		Free: func() (uint64, error) {
			if v := e.avail.Load(); v != 0 {
				return v, nil
			}
			return st.FreeBytes()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.st, e.svc = st, svc
	e.h = New(svc, tokens, e.lim, Options{Log: log, Tagger: tagger})
	e.srv = httptest.NewServer(e.h)
	t.Cleanup(e.srv.Close)
	return e
}

// solid — однотонный JPEG w×h, сгенерированный в тесте (никаких реальных фото); shade различает файлы.
func solid(t testing.TB, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = shade, 120, 40, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seg — сегмент JPEG с маркером m; withSegments вставляет сегменты сразу после SOI.
func seg(m byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, m, byte(n >> 8), byte(n)}, payload...)
}

func withSegments(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte(nil), jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

// pageOf — описание страницы n с данными data (размеры w×h заявляет тест).
func pageOf(n int, data []byte, w, h int) albums.Page {
	sum := sha256.Sum256(data)
	return albums.Page{N: n, Role: albums.RolePage, Type: albums.TypeJPEG, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), W: w, H: h}
}

// manifestOf — книга из страниц 16×16; nil — белая страница. Число страниц чётное.
func manifestOf(title string, datas ...[]byte) albums.Manifest {
	m := albums.Manifest{Schema: 1, Generator: "test", Title: title, Book: albums.Book{PageMM: albums.Size{W: 100, H: 100}}}
	for i, d := range datas {
		if d == nil {
			m.Pages = append(m.Pages, albums.Page{N: i + 1, Role: albums.RolePage, Blank: true})
			continue
		}
		m.Pages = append(m.Pages, pageOf(i+1, d, 16, 16))
	}
	return m
}

type resp struct {
	status int
	hdr    http.Header
	body   []byte
}

func (r *resp) errBody(t testing.TB) albums.Error {
	t.Helper()
	var eb albums.ErrorBody
	if err := json.Unmarshal(r.body, &eb); err != nil {
		t.Fatalf("тело ошибки не JSON: %q", r.body)
	}
	return eb.Error
}

// do — запрос к тестовому серверу; tok пуст — без Authorization. Body закрывается здесь.
func (e *env) do(method, path, tok string, body io.Reader, hdr map[string]string) *resp {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s: %v", method, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return &resp{status: res.StatusCode, hdr: res.Header, body: data}
}

func (e *env) json(method, path, tok string, v any) *resp {
	e.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.do(method, path, tok, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
}

// create создаёт альбом владельца токена tok; ждёт 201.
func (e *env) create(tok string, m albums.Manifest) albums.Created {
	e.t.Helper()
	r := e.json("POST", "/v1/albums", tok, albums.CreateRequest{Manifest: m, ExpiresAt: time.Now().Add(48 * time.Hour)})
	var cr albums.Created
	if r.status != 201 || json.Unmarshal(r.body, &cr) != nil {
		e.t.Fatalf("создание: %d %s", r.status, r.body)
	}
	return cr
}

func (e *env) putPage(tok, id string, n int, ctype string, data []byte) *resp {
	e.t.Helper()
	return e.do("PUT", "/v1/albums/"+id+"/pages/"+itoa(n), tok, bytes.NewReader(data), map[string]string{"Content-Type": ctype})
}

func itoa(n int) string { return strconv.Itoa(n) }

func readDirNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, err
}

func (e *env) pageFiles(id string) []string {
	e.t.Helper()
	f, err := e.st.ListPages(pOne, id)
	if err != nil {
		e.t.Fatalf("ListPages: %v", err)
	}
	return f
}

func (e *env) noParts() {
	e.t.Helper()
	ents, err := readDirNames(filepath.Join(e.dir, "tmp"))
	if err != nil || len(ents) != 0 {
		e.t.Fatalf("в tmp/ остались файлы: %v, %v", ents, err)
	}
}
