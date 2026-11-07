package access

import (
	"container/list"
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Rate — предел «Count попыток за Window».
type Rate struct {
	Count  int
	Window time.Duration
}

// LimitConfig — три ключа лимита попыток входа (design §5.5): альбом+IP, IP, альбом. Ограничение по альбому
// держит пароль «Придумать» (~21 бит) от перебора с множества адресов; два других не дают одному адресу
// перебирать подряд.
type LimitConfig struct {
	PerAlbumIP, PerIP, PerAlbum Rate
}

// DefaultMaxIPKeys — сколько ключей с IP хранит каждая из двух таблиц; сверх этого вытесняется ключ,
// к которому не обращались дольше всех (design §5.5).
const DefaultMaxIPKeys = 50000

// gcEvery — как часто из таблиц убираются ключи без живых попыток.
const gcEvery = time.Minute

// LimiterOptions — зависимости лимитера. Нулевые значения годны для боевого запуска.
type LimiterOptions struct {
	Now       func() time.Time // по умолчанию time.Now
	MaxIPKeys int              // по умолчанию DefaultMaxIPKeys
}

// Limiter — лимит попыток входа в памяти. Проверка и учёт — в одной критической секции: двести параллельных
// запросов не пройдут за предел, потому что каждый берёт место до сверки пароля. После перезапуска — с нуля.
type Limiter struct {
	mu        sync.Mutex
	cfg       LimitConfig
	now       func() time.Time
	albumIP   *table[albumIPKey]
	ip        *table[netip.Addr]
	album     *table[string] // ключ — id|hex(соль)|epoch: смена пароля или сброс входов открывают новую корзину
	lastSweep time.Time
}

type albumIPKey struct {
	id string
	ip netip.Addr
}

// NewLimiter проверяет пределы (положительные число и окно) и создаёт лимитер.
func NewLimiter(cfg LimitConfig, opt LimiterOptions) (*Limiter, error) {
	for _, r := range []Rate{cfg.PerAlbumIP, cfg.PerIP, cfg.PerAlbum} {
		if r.Count <= 0 || r.Window <= 0 {
			return nil, errors.New("предел попыток входа: нужны положительные число и окно")
		}
	}
	l := &Limiter{cfg: cfg, now: opt.Now}
	if l.now == nil {
		l.now = time.Now
	}
	maxIP := opt.MaxIPKeys
	if maxIP <= 0 {
		maxIP = DefaultMaxIPKeys
	}
	l.albumIP, l.ip, l.album = newTable[albumIPKey](maxIP), newTable[netip.Addr](maxIP), newTable[string](0)
	l.lastSweep = l.now()
	return l, nil
}

// ClientKey — ключ адреса клиента: ::ffff:a.b.c.d — это IPv4 (Unmap), IPv6 считается по сети /64 (у одного
// абонента их сколько угодно), зона отбрасывается.
func ClientKey(ip netip.Addr) netip.Addr {
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.Addr()
		}
	}
	return ip
}

// Attempt — занятая попытка входа. Success возвращает её, если пароль верен.
type Attempt struct {
	l    *Limiter
	id   string // альбом (ключ «альбом+IP»)
	gen  string // поколение доступа альбома (ключ «альбом»)
	ip   netip.Addr
	at   time.Time
	done bool
}

// generation — ключ корзины «альбом»: id, соль пароля и epoch. Держатель ссылки может занять 20 попыток в сутки
// и закрыть вход остальным, у кого нет cookie; смена пароля (новая соль) или «сбросить входы» (epoch) дают
// фотографу новую корзину. Соль в виде hex живёт только внутри карты и нигде не пишется (аудит Г3).
func generation(s Session) string {
	return s.ID + "|" + hex.EncodeToString(s.Salt) + "|" + strconv.FormatInt(s.Epoch, 10)
}

// Begin занимает попытку входа в альбом сессии s (id, соль, epoch из meta) с адреса ip до сверки пароля.
// Предел превышен — попытка не занимается, возвращается через сколько секунд освободятся все превышенные
// ключи (берётся наибольшее из времён освобождения, не меньше секунды) и ok=false.
// Любой исход сверки, кроме Success, остаётся засчитанным: неверный, пустой, неразбираемый пароль.
func (l *Limiter) Begin(s Session, ip netip.Addr) (a *Attempt, retryAfter time.Duration, ok bool) {
	id, gen := s.ID, generation(s)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	ck := ClientKey(ip)
	aik := albumIPKey{id: id, ip: ck}

	var wait time.Duration
	for _, c := range []struct {
		b *bucket
		r Rate
	}{
		{l.albumIP.peek(aik), l.cfg.PerAlbumIP},
		{l.ip.peek(ck), l.cfg.PerIP},
		{l.album.peek(gen), l.cfg.PerAlbum},
	} {
		if c.b == nil {
			continue
		}
		c.b.prune(now, c.r.Window)
		if len(c.b.times) >= c.r.Count {
			wait = max(wait, c.b.times[0].Add(c.r.Window).Sub(now))
		}
	}
	if wait > 0 {
		return nil, max(1, (wait+time.Second-1)/time.Second) * time.Second, false // вверх до секунды: раньше нельзя
	}
	l.albumIP.touch(aik).add(now)
	l.ip.touch(ck).add(now)
	l.album.touch(gen).add(now)
	return &Attempt{l: l, id: id, gen: gen, ip: ck, at: now}, 0, true
}

