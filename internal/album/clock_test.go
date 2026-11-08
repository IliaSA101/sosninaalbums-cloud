package album

import (
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// timeAlbums — три альбома, которые уборка по времени удалила бы после скачка часов вперёд на 60 ч:
// истёкший (срок 48 ч), ни разу не опубликованный (срок далеко, но сутки прошли), опубликованный с правкой
// старше суток.
type timeAlbums struct{ expired, abandoned, drafted string }

func seedTimeAlbums(t *testing.T, e *env) timeAlbums {
	t.Helper()
	var ta timeAlbums
	ta.expired = e.published(pOne, book("Истечёт", d1, d2), d1, d2).ID
	far, err := e.svc.Create(pOne, &albums.CreateRequest{Manifest: book("Брошен", d3, d4), ExpiresAt: t0.Add(300 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ta.abandoned = far.ID
	ta.drafted = e.published(pOne, book("С правкой", d1, d3), d1, d3).ID
	e.cfgLongExpiry(ta.drafted)
	if _, err := e.svc.Update(pOne, ta.drafted, book("С правкой", d1, d4)); err != nil {
		t.Fatal(err)
	}
	return ta
}

// stillThere — ничего не удалено: ни альбомы, ни правка.
func (ta timeAlbums) stillThere(t *testing.T, e *env) {
	t.Helper()
	for name, id := range map[string]string{"истёкший": ta.expired, "брошенный": ta.abandoned, "с правкой": ta.drafted} {
		if !e.inIndex(id) {
			t.Fatalf("пауза часов: %s альбом удалён", name)
		}
	}
	if m := e.svc.lookup(ta.drafted).meta.Load(); m.Draft == nil {
		t.Fatal("пауза часов: правка отброшена")
	}
}

func (ta timeAlbums) allGone(t *testing.T, e *env) {
	t.Helper()
	if e.inIndex(ta.expired) || e.inIndex(ta.abandoned) {
		t.Fatal("после снятия паузы истёкший и брошенный удалены")
	}
	if m := e.svc.lookup(ta.drafted).meta.Load(); m.Draft != nil {
		t.Fatal("после снятия паузы просроченная правка отброшена")
	}
}

func TestClockHoldPersists(t *testing.T) {
	setLast := func(t *testing.T, e *env, last time.Time) {
		t.Helper()
		if err := e.st.SetLayout(store.Layout{LastSeen: last}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		trigger func(t *testing.T, e *env) // выводит часы из строя; пауза должна появиться к первому Sweep
		reason  string
	}{
		{"вперёд в работе", func(_ *testing.T, e *env) { e.clk.jump(60 * time.Hour) }, "разошлись"},
		{"назад в работе", func(_ *testing.T, e *env) { e.clk.advance(60 * time.Hour); e.clk.jump(-3 * time.Hour) }, "разошлись"},
		{"при старте: last_seen в будущем", func(t *testing.T, e *env) {
			e.clk.advance(60 * time.Hour)
			setLast(t, e, e.clk.Wall().Add(2*time.Hour))
			e.reopen()
		}, "раньше last_seen"},
		{"при старте: last_seen старше 30 суток", func(t *testing.T, e *env) {
			e.clk.advance(60 * time.Hour)
			setLast(t, e, e.clk.Wall().Add(-31*24*time.Hour))
			e.reopen()
		}, "старше 30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ta := seedTimeAlbums(t, e)
			tc.trigger(t, e)
			lastBefore := e.st.Layout().LastSeen
			// Доступ решается по времени запроса, даже при паузе: просроченный — 404.
			if _, err := e.svc.Access(ta.expired); err == nil && e.clk.Wall().After(t0.Add(48*time.Hour)) {
				t.Fatal("просроченный альбом открыт при паузе часов")
			}
			st, err := e.svc.Sweep()
			if err != nil || !st.Held || st.Expired+st.Abandoned+st.Drafts != 0 {
				t.Fatalf("уборка при паузе: %+v, %v", st, err)
			}
			ta.stillThere(t, e)
			hold := e.st.Layout().ClockHold
			if hold == nil || !strings.Contains(hold.Reason, tc.reason) {
				t.Fatalf("пауза не записана в LAYOUT: %+v", hold)
			}
			if !e.st.Layout().LastSeen.Equal(lastBefore) {
				t.Fatalf("при паузе last_seen не двигается: %v → %v", lastBefore, e.st.Layout().LastSeen)
			}
			// Пауза переживает перезапуск, а часы к тому времени «исправны».
			e.reopen()
			e.clk.advance(10 * time.Hour)
			if st, err := e.svc.Sweep(); err != nil || !st.Held {
				t.Fatalf("после перезапуска пауза держится: %+v, %v", st, err)
			}
			ta.stillThere(t, e)
			// -clock-ok: чистит паузу и ставит last_seen ← now (разовый запуск при остановленном сервисе).
			if err := e.st.SetLayout(store.Layout{LastSeen: e.clk.Wall()}); err != nil {
				t.Fatal(err)
			}
			e.reopen()
			st, err = e.svc.Sweep()
			if err != nil || st.Held {
				t.Fatalf("после снятия паузы: %+v, %v", st, err)
			}
			ta.allGone(t, e)
			if got := e.st.Layout(); got.ClockHold != nil || !got.LastSeen.Equal(e.clk.Wall()) {
				t.Fatalf("после уборки без паузы last_seen = now: %+v", got)
			}
		})
	}
}

func TestClockHealthy(t *testing.T) {
	e := newEnv(t)
	e.clk.advance(7 * time.Minute)
	st, err := e.svc.Sweep()
	if err != nil || st.Held {
		t.Fatalf("исправные часы: %+v, %v", st, err)
	}
	if l := e.st.Layout(); l.ClockHold != nil || !l.LastSeen.Equal(e.clk.Wall()) {
		t.Fatalf("last_seen двигается каждым проходом: %+v", l)
	}
	// Перезапуск через 29 суток простоя — допустим, пауза не ставится.
	e.clk.advance(29 * 24 * time.Hour)
	e.reopen()
	if e.st.Layout().ClockHold != nil {
		t.Fatalf("29 суток — не повод для паузы: %+v", e.st.Layout().ClockHold)
	}
	// Дрейф меньше 10 минут допустим.
	e.clk.jump(9 * time.Minute)
	if st, err := e.svc.Sweep(); err != nil || st.Held {
		t.Fatalf("дрейф 9 минут: %+v, %v", st, err)
	}
	e.clk.jump(2 * time.Minute) // всего 11 минут
	if st, err := e.svc.Sweep(); err != nil || !st.Held {
		t.Fatalf("дрейф 11 минут: %+v, %v", st, err)
	}
}

// Скачок часов посреди прохода уборки: часы проверяются перед каждым удалением по времени, а не один раз на
// проход. Первый альбом убран при исправных часах, на втором пауза остановила удаление.
func TestClockJumpMidSweep(t *testing.T) {
	e := newEnv(t)
	a := e.published(pOne, book("A", d1, d2), d1, d2).ID
	b := e.published(pOne, book("B", d3, d4), d3, d4).ID
	e.clk.advance(49 * time.Hour) // оба истекли
	e.clk.mu.Lock()
	e.clk.jumpAt, e.clk.jumpBy = e.clk.calls+3, 60*time.Hour // 1-й вызов — проход, 2-й — первый альбом, 3-й — второй
	e.clk.mu.Unlock()
	st, err := e.svc.Sweep()
	if err != nil || !st.Held || st.Expired != 1 {
		t.Fatalf("уборка со скачком посреди прохода: %+v, %v", st, err)
	}
	if e.inIndex(a) == e.inIndex(b) {
		t.Fatalf("ровно один из двух альбомов должен остаться: a=%v b=%v", e.inIndex(a), e.inIndex(b))
	}
	if e.st.Layout().ClockHold == nil {
		t.Fatal("пауза не записана в LAYOUT")
	}
}

// last_seen не затирает паузу на диске: пауза, поставленная раньше, остаётся, а last_seen не двигается.
func TestTouchLastSeenKeepsHold(t *testing.T) {
	e := newEnv(t)
	before := e.st.Layout().LastSeen
	e.svc.setHold(reasonDrift, e.clk.Wall())
	e.clk.advance(time.Minute)
	if err := e.svc.touchLastSeen(e.clk.Wall()); err != nil {
		t.Fatal(err)
	}
	if l := e.st.Layout(); l.ClockHold == nil || !l.LastSeen.Equal(before) {
		t.Fatalf("пауза или last_seen изменены: %+v", l)
	}
	// Пауза только в памяти (запись LAYOUT не удалась) тоже не даёт двигать last_seen.
	e2 := newEnv(t)
	e2.svc.held = true
	e2.clk.advance(time.Minute)
	if err := e2.svc.touchLastSeen(e2.clk.Wall()); err != nil || !e2.st.Layout().LastSeen.Equal(t0) {
		t.Fatalf("last_seen при паузе в памяти: %+v, %v", e2.st.Layout(), err)
	}
}

func TestActiveAgeFromPageBytes(t *testing.T) {
	if got := ActiveAge(15 << 20); got != 630*time.Second {
		t.Fatalf("15 МБ: %v", got)
	}
	// Срок не меньше самого долгого запроса: чтение + ответ.
	for _, n := range []int64{1 << 20, 15 << 20, 100 << 20, 1 << 30} {
		if ActiveAge(n) < ReadBudget(n)+WriteExtra {
			t.Fatalf("page_bytes %d: ActiveAge %v меньше срока запроса", n, ActiveAge(n))
		}
	}
	if ActiveAge(1<<30) <= ActiveAge(15<<20) {
		t.Fatal("ActiveAge не растёт с page_bytes")
	}
}
