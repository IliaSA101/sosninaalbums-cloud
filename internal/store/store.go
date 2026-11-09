// Package store — единственное место, где сервис трогает диск (design §2): каталог data/ открыт как
// os.Root, поэтому ни «..», ни символическая ссылка наружу не выводят за его пределы; id, slug и имена
// файлов проверяются по грамматике до того, как попадут в путь; запись — во временный файл, fsync,
// атомарный rename, fsync каталога; права 0600 на файлы и 0700 на каталоги.
//
// store не знает ни об HTTP, ни о квотах, ни о сроках: он хранит и отдаёт. Мьютексы альбомов и индекс
// принадлежат пакету album; уборке они передаются интерфейсом Guard.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600

	layoutFile = "LAYOUT"
	lockFile   = "LOCK"
	tmpDir     = "tmp"
	trashDir   = "trash"
	quarDir    = "quarantine"
	ownersDir  = "owners"

	// LayoutVersion — версия раскладки data/ (design §2): необратимо.
	LayoutVersion = 1
)

// Options — зависимости store. Нулевые значения годны для тестов.
type Options struct {
	Clock  func() time.Time // по умолчанию time.Now
	Tagger *logx.Tagger     // метка альбома в ошибках и журнале; nil — «00000000»
	Log    *slog.Logger     // по умолчанию — никуда
	// ActiveAge — возраст, с которого .part и каталог без meta считаются брошенными в работе. Должен быть не
	// меньше наибольшего срока приёма страницы (из page_bytes конфига); по умолчанию ActiveAge (600 с).
	ActiveAge time.Duration
}

// Store — открытый каталог данных. Безопасен для одновременного использования из многих горутин:
// сериализация изменений одного альбома — на вызывающем (мьютекс альбома).
type Store struct {
	dir    string
	root   *os.Root
	lock   *os.File
	clock  func() time.Time
	tagger *logx.Tagger
	log    *slog.Logger

	activeAge time.Duration

	// hook вызывается перед каждым шагом записи на диск; тесты отказов ФС подставляют отказ или панику.
	hook func(step string) error

	ro     bool       // открыто OpenReadOnly: ничего не пишет и не берёт замок
	mu     sync.Mutex // layout
	layout Layout
}

// Open открывает (создавая при отсутствии) каталог данных: права 0700, flock на LOCK (второй экземпляр не
// стартует), подкаталоги, LAYOUT. LAYOUT новее понимаемого — ErrNewerFormat. Блокировка — на отдельном
// файле, а не на LAYOUT: LAYOUT переписывается атомарным rename, и замена файла сняла бы замок.
func Open(dir string, opt Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: каталог данных не задан")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("store open: %w", reduce(err))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("store open: %w", reduce(err))
	}
	s := &Store{dir: dir, root: root, clock: opt.Clock, tagger: opt.Tagger, log: opt.Log, activeAge: opt.ActiveAge}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.activeAge <= 0 {
		s.activeAge = ActiveAge
	}
	if err := s.init(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly открывает существующую раскладку только для чтения (verify копии, design §8): ничего не
// создаётся, права не меняются, замок LOCK не берётся (копия может лежать на носителе только для чтения, а
// рядом может работать боевой экземпляр), LAYOUT не пишется. Нет каталога или LAYOUT — ошибка (ErrNoLayout).
// Любое изменение через такое хранилище — ErrReadOnly.
func OpenReadOnly(dir string, opt Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: каталог данных не задан")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("store open: %w", reduce(err))
	}
	s := &Store{dir: dir, root: root, clock: opt.Clock, tagger: opt.Tagger, log: opt.Log, activeAge: opt.ActiveAge, ro: true}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.activeAge <= 0 {
		s.activeAge = ActiveAge
	}
	if err := s.loadLayout(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	if err := s.root.Chmod(".", dirPerm); err != nil {
		return s.opErr("open chmod", "", err)
	}
	lf, err := s.root.OpenFile(lockFile, os.O_RDWR|os.O_CREATE, filePerm)
	if err != nil {
		return s.opErr("open lock", "", err)
	}
	s.lock = lf
	if err := flockNB(lf); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ErrLocked
		}
		return s.opErr("flock", "", err)
	}
	for _, d := range []string{tmpDir, trashDir, quarDir, ownersDir} {
		if err := s.root.MkdirAll(d, dirPerm); err != nil {
			return s.opErr("mkdir", "", err)
		}
	}
	return s.loadLayout()
}

// Close снимает блокировку и закрывает каталог.
func (s *Store) Close() error {
	var errs []error
	if s.lock != nil {
		errs = append(errs, s.lock.Close()) // закрытие дескриптора снимает flock
	}
	if s.root != nil {
		errs = append(errs, s.root.Close())
	}
	return errors.Join(errs...)
}

func (s *Store) tag(id string) string { return s.tagger.Tag(id) }

// step — точка отказа для тестов: перед каждой операцией записи.
func (s *Store) step(name string) error {
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}

// randName — 16 случайных байт hex из crypto/rand: имена временных файлов и корзины непредсказуемы.
func randName(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("случайные байты: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// FreeBytes — свободное для непривилегированного процесса место на разделе data/ (design §4: запас места).
func (s *Store) FreeBytes() (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.dir, &st); err != nil {
		return 0, s.opErr("statfs", "", err)
	}
	if st.Bsize <= 0 {
		return 0, s.opErr("statfs", "", errors.New("размер блока не положителен"))
	}
	return st.Bavail * uint64(st.Bsize), nil
}
