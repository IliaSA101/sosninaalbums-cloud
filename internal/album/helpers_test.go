package album

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// clock — фальшивые часы: настенные и монотонные двигаются раздельно. advance — исправные часы (оба вперёд),
// jump — скачок настенных (перевод времени), монотонные стоят.
type clock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration

	// jumpAt > 0: на вызове Wall с этим номером настенные часы прыгают на jumpBy (скачок посреди прохода).
	calls, jumpAt int
	jumpBy        time.Duration
}

func (c *clock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.jumpAt > 0 && c.calls == c.jumpAt {
		c.wall = c.wall.Add(c.jumpBy)
	}
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

func (c *clock) jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
}

const (
	pOne = "p1"
	pTwo = "p2"
)

// env — сервис на настоящем store во временном каталоге.
type env struct {
	t   *testing.T
	dir string
	clk *clock
	cfg Config
	opt Options
	log *bytes.Buffer
	st  *store.Store
	svc *Service
}

func testConfig() Config {
	return Config{
		PublicBase:     "https://albums.test",
		Limits:         albums.Limits{PageBytes: 1 << 20, Pages: 20, AlbumBytes: 8 << 20, TitleLen: 200, MaxDays: 366},
		UnpublishedTTL: 24 * time.Hour,
		Owners: map[string]OwnerLimits{
			pOne: {QuotaBytes: 10 << 20, MaxAlbums: 10},
			pTwo: {QuotaBytes: 10 << 20, MaxAlbums: 10},
		},
	}
}

func newEnv(t *testing.T, mutate ...func(*Config, *Options)) *env {
	t.Helper()
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "data"), clk: &clock{wall: t0}, cfg: testConfig(), log: &bytes.Buffer{}}
	e.opt = Options{
		Wall: e.clk.Wall, Mono: e.clk.Mono,
		Passwords: access.NewPasswords([]byte("ключ-пароля-для-теста-0123456789")),
		Tagger:    logx.NewTagger([]byte("ключ-журнала-для-теста-0123456789")),
		Log:       logx.New(e.log),
	}
	for _, m := range mutate {
		m(&e.cfg, &e.opt)
	}
	e.open()
	return e
}

// open (пере)открывает store и сервис в том же каталоге с теми же часами.
func (e *env) open() {
	e.t.Helper()
	st, err := store.Open(e.dir, store.Options{Clock: e.clk.Wall, Tagger: e.opt.Tagger, Log: e.opt.Log})
	if err != nil {
		e.t.Fatalf("store.Open: %v", err)
	}
	svc, err := New(st, e.cfg, e.opt)
	if err != nil {
		_ = st.Close()
		e.t.Fatalf("New: %v", err)
	}
	e.st, e.svc = st, svc
	e.t.Cleanup(func() { _ = st.Close() })
}

// reopen — перезапуск процесса: замок снят, состояние читается с диска.
func (e *env) reopen() {
	e.t.Helper()
	if err := e.st.Close(); err != nil {
		e.t.Fatalf("Close: %v", err)
	}
	e.open()
}

// blob — «страница» из сида и размера: уникальное содержимое. Содержимое album не разбирает (это дело imgcheck).
func blob(seed int, size int) []byte {
	b := make([]byte, size)
	binary.BigEndian.PutUint64(b, uint64(seed)+1)
	for i := 8; i < size; i++ {
		b[i] = byte(seed + i)
	}
	return b
}

// page — описание небелой страницы n по данным.
func page(n int, data []byte) albums.Page {
	sum := sha256.Sum256(data)
	return albums.Page{N: n, Role: albums.RolePage, Type: albums.TypeJPEG, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), W: 16, H: 16}
}

// book — манифест из страниц: nil — белая страница. Число страниц должно быть чётным.
func book(title string, datas ...[]byte) albums.Manifest {
	m := albums.Manifest{Schema: 1, Generator: "test", Title: title, Book: albums.Book{PageMM: albums.Size{W: 100, H: 100}}}
	for i, d := range datas {
		if d == nil {
			m.Pages = append(m.Pages, albums.Page{N: i + 1, Role: albums.RolePage, Blank: true})
			continue
		}
		m.Pages = append(m.Pages, page(i+1, d))
	}
	return m
}

func (e *env) create(owner string, m albums.Manifest, password string) albums.Created {
	e.t.Helper()
	cr, err := e.svc.Create(owner, &albums.CreateRequest{Manifest: m, ExpiresAt: e.clk.Wall().Add(48 * time.Hour), Password: password})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return cr
}

// put принимает страницу так же, как api: тело — в Part, финал — CommitPage под мьютексом альбома.
func (e *env) put(owner, id string, data []byte) error {
	e.t.Helper()
	part := e.part(data)
	return e.commit(owner, id, data, part)
}

func (e *env) part(data []byte) *store.Part {
	e.t.Helper()
	p, err := e.st.NewPart()
	if err != nil {
		e.t.Fatalf("NewPart: %v", err)
	}
	if _, err := p.Write(data); err != nil {
		e.t.Fatalf("Part.Write: %v", err)
	}
	return p
}

func (e *env) commit(owner, id string, data []byte, part *store.Part) error {
	e.t.Helper()
	pages, err := e.svc.CurrentPages(owner, id)
	if err != nil {
		_ = part.Abort()
		return err
	}
	want := page(0, data).SHA256
	for _, p := range pages {
		if p.SHA256 == want {
			return e.svc.CommitPage(owner, id, p, part)
		}
	}
	// Страницы нет в текущей редакции: как у api после замены правки — файл вне published ∪ draft.
	p := page(0, data)
	p.File = fileOf(p)
	return e.svc.CommitPage(owner, id, p, part)
}

func (e *env) mustPut(owner, id string, datas ...[]byte) {
	e.t.Helper()
	for _, d := range datas {
		if err := e.put(owner, id, d); err != nil {
			e.t.Fatalf("put: %v", err)
		}
	}
}

// published — создан, загружен и опубликован.
func (e *env) published(owner string, m albums.Manifest, datas ...[]byte) albums.Created {
	e.t.Helper()
	cr := e.create(owner, m, "")
	e.mustPut(owner, cr.ID, datas...)
	if _, err := e.svc.Publish(owner, cr.ID); err != nil {
		e.t.Fatalf("Publish: %v", err)
	}
	return cr
}

func (e *env) files(owner, id string) []string {
	e.t.Helper()
	f, err := e.st.ListPages(owner, id)
	if err != nil {
		e.t.Fatalf("ListPages: %v", err)
	}
	return f
}

func (e *env) hasFile(owner, id string, data []byte) bool {
	e.t.Helper()
	want := fileOf(page(0, data))
	for _, f := range e.files(owner, id) {
		if f == want {
			return true
		}
	}
	return false
}

func (e *env) inIndex(id string) bool { return e.svc.lookup(id) != nil }

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("ждали %v, получили %v", target, err)
	}
}
