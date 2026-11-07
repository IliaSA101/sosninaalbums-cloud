package album

import (
	"fmt"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// currentRev — редакция, которую правит и дополняет фотограф: draft, если есть, иначе published (design §3).
func currentRev(m *store.Meta) *store.Revision {
	if m.Draft != nil {
		return m.Draft
	}
	return m.Published
}

func ext(typ string) string {
	if typ == albums.TypeWebP {
		return "webp"
	}
	return "jpg"
}

// fileOf — имя файла страницы: <sha256>.<ext> (design §2). Имя даёт сервис, клиент его не присылает.
func fileOf(p albums.Page) string { return p.SHA256 + "." + ext(p.Type) }

// fill — копия манифеста с именами файлов у небелых страниц. Исходный не меняется.
func fill(m albums.Manifest) albums.Manifest {
	pages := make([]albums.Page, len(m.Pages))
	copy(pages, m.Pages)
	for i := range pages {
		if !pages[i].Blank {
			pages[i].File = fileOf(pages[i])
		}
	}
	m.Pages = pages
	return m
}

// addFiles дописывает в dst различные файлы редакции: имя → байты. Одинаковые страницы (один хэш) — один файл.
func addFiles(dst map[string]int64, r *store.Revision) {
	if r == nil {
		return
	}
	for _, p := range r.Manifest.Pages {
		if !p.Blank && p.File != "" {
			dst[p.File] = p.Bytes
		}
	}
}

// keepSet — файлы, которые альбом вправе хранить: published ∪ draft (design §3).
func keepSet(m *store.Meta) map[string]int64 {
	out := map[string]int64{}
	addFiles(out, m.Published)
	addFiles(out, m.Draft)
	return out
}

// usageOf — вклад альбома в квоту владельца: байты различных файлов published ∪ draft (001-11).
func usageOf(m *store.Meta) int64 {
	var sum int64
	for _, b := range keepSet(m) {
		sum += b
	}
	return sum
}

// newBytes — сколько байт редакции r ещё нет ни в одном файле набора have: столько места альбому понадобится.
func newBytes(r *store.Revision, have map[string]int64) int64 {
	files := map[string]int64{}
	addFiles(files, r)
	var sum int64
	for f, b := range files {
		if _, ok := have[f]; !ok {
			sum += b
		}
	}
	return sum
}

// missingOf — номера небелых страниц редакции, чьего файла нет на диске (present). Не nil: в JSON — [].
func missingOf(r *store.Revision, present map[string]bool) []int {
	out := []int{}
	if r == nil {
		return out
	}
	for _, p := range r.Manifest.Pages {
		if !p.Blank && !present[p.File] {
			out = append(out, p.N)
		}
	}
	return out
}

// presentFiles — какие файлы страниц есть в каталоге альбома.
func (s *Service) presentFiles(owner, id string) (map[string]bool, error) {
	files, err := s.st.ListPages(owner, id)
	if err != nil {
		return nil, fmt.Errorf("список страниц: %w", err)
	}
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f] = true
	}
	return out, nil
}
