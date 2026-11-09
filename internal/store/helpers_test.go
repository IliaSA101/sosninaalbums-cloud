package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// id для тестов — по грамматике a–z2–7, 26 знаков.
const (
	idA = "abcdefghijklmnopqrstuvwxyz"
	idB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	idC = "cccccccccccccccccccccccccc"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type env struct {
	*Store
	dir string
	log *bytes.Buffer
}

// open открывает store в каталоге t.TempDir()/data; mutate правит Options.
func open(t *testing.T, mutate ...func(*Options)) *env {
	t.Helper()
	return openIn(t, filepath.Join(t.TempDir(), "data"), mutate...)
}

func openIn(t *testing.T, dir string, mutate ...func(*Options)) *env {
	t.Helper()
	buf := &bytes.Buffer{}
	opt := Options{
		Clock:  func() time.Time { return t0 },
		Tagger: logx.NewTagger([]byte("ключ-журнала-для-теста-0123456789")),
		Log:    logx.New(buf),
	}
	for _, m := range mutate {
		m(&opt)
	}
	s, err := Open(dir, opt)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &env{Store: s, dir: dir, log: buf}
}

func manifest(files ...string) albums.Manifest {
	m := albums.Manifest{Schema: 1, Generator: "test", Title: "Название для теста", Book: albums.Book{Cover: false, PageMM: albums.Size{W: 200, H: 200}}}
	for i, f := range files {
		m.Pages = append(m.Pages, albums.Page{N: i + 1, Role: albums.RolePage, File: f, Type: albums.TypeJPEG})
	}
	return m
}

func newMeta(owner, id string) *Meta {
	return &Meta{
		Format: 1, ID: id, Owner: owner, Created: t0, Updated: t0, Expires: t0.Add(24 * time.Hour),
		Draft: &Revision{At: t0, Manifest: manifest()},
	}
}

func pageName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + ".jpg"
}

// addPage принимает страницу через Part и CommitPage и возвращает имя файла.
func addPage(t *testing.T, s *Store, owner, id string, data []byte) string {
	t.Helper()
	p, err := s.NewPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(data); err != nil {
		t.Fatal(err)
	}
	name := pageName(data)
	if err := s.CommitPage(owner, id, name, p); err != nil {
		t.Fatalf("CommitPage: %v", err)
	}
	return name
}

func (e *env) create(t *testing.T, owner, id string) *Meta {
	t.Helper()
	m := newMeta(owner, id)
	if err := e.CreateAlbum(m); err != nil {
		t.Fatalf("CreateAlbum: %v", err)
	}
	return m
}

func (e *env) abs(rel string) string { return filepath.Join(e.dir, filepath.FromSlash(rel)) }

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(e.abs(rel))
	return err == nil
}

func (e *env) names(t *testing.T, rel string) []string {
	t.Helper()
	ents, err := os.ReadDir(e.abs(rel))
	if err != nil {
		t.Fatalf("ReadDir %s: %v", rel, err)
	}
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

func (e *env) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(e.abs(rel))
	if err != nil {
		t.Fatalf("ReadFile %s: %v", rel, err)
	}
	return string(b)
}

func (e *env) write(t *testing.T, rel, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs(rel), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// age откатывает mtime пути назад на d.
func (e *env) age(t *testing.T, rel string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(e.abs(rel), old, old); err != nil {
		t.Fatal(err)
	}
}

// noPartFiles проверяет, что в tmp/ нет временных файлов.
func (e *env) noPartFiles(t *testing.T) {
	t.Helper()
	if n := e.names(t, "tmp"); len(n) != 0 {
		t.Fatalf("в tmp/ остались файлы: %v", n)
	}
}

// guard — Guard для тестов: индекс из множества, мьютекс один на всех.
type guard struct {
	inIndex map[string]bool
	before  func(id string) // вызывается под «мьютексом», перед fn
	calls   []string
}

func (g *guard) WithAlbumLock(id string, fn func(inIndex bool) error) error {
	g.calls = append(g.calls, id)
	if g.before != nil {
		g.before(id)
	}
	return fn(g.inIndex[id])
}

func mustNotContain(t *testing.T, text string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if b != "" && strings.Contains(text, b) {
			t.Fatalf("в тексте есть %q: %s", b, text)
		}
	}
}
