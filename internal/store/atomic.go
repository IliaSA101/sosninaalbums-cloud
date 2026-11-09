package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
)

// Шаги записи на диск — точки, где тест отказов ФС ломает операцию (Store.hook).
const (
	stepCreate  = "create"  // создание временного файла
	stepWrite   = "write"   // запись содержимого
	stepSync    = "sync"    // fsync файла
	stepRename  = "rename"  // атомарная подмена
	stepSyncDir = "syncdir" // fsync каталога назначения
)

// newTmp создаёт tmp/<rand>.part (O_EXCL, 0600). Временные файлы лежат в том же data/, то есть на том же
// разделе, что и назначение: rename между ними атомарен.
func (s *Store) newTmp() (*os.File, string, error) {
	if s.ro {
		return nil, "", ErrReadOnly
	}
	n, err := randName(16)
	if err != nil {
		return nil, "", err
	}
	name := tmpDir + "/" + n + ".part"
	if err := s.step(stepCreate); err != nil {
		return nil, "", err
	}
	f, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return nil, "", err
	}
	return f, name, nil
}

// discard закрывает и удаляет временный файл после отказа и возвращает первичную ошибку; ошибки уборки
// присоединяются, кроме «уже нет». Намеренно не defer: при панике (имитация обрыва питания в тесте)
// уборка не выполняется — сирот убирает следующий старт.
func (s *Store) discard(f *os.File, name string, cause error) error {
	errs := []error{cause}
	if f != nil {
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// writeAtomic: tmp → запись → fsync → rename → fsync каталога. Пока rename не состоялся, на месте final
// остаётся прежнее содержимое; после — новое; смеси нет. id — для метки в ошибке.
func (s *Store) writeAtomic(final string, data []byte, id string) error {
	f, tmp, err := s.newTmp()
	if err != nil {
		return s.opErr("write create", id, err)
	}
	if err = s.step(stepWrite); err == nil {
		_, err = f.Write(data)
	}
	if err != nil {
		return s.opErr("write", id, s.discard(f, tmp, err))
	}
	if err = s.step(stepSync); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return s.opErr("write sync", id, s.discard(f, tmp, err))
	}
	if err = f.Close(); err != nil {
		return s.opErr("write close", id, s.discard(nil, tmp, err))
	}
	return s.finish(tmp, final, id)
}

// finish — общий конец записи meta и страниц: rename и fsync каталога назначения.
func (s *Store) finish(tmp, final, id string) error {
	err := s.step(stepRename)
	if err == nil {
		err = s.root.Rename(tmp, final)
	}
	if err != nil {
		return s.opErr("rename", id, s.discard(nil, tmp, err))
	}
	if err := s.syncDir(path.Dir(final)); err != nil {
		// Файл уже на месте: это не провал записи, а недоказанная долговечность.
		oe := &OpError{Op: "syncdir", Err: fmt.Errorf("%w: %w", ErrNotDurable, reduce(err))}
		if id != "" {
			oe.Album = s.tag(id)
		}
		return oe
	}
	return nil
}

func (s *Store) syncDir(dir string) error {
	if err := s.step(stepSyncDir); err != nil {
		return err
	}
	d, err := s.root.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// Part — принимаемая страница во временном файле tmp/<rand>.part. Тело читается в него без блокировки
// альбома; в каталог альбома страница попадает только через CommitPage, под мьютексом альбома.
type Part struct {
	s    *Store
	f    *os.File
	name string
	done bool
}

// NewPart создаёт временный файл для приёма страницы.
func (s *Store) NewPart() (*Part, error) {
	f, name, err := s.newTmp()
	if err != nil {
		return nil, s.opErr("part create", "", err)
	}
	return &Part{s: s, f: f, name: name}, nil
}

// Write дописывает тело страницы.
func (p *Part) Write(b []byte) (int, error) {
	n, err := p.f.Write(b)
	if err != nil {
		return n, p.s.opErr("part write", "", err)
	}
	return n, nil
}

// ReadAt читает записанное: imgcheck разбирает заголовки уже принятого файла.
func (p *Part) ReadAt(b []byte, off int64) (int, error) { return p.f.ReadAt(b, off) }

// Size — сколько байт записано.
func (p *Part) Size() (int64, error) {
	fi, err := p.f.Stat()
	if err != nil {
		return 0, p.s.opErr("part stat", "", err)
	}
	return fi.Size(), nil
}

// Abort закрывает и удаляет временный файл. Повторный вызов и вызов после CommitPage ничего не делают.
func (p *Part) Abort() error {
	if p.done {
		return nil
	}
	p.done = true
	return p.s.opErr("part abort", "", p.s.discard(p.f, p.name, nil))
}
