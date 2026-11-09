package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

const (
	idOne = "abcdefghijklmnopqrstuvwxyz"
	idTwo = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// seed кладёт в data/ два опубликованных альбома владельца p1 (по странице, байты придуманы тестом); epoch —
// у первого 3, у второго 0. Возвращает имя файла страницы первого альбома.
func seed(t *testing.T, dir string) string {
	t.Helper()
	st, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var first string
	for i, id := range []string{idOne, idTwo} {
		data := bytes.Repeat([]byte{byte(i + 1)}, 100+i)
		sum := sha256.Sum256(data)
		name := hex.EncodeToString(sum[:]) + ".jpg"
		if i == 0 {
			first = name
		}
		pg := albums.Page{N: 1, Role: albums.RolePage, File: name, Type: albums.TypeJPEG, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), W: 8, H: 8}
		man := albums.Manifest{Schema: 1, Generator: "t", Title: "Т", Book: albums.Book{PageMM: albums.Size{W: 100, H: 100}}, Pages: []albums.Page{pg, {N: 2, Role: albums.RolePage, Blank: true}}}
		now := time.Now().UTC()
		m := &store.Meta{Format: 1, ID: id, Owner: "p1", Created: now, Updated: now, Expires: now.Add(48 * time.Hour),
			Published: &store.Revision{At: now, Manifest: man}, Epoch: int64(3 * (1 - i))}
		if err := st.CreateAlbum(m); err != nil {
			t.Fatal(err)
		}
		part, err := st.NewPart()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := st.CommitPage("p1", id, name, part); err != nil {
			t.Fatal(err)
		}
	}
	return first
}

