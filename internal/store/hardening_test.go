package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapshot — имена, права, размеры и mtime всего дерева: что-либо созданное, изменённое или удалённое
// OpenReadOnly и чтением меняет снимок.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(strings.TrimPrefix(p, dir) + " " + fi.Mode().String() + " " + fi.ModTime().String() + " ")
		if fi.Mode().IsRegular() {
			c, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.WriteString(string(c))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestOpenReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := openIn(t, dir)
	e.create(t, "p1", idA)
	name := addPage(t, e.Store, "p1", idA, []byte("страница"))
	// Боевой экземпляр жив и держит замок; копия «с бэкапа» — с правами шире, без LOCK.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(copyDir, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(copyDir, "LOCK")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Мусор в tmp/ и каталог-сирота: чтение их не убирает.
	if err := os.WriteFile(filepath.Join(copyDir, "tmp", "x.part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, copyDir)

	ro, err := OpenReadOnly(copyDir, Options{})
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if got := ro.Layout(); got.Layout != LayoutVersion {
		t.Fatalf("Layout: %+v", got)
	}
	metas, err := ro.LoadAll()
	if err != nil || len(metas) != 1 {
		t.Fatalf("LoadAll: %d %v", len(metas), err)
	}
	if refs, err := ro.AlbumDirs(); err != nil || len(refs) != 1 || refs[0] != (AlbumRef{"p1", idA}) {
		t.Fatalf("AlbumDirs: %v %v", refs, err)
	}
	if _, err := ro.ReadMeta("p1", idA); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.PageSize("p1", idA, name); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.ListPages("p1", idA); err != nil {
		t.Fatal(err)
	}
	f, err := ro.OpenPage("p1", idA, name)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Любое изменение — ErrReadOnly и ничего на диске.
	m := newMeta("p1", idB)
	mutators := map[string]error{
		"CreateAlbum": ro.CreateAlbum(m),
		"WriteMeta":   ro.WriteMeta(newMeta("p1", idA)),
		"DeleteAlbum": ro.DeleteAlbum("p1", idA),
		"Quarantine":  ro.Quarantine("p1", idA),
		"SetLayout":   ro.SetLayout(Layout{LastSeen: t0}),
	}
	if _, err := ro.NewPart(); err != nil {
		mutators["NewPart"] = err
	}
	_, mutators["PrunePages"] = ro.PrunePages("p1", idA, func(string) bool { return false })
	_, mutators["SweepAtStart"] = ro.SweepAtStart()
	_, mutators["SweepRunning"] = ro.SweepRunning(&guard{})
	if len(mutators) != 9 {
		t.Fatalf("NewPart не вернул ошибку: %v", mutators)
	}
	for name, err := range mutators {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, copyDir); after != before {
		t.Fatal("OpenReadOnly и чтение изменили дерево (создан LOCK, права, mtime, файлы)")
	}
}

func TestOpenReadOnlyRefuses(t *testing.T) {
	base := t.TempDir()
	if _, err := OpenReadOnly(filepath.Join(base, "нет"), Options{}); err == nil {
		t.Fatal("несуществующий каталог открыт")
	}
	if _, err := os.Stat(filepath.Join(base, "нет")); err == nil {
		t.Fatal("OpenReadOnly создал каталог")
	}
	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(empty, Options{}); !errors.Is(err, ErrNoLayout) {
		t.Fatalf("каталог без LAYOUT: %v", err)
	}
	if ents, _ := os.ReadDir(empty); len(ents) != 0 {
		t.Fatalf("в пустом каталоге что-то создано: %v", ents)
	}
	if err := os.WriteFile(filepath.Join(empty, "LAYOUT"), []byte(`{"layout":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(empty, Options{}); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("LAYOUT новее: %v", err)
	}
	if err := os.WriteFile(filepath.Join(empty, "LAYOUT"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(empty, Options{}); !errors.Is(err, ErrCorruptMeta) {
		t.Fatalf("битый LAYOUT: %v", err)
	}
}

// verify копии: негодная meta — ошибка вызывающему, а не перенос в карантин.
func TestOpenReadOnlyCorruptMetaNotQuarantined(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := openIn(t, dir)
	e.write(t, "owners/p1/albums/"+idA+"/meta.json", "{нет")
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.LoadAll(); !errors.Is(err, ErrCorruptMeta) {
		t.Fatalf("LoadAll: %v", err)
	}
	if !e.exists("owners/p1/albums/"+idA) || len(e.names(t, "quarantine")) != 0 {
		t.Fatal("verify перенёс каталог")
	}
}

// Формат смотрится до полного разбора: «новее» с несовместимой формой остальных полей — всё равно отказ
// старта, а не карантин.
func TestNewerFormatBeforeFullParse(t *testing.T) {
	e := open(t)
	e.write(t, "owners/p1/albums/"+idA+"/meta.json", `{"format":2,"id":123,"created":{"сменили":"вид"},"epoch":"строка"}`)
	if _, err := e.ReadMeta("p1", idA); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("ReadMeta: %v", err)
	}
	if _, err := e.LoadAll(); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(e.names(t, "quarantine")) != 0 || !e.exists("owners/p1/albums/"+idA+"/meta.json") {
		t.Fatal("новый формат ушёл в карантин")
	}
	dir := filepath.Join(t.TempDir(), "data")
	e2 := openIn(t, dir)
	_ = e2.Close()
	if err := os.WriteFile(filepath.Join(dir, "LAYOUT"), []byte(`{"layout":2,"last_seen":5,"clock_hold":"иначе"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("LAYOUT новее с иной формой: %v", err)
	}
}

// Возраст mtime — от настенных часов процесса: убежавшие вперёд часы хранилища не делают свежий tmp брошенным.
func TestSweepUsesWallClock(t *testing.T) {
	e := open(t, func(o *Options) { o.Clock = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) } })
	e.write(t, "tmp/fresh.part", "идущий приём")
	e.write(t, "owners/p1/albums/"+idB+"/x", "")
	if st, err := e.SweepRunning(&guard{}); err != nil || st != (SweepStats{}) {
		t.Fatalf("свежее убрано по часам хранилища: %+v %v", st, err)
	}
	if !e.exists("tmp/fresh.part") || !e.exists("owners/p1/albums/"+idB) {
		t.Fatal("свежее удалено")
	}
}

// Пока стоит пауза сторожа часов, tmp не убирается; после снятия — убирается.
func TestSweepSkipsTmpUnderClockHold(t *testing.T) {
	e := open(t)
	e.write(t, "tmp/old.part", "x")
	e.age(t, "tmp/old.part", ActiveAge+time.Hour)
	if err := e.SetLayout(Layout{LastSeen: t0, ClockHold: &ClockHold{Reason: "скачок", At: t0}}); err != nil {
		t.Fatal(err)
	}
	if st, err := e.SweepRunning(&guard{}); err != nil || st.Tmp != 0 || !e.exists("tmp/old.part") {
		t.Fatalf("tmp убран при паузе часов: %+v %v", st, err)
	}
	if err := e.SetLayout(Layout{LastSeen: t0}); err != nil {
		t.Fatal(err)
	}
	if st, err := e.SweepRunning(&guard{}); err != nil || st.Tmp != 1 || e.exists("tmp/old.part") {
		t.Fatalf("tmp не убран после снятия паузы: %+v %v", st, err)
	}
}

// Отказ fsync каталога после rename — ErrNotDurable: запись видна, CreateAlbum альбом не откатывает.
func TestNotDurableKeepsWrite(t *testing.T) {
	e := open(t)
	e.hook = func(step string) error {
		if step == stepSyncDir {
			return errors.New("fsync каталога отказал")
		}
		return nil
	}
	err := e.CreateAlbum(newMeta("p1", idA))
	if !errors.Is(err, ErrNotDurable) {
		t.Fatalf("ждали ErrNotDurable: %v", err)
	}
	mustNotContain(t, err.Error(), e.dir, idA)
	e.hook = nil
	if m, err := e.ReadMeta("p1", idA); err != nil || m.ID != idA {
		t.Fatalf("альбом после ErrNotDurable должен остаться: %v", err)
	}
}
