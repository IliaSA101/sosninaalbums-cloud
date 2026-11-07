package access

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock — часы лимитера, которые двигает тест.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testRates = LimitConfig{
	PerAlbumIP: Rate{Count: 5, Window: 15 * time.Minute},
	PerIP:      Rate{Count: 20, Window: time.Hour},
	PerAlbum:   Rate{Count: 20, Window: 24 * time.Hour},
}

func newLimiter(t *testing.T, cfg LimitConfig, maxIP int) (*Limiter, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: cookieNow}
	l, err := NewLimiter(cfg, LimiterOptions{Now: clk.Now, MaxIPKeys: maxIP})
	if err != nil {
		t.Fatal(err)
	}
	return l, clk
}

func sess(id string) Session { return Session{ID: id, Salt: cookieSalt} }

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

const albumB = "bcdefghijklmnopqrstuvwxyza"

func TestLimiterRejectsBadConfig(t *testing.T) {
	for _, bad := range []Rate{{}, {Count: 0, Window: time.Hour}, {Count: 3}, {Count: -1, Window: time.Hour}} {
		cfg := testRates
		cfg.PerIP = bad
		if _, err := NewLimiter(cfg, LimiterOptions{}); err == nil {
			t.Errorf("предел %+v принят", bad)
		}
	}
}

// Три ключа считаются независимо: у каждого свой предел и своё окно.
func TestLimiterKeys(t *testing.T) {
	t.Run("альбом+IP: шестая попытка отказ, другой адрес и другой альбом не затронуты", func(t *testing.T) {
		l, _ := newLimiter(t, testRates, 0)
		for i := range 5 {
			if _, _, ok := l.Begin(sess(albumID), addr("192.0.2.1")); !ok {
				t.Fatalf("попытка %d отказана", i+1)
			}
		}
		if _, ra, ok := l.Begin(sess(albumID), addr("192.0.2.1")); ok || ra != 15*time.Minute {
			t.Fatalf("шестая: ok=%v retry=%v", ok, ra)
		}
		if _, _, ok := l.Begin(sess(albumID), addr("192.0.2.2")); !ok {
			t.Fatal("другой адрес закрыт ключом альбом+IP")
		}
		if _, _, ok := l.Begin(sess(albumB), addr("192.0.2.1")); !ok {
			t.Fatal("другой альбом закрыт ключом альбом+IP")
		}
	})
	t.Run("IP: 20 за час по любым альбомам", func(t *testing.T) {
		l, _ := newLimiter(t, testRates, 0)
		for i := range 20 {
			if _, _, ok := l.Begin(sess(fmt.Sprintf("%026d", i)), addr("192.0.2.1")); !ok {
				t.Fatalf("попытка %d отказана", i+1)
			}
		}
		if _, _, ok := l.Begin(sess(albumB), addr("192.0.2.1")); ok {
			t.Fatal("21-я попытка с адреса прошла")
		}
		if _, _, ok := l.Begin(sess(albumB), addr("192.0.2.9")); !ok {
			t.Fatal("другой адрес закрыт")
		}
	})
	t.Run("альбом: 20 за сутки с разных адресов", func(t *testing.T) {
		l, _ := newLimiter(t, testRates, 0)
		for i := range 20 {
			if _, _, ok := l.Begin(sess(albumID), addr(fmt.Sprintf("198.51.100.%d", i+1))); !ok {
				t.Fatalf("попытка %d отказана", i+1)
			}
		}
		if _, ra, ok := l.Begin(sess(albumID), addr("203.0.113.5")); ok || ra != 24*time.Hour {
			t.Fatalf("21-я на альбом: ok=%v retry=%v", ok, ra)
		}
		if _, _, ok := l.Begin(sess(albumB), addr("203.0.113.5")); !ok {
			t.Fatal("другой альбом закрыт")
		}
	})
	t.Run("IPv6 — по /64, IPv4-в-IPv6 — это IPv4", func(t *testing.T) {
		l, _ := newLimiter(t, testRates, 0)
		for i := range 5 {
			if _, _, ok := l.Begin(sess(albumID), addr(fmt.Sprintf("2001:db8:1:2::%x", i+1))); !ok {
				t.Fatalf("попытка %d отказана", i+1)
			}
		}
		// Другие 64 бита той же сети — тот же ключ.
		if _, _, ok := l.Begin(sess(albumID), addr("2001:db8:1:2:ffff:ffff:ffff:ffff")); ok {
			t.Fatal("адрес из той же /64 не засчитан одним ключом")
		}
		// Соседняя сеть /64 — другой ключ.
		if _, _, ok := l.Begin(sess(albumID), addr("2001:db8:1:3::1")); !ok {
			t.Fatal("соседняя /64 закрыта")
		}
		for i := range 5 {
			ip := addr("::ffff:192.0.2.77")
			if i%2 == 1 {
				ip = addr("192.0.2.77")
			}
			if _, _, ok := l.Begin(sess(albumB), ip); !ok {
				t.Fatalf("v4 попытка %d отказана", i+1)
			}
		}
		if _, _, ok := l.Begin(sess(albumB), addr("::ffff:192.0.2.77")); ok {
			t.Fatal("::ffff:a.b.c.d и a.b.c.d посчитаны разными ключами")
		}
	})
	t.Run("ClientKey", func(t *testing.T) {
		for in, want := range map[string]string{
			"192.0.2.1": "192.0.2.1", "::ffff:192.0.2.1": "192.0.2.1", "2001:db8:1:2:3:4:5:6": "2001:db8:1:2::",
			"fe80::1%eth0": "fe80::",
		} {
			if got := ClientKey(addr(in)).String(); got != want {
				t.Errorf("ClientKey(%s) = %s, ждали %s", in, got, want)
			}
		}
	})
}

