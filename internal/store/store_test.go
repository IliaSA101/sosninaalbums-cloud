package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreAtomicWrite(t *testing.T) {
	e := open(t)
	m := e.create(t, "p1", idA)
	m.Epoch = 7
	if err := e.WriteMeta(m); err != nil {
		t.Fatal(err)
	}
	got, err := e.ReadMeta("p1", idA)
	if err != nil || got.Epoch != 7 || got.Format != MetaFormat {
		t.Fatalf("ReadMeta: %+v %v", got, err)
	}
	e.noPartFiles(t)

	// Читатели видят только целую старую или целую новую meta, пока писатель переписывает файл.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				g, err := e.ReadMeta("p1", idA)
				if err != nil {
					t.Errorf("читатель увидел негодную meta: %v", err)
					return
				}
				if g.Epoch < 7 || g.Epoch > 207 {
					t.Errorf("epoch %d вне записанных", g.Epoch)
					return
				}
			}
		}()
	}
	for i := int64(8); i <= 207; i++ {
		m.Epoch = i
		if err := e.WriteMeta(m); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	e.noPartFiles(t)
}

func TestStorePerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o755); err != nil { // заранее созданный том: права шире нужных
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := openIn(t, dir)
	e.create(t, "p1", idA)
	addPage(t, e.Store, "p1", idA, []byte("тело страницы"))
	if err := e.SetLayout(Layout{LastSeen: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteAlbum("p1", idA); err != nil {
		t.Fatal(err)
	}
	e.create(t, "p1", idB)
	var seen int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		seen++
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: права %o, нужны %o", strings.TrimPrefix(p, dir), fi.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 8 {
		t.Fatalf("обошли подозрительно мало: %d", seen)
	}
}

func TestStoreRootEscape(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	badIDs := []string{
		"", "..", "../" + idA[:23], idA[:25], idA + "a", strings.ToUpper(idA), "%2e%2e%2f" + idA[:17],
		"..%2f" + idA[:21], idA[:25] + "/", idA[:24] + "/.", "/" + idA[:25], idA[:25] + "\x00", idA[:25] + "1", idA[:25] + "8",
		idA[:25] + "-", "a/../" + idA[:20], "\\..\\" + idA[:22],
	}
	badOwners := []string{"", "..", ".", "P1", "p/1", "../p1", "-p1", strings.Repeat("a", 33), "p1/..", "p1\x00"}
	badPages := []string{
		"", "meta.json", "../meta.json", "..%2fmeta.json", pageName([]byte("x"))[:63] + ".jpg",
		strings.Repeat("A", 64) + ".jpg", strings.Repeat("a", 64) + ".png", strings.Repeat("a", 64) + ".jpg/",
		"../" + pageName([]byte("x")), strings.Repeat("a", 64) + ".jpg\x00", "a/" + pageName([]byte("x")),
	}
	goodPage := pageName([]byte("ok"))
	for _, id := range badIDs {
		if _, err := e.ReadMeta("p1", id); !errors.Is(err, ErrBadName) {
			t.Errorf("ReadMeta id=%q: %v", id, err)
		}
		if err := e.DeleteAlbum("p1", id); !errors.Is(err, ErrBadName) {
			t.Errorf("DeleteAlbum id=%q: %v", id, err)
		}
		if err := e.Quarantine("p1", id); !errors.Is(err, ErrBadName) {
			t.Errorf("Quarantine id=%q: %v", id, err)
		}
		if err := e.CreateAlbum(newMeta("p1", id)); !errors.Is(err, ErrBadName) {
			t.Errorf("CreateAlbum id=%q: %v", id, err)
		}
		if err := e.WriteMeta(newMeta("p1", id)); !errors.Is(err, ErrBadName) {
			t.Errorf("WriteMeta id=%q: %v", id, err)
		}
		if _, err := e.OpenPage("p1", id, goodPage); !errors.Is(err, ErrBadName) {
			t.Errorf("OpenPage id=%q: %v", id, err)
		}
		if _, err := e.ListPages("p1", id); !errors.Is(err, ErrBadName) {
			t.Errorf("ListPages id=%q: %v", id, err)
		}
		if _, err := e.PrunePages("p1", id, func(string) bool { return false }); !errors.Is(err, ErrBadName) {
			t.Errorf("PrunePages id=%q: %v", id, err)
		}
	}
	for _, o := range badOwners {
		if _, err := e.ReadMeta(o, idA); !errors.Is(err, ErrBadName) {
			t.Errorf("ReadMeta owner=%q: %v", o, err)
		}
		if err := e.CreateAlbum(newMeta(o, idB)); !errors.Is(err, ErrBadName) {
			t.Errorf("CreateAlbum owner=%q: %v", o, err)
		}
	}
	for _, n := range badPages {
		if _, err := e.OpenPage("p1", idA, n); !errors.Is(err, ErrBadName) {
			t.Errorf("OpenPage name=%q: %v", n, err)
		}
		if _, err := e.PageSize("p1", idA, n); !errors.Is(err, ErrBadName) {
			t.Errorf("PageSize name=%q: %v", n, err)
		}
		p, err := e.NewPart()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.CommitPage("p1", idA, n, p); !errors.Is(err, ErrBadName) {
			t.Errorf("CommitPage name=%q: %v", n, err)
		}
	}
	e.noPartFiles(t) // отвергнутый CommitPage не оставляет .part
	if got := e.names(t, "owners"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("плохие имена создали каталоги: %v", got)
	}
}

// Символическая ссылка внутри data/ наружу не работает: os.Root её не пропускает.
func TestStoreRootEscapeSymlink(t *testing.T) {
	e := open(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "meta.json"), []byte(`{"format":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Каталог альбома — ссылка наружу.
	if err := os.MkdirAll(e.abs("owners/p1/albums"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("owners/p1/albums/"+idA)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadMeta("p1", idA); err == nil {
		t.Fatal("meta прочитана через ссылку наружу")
	}
	m := newMeta("p1", idA)
	if err := e.WriteMeta(m); err == nil {
		t.Fatal("meta записана через ссылку наружу")
	}
	p, err := e.NewPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := e.CommitPage("p1", idA, pageName([]byte("x")), p); err == nil {
		t.Fatal("страница записана через ссылку наружу")
	}
	// Удаление переносит саму ссылку в корзину и стирает её; цель за пределами data/ не тронута.
	if err := e.DeleteAlbum("p1", idA); err != nil {
		t.Fatalf("DeleteAlbum ссылки: %v", err)
	}
	ents, err := os.ReadDir(outside)
	if err != nil || len(ents) != 1 || ents[0].Name() != "meta.json" {
		t.Fatalf("снаружи что-то появилось или исчезло: %v %v", ents, err)
	}
	// Подмена tmp/ ссылкой наружу: приём страницы не должен писать за пределы data/.
	if err := os.RemoveAll(e.abs("tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, e.abs("tmp")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NewPart(); err == nil {
		t.Fatal("временный файл создан за пределами data/")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 {
		t.Fatalf("снаружи появились файлы: %v", ents)
	}
}

func TestStoreLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := openIn(t, dir)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("второй экземпляр: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("после Close замок должен сниматься: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	// Замок не слетает от записи LAYOUT (он переписывается rename-ом, замок — на отдельном файле).
	s3 := openIn(t, dir)
	for i := 0; i < 3; i++ {
		if err := s3.SetLayout(Layout{LastSeen: t0.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
			t.Fatalf("после SetLayout #%d замок потерян: %v", i, err)
		}
	}
}

func TestLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := openIn(t, dir)
	l := e.Layout()
	if l.Layout != LayoutVersion || !l.LastSeen.Equal(t0) || l.ClockHold != nil {
		t.Fatalf("новый LAYOUT: %+v", l)
	}
	hold := &ClockHold{Reason: "скачок вперёд", At: t0.Add(time.Minute)}
	if err := e.SetLayout(Layout{LastSeen: t0.Add(2 * time.Minute), ClockHold: hold}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := openIn(t, dir)
	l = e2.Layout()
	if !l.LastSeen.Equal(t0.Add(2*time.Minute)) || l.ClockHold == nil || l.ClockHold.Reason != hold.Reason {
		t.Fatalf("LAYOUT после перезапуска: %+v", l)
	}
	// Версия новее — отказ старта; замок при этом отпущен.
	if err := e2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "LAYOUT"), []byte(`{"layout":2,"last_seen":"2026-10-31T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("LAYOUT 2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "LAYOUT"), []byte(`{"layout":1,"last_seen":"2026-10-31T12:00:00Z","будущее":{"a":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e3 := openIn(t, dir)
	if err := e3.SetLayout(Layout{LastSeen: t0}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e3.read(t, "LAYOUT"), `"будущее":{"a":1}`) {
		t.Fatalf("неизвестное поле LAYOUT потеряно: %s", e3.read(t, "LAYOUT"))
	}
	if err := os.WriteFile(filepath.Join(dir, "LAYOUT"), []byte(`{не json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = e3.Close()
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrCorruptMeta) {
		t.Fatalf("битый LAYOUT: %v", err)
	}
}

func TestOpErrorHasNoPathsOrIDs(t *testing.T) {
	e := open(t)
	_, err1 := e.ReadMeta("p1", idA) // каталога нет
	err2 := e.WriteMeta(newMeta("p1", idA))
	p, _ := e.NewPart()
	err3 := e.CommitPage("p1", idA, pageName([]byte("x")), p)
	_, err4 := e.PageSize("p1", idA, pageName([]byte("x")))
	err5 := e.DeleteAlbum("p1", idA)
	tag := e.tagger.Tag(idA)
	for i, err := range []error{err1, err2, err3, err4, err5} {
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("#%d: ждали fs.ErrNotExist, получили %v", i+1, err)
		}
		mustNotContain(t, err.Error(), e.dir, idA, "owners", ".part", "p1", pageName([]byte("x")))
		var oe *OpError
		if !errors.As(err, &oe) || (oe.Album != tag && oe.Album != "") {
			t.Fatalf("#%d: метка альбома: %v", i+1, err)
		}
	}
	if !strings.Contains(err1.Error(), tag) {
		t.Fatalf("в ошибке нет метки альбома: %v", err1)
	}
	if !strings.Contains(err1.Error(), "no such file or directory") {
		t.Fatalf("в ошибке нет errno: %v", err1)
	}
}

func TestFreeBytes(t *testing.T) {
	e := open(t)
	n, err := e.FreeBytes()
	if err != nil || n == 0 {
		t.Fatalf("FreeBytes: %d %v", n, err)
	}
}

func TestPages(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	a := addPage(t, e.Store, "p1", idA, []byte("страница-1"))
	b := addPage(t, e.Store, "p1", idA, []byte("страница-2"))
	if got, err := e.PageSize("p1", idA, a); err != nil || got != int64(len("страница-1")) {
		t.Fatalf("PageSize: %d %v", got, err)
	}
	f, err := e.OpenPage("p1", idA, b)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _ := f.Read(buf)
	if string(buf[:n]) != "страница-2" {
		t.Fatalf("содержимое: %q", buf[:n])
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Повторный приём той же страницы: успех, .part убран, файл тот же.
	if again := addPage(t, e.Store, "p1", idA, []byte("страница-1")); again != a {
		t.Fatalf("имя %s != %s", again, a)
	}
	e.noPartFiles(t)
	got, err := e.ListPages("p1", idA)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListPages: %v %v", got, err)
	}
	// meta.json и посторонние файлы не страницы и не прореживаются.
	e.write(t, "owners/p1/albums/"+idA+"/stray.txt", "x")
	removed, err := e.PrunePages("p1", idA, func(n string) bool { return n == a })
	if err != nil || removed != 1 {
		t.Fatalf("PrunePages: %d %v", removed, err)
	}
	if _, err := e.PageSize("p1", idA, b); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("страница b должна исчезнуть: %v", err)
	}
	if !e.exists("owners/p1/albums/"+idA+"/meta.json") || !e.exists("owners/p1/albums/"+idA+"/stray.txt") {
		t.Fatal("PrunePages тронул не страницы")
	}
	// Abort идемпотентен и убирает файл.
	p, _ := e.NewPart()
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	e.noPartFiles(t)
}

func TestCreateAlbumExists(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	if err := e.CreateAlbum(newMeta("p1", idA)); !errors.Is(err, ErrExists) {
		t.Fatalf("повтор id: %v", err)
	}
	// Тот же id у другого владельца — другой каталог (индекс id общий, но это решает album).
	if err := e.CreateAlbum(newMeta("p2", idA)); err != nil {
		t.Fatalf("тот же id у другого владельца: %v", err)
	}
}

func TestCreateAlbumFailureLeavesNoDir(t *testing.T) {
	e := open(t)
	e.hook = func(step string) error {
		if step == stepWrite {
			return errors.New("отказ записи")
		}
		return nil
	}
	if err := e.CreateAlbum(newMeta("p1", idA)); err == nil {
		t.Fatal("отказ записи meta проглочен")
	}
	e.hook = nil
	if e.exists("owners/p1/albums/"+idA) || len(e.names(t, "trash")) != 0 {
		t.Fatal("после неудачного создания остался каталог или мусор в корзине")
	}
	e.noPartFiles(t)
}

// Страница и meta удалённого альбома не воскрешают каталог: MkdirAll есть только в CreateAlbum.
func TestNoResurrect(t *testing.T) {
	e := open(t)
	m := e.create(t, "p1", idA)
	p, _ := e.NewPart()
	if _, err := p.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteAlbum("p1", idA); err != nil {
		t.Fatal(err)
	}
	if err := e.CommitPage("p1", idA, pageName([]byte("x")), p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CommitPage в удалённый альбом: %v", err)
	}
	if err := e.WriteMeta(m); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("WriteMeta в удалённый альбом: %v", err)
	}
	if e.exists("owners/p1/albums/" + idA) {
		t.Fatal("каталог удалённого альбома воскрес")
	}
	e.noPartFiles(t)
	if err := e.DeleteAlbum("p1", idA); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

// Гонка: приём страницы, запись meta и удаление одного альбома под -race; итог — каталога нет, мусора нет.
// Контракт store: CommitPage, WriteMeta и DeleteAlbum одного альбома вызываются под его мьютексом (его держит
// album, Г2); здесь он — один sync.Mutex. Тело страницы, как и в сервисе, пишется в .part без блокировки.
func TestStoreRaceCommitDelete(t *testing.T) {
	for round := 0; round < 20; round++ {
		e := open(t)
		m := e.create(t, "p1", idA)
		var wg sync.WaitGroup
		var album sync.Mutex
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				data := []byte{byte(i), 1, 2, 3}
				p, err := e.NewPart()
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := p.Write(data); err != nil {
					t.Error(err)
					return
				}
				album.Lock()
				defer album.Unlock()
				err = e.CommitPage("p1", idA, pageName(data), p)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("CommitPage: %v", err)
				}
				if err := e.WriteMeta(m); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("WriteMeta: %v", err)
				}
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			album.Lock()
			defer album.Unlock()
			if err := e.DeleteAlbum("p1", idA); err != nil {
				t.Errorf("DeleteAlbum: %v", err)
			}
		}()
		wg.Wait()
		if e.exists("owners/p1/albums/" + idA) {
			t.Fatal("каталог жив после удаления")
		}
		e.noPartFiles(t)
		if n := e.names(t, "trash"); len(n) != 0 {
			t.Fatalf("корзина не пуста: %v", n)
		}
	}
}
