package store

import (
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// ActiveAge — значение по умолчанию (Options.ActiveAge): возраст, начиная с которого временный файл или каталог без meta считаются брошенными в
// работе (001-23): дольше любого запроса (чтение страницы — до 540 с, design §4), поэтому идущий `PUT`,
// чужой writeAtomic и окно между созданием каталога и записью meta в `POST` не режутся.
const ActiveAge = 600 * time.Second

// Guard — то, что уборке нужно от владельца индекса альбомов (пакет album). Условия реализации:
//   - fn вызывается синхронно, в той же горутине, что и WithAlbumLock, и до его возврата;
//   - мьютекс альбома id (на запись) взят до вычисления inIndex и удерживается всё время fn: inIndex
//     описывает индекс на момент под мьютексом, а не на момент вызова — иначе POST успеет положить id в
//     индекс между проверкой и удалением каталога;
//   - мьютекс даётся и для id, которых в индексе нет (сироты).
type Guard interface {
	WithAlbumLock(id string, fn func(inIndex bool) error) error
}

// SweepStats — сколько убрано.
type SweepStats struct {
	Tmp     int // временные файлы
	Trash   int // каталоги корзины
	Orphans int // каталоги альбомов без meta.json
}

// SweepAtStart убирает весь мусор: tmp/*, trash/*, каталоги альбомов без meta.json. Зовётся один раз, до
// приёма запросов (в процессе нет ни одного запроса, который мог бы писать). Негодную meta сюда не
// отдают: её переносит в карантин LoadAll, поэтому LoadAll вызывают раньше.
func (s *Store) SweepAtStart() (SweepStats, error) {
	if s.ro {
		return SweepStats{}, ErrReadOnly
	}
	var st SweepStats
	var errs []error
	ents, err := s.readDir(tmpDir)
	if err != nil {
		errs = append(errs, s.opErr("sweep tmp", "", err))
	}
	for _, e := range ents {
		if err := s.root.RemoveAll(tmpDir + "/" + e.Name()); err != nil {
			errs = append(errs, s.opErr("sweep tmp", "", err))
			continue
		}
		st.Tmp++
	}
	ents, err = s.readDir(trashDir)
	if err != nil {
		errs = append(errs, s.opErr("sweep trash", "", err))
	}
	for _, e := range ents {
		// В корзину попадает только своё; всё, что в ней лежит, — к удалению, и по шаблону, и нет.
		if err := s.purgeTrash(e.Name()); err != nil {
			errs = append(errs, err)
			continue
		}
		st.Trash++
	}
	err = s.eachAlbumDir(func(owner, id string) {
		if !s.hasMeta(owner, id) {
			if err := s.removeDir(albumPath(owner, id), id); err != nil {
				errs = append(errs, err)
				return
			}
			st.Orphans++
		}
	})
	if err != nil {
		errs = append(errs, err)
	}
	return st, errors.Join(errs...)
}

// SweepRunning — уборка в работе (раз в 5 минут): только tmp/* старше ActiveAge; trash/* и каталог альбома
// без meta.json — под мьютексом этого альбома, только если id нет в индексе и mtime старше ActiveAge.
func (s *Store) SweepRunning(g Guard) (SweepStats, error) {
	if s.ro {
		return SweepStats{}, ErrReadOnly
	}
	var st SweepStats
	var errs []error
	// Возраст — от настенных часов процесса, а не от часов хранилища: mtime ставит ядро по тем же часам.
	now := time.Now()
	old := func(e fs.DirEntry) bool {
		fi, err := e.Info()
		return err == nil && now.Sub(fi.ModTime()) > s.activeAge
	}

	// Пока стоит пауза сторожа часов (clock_hold), «старше 600 с» ничего не значит: часы ушли вперёд, и
	// идущий приём выглядел бы брошенным. tmp не трогаем до снятия паузы.
	var ents []fs.DirEntry
	var err error
	if s.Layout().ClockHold == nil {
		if ents, err = s.readDir(tmpDir); err != nil {
			errs = append(errs, s.opErr("sweep tmp", "", err))
		}
	}
	for _, e := range ents {
		if !old(e) {
			continue
		}
		if err := s.root.RemoveAll(tmpDir + "/" + e.Name()); err != nil {
			errs = append(errs, s.opErr("sweep tmp", "", err))
			continue
		}
		st.Tmp++
	}

	ents, err = s.readDir(trashDir)
	if err != nil {
		errs = append(errs, s.opErr("sweep trash", "", err))
	}
	for _, e := range ents {
		id, _, _ := strings.Cut(e.Name(), ".")
		if !names.ValidID(id) || !trashRE.MatchString(e.Name()) || !old(e) {
			continue
		}
		name := e.Name()
		err := g.WithAlbumLock(id, func(inIndex bool) error {
			if inIndex { // id в корзине, но жив в индексе: так не бывает, не трогаем
				return nil
			}
			if err := s.purgeTrash(name); err != nil {
				return err
			}
			st.Trash++
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}

	err = s.eachAlbumDir(func(owner, id string) {
		if s.hasMeta(owner, id) {
			return
		}
		// Свежий каталог (окно POST) мьютекса не стоит; возраст проверяется ещё раз под ним.
		if fi, err := s.root.Lstat(albumPath(owner, id)); err != nil || now.Sub(fi.ModTime()) <= s.activeAge {
			return
		}
		err := g.WithAlbumLock(id, func(inIndex bool) error {
			if inIndex || s.hasMeta(owner, id) { // повторная проверка под мьютексом
				return nil
			}
			fi, err := s.root.Lstat(albumPath(owner, id))
			if err != nil {
				return s.opErr("sweep orphan", id, err)
			}
			if now.Sub(fi.ModTime()) <= s.activeAge {
				return nil
			}
			if err := s.removeDir(albumPath(owner, id), id); err != nil {
				return err
			}
			st.Orphans++
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	})
	if err != nil {
		errs = append(errs, err)
	}
	if st.Orphans+st.Trash+st.Tmp > 0 {
		s.log.Info("уборка мусора", slog.String(logx.KeyOp, "sweep"), slog.Int("count", st.Orphans+st.Trash+st.Tmp))
	}
	return st, errors.Join(errs...)
}

// hasMeta — есть ли в каталоге альбома meta.json (любой: разбор — дело LoadAll).
func (s *Store) hasMeta(owner, id string) bool {
	_, err := s.root.Lstat(albumPath(owner, id) + "/meta.json")
	return err == nil
}

// eachAlbumDir обходит owners/<slug>/albums/<id> с допустимыми именами, по порядку.
func (s *Store) eachAlbumDir(fn func(owner, id string)) error {
	owners, err := s.subdirs(ownersDir)
	if err != nil {
		return err
	}
	for _, o := range owners {
		if !names.ValidSlug(o) {
			continue
		}
		ids, err := s.subdirs(albumsPath(o))
		if err != nil {
			return err
		}
		for _, id := range ids {
			if names.ValidID(id) {
				fn(o, id)
			}
		}
	}
	return nil
}
