package album

import (
	"errors"
	"log/slog"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// CurrentPages — страницы текущей редакции (draft, иначе published) для PUT страницы: копия, с именами
// файлов. Без мьютекса альбома: последнее слово — за CommitPage, который проверяет всё ещё раз под ним.
// Нет, чужой или просрочен — ErrNotFound.
func (s *Service) CurrentPages(owner, id string) ([]albums.Page, error) {
	a := s.lookup(id)
	if a == nil {
		return nil, ErrNotFound
	}
	m := a.meta.Load()
	if m.Owner != owner || !s.now().Before(m.Expires) {
		return nil, ErrNotFound
	}
	rev := currentRev(m)
	if rev == nil {
		return nil, ErrNotFound
	}
	return append([]albums.Page(nil), rev.Manifest.Pages...), nil
}

// PageExists — файл страницы уже лежит в каталоге альбома (повторный PUT: места ему не нужно).
func (s *Service) PageExists(owner, id, name string) bool {
	_, err := s.st.PageSize(owner, id, name)
	return err == nil
}

// CommitPage переносит принятую страницу из tmp/ в каталог альбома (design §3: финал — под мьютексом с
// повторной проверкой). Повторно проверяется: альбом в индексе, принадлежит owner, срок не истёк, файл
// есть в published ∪ draft. Иначе временный файл удаляется, ответ — ErrNotFound или ErrPageGone. Каталог
// удалённого альбома не воскресает: store.CommitPage каталог не создаёт. Part закрыт в любом исходе.
func (s *Service) CommitPage(owner, id string, p albums.Page, part *store.Part) error {
	err := s.owned(owner, id, true, func(_ *album, m *store.Meta) error {
		if _, ok := keepSet(m)[p.File]; !ok {
			return ErrPageGone
		}
		if err := s.st.CommitPage(owner, id, p.File, part); err != nil {
			if errors.Is(err, store.ErrNotDurable) {
				s.log.Warn("страница записана, но не доказана на диске", slog.String(logx.KeyOp, "page"), s.tagger.Attr(id))
				return nil
			}
			return wrapStore("запись страницы", err)
		}
		return nil
	})
	if err != nil {
		if aerr := part.Abort(); aerr != nil {
			s.log.Warn("временный файл не удалён", slog.String(logx.KeyOp, "page"), slog.Any(logx.KeyErr, aerr))
		}
	}
	return err
}