// Success возвращает попытку (ключи «IP» и «альбом» не считают верный вход) и сбрасывает ключ «альбом+IP»:
// владелец, вспомнивший пароль, не остаётся заблокированным собственными опечатками. Повторный вызов
// ничего не делает.
func (a *Attempt) Success() {
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if b := l.ip.peek(a.ip); b != nil {
		b.remove(a.at)
	}
	if b := l.album.peek(a.gen); b != nil {
		b.remove(a.at)
	}
	l.albumIP.drop(albumIPKey{id: a.id, ip: a.ip})
}

// sweep раз в gcEvery убирает ключи, у которых не осталось живых попыток: так таблица альбомов не копит
// ключи удалённых альбомов.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < gcEvery {
		return
	}
	l.lastSweep = now
	l.albumIP.sweep(now, l.cfg.PerAlbumIP.Window)
	l.ip.sweep(now, l.cfg.PerIP.Window)
	l.album.sweep(now, l.cfg.PerAlbum.Window)
}

// size — число ключей в трёх таблицах (для теста границы памяти).
func (l *Limiter) size() (albumIP, ip, album int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.albumIP.l.Len(), l.ip.l.Len(), l.album.l.Len()
}

// bucket — время попыток по ключу, по возрастанию. Не длиннее Count: сверх предела попытка не берётся.
type bucket struct{ times []time.Time }

func (b *bucket) add(t time.Time) { b.times = append(b.times, t) }

// prune убирает попытки, вышедшие из окна.
func (b *bucket) prune(now time.Time, window time.Duration) {
	cut := now.Add(-window)
	i := 0
	for i < len(b.times) && !b.times[i].After(cut) {
		i++
	}
	if i > 0 {
		b.times = append(b.times[:0], b.times[i:]...)
	}
}

// remove убирает одну попытку со временем t (с конца: возвращается обычно последняя).
func (b *bucket) remove(t time.Time) {
	for i := len(b.times) - 1; i >= 0; i-- {
		if b.times[i].Equal(t) {
			b.times = append(b.times[:i], b.times[i+1:]...)
			return
		}
	}
}

// table — ключи с корзинами; при max > 0 вытесняется ключ, к которому не обращались дольше всех.
type table[K comparable] struct {
	m   map[K]*list.Element
	l   *list.List // front — недавно использованные
	max int
}

type entry[K comparable] struct {
	key K
	b   bucket
}

func newTable[K comparable](maxKeys int) *table[K] {
	return &table[K]{m: map[K]*list.Element{}, l: list.New(), max: maxKeys}
}

// peek — корзина ключа без создания и без перестановки; nil — ключа нет.
func (t *table[K]) peek(k K) *bucket {
	if e, ok := t.m[k]; ok {
		return &e.Value.(*entry[K]).b
	}
	return nil
}

// touch — корзина ключа (создаётся), ключ становится самым свежим; лишний старейший вытесняется.
func (t *table[K]) touch(k K) *bucket {
	if e, ok := t.m[k]; ok {
		t.l.MoveToFront(e)
		return &e.Value.(*entry[K]).b
	}
	e := t.l.PushFront(&entry[K]{key: k})
	t.m[k] = e
	for t.max > 0 && t.l.Len() > t.max {
		old := t.l.Back()
		delete(t.m, old.Value.(*entry[K]).key)
		t.l.Remove(old)
	}
	return &e.Value.(*entry[K]).b
}

func (t *table[K]) drop(k K) {
	if e, ok := t.m[k]; ok {
		delete(t.m, k)
		t.l.Remove(e)
	}
}

func (t *table[K]) sweep(now time.Time, window time.Duration) {
	for e := t.l.Front(); e != nil; {
		next := e.Next()
		en := e.Value.(*entry[K])
		en.b.prune(now, window)
		if len(en.b.times) == 0 {
			delete(t.m, en.key)
			t.l.Remove(e)
		}
		e = next
	}
}
