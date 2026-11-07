package store

import (
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	trashA = idA + ".0123456789abcdef"
	trashB = idB + ".fedcba9876543210"
)

func TestSweepAtStart(t *testing.T) {
	e := open(t)
	good := e.create(t, "p1", idA)
	addPage(t, e.Store, "p1", idA, []byte("живая страница"))

	e.write(t, "tmp/aaaa.part", "обрывок приёма")            // свежий: при старте всё равно к удалению
	e.write(t, "tmp/bbbb.part", "ещё обрывок")               // и такой
	e.write(t, "trash/"+trashB+"/x.jpg", "хвост")            // недочищенная корзина
	e.write(t, "trash/странное-имя", "тоже мусор")           // и с нестандартным именем
	e.write(t, "owners/p1/albums/"+idC+"/x.jpg", "без meta") // каталог без meta.json
	e.write(t, "owners/p2/albums/"+idB+"/"+pageName([]byte("z")), "без meta у другого владельца")
	e.write(t, "quarantine/"+trashB+"/meta.json", "разбор вручную") // карантин не трогаем

	metas, err := e.LoadAll()
	if err != nil || len(metas) != 1 {
		t.Fatalf("LoadAll: %d %v", len(metas), err)
	}
	st, err := e.SweepAtStart()
	if err != nil {
		t.Fatalf("SweepAtStart: %v", err)
	}
	if st.Tmp != 2 || st.Trash != 2 || st.Orphans != 2 {
		t.Fatalf("убрано: %+v", st)
	}
	for _, d := range []string{"tmp", "trash"} {
		if n := e.names(t, d); len(n) != 0 {
			t.Fatalf("%s/ не пуст: %v", d, n)
		}
	}
	if e.exists("owners/p1/albums/"+idC) || e.exists("owners/p2/albums/"+idB) {
		t.Fatal("каталоги без meta остались")
	}
	if !e.exists("owners/p1/albums/"+good.ID+"/meta.json") || len(e.names(t, "owners/p1/albums/"+idA)) != 2 {
		t.Fatal("исправный альбом задет")
	}
	if !e.exists("quarantine/" + trashB + "/meta.json") {
		t.Fatal("карантин задет уборкой")
	}
	// Повтор — ничего не находит.
	if st, err := e.SweepAtStart(); err != nil || st != (SweepStats{}) {
		t.Fatalf("повторная уборка: %+v %v", st, err)
	}
}

