package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// CreateAlbum создаёт каталог альбома (0700) и пишет первую meta. Единственное место, где сервис создаёт
// каталог альбома (design §2): ни приём страницы, ни запись meta его не воскрешают. Каталог с таким id уже
// есть — ErrExists (вызывающий разыгрывает новый id). Вызывать под мьютексом альбома.
func (s *Store) CreateAlbum(m *Meta) error {
	if s.ro {
		return ErrReadOnly
	}
	if m == nil {
		return errors.New("store: пустая meta")
	}
	if err := checkNames(m.Owner, m.ID); err != nil {
		return err
	}
	if err := s.root.MkdirAll(albumsPath(m.Owner), dirPerm); err != nil {
		return s.opErr("mkdir owner", m.ID, err)
	}
	if err := s.root.Mkdir(albumPath(m.Owner, m.ID), dirPerm); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return s.opErr("mkdir album", m.ID, err)
	}
	if err := s.WriteMeta(m); err != nil {
		if errors.Is(err, ErrNotDurable) {
			return err // meta на месте: альбом создан, недоказанная долговечность — забота вызывающего
		}
		// Каталог без meta — сирота; убираем сразу, а не ждём уборки.
		return errors.Join(err, s.removeDir(albumPath(m.Owner, m.ID), m.ID))
	}
	return nil
}

// removeDir переносит каталог в trash/<имя>.<rand> и удаляет его. Перенос — момент, с которого каталога
// «нет» по прежнему пути; удаление содержимого, если не вышло, достанет уборка.
func (s *Store) removeDir(rel, id string) error {
	if s.ro {
		return ErrReadOnly
	}
	r, err := randName(8)
	if err != nil {
		return err
	}
	name := id + "." + r
	if err := s.root.Rename(rel, trashDir+"/"+name); err != nil {
		return s.opErr("trash", id, err)
	}
	if err := s.purgeTrash(name); err != nil {
		s.log.Warn("корзина не очищена, уберёт следующая уборка", slog.String(logx.KeyOp, "purge"),
			s.tagger.Attr(id), slog.Any(logx.KeyErr, err))
	}
	return nil
}

var trashRE = regexp.MustCompile(`^[a-z2-7]{26}\.[0-9a-f]{16}$`)

// purgeTrash — единственное место, где что-либо удаляется рекурсивно внутри trash/: её зовут и удаление
// альбома, и уборка. Принимается одно имя из trash/ — без разделителей пути; всё остальное отвергает и os.Root.
func (s *Store) purgeTrash(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ErrBadName
	}
	id := ""
	if trashRE.MatchString(name) {
		id = name[:26]
	}
	return s.opErr("purge", id, s.root.RemoveAll(trashDir+"/"+name))
}

// DeleteAlbum — удаление альбома: перенос каталога в корзину (с этого момента по прежнему пути его нет) и
// очистка корзины. Сбой очистки не возвращается: каталог уже недоступен, остаток уберёт уборка (в журнал).
// Нет каталога — ошибка для fs.ErrNotExist. Вызывать под мьютексом альбома.
func (s *Store) DeleteAlbum(owner, id string) error {
	if err := checkNames(owner, id); err != nil {
		return err
	}
	return s.removeDir(albumPath(owner, id), id)
}

// Quarantine переносит каталог альбома в quarantine/<id>.<rand> для разбора вручную: страницы целы.
func (s *Store) Quarantine(owner, id string) error {
	if s.ro {
		return ErrReadOnly
	}
	if err := checkNames(owner, id); err != nil {
		return err
	}
	r, err := randName(8)
	if err != nil {
		return err
	}
	if err := s.root.Rename(albumPath(owner, id), quarDir+"/"+id+"."+r); err != nil {
		return s.opErr("quarantine", id, err)
	}
	return nil
}

// LoadAll читает meta всех альбомов при старте. Негодная meta (не разбирается, id/owner не сошлись с путём)
// — каталог в карантин и строка в журнал, а не удаление. Формат новее своего — ErrNewerFormat: старт
// отказывает. Каталоги без meta.json пропускаются — их убирает SweepAtStart. Порядок — по владельцу и id.
func (s *Store) LoadAll() ([]*Meta, error) {
	var out []*Meta
	owners, err := s.subdirs(ownersDir)
	if err != nil {
		return nil, err
	}
	for _, owner := range owners {
		if !names.ValidSlug(owner) {
			s.log.Warn("каталог владельца с недопустимым именем пропущен", slog.String(logx.KeyOp, "load"))
			continue
		}
		ids, err := s.subdirs(albumsPath(owner))
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !names.ValidID(id) {
				s.log.Warn("каталог альбома с недопустимым именем пропущен", slog.String(logx.KeyOp, "load"))
				continue
			}
			m, err := s.ReadMeta(owner, id)
			switch {
			case err == nil:
				out = append(out, m)
			case errors.Is(err, fs.ErrNotExist):
				// без meta — сирота, его уберёт SweepAtStart
			case errors.Is(err, ErrCorruptMeta) && s.ro:
				return nil, err // verify копии ничего не переносит: ошибку видит вызывающий
			case errors.Is(err, ErrCorruptMeta):
				s.log.Error("meta негодна, каталог в карантин", slog.String(logx.KeyOp, "load"),
					s.tagger.Attr(id), slog.Any(logx.KeyErr, err))
				if qerr := s.Quarantine(owner, id); qerr != nil {
					return nil, fmt.Errorf("карантин: %w", qerr)
				}
			default:
				return nil, err
			}
		}
	}
	return out, nil
}

