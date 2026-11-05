package main

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
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// stack — вся сборка сервиса (assemble: api + view) на настоящем хранилище во временном каталоге, за httptest.
// Перед обработчиком стоит имитация Caddy: срезает входящие X-Sa-* и X-Forwarded-*, ставит X-Sa-Client-IP.
type stack struct {
	t      *testing.T
	srv    *httptest.Server
	log    *syncBuf
	data   string
	tok1   string
	tok2   string
	keys   *config.Keys
	tagger *logx.Tagger
	cfg    *config.Config
	svc    *album.Service
	// panicNow: пока не пуст, настенные часы паникуют с этим значением (проверка журнала при панике).
	panicNow atomic.Pointer[string]
	// free — подставное свободное место; 0 значит «реальное».
	free atomic.Uint64
	st   *store.Store
}

func newStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{t: t, log: &syncBuf{}, data: filepath.Join(t.TempDir(), "data")}
	var owners []any
	for i, tok := range []*string{&s.tok1, &s.tok2} {
		var sum string
		var err error
		if *tok, sum, err = access.GenToken(); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, map[string]any{
			"slug": []string{"p1", "p2"}[i], "studio": []string{"Студия Один", "Студия Два"}[i],
			"quota_bytes": 64 << 20, "max_albums": 100,
			"tokens": []any{map[string]any{"name": "t", "sha256": sum}},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"listen": "unix:/run/albums-svc/http.sock", "public_base": "https://albums.test", "data_dir": "/data", "secret_file": "/run/secrets/x",
		"limits":             map[string]any{"page_bytes": 256 << 10, "pages": 20, "album_bytes": 8 << 20, "title_len": 200, "max_days": 366},
		"disk_reserve_bytes": 1, "owners": owners,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg, err = config.Parse(raw); err != nil {
		t.Fatal(err)
	}
	master, err := config.GenSecret(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := config.ParseSecret(master)
	if err != nil {
		t.Fatal(err)
	}
	if s.keys, err = config.DeriveKeys(mb); err != nil {
		t.Fatal(err)
	}
	log := logx.New(s.log)
	s.tagger = logx.NewTagger(s.keys.Log)
	if s.st, err = store.Open(s.data, storeOptions(s.cfg, s.tagger, log)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.st.Close() })
	s.svc, err = album.New(s.st, albumConfig(s.cfg), album.Options{
		Passwords: access.NewPasswords(s.keys.Password), Tagger: s.tagger, Log: log,
		Wall: func() time.Time {
			if p := s.panicNow.Load(); p != nil {
				panic(*p)
			}
			return time.Now()
		},
		Free: func() (uint64, error) {
			if v := s.free.Load(); v != 0 {
				return v, nil
			}
			return s.st.FreeBytes()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := access.NewTokens(tokenEntries(s.cfg))
	if err != nil {
		t.Fatal(err)
	}
	h, err := assemble(s.cfg, s.keys, s.svc, tokens, log, s.tagger)
	if err != nil {
		t.Fatal(err)
	}
	s.srv = httptest.NewServer(caddySim(h))
	t.Cleanup(s.srv.Close)
	return s
}

// caddySim делает с запросом то же, что Caddyfile: срезает входящие X-Sa-* и X-Forwarded-*, ставит адрес клиента.
func caddySim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Sa-") || strings.HasPrefix(k, "X-Forwarded-") {
				r.Header.Del(k)
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "bad peer", http.StatusBadGateway)
			return
		}
		r.Header.Set("X-Sa-Client-IP", host)
		next.ServeHTTP(w, r)
	})
}

// reply — ответ сервера.
type reply struct {
	status int
	hdr    http.Header
	body   []byte
}

var testClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *stack) do(method, path, tok string, body io.Reader, hdr map[string]string) reply {
	s.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, s.srv.URL+path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := testClient.Do(req)
	if err != nil {
		s.t.Fatalf("запрос: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return reply{status: res.StatusCode, hdr: res.Header, body: b}
}

func (s *stack) jsonReq(method, path, tok string, v any) reply {
	s.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.do(method, path, tok, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
}

// solid — однотонный JPEG w×h, сгенерированный в тесте (никаких реальных фото); shade различает файлы.
func solid(t *testing.T, w, h int, shade uint8) []byte {
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

// uploaded — один опубликованный альбом: id, пароль, файл страницы 1 и пароль (может быть пустым).
type uploaded struct {
	id, file string
	data     [][]byte
}

// publish выгружает альбом из трёх страниц (вторая — белая): создание, страницы, публикация.
func (s *stack) publish(tok, title, password string) uploaded {
	s.t.Helper()
	datas := [][]byte{solid(s.t, 16, 16, 100), nil, solid(s.t, 16, 16, 150), solid(s.t, 16, 16, 200)}
	m := albums.Manifest{Schema: 1, Generator: "test", Title: title, Book: albums.Book{PageMM: albums.Size{W: 100, H: 100}}}
	for i, d := range datas {
		if d == nil {
			m.Pages = append(m.Pages, albums.Page{N: i + 1, Role: albums.RolePage, Blank: true})
			continue
		}
		m.Pages = append(m.Pages, pageDesc(i+1, d))
	}
	r := s.jsonReq("POST", "/v1/albums", tok, albums.CreateRequest{Manifest: m, ExpiresAt: time.Now().Add(48 * time.Hour), Password: password})
	var cr albums.Created
	if r.status != 201 || json.Unmarshal(r.body, &cr) != nil {
		s.t.Fatalf("создание: %d %s", r.status, r.body)
	}
	for _, n := range cr.Missing {
		if r := s.do("PUT", "/v1/albums/"+cr.ID+"/pages/"+itoa(n), tok, bytes.NewReader(datas[n-1]), map[string]string{"Content-Type": albums.TypeJPEG}); r.status != 204 {
			s.t.Fatalf("страница %d: %d %s", n, r.status, r.body)
		}
	}
	if r := s.do("POST", "/v1/albums/"+cr.ID+"/publish", tok, nil, nil); r.status != 200 {
		s.t.Fatalf("publish: %d %s", r.status, r.body)
	}
	return uploaded{id: cr.ID, file: m.Pages[0].SHA256 + ".jpg", data: datas}
}

// pageDesc — описание небелой страницы n по данным (размеры — как у solid в publish).
func pageDesc(n int, data []byte) albums.Page {
	sum := sha256.Sum256(data)
	return albums.Page{N: n, Role: albums.RolePage, Type: albums.TypeJPEG, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), W: 16, H: 16}
}

func itoa(n int) string { return strconv.Itoa(n) }