func TestSweepSparesActive(t *testing.T) {
	e := open(t, func(o *Options) { o.Clock = time.Now })
	e.create(t, "p1", idA)

	// Идущий PUT: открытый Part; чужой writeAtomic: свежий .part; окно POST: каталог без meta.
	part, err := e.NewPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("тело в пути")); err != nil {
		t.Fatal(err)
	}
	e.write(t, "tmp/atomic-in-flight.part", "meta в записи")
	e.write(t, "owners/p1/albums/"+idB+"/x", "") // свежий каталог без meta (POST между MkdirAll и meta)
	e.write(t, "trash/"+trashA+"/x", "свежая корзина")

	g := &guard{inIndex: map[string]bool{}}
	st, err := e.SweepRunning(g)
	if err != nil {
		t.Fatal(err)
	}
	if st != (SweepStats{}) {
		t.Fatalf("уборка тронула живое: %+v", st)
	}
	if len(g.calls) != 0 {
		t.Fatalf("мьютексы свежих каталогов не должны даже браться: %v", g.calls)
	}
	// Идущий PUT доходит до конца: файл на месте и принимается.
	name := pageName([]byte("тело в пути"))
	if err := e.CommitPage("p1", idA, name, part); err != nil {
		t.Fatalf("PUT после уборки: %v", err)
	}
	if !e.exists("tmp/atomic-in-flight.part") {
		t.Fatal("свежий .part чужой записи удалён")
	}

	// Состарившееся: tmp, корзина и каталог без meta — убираются, но только под мьютексом и вне индекса.
	e.age(t, "tmp/atomic-in-flight.part", ActiveAge+time.Minute)
	e.age(t, "owners/p1/albums/"+idB+"/x", ActiveAge+time.Minute)
	e.age(t, "owners/p1/albums/"+idB, ActiveAge+time.Minute)
	e.age(t, "trash/"+trashA+"/x", ActiveAge+time.Minute)
	e.age(t, "trash/"+trashA, ActiveAge+time.Minute)

	// id каталога без meta уже есть в индексе (POST успел положить в индекс, meta ещё в пути): не трогаем.
	g = &guard{inIndex: map[string]bool{idB: true}}
	st, err = e.SweepRunning(g)
	if err != nil || st.Orphans != 0 || !e.exists("owners/p1/albums/"+idB) {
		t.Fatalf("каталог из индекса удалён: %+v %v", st, err)
	}
	if st.Tmp != 1 || st.Trash != 1 {
		t.Fatalf("старые tmp и корзина должны уйти: %+v", st)
	}
	// meta появилась, пока ждали мьютекс: повторная проверка под ним спасает каталог.
	g = &guard{inIndex: map[string]bool{}, before: func(id string) {
		if id == idB {
			e.write(t, "owners/p1/albums/"+idB+"/meta.json", metaRaw("p1", idB, ""))
		}
	}}
	st, err = e.SweepRunning(g)
	if err != nil || st.Orphans != 0 || !e.exists("owners/p1/albums/"+idB+"/meta.json") {
		t.Fatalf("каталог с появившейся meta удалён: %+v %v", st, err)
	}
	// Каталог без meta, давно, вне индекса — уходит.
	e.write(t, "owners/p1/albums/"+idC+"/x", "")
	e.age(t, "owners/p1/albums/"+idC+"/x", ActiveAge+time.Minute)
	e.age(t, "owners/p1/albums/"+idC, ActiveAge+time.Minute)
	g = &guard{inIndex: map[string]bool{}}
	st, err = e.SweepRunning(g)
	if err != nil || st.Orphans != 1 || e.exists("owners/p1/albums/"+idC) {
		t.Fatalf("давний сирота остался: %+v %v", st, err)
	}
	if len(g.calls) != 1 || g.calls[0] != idC {
		t.Fatalf("мьютекс должен браться только для кандидата: %v", g.calls)
	}
}

func TestSweepTrashInIndexSpared(t *testing.T) {
	e := open(t, func(o *Options) { o.Clock = time.Now })
	e.write(t, "trash/"+trashA+"/x", "x")
	e.age(t, "trash/"+trashA+"/x", ActiveAge+time.Minute)
	e.age(t, "trash/"+trashA, ActiveAge+time.Minute)
	// Мусорное имя в корзине и имя не по шаблону уборка «в работе» не трогает (это дело старта).
	e.write(t, "trash/странное-имя", "x")
	e.age(t, "trash/странное-имя", ActiveAge+time.Minute)
	g := &guard{inIndex: map[string]bool{idA: true}}
	if st, err := e.SweepRunning(g); err != nil || st.Trash != 0 || !e.exists("trash/"+trashA) || !e.exists("trash/странное-имя") {
		t.Fatalf("корзина с id из индекса: %+v %v", st, err)
	}
}

func TestTrashOnePurge(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	addPage(t, e.Store, "p1", idA, []byte("страница"))
	if err := e.DeleteAlbum("p1", idA); err != nil {
		t.Fatal(err)
	}
	if e.exists("owners/p1/albums/"+idA) || len(e.names(t, "trash")) != 0 {
		t.Fatal("после удаления остались каталог или корзина")
	}
	if _, err := e.ReadMeta("p1", idA); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadMeta удалённого: %v", err)
	}
	// purgeTrash берёт одно имя из trash/; всё с разделителями и точками отвергает.
	for _, bad := range []string{"", ".", "..", "../owners", "a/b", `a\b`, "trash/" + trashA, "/etc"} {
		if err := e.purgeTrash(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("purgeTrash(%q): %v", bad, err)
		}
	}
	e.create(t, "p1", idB)
	if err := e.purgeTrash(trashA); err != nil { // нет такого — не ошибка (RemoveAll)
		t.Fatalf("purgeTrash отсутствующего: %v", err)
	}
	if !e.exists("owners/p1/albums/" + idB + "/meta.json") {
		t.Fatal("purgeTrash задел альбомы")
	}
	// Корзину чистят и удаление, и уборка — через ту же функцию: недочищенное уходит на старте.
	e.write(t, "trash/"+trashB+"/x.jpg", "хвост")
	if st, err := e.SweepAtStart(); err != nil || st.Trash != 1 || len(e.names(t, "trash")) != 0 {
		t.Fatalf("SweepAtStart корзины: %+v %v", st, err)
	}
}