func TestLimiterWindowAndSuccess(t *testing.T) {
	l, clk := newLimiter(t, testRates, 0)
	ip := addr("192.0.2.1")
	for range 5 {
		if _, _, ok := l.Begin(sess(albumID), ip); !ok {
			t.Fatal("отказ до предела")
		}
	}
	clk.advance(15*time.Minute - time.Second)
	if _, _, ok := l.Begin(sess(albumID), ip); ok {
		t.Fatal("окно ещё не прошло")
	}
	clk.advance(time.Second)
	if _, _, ok := l.Begin(sess(albumID), ip); !ok {
		t.Fatal("окно прошло, а попытка отказана")
	}

	// Успех возвращает попытку и сбрасывает «альбом+IP»: владелец после опечаток входит и не заблокирован.
	l, _ = newLimiter(t, testRates, 0)
	for range 4 {
		if _, _, ok := l.Begin(sess(albumID), ip); !ok {
			t.Fatal("отказ до предела")
		}
	}
	a, _, ok := l.Begin(sess(albumID), ip) // пятая — верный пароль
	if !ok {
		t.Fatal("пятая отказана")
	}
	a.Success()
	a.Success() // повтор безвреден
	for i := range 5 {
		if _, _, ok := l.Begin(sess(albumID), ip); !ok {
			t.Fatalf("после успеха попытка %d отказана", i+1)
		}
	}
	// «IP» и «альбом» успех не считают: 4 неверных + успех + 5 неверных = 9 неудач, а не 10 по ключам.
	if b := l.ip.peek(ClientKey(ip)); b == nil || len(b.times) != 9 {
		t.Fatalf("ключ IP после успеха: %+v", b)
	}
}

