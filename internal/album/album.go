// Package album — жизненный цикл альбомов (design §3): создание, редакции published/draft, missing, срок,
// квоты и запас места, индекс и мьютексы альбомов, уборка по сроку, сторож часов. Пакет не знает об HTTP:
// ответы клиенту строят api и view по ошибкам этого пакета.
//
// Порядок блокировок (design §3): сначала мьютекс альбома, затем коротко — квота, затем индекс. Обратно не
// берётся никогда: так нет цикла. Состояние альбома (meta) неизменяемо: изменение — новая копия, записанная на
// диск и затем опубликованная атомарно, поэтому читатели без мьютекса (список, снимок доступа) видят либо
// старую meta целиком, либо новую.
package album

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// Part — принимаемая страница во временном файле: api читает тело в неё без блокировки альбома, финал — CommitPage.
type Part = store.Part

// NewPart создаёт временный файл для приёма страницы (tmp/, 0600).
func (s *Service) NewPart() (*Part, error) {
	p, err := s.st.NewPart()
	if err != nil {
		return nil, wrapStore("временный файл страницы", err)
	}
	return p, nil
}

// OwnerLimits — квоты одного владельца (из конфига).
type OwnerLimits struct {
	QuotaBytes int64 // байты различных файлов published ∪ draft всех его альбомов
	MaxAlbums  int
}

// Config — настройки сервиса, которые решают жизненный цикл (подмножество конфига процесса).
type Config struct {
	PublicBase     string // https://домен, без пути; ссылка — PublicBase + /a/<id>/
	Limits         albums.Limits
	AcceptWebP     bool
	UnpublishedTTL time.Duration // сколько живёт ни разу не опубликованный альбом и неопубликованная правка
	DiskReserve    int64         // запас свободного места, байт
	Owners         map[string]OwnerLimits
}

// Options — зависимости сервиса. Нулевые значения годны для боевого запуска; тесты подставляют часы и ГСЧ.
type Options struct {
	// Wall — настенные часы; по умолчанию time.Now.
	Wall func() time.Time
	// Mono — монотонное время от старта процесса; по умолчанию time.Since(старт). Сторож часов сравнивает
	// приращения того и другого.
	Mono func() time.Duration
	// Rand — источник случайных байт для id и соли; по умолчанию crypto/rand.
	Rand io.Reader
	// Free — свободное место на разделе data/, байт; по умолчанию Statfs хранилища. Тесты подставляют своё
	// значение, чтобы не зависеть от реального диска.
	Free func() (uint64, error)
	// Passwords — HMAC паролей листалки (ключ k_pw). Обязателен.
	Passwords *access.Passwords
	Tagger    *logx.Tagger // метка альбома в журнале; nil — «00000000»
	Log       *slog.Logger // по умолчанию — никуда
}

// album — запись индекса. meta заменяется целиком под мьютексом альбома; читают без него.
type album struct {
	meta       atomic.Pointer[store.Meta]
	usage      int64       // вклад в used[owner]; под quotaMu
	incomplete atomic.Bool // у опубликованного нет файла: 404 на /a/, пока не опубликуют заново
}

// Service — все альбомы процесса. Безопасен для одновременного использования.
type Service struct {
	st     *store.Store
	cfg    Config
	pw     *access.Passwords
	wall   func() time.Time
	mono   func() time.Duration
	rnd    io.Reader
	free   func() (uint64, error)
	tagger *logx.Tagger
	log    *slog.Logger

	locks lockTable

	idxMu sync.RWMutex
	idx   map[string]*album

	quotaMu sync.Mutex
	used    map[string]int64 // владелец → байты
	count   map[string]int   // владелец → число альбомов

	clockMu sync.Mutex
	w0      time.Time     // настенное время старта (без монотонного отсчёта)
	m0      time.Duration // монотонное время старта
	held    bool          // пауза поставлена в этом процессе (на случай отказа записи LAYOUT)
}

