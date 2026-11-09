package album

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// Гонки приёма страницы с удалением, правкой и уборкой, publish с приёмом, уборки с PATCH. Тело страницы
// читается без блокировки в tmp/; финал — под мьютексом альбома с повторной проверкой (design §3).

func (e *env) albumDir(id string) string { return filepath.Join(e.dir, "owners", pOne, "albums", id) }

func (e *env) noParts(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(e.dir, "tmp"))
	if err != nil || len(ents) != 0 {
		t.Fatalf("в tmp/ остались файлы: %v, %v", ents, err)
	}
}

func (e *env) noDir(t *testing.T, id string) {
	t.Helper()
	if _, err := os.Stat(e.albumDir(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("каталог удалённого альбома воскрес или остался: %v", err)
	}
}

// pending — страница, тело которой уже принято в tmp/, но финал ещё не наступил.
func (e *env) pending(owner, id string, data []byte) func() error {
	part := e.part(data)
	return func() error { return e.commit(owner, id, data, part) }
}

func TestPutPageRaceDelete(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("A", d1, d2), "")
	finish := e.pending(pOne, cr.ID, d1)
	if err := e.svc.Delete(pOne, cr.ID); err != nil { // удаление, пока читалось тело
		t.Fatal(err)
	}
	wantErr(t, finish(), ErrNotFound)
	e.noDir(t, cr.ID)
	e.noParts(t)

	// Конкурентно: приём и удаление сходятся в любом порядке, каталог не воскресает, tmp чист.
	for i := range 40 {
		cr := e.create(pOne, book("A", d1, d2), "")
		var wg sync.WaitGroup
		var putErr error
		wg.Add(2)
		go func() { defer wg.Done(); putErr = e.put(pOne, cr.ID, d1) }()
		go func() { defer wg.Done(); _ = e.svc.Delete(pOne, cr.ID) }()
		wg.Wait()
		if putErr != nil && !errors.Is(putErr, ErrNotFound) {
			t.Fatalf("итерация %d: %v", i, putErr)
		}
		e.noDir(t, cr.ID)
	}
	e.noParts(t)
}

func TestPutPageRaceUpdate(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("A", d1, d2), "")
	finish := e.pending(pOne, cr.ID, d1)
	// Правка заменила редакцию: d1 больше нет ни в published, ни в draft.
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d4)); err != nil {
		t.Fatal(err)
	}
	wantErr(t, finish(), ErrPageGone)
	if e.hasFile(pOne, cr.ID, d1) {
		t.Fatal("файл ненужной страницы остался в каталоге")
	}
	e.noParts(t)
	if !e.inIndex(cr.ID) {
		t.Fatal("отказ страницы не удалил альбом")
	}

	// Конкурентно: страница, которой нет в итоговой редакции, на диске не остаётся.
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = e.put(pOne, cr.ID, d1) }()
		go func() {
			defer wg.Done()
			if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d4)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d4)); err != nil { // финальная правка: d1 вне редакции
		t.Fatal(err)
	}
	if e.hasFile(pOne, cr.ID, d1) {
		t.Fatalf("после правки без d1 на диске остался его файл: %v", e.files(pOne, cr.ID))
	}
	e.noParts(t)
}

func TestPutPageRaceSweep(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("A", d1, d2), "")
	other := e.create(pOne, book("B", d3, d4), "")
	finish := e.pending(pOne, cr.ID, d1)
	live := e.pending(pOne, other.ID, d3)
	e.clk.advance(25 * time.Hour) // оба альбома брошены (ни разу не опубликованы)
	st, err := e.svc.Sweep()
	if err != nil || st.Abandoned != 2 {
		t.Fatalf("уборка: %+v, %v", st, err)
	}
	wantErr(t, finish(), ErrNotFound)
	wantErr(t, live(), ErrNotFound)
	e.noDir(t, cr.ID)
	e.noDir(t, other.ID)
	e.noParts(t)

	// Идущий приём живого альбома уборка не режет: свежий .part (моложе 600 с) остаётся.
	alive := e.create(pOne, book("C", d1, d2), "")
	running := e.pending(pOne, alive.ID, d1)
	if _, err := e.svc.Sweep(); err != nil {
		t.Fatal(err)
	}
	if err := running(); err != nil {
		t.Fatalf("уборка порезала идущий приём: %v", err)
	}
	if !e.hasFile(pOne, alive.ID, d1) {
		t.Fatal("страница не записана")
	}

	// Конкурентно: уборка и приём в любом порядке — ни сирот, ни воскресших каталогов.
	for range 20 {
		id := e.create(pOne, book("D", d1, d2), "").ID
		e.clk.advance(25 * time.Hour)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = e.put(pOne, id, d1) }()
		go func() { defer wg.Done(); _, _ = e.svc.Sweep() }()
		wg.Wait()
		if _, err := e.svc.Sweep(); err != nil {
			t.Fatal(err)
		}
		e.noDir(t, id)
	}
	e.noParts(t)
}