func TestLimiterRetryAfter(t *testing.T) {
	cfg := LimitConfig{
		PerAlbumIP: Rate{Count: 2, Window: 100 * time.Second},
		PerIP:      Rate{Count: 50, Window: time.Hour},
		PerAlbum:   Rate{Count: 50, Window: time.Hour},
	}
	l, clk := newLimiter(t, cfg, 0)
	ip := addr("192.0.2.1")
	l.Begin(sess(albumID), ip)
	clk.advance(30 * time.Second)
	l.Begin(sess(albumID), ip)
	clk.advance(10*time.Second + 500*time.Millisecond)
	// Освободится первая попытка: 100 с − 40,5 с = 59,5 с → вверх до целой секунды.
	if _, ra, ok := l.Begin(sess(albumID), ip); ok || ra != 60*time.Second {
		t.Fatalf("retry-after: ok=%v %v", ok, ra)
	}
	clk.advance(59*time.Second + 400*time.Millisecond)
	if _, ra, ok := l.Begin(sess(albumID), ip); ok || ra != time.Second {
		t.Fatalf("за долю секунды до конца: ok=%v %v, нужно 1 с (не 0)", ok, ra)
	}
	// Несколько ключей заняты: ждать нужно того, что освободится последним.
	cfg = LimitConfig{
		PerAlbumIP: Rate{Count: 1, Window: 10 * time.Second},
		PerIP:      Rate{Count: 50, Window: time.Hour},
		PerAlbum:   Rate{Count: 1, Window: 90 * time.Second},
	}
	l, _ = newLimiter(t, cfg, 0)
	l.Begin(sess(albumID), ip)
	if _, ra, ok := l.Begin(sess(albumID), ip); ok || ra != 90*time.Second {
		t.Fatalf("два ключа: ok=%v %v", ok, ra)
	}
}

// Двести параллельных неверных попыток проходят не дальше предела: проверка и учёт — одна секция.
func TestLimiterConcurrent(t *testing.T) {
	l, _ := newLimiter(t, testRates, 0)
	var passed atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, ok := l.Begin(sess(albumID), addr("192.0.2.1")); ok {
				passed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := passed.Load(); n != 5 {
		t.Fatalf("прошло %d попыток при пределе 5", n)
	}

	// То же с разных адресов: держит предел по альбому (20).
	l, _ = newLimiter(t, testRates, 0)
	passed.Store(0)
	start = make(chan struct{})
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, ok := l.Begin(sess(albumID), addr(fmt.Sprintf("10.0.%d.%d", i/250, i%250+1))); ok {
				passed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := passed.Load(); n != 20 {
		t.Fatalf("с разных адресов прошло %d при пределе по альбому 20", n)
	}
}

// Память ограничена: ключей с IP не больше MaxIPKeys, вытесняется давнее; ключи альбомов убираются, когда
// живых попыток нет.
func TestLimiterBounded(t *testing.T) {
	l, _ := newLimiter(t, testRates, 100)
	for i := range 1000 {
		l.Begin(sess(fmt.Sprintf("%026d", i)), addr(fmt.Sprintf("10.%d.%d.1", i/256, i%256)))
		if a, ip, _ := l.size(); a > 100 || ip > 100 {
			t.Fatalf("после %d адресов: %d и %d ключей при пределе 100", i+1, a, ip)
		}
	}
	// Вытеснен старейший, свежий на месте.
	if l.ip.peek(addr("10.0.0.1")) != nil {
		t.Fatal("самый старый ключ не вытеснен")
	}
	if l.ip.peek(addr("10.3.231.1")) == nil {
		t.Fatal("самый свежий ключ вытеснен")
	}
	// Обращение освежает ключ: он переживает вытеснение.
	l, _ = newLimiter(t, testRates, 3)
	keep := addr("192.0.2.1")
	l.Begin(sess(albumID), keep)
	for i := range 10 {
		l.Begin(sess(albumID), addr(fmt.Sprintf("198.51.100.%d", i+1)))
		l.Begin(sess(fmt.Sprintf("%026d", i)), keep)
	}
	if l.ip.peek(keep) == nil {
		t.Fatal("активный ключ вытеснен")
	}
	// Уборка: по истечении окон таблицы пустеют.
	l, clk := newLimiter(t, testRates, 0)
	for i := range 50 {
		l.Begin(sess(fmt.Sprintf("%026d", i)), addr(fmt.Sprintf("10.0.0.%d", i+1)))
	}
	clk.advance(25 * time.Hour)
	l.Begin(sess(albumB), addr("192.0.2.2"))
	if a, ip, al := l.size(); a != 1 || ip != 1 || al != 1 {
		t.Fatalf("после уборки: %d %d %d ключей, ждали по одному", a, ip, al)
	}
}