// New читает индекс с диска (LoadAll: негодная meta уходит в карантин, формат новее своего — ошибка),
// убирает мусор прошлого процесса (SweepAtStart), проверяет, что у опубликованных альбомов есть все файлы,
// и проверяет часы (design §3, сторож часов). Вызывать до приёма запросов.
func New(st *store.Store, cfg Config, opt Options) (*Service, error) {
	if opt.Passwords == nil {
		return nil, errors.New("album: не задан HMAC паролей")
	}
	s := &Service{
		st: st, cfg: cfg, pw: opt.Passwords, wall: opt.Wall, mono: opt.Mono, rnd: opt.Rand, free: opt.Free,
		tagger: opt.Tagger, log: opt.Log,
		locks: lockTable{m: map[string]*lockRef{}},
		idx:   map[string]*album{}, used: map[string]int64{}, count: map[string]int{},
	}
	if s.wall == nil {
		s.wall = time.Now
	}
	if s.mono == nil {
		start := time.Now()
		s.mono = func() time.Duration { return time.Since(start) }
	}
	if s.free == nil {
		s.free = st.FreeBytes
	}
	if s.rnd == nil {
		s.rnd = rand.Reader
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.w0, s.m0 = s.now(), s.mono()

	metas, err := st.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("загрузка альбомов: %w", err)
	}
	for _, m := range metas {
		s.add(m)
	}
	if _, err := st.SweepAtStart(); err != nil {
		// Мусор не мешает работе: остаток уберёт уборка в работе.
		s.log.Warn("уборка при старте не завершена", slog.String(logx.KeyOp, "sweep"), slog.Any(logx.KeyErr, err))
	}
	s.checkComplete()
	s.checkClockAtStart()
	return s, nil
}

// now — настенные часы в UTC без монотонного отсчёта: сравнения и Sub идут по настенному времени, иначе
// сторож часов не заметил бы скачок.
func (s *Service) now() time.Time { return s.wall().UTC() }

// Now — текущее время сервиса (для view: срок cookie не дольше срока альбома).
func (s *Service) Now() time.Time { return s.now() }

// add кладёт загруженную meta в индекс (старт).
func (s *Service) add(m *store.Meta) {
	a := &album{}
	a.meta.Store(m)
	a.usage = usageOf(m)
	s.idx[m.ID] = a
	s.used[m.Owner] += a.usage
	s.count[m.Owner]++
}

func (s *Service) lookup(id string) *album {
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	return s.idx[id]
}

func (s *Service) ids() []string {
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	out := make([]string, 0, len(s.idx))
	for id := range s.idx {
		out = append(out, id)
	}
	return out
}

// checkComplete при старте помечает «неполными» опубликованные альбомы, у которых нет файла (восстановление
// из копии, сбой диска): 404 на /a/ до новой публикации (design §3).
func (s *Service) checkComplete() {
	for _, id := range s.ids() {
		a := s.lookup(id)
		m := a.meta.Load()
		if m.Published == nil {
			continue
		}
		present := map[string]bool{}
		if files, err := s.st.ListPages(m.Owner, id); err == nil {
			for _, f := range files {
				present[f] = true
			}
		}
		for _, p := range m.Published.Manifest.Pages {
			if !p.Blank && !present[p.File] {
				a.incomplete.Store(true)
				s.log.Warn("у опубликованного альбома нет файла страницы", slog.String(logx.KeyOp, "load"),
					s.tagger.Attr(id))
				break
			}
		}
	}
}

// url — ссылка на листалку.
func (s *Service) url(id string) string { return s.cfg.PublicBase + "/a/" + id + "/" }

// lockRef — мьютекс альбома со счётчиком держателей: запись в таблице живёт, пока им кто-то пользуется.
type lockRef struct {
	mu sync.RWMutex
	n  int
}

type lockTable struct {
	mu sync.Mutex
	m  map[string]*lockRef
}

func (t *lockTable) acquire(id string) *lockRef {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.m[id]
	if r == nil {
		r = &lockRef{}
		t.m[id] = r
	}
	r.n++
	return r
}

func (t *lockTable) release(id string, r *lockRef) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r.n--; r.n == 0 {
		delete(t.m, id)
	}
}

// lock берёт мьютекс альбома (на запись или на чтение) и возвращает функцию освобождения. Мьютекс есть у
// любого id, в том числе у сирот, которых в индексе нет.
func (s *Service) lock(id string, write bool) func() {
	r := s.locks.acquire(id)
	if write {
		r.mu.Lock()
	} else {
		r.mu.RLock()
	}
	return func() {
		if write {
			r.mu.Unlock()
		} else {
			r.mu.RUnlock()
		}
		s.locks.release(id, r)
	}
}

// WithAlbumLock — store.Guard: мьютекс альбома на запись взят до вычисления inIndex и держится всё время fn.
func (s *Service) WithAlbumLock(id string, fn func(inIndex bool) error) error {
	defer s.lock(id, true)()
	return fn(s.lookup(id) != nil)
}

// owned выполняет fn под мьютексом альбома, если альбом есть, принадлежит owner и не истёк. Иначе
// ErrNotFound: несуществующий, чужой и просроченный неотличимы.
func (s *Service) owned(owner, id string, write bool, fn func(a *album, m *store.Meta) error) error {
	defer s.lock(id, write)()
	a := s.lookup(id)
	if a == nil {
		return ErrNotFound
	}
	m := a.meta.Load()
	if m.Owner != owner || !s.now().Before(m.Expires) {
		return ErrNotFound
	}
	return fn(a, m)
}