// Уборка в работе идёт одновременно с приёмом, записью meta и созданием альбомов (-race): живое цело.
func TestSweepRacesWithWriters(t *testing.T) {
	e := open(t, func(o *Options) { o.Clock = time.Now })
	m := e.create(t, "p1", idA)
	g := &guard{inIndex: map[string]bool{idA: true}}
	var mu sync.Mutex
	locked := &lockedGuard{g: g, mu: &mu}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := e.SweepRunning(locked); err != nil {
				t.Errorf("SweepRunning: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		data := []byte{byte(i), 9, 9}
		p, err := e.NewPart()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := e.CommitPage("p1", idA, pageName(data), p); err != nil {
			t.Fatalf("CommitPage под уборкой: %v", err)
		}
		cp := *m
		cp.Epoch = int64(i)
		if err := e.WriteMeta(&cp); err != nil {
			t.Fatalf("WriteMeta под уборкой: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if pages, err := e.ListPages("p1", idA); err != nil || len(pages) != 50 {
		t.Fatalf("страниц %d, %v", len(pages), err)
	}
	if _, err := e.ReadMeta("p1", idA); err != nil {
		t.Fatal(err)
	}
}

type lockedGuard struct {
	g  *guard
	mu *sync.Mutex
}

func (l *lockedGuard) WithAlbumLock(id string, fn func(bool) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.g.WithAlbumLock(id, fn)
}

func TestSweepRunningLogsNoIDs(t *testing.T) {
	e := open(t, func(o *Options) { o.Clock = time.Now })
	e.write(t, "tmp/old.part", "x")
	e.age(t, "tmp/old.part", ActiveAge+time.Minute)
	if st, err := e.SweepRunning(&guard{}); err != nil || st.Tmp != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	if !strings.Contains(e.log.String(), "уборка") {
		t.Fatalf("уборка ничего не записала в журнал: %q", e.log.String())
	}
	mustNotContain(t, e.log.String(), e.dir, "old.part")
}

// ActiveAge задаётся из конфига (наибольший срок приёма страницы): .part старше 600 с, но моложе заданного
// возраста, принадлежит ещё идущему медленному приёму и не режется.
func TestSweepActiveAgeOption(t *testing.T) {
	long := open(t, func(o *Options) { o.Clock = time.Now; o.ActiveAge = 2 * time.Hour })
	long.write(t, "tmp/slow.part", "медленный приём")
	long.age(t, "tmp/slow.part", time.Hour)
	if st, err := long.SweepRunning(&guard{}); err != nil || st.Tmp != 0 || !long.exists("tmp/slow.part") {
		t.Fatalf("при ActiveAge 2 ч часовой .part тронут: %+v, %v", st, err)
	}
	long.age(t, "tmp/slow.part", 3*time.Hour)
	if st, err := long.SweepRunning(&guard{}); err != nil || st.Tmp != 1 {
		t.Fatalf("старше ActiveAge должен убираться: %+v, %v", st, err)
	}
	def := open(t, func(o *Options) { o.Clock = time.Now })
	def.write(t, "tmp/old.part", "брошен")
	def.age(t, "tmp/old.part", time.Hour)
	if st, err := def.SweepRunning(&guard{}); err != nil || st.Tmp != 1 {
		t.Fatalf("по умолчанию 600 с: %+v, %v", st, err)
	}
}