func readMeta(t *testing.T, dir, id string) *store.Meta {
	t.Helper()
	st, err := store.OpenReadOnly(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	m, err := st.ReadMeta("p1", id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// tree — все пути под dir с размером и временем изменения: verify ничего не должен менять.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		lines = append(lines, rel+" "+fi.Mode().String()+" "+fi.ModTime().String()+" "+strings.Repeat("x", int(fi.Size()%7)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	page := seed(t, dir)
	before := tree(t, dir)
	var out, errb bytes.Buffer
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitOK || !strings.Contains(out.String(), "проверено альбомов: 2, проблем: 0") {
		t.Fatalf("целая копия: код %d, %q %q", code, out.String(), errb.String())
	}
	if tree(t, dir) != before {
		t.Fatal("verify изменил каталог (должен только читать)")
	}

	// Копия на носителе только для чтения: verify всё равно работает (ни замка, ни записи).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitOK {
		t.Fatalf("каталог только для чтения: код %d, %q", code, errb.String())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Нет файла страницы; id в вывод не попадает.
	if err := os.Remove(filepath.Join(dir, "owners", "p1", "albums", idOne, page)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(out.String(), "нет файла страницы 1") {
		t.Fatalf("нет страницы: код %d, %q", code, out.String())
	}
	if strings.Contains(out.String(), idOne) || strings.Contains(out.String(), idTwo) {
		t.Fatalf("id альбома в выводе verify: %q", out.String())
	}

	// Размер файла не как в манифесте; негодная meta; формат новее своего.
	second := filepath.Join(dir, "owners", "p1", "albums", idTwo)
	files, err := os.ReadDir(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".jpg") {
			if err := os.WriteFile(filepath.Join(second, f.Name()), []byte("короче"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	out.Reset()
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(out.String(), "размер файла страницы 1") {
		t.Fatalf("размер: код %d, %q", code, out.String())
	}
	if err := os.WriteFile(filepath.Join(second, "meta.json"), []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(out.String(), "meta.json негоден") {
		t.Fatalf("негодная meta: код %d, %q", code, out.String())
	}
	if err := os.WriteFile(filepath.Join(second, "meta.json"), []byte(`{"format":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"verify", "-data", dir}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(out.String(), "новее") {
		t.Fatalf("новый формат: код %d, %q", code, out.String())
	}
	// Проблемы перечислены все, а не только первая.
	if got := strings.Count(out.String(), "проблема:"); got != 2 {
		t.Fatalf("проблем в выводе %d: %q", got, out.String())
	}

	// Не data/ вовсе: ничего не создаётся.
	empty := t.TempDir()
	out.Reset()
	errb.Reset()
	if code := run([]string{"verify", "-data", empty}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("пустой каталог: код %d", code)
	}
	if ents, _ := os.ReadDir(empty); len(ents) != 0 {
		t.Fatalf("verify создал файлы: %v", ents)
	}
}

func TestAfterRestoreEpoch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seed(t, dir)
	// Снимок месячной давности: last_seen старше 30 суток включил бы сторож часов.
	st, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLayout(store.Layout{LastSeen: time.Now().Add(-40 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"after-restore", "-data", dir}, &out, &errb, rand.Reader); code != exitOK {
		t.Fatalf("код %d, %q %q", code, out.String(), errb.String())
	}
	if m := readMeta(t, dir, idOne); m.Epoch != 4 {
		t.Fatalf("epoch первого альбома %d, ждали 4", m.Epoch)
	}
	if m := readMeta(t, dir, idTwo); m.Epoch != 1 {
		t.Fatalf("epoch второго альбома %d, ждали 1", m.Epoch)
	}
	ro, err := store.OpenReadOnly(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	l := ro.Layout()
	_ = ro.Close()
	if d := time.Since(l.LastSeen); d < 0 || d > time.Minute || l.ClockHold != nil {
		t.Fatalf("last_seen после восстановления: %v назад, пауза %+v", d, l.ClockHold)
	}
	// Сервис после этого стартует без паузы часов и видит альбомы.
	st2, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	svc, err := album.New(st2, album.Config{PublicBase: "https://x.test", Limits: albums.DefaultLimits(), UnpublishedTTL: time.Hour,
		Owners: map[string]album.OwnerLimits{"p1": {QuotaBytes: 1 << 20, MaxAlbums: 5}}},
		album.Options{Passwords: access.NewPasswords(bytes.Repeat([]byte{7}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Layout().ClockHold != nil || len(svc.List("p1")) != 2 {
		t.Fatalf("после восстановления: пауза %+v, альбомов %d", st2.Layout().ClockHold, len(svc.List("p1")))
	}
}

func TestAfterRestoreLocked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seed(t, dir)
	running, err := store.Open(dir, store.Options{}) // «работающий сервис» держит flock
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = running.Close() }()
	var out, errb bytes.Buffer
	if code := run([]string{"after-restore", "-data", dir}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(errb.String(), "занят") {
		t.Fatalf("при работающем сервисе: код %d, %q", code, errb.String())
	}
	if code := run([]string{"-clock-ok", "-data", dir}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("-clock-ok при работающем сервисе: код %d", code)
	}
	if m := readMeta(t, dir, idOne); m.Epoch != 3 {
		t.Fatalf("epoch изменён несмотря на отказ: %d", m.Epoch)
	}
	// Не data/: нет LAYOUT — ничего не создаётся.
	empty := t.TempDir()
	if code := run([]string{"after-restore", "-data", empty}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("пустой каталог: код %d", code)
	}
	if ents, _ := os.ReadDir(empty); len(ents) != 0 {
		t.Fatalf("after-restore создал файлы: %v", ents)
	}
}

func TestClockOK(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seed(t, dir)
	st, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour).UTC()
	hold := &store.ClockHold{Reason: "last_seen старше 30 суток", At: time.Now().UTC()}
	if err := st.SetLayout(store.Layout{LastSeen: old, ClockHold: hold}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-clock-ok", "-data", dir}, &out, &errb, rand.Reader); code != exitOK || !strings.Contains(out.String(), "пауза снята") {
		t.Fatalf("код %d, %q %q", code, out.String(), errb.String())
	}
	ro, err := store.OpenReadOnly(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	l := ro.Layout()
	_ = ro.Close()
	if l.ClockHold != nil || time.Since(l.LastSeen) > time.Minute || time.Since(l.LastSeen) < 0 {
		t.Fatalf("после -clock-ok: %+v", l)
	}
	// Альбомы на месте и не тронуты.
	if m := readMeta(t, dir, idOne); m.Epoch != 3 {
		t.Fatalf("-clock-ok не должен менять альбомы: epoch %d", m.Epoch)
	}
	// Повторный запуск без паузы — не ошибка.
	out.Reset()
	if code := run([]string{"-clock-ok", "-data", dir}, &out, &errb, rand.Reader); code != exitOK || !strings.Contains(out.String(), "паузы не было") {
		t.Fatalf("повтор: код %d, %q", code, out.String())
	}
	// Без -data и не data/ — отказ.
	if code := run([]string{"-clock-ok"}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("без -data: код %d", code)
	}
	if code := run([]string{"-clock-ok", "-data", t.TempDir()}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("не data/: код %d", code)
	}
}
