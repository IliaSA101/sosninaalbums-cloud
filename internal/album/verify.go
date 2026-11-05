package album

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// Problem — найденная проблема копии данных. Ref — первые 8 hex SHA-256 от id (не сам id: id — ключ доступа,
// а вывод verify попадает в терминал и журнал оператора); по нему каталог находят так же: sha256sum от имени.
type Problem struct {
	Owner string
	Ref   string
	What  string
}

// Report — итог verify.
type Report struct {
	Albums   int
	Problems []Problem
}

// OK — проблем нет.
func (r Report) OK() bool { return len(r.Problems) == 0 }

func refOf(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}

// Verify проверяет копию data/ только на чтение (design §8; st — после store.OpenReadOnly): у каждого
// каталога альбома meta разбирается, формат не новее своего, id и владелец сходятся с путём; у каждой
// страницы опубликованной редакции есть файл заявленного размера. Все проблемы собираются, а не
// только первая; ошибка — только если пройти обход не удалось.
func Verify(st *store.Store) (Report, error) {
	dirs, err := st.AlbumDirs()
	if err != nil {
		return Report{}, fmt.Errorf("обход каталогов: %w", err)
	}
	var rep Report
	for _, d := range dirs {
		rep.Albums++
		add := func(what string) {
			rep.Problems = append(rep.Problems, Problem{Owner: d.Owner, Ref: refOf(d.ID), What: what})
		}
		m, err := st.ReadMeta(d.Owner, d.ID)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			add("нет meta.json")
			continue
		case errors.Is(err, store.ErrNewerFormat):
			add("формат meta.json новее этой версии")
			continue
		case errors.Is(err, store.ErrCorruptMeta):
			add("meta.json негоден или не сходится с путём")
			continue
		case err != nil:
			return rep, fmt.Errorf("чтение meta: %w", err)
		}
		if m.Published == nil {
			continue
		}
		for _, p := range m.Published.Manifest.Pages {
			if p.Blank {
				continue
			}
			size, err := st.PageSize(d.Owner, d.ID, p.File)
			switch {
			case errors.Is(err, fs.ErrNotExist) || errors.Is(err, store.ErrBadName):
				add(fmt.Sprintf("нет файла страницы %d", p.N))
			case err != nil:
				return rep, fmt.Errorf("размер страницы: %w", err)
			case size != p.Bytes:
				add(fmt.Sprintf("размер файла страницы %d не совпадает с манифестом", p.N))
			}
		}
	}
	return rep, nil
}