// subdirs — имена подкаталогов (не файлов и не ссылок) каталога rel, по порядку. Нет каталога — пусто.
func (s *Store) subdirs(rel string) ([]string, error) {
	ents, err := s.readDir(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, s.opErr("readdir", "", err)
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (s *Store) readDir(rel string) ([]fs.DirEntry, error) {
	d, err := s.root.Open(rel)
	if err != nil {
		return nil, err
	}
	ents, err := d.ReadDir(-1)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, err
}

// CommitPage переносит принятую страницу из tmp/ в каталог альбома под именем name (<sha256>.jpg): fsync
// файла, rename, fsync каталога. Каталога альбома нет (удалён, пока читалось тело) — ошибка для
// fs.ErrNotExist, каталог не создаётся. Файл уже есть — временный удаляется, это успех (имя — хэш
// содержимого). В любом исходе Part закрыт. Вызывать под мьютексом альбома.
func (s *Store) CommitPage(owner, id, name string, p *Part) error {
	if err := checkNames(owner, id); err != nil {
		return errors.Join(err, p.Abort())
	}
	if !names.ValidPageName(name) {
		return errors.Join(ErrBadName, p.Abort())
	}
	final := albumPath(owner, id) + "/" + name
	if _, err := s.root.Lstat(final); err == nil {
		return p.Abort()
	}
	err := s.step(stepSync)
	if err == nil {
		err = p.f.Sync()
	}
	if err != nil {
		return s.opErr("page sync", id, errors.Join(err, p.Abort()))
	}
	p.done = true
	if err := p.f.Close(); err != nil {
		return s.opErr("page close", id, s.discard(nil, p.name, err))
	}
	return s.finish(p.name, final, id)
}

// OpenPage открывает файл страницы для отдачи. Имя — только по шаблону.
func (s *Store) OpenPage(owner, id, name string) (io.ReadSeekCloser, error) {
	if err := checkNames(owner, id); err != nil {
		return nil, err
	}
	if !names.ValidPageName(name) {
		return nil, ErrBadName
	}
	f, err := s.root.Open(albumPath(owner, id) + "/" + name)
	if err != nil {
		return nil, s.opErr("open page", id, err)
	}
	return f, nil
}

// PageSize — размер файла страницы; нет файла — ошибка для fs.ErrNotExist.
func (s *Store) PageSize(owner, id, name string) (int64, error) {
	if err := checkNames(owner, id); err != nil {
		return 0, err
	}
	if !names.ValidPageName(name) {
		return 0, ErrBadName
	}
	fi, err := s.root.Lstat(albumPath(owner, id) + "/" + name)
	if err != nil {
		return 0, s.opErr("stat page", id, err)
	}
	if !fi.Mode().IsRegular() {
		return 0, s.opErr("stat page", id, errors.New("не обычный файл"))
	}
	return fi.Size(), nil
}

// ListPages — имена файлов страниц в каталоге альбома (всё, что подходит под шаблон), по порядку.
func (s *Store) ListPages(owner, id string) ([]string, error) {
	if err := checkNames(owner, id); err != nil {
		return nil, err
	}
	ents, err := s.readDir(albumPath(owner, id))
	if err != nil {
		return nil, s.opErr("list pages", id, err)
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && names.ValidPageName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// PrunePages удаляет файлы страниц, которых keep не признаёт (файлы вне published ∪ draft), и возвращает
// их число. Первая ошибка не прерывает проход: остальное тоже пытаются удалить, ошибки объединяются.
// Вызывать под мьютексом альбома.
func (s *Store) PrunePages(owner, id string, keep func(name string) bool) (int, error) {
	if s.ro {
		return 0, ErrReadOnly
	}
	files, err := s.ListPages(owner, id)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, n := range files {
		if keep(n) {
			continue
		}
		if err := s.root.Remove(albumPath(owner, id) + "/" + n); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, s.opErr("remove page", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// AlbumRef — адрес каталога альбома: владелец и id (имена по грамматике).
type AlbumRef struct{ Owner, ID string }

// AlbumDirs — все каталоги альбомов с допустимыми именами, по владельцу и id, без разбора meta: verify
// обходит их и сам читает каждую meta, чтобы собрать все проблемы, а не остановиться на первой.
func (s *Store) AlbumDirs() ([]AlbumRef, error) {
	var out []AlbumRef
	err := s.eachAlbumDir(func(owner, id string) { out = append(out, AlbumRef{Owner: owner, ID: id}) })
	return out, err
}