func TestPublishRacePut(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Options) { c.Owners[pOne] = OwnerLimits{QuotaBytes: 1 << 30, MaxAlbums: 1000} })
	for i := range 40 {
		cr := e.create(pOne, book("A", d1, d2), "")
		e.mustPut(pOne, cr.ID, d1)
		var wg sync.WaitGroup
		var pubErr, putErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, pubErr = e.svc.Publish(pOne, cr.ID) }()
		go func() { defer wg.Done(); putErr = e.put(pOne, cr.ID, d2) }()
		wg.Wait()
		var me *MissingError
		if pubErr != nil && !errors.As(pubErr, &me) {
			t.Fatalf("итерация %d: publish: %v", i, pubErr)
		}
		if putErr != nil {
			t.Fatalf("итерация %d: put: %v", i, putErr)
		}
		// Инвариант: опубликованная редакция всегда с полным набором файлов.
		if pubErr == nil {
			m := e.svc.lookup(cr.ID).meta.Load()
			if m.Published == nil || !e.hasFile(pOne, cr.ID, d1) || !e.hasFile(pOne, cr.ID, d2) {
				t.Fatalf("итерация %d: опубликован без файла: %v", i, e.files(pOne, cr.ID))
			}
		}
		if _, err := e.svc.Publish(pOne, cr.ID); err != nil { // страница на месте — publish проходит всегда
			t.Fatalf("итерация %d: повторный publish: %v", i, err)
		}
		if _, err := e.svc.Access(cr.ID); err != nil {
			t.Fatalf("итерация %d: %v", i, err)
		}
	}
	e.noParts(t)
}

func TestSweepRacePatch(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Options) { c.Owners[pOne] = OwnerLimits{QuotaBytes: 1 << 30, MaxAlbums: 1000} })
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	// Срок продлён, затем часы дошли до прежнего срока: уборка смотрит на текущую meta, а не на прежнюю.
	far := t0.Add(30 * 24 * time.Hour)
	if _, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{ExpiresAt: &far}); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(49 * time.Hour)
	st, err := e.svc.Sweep()
	if err != nil || st.Expired != 0 || !e.inIndex(cr.ID) {
		t.Fatalf("продлённый альбом убран: %+v, %v", st, err)
	}

	// Конкурентно: PATCH сроков и уборка; в конце память, диск и учёт квоты сходятся.
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = e.published(pOne, book("A", d1, d2), d1, d2).ID
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(3)
		go func() {
			defer wg.Done()
			soon := e.clk.Wall().Add(time.Second)
			_, _ = e.svc.Patch(pOne, id, &albums.Patch{ExpiresAt: &soon, RevokeSessions: true})
		}()
		go func() { defer wg.Done(); e.clk.advance(2 * time.Second); _, _ = e.svc.Sweep() }()
		go func() {
			defer wg.Done()
			far := e.clk.Wall().Add(time.Hour)
			_, _ = e.svc.Patch(pOne, id, &albums.Patch{ExpiresAt: &far})
		}()
	}
	wg.Wait()
	var want int64
	count := 0
	for _, id := range append(ids, cr.ID) {
		a := e.svc.lookup(id)
		_, statErr := os.Stat(e.albumDir(id))
		switch {
		case a == nil && statErr == nil:
			t.Fatalf("альбом убран из индекса, а каталог на месте")
		case a != nil && statErr != nil:
			t.Fatalf("альбом в индексе, а каталога нет: %v", statErr)
		case a != nil:
			m, err := e.st.ReadMeta(pOne, id)
			if err != nil || !m.Expires.Equal(a.meta.Load().Expires) || m.Epoch != a.meta.Load().Epoch {
				t.Fatalf("meta на диске и в памяти разошлись: %v", err)
			}
			want += a.usage
			count++
		}
	}
	if e.svc.used[pOne] != want || e.svc.count[pOne] != count {
		t.Fatalf("учёт квоты разошёлся с индексом: %d/%d и %d/%d", e.svc.used[pOne], e.svc.count[pOne], want, count)
	}
}
