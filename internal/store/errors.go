package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// Ошибки store, по которым вызывающий выбирает ответ. Ошибки ФС отдаются как *OpError и сверяются через
// errors.Is(err, fs.ErrNotExist) и т. п.
var (
	// ErrBadName — id, slug или имя страницы не по грамматике: до пути дело не дошло.
	ErrBadName = errors.New("store: имя не по грамматике")
	// ErrExists — каталог альбома с таким id уже есть: розыгрыш id повторяют.
	ErrExists = errors.New("store: альбом уже есть")
	// ErrLocked — data/ занята другим процессом (flock).
	ErrLocked = errors.New("store: каталог данных занят другим процессом")
	// ErrNewerFormat — формат LAYOUT или meta.json новее понимаемого: старт отказывает, не чистит
	// (откат образа не должен разрушать данные новой версии).
	ErrNewerFormat = errors.New("store: формат данных новее этой версии сервиса")
	// ErrNotDurable — rename состоялся, а fsync каталога не удался: запись видна и считается сделанной,
	// но переживёт ли она сбой питания, не доказано. Вызывающий не откатывает своё состояние, ошибку пишет в журнал.
	ErrNotDurable = errors.New("store: запись состоялась, но не доказана на диске")
	// ErrReadOnly — изменение через хранилище, открытое OpenReadOnly (verify копии).
	ErrReadOnly = errors.New("store: хранилище открыто только для чтения")
	// ErrNoLayout — в каталоге нет LAYOUT: это не копия data/ (OpenReadOnly ничего не создаёт).
	ErrNoLayout = errors.New("store: в каталоге нет LAYOUT")
	// ErrCorruptMeta — meta.json не разобран или не сходится с путём; каталог отправляют в карантин.
	ErrCorruptMeta = errors.New("store: meta.json негоден")
)

// OpError — ошибка операции store без имени файла и без значения id: операция, метка альбома (8 hex, если
// знает logx.Tagger) и errno. Имя файла и путь в тексте не нужны — это либо хэш страницы, либо id, а id
// открывает альбом. Причина доступна через Unwrap.
type OpError struct {
	Op    string
	Album string
	Err   error
}

func (e *OpError) Error() string {
	if e.Album != "" {
		return fmt.Sprintf("store %s [album %s]: %v", e.Op, e.Album, e.Err)
	}
	return fmt.Sprintf("store %s: %v", e.Op, e.Err)
}

// Unwrap отдаёт причину для errors.Is/As.
func (e *OpError) Unwrap() error { return e.Err }

// opErr оборачивает ошибку ФС, срезая из неё путь (PathError, LinkError, SyscallError → errno).
func (s *Store) opErr(op, id string, err error) error {
	if err == nil {
		return nil
	}
	var oe *OpError
	if errors.As(err, &oe) {
		return err
	}
	e := &OpError{Op: op, Err: reduce(err)}
	if id != "" {
		e.Album = s.tag(id)
	}
	return e
}

func reduce(err error) error {
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) { // errors.Join: срезаем путь у каждой причины, не теряя остальных
		var out []error
		for _, e := range multi.Unwrap() {
			out = append(out, reduce(e))
		}
		return errors.Join(out...)
	}
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	switch {
	case errors.As(err, &pe):
		return pe.Err
	case errors.As(err, &le):
		return le.Err
	case errors.As(err, &se):
		return se.Err
	}
	return err
}

// IsNoSpace — на диске нет места (или квота ФС): вызывающему это 413, а не 500.
func IsNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

func checkNames(owner, id string) error {
	if !names.ValidSlug(owner) || !names.ValidID(id) {
		return ErrBadName
	}
	return nil
}
