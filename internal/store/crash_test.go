package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Отказ ФС на каждом шаге записи и перезапуск (design §2): сирот нет, meta старая или новая.
//
// Два режима: «error» — шаг возвращает ошибку, store убирает за собой; «crash» — шаг паникует, и
// уборка не выполняется, как при обрыве питания; сирот в tmp/ убирает следующий старт.

type crashed struct{}

var errInjected = errors.New("отказ ФС (тест)")

// failAt ставит отказ на шаг step в режиме mode и запускает op; возвращает ошибку операции.
func failAt(e *env, mode, step string, op func() error) (err error) {
	e.hook = func(s string) error {
		if s != step {
			return nil
		}
		if mode == "crash" {
			panic(crashed{})
		}
		return errInjected
	}
	defer func() {
		e.hook = nil
		if r := recover(); r != nil {
			if _, ok := r.(crashed); !ok {
				panic(r)
			}
			err = errors.New("процесс оборвался")
		}
	}()
	return op()
}

var allSteps = []string{stepCreate, stepWrite, stepSync, stepRename, stepSyncDir}

// restart — «перезапуск»: старое хранилище закрыто, открыто новое, выполнены LoadAll и SweepAtStart.
func restart(t *testing.T, e *env, dir string) *env {
	t.Helper()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := openIn(t, dir)
	if _, err := e2.LoadAll(); err != nil {
		t.Fatalf("LoadAll после перезапуска: %v", err)
	}
	if _, err := e2.SweepAtStart(); err != nil {
		t.Fatalf("SweepAtStart после перезапуска: %v", err)
	}
	e2.noPartFiles(t)
	return e2
}

func TestStoreCrashEachStep(t *testing.T) {
	for _, mode := range []string{"error", "crash"} {
		for _, step := range allSteps {
			t.Run("meta/"+mode+"/"+step, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				e := openIn(t, dir)
				m := e.create(t, "p1", idA)
				m.Epoch = 1
				if err := e.WriteMeta(m); err != nil {
					t.Fatal(err)
				}
				m.Epoch = 2
				checkFailure(t, mode, step, failAt(e, mode, step, func() error { return e.WriteMeta(m) }))
				orphans := len(e.names(t, "tmp"))
				switch {
				case mode == "error" && orphans != 0:
					t.Fatalf("при отказе store обязан убрать .part за собой: %d", orphans)
				case mode == "crash" && (step == stepWrite || step == stepSync || step == stepRename) && orphans != 1:
					t.Fatalf("обрыв на %s должен оставить .part (иначе тест ничего не проверяет): %d", step, orphans)
				}
				e2 := restart(t, e, dir)
				got, err := e2.ReadMeta("p1", idA)
				if err != nil {
					t.Fatalf("meta нечитаема после сбоя: %v", err)
				}
				want := int64(1) // до rename — прежняя редакция, после — новая; смеси нет
				if step == stepSyncDir {
					want = 2
				}
				if got.Epoch != want {
					t.Fatalf("epoch %d, ждали %d", got.Epoch, want)
				}
			})

			t.Run("page/"+mode+"/"+step, func(t *testing.T) {
				if step == stepCreate || step == stepWrite {
					t.Skip("создание и запись .part — NewPart/Write, не шаги CommitPage")
				}
				dir := filepath.Join(t.TempDir(), "data")
				e := openIn(t, dir)
				e.create(t, "p1", idA)
				data := bytes.Repeat([]byte("страница"), 1000)
				p, err := e.NewPart()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.Write(data); err != nil {
					t.Fatal(err)
				}
				name := pageName(data)
				checkFailure(t, mode, step, failAt(e, mode, step, func() error { return e.CommitPage("p1", idA, name, p) }))
				if mode == "error" {
					e.noPartFiles(t)
				}
				e2 := restart(t, e, dir)
				size, err := e2.PageSize("p1", idA, name)
				switch step {
				case stepSyncDir: // rename состоялся: страница целиком на месте
					if err != nil || size != int64(len(data)) {
						t.Fatalf("страница после syncdir: %d %v", size, err)
					}
				default: // страницы нет вовсе, а не обрезок
					if err == nil {
						t.Fatalf("страница появилась до rename (%d байт)", size)
					}
				}
				if _, err := e2.ReadMeta("p1", idA); err != nil {
					t.Fatalf("meta: %v", err)
				}
			})

			t.Run("create/"+mode+"/"+step, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				e := openIn(t, dir)
				checkFailure(t, mode, step, failAt(e, mode, step, func() error { return e.CreateAlbum(newMeta("p1", idA)) }))
				e2 := restart(t, e, dir)
				got, lerr := e2.LoadAll()
				if lerr != nil {
					t.Fatal(lerr)
				}
				// Либо альбом целиком (meta валидна), либо ничего: каталога-сироты нет.
				switch {
				case len(got) == 1 && got[0].ID == idA:
					if step != stepSyncDir {
						t.Fatalf("альбом появился при отказе на %s", step)
					}
				case len(got) == 0:
					if e2.exists("owners/p1/albums/"+idA) || len(e2.names(t, "trash")) != 0 {
						t.Fatal("остался каталог-сирота или мусор в корзине")
					}
				default:
					t.Fatalf("загружено: %d", len(got))
				}
			})

			t.Run("layout/"+mode+"/"+step, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				e := openIn(t, dir)
				newer := t0.Add(time.Hour)
				checkFailure(t, mode, step, failAt(e, mode, step, func() error { return e.SetLayout(Layout{LastSeen: newer}) }))
				if mode == "error" {
					// До rename память прежняя; после — запись состоялась (ErrNotDurable), память не откатывается.
					want := t0
					if step == stepSyncDir {
						want = newer
					}
					if got := e.Layout().LastSeen; !got.Equal(want) {
						t.Fatalf("в памяти после отказа: %v, ждали %v", got, want)
					}
				}
				e2 := restart(t, e, dir)
				got := e2.Layout().LastSeen
				want := t0
				if step == stepSyncDir {
					want = newer
				}
				if !got.Equal(want) {
					t.Fatalf("LAYOUT после перезапуска: %v, ждали %v", got, want)
				}
			})
		}
	}
}

// checkFailure: отказ шага дошёл до вызывающего; после rename (шаг syncdir) он — ErrNotDurable, до него — нет.
func checkFailure(t *testing.T, mode, step string, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("отказ шага не дошёл до вызывающего")
	}
	if mode == "error" && errors.Is(err, ErrNotDurable) != (step == stepSyncDir) {
		t.Fatalf("шаг %s: ErrNotDurable=%v (%v)", step, errors.Is(err, ErrNotDurable), err)
	}
}
