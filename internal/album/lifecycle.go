package album

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz234567" // base32 строчными: ровно 32 знака, байт & 31 без перекоса
	idLen      = 26                                 // 130 бит
	idAttempts = 8
)

// newID — id альбома (001-15): 26 знаков a–z2–7, каждый — байт crypto/rand & 31. crypto/rand.Text не берём:
// его длина может вырасти в будущих версиях Go. id проходит albums.CheckID.
func (s *Service) newID() (string, error) {
	var raw [idLen]byte
	if _, err := io.ReadFull(s.rnd, raw[:]); err != nil {
		return "", fmt.Errorf("случайные байты для id: %w", err)
	}
	var b [idLen]byte
	for i, c := range raw {
		b[i] = idAlphabet[c&31]
	}
	return string(b[:]), nil
}

// clamp зажимает срок к now + max_days и приводит к UTC с точностью до секунды.
func (s *Service) clamp(t, now time.Time) time.Time {
	limit := now.Add(time.Duration(s.cfg.Limits.MaxDays) * 24 * time.Hour)
	if t.After(limit) {
		t = limit
	}
	return t.UTC().Truncate(time.Second)
}

// wrapStore оборачивает ошибку хранилища; «нет места» становится TooLargeError (413), а не внутренней ошибкой.
func wrapStore(what string, err error) error {
	if store.IsNoSpace(err) {
		return &TooLargeError{Reason: ReasonDisk}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// newPassword — запись пароля листалки: новая соль, HMAC от соли, id и нормализованного пароля (design §5.3).
func (s *Service) newPassword(id, password string) (*store.Password, error) {
	n, err := access.Prepare(password)
	if err != nil {
		return nil, &BadManifestError{Err: err}
	}
	salt, err := access.NewSalt(s.rnd)
	if err != nil {
		return nil, fmt.Errorf("соль пароля: %w", err)
	}
	h, err := s.pw.Hash(id, salt, n)
	if err != nil {
		return nil, fmt.Errorf("хэш пароля: %w", err)
	}
	enc := base64.RawURLEncoding
	return &store.Password{Alg: "hmac-sha256", Key: "pw/1", Salt: enc.EncodeToString(salt), Hash: enc.EncodeToString(h)}, nil
}

// Create — POST /v1/albums. Проверки: предел страниц и суммы байт (413), контракт (400), правила сервиса
// (WebP выключен, одинаковый хэш — одинаковое описание), пароль, число альбомов, квота, запас места (413).
func (s *Service) Create(owner string, req *albums.CreateRequest) (albums.Created, error) {
	lim, ok := s.cfg.Owners[owner]
	if !ok {
		return albums.Created{}, errors.New("album: владелец не настроен")
	}
	now := s.now()
	if err := s.checkLimits(&req.Manifest); err != nil {
		return albums.Created{}, err
	}
	if err := albums.ValidateCreate(req, s.cfg.Limits, now); err != nil {
		return albums.Created{}, &BadManifestError{Err: err}
	}
	if err := s.checkRules(&req.Manifest); err != nil {
		return albums.Created{}, err
	}
	if req.Password != "" {
		if _, err := access.Prepare(req.Password); err != nil {
			return albums.Created{}, &BadManifestError{Err: err}
		}
	}
	rev := &store.Revision{At: now, Manifest: fill(req.Manifest)}
	expires := s.clamp(req.ExpiresAt, now)
	if err := s.checkDisk(newBytes(rev, nil)); err != nil {
		return albums.Created{}, err
	}
	for range idAttempts {
		id, err := s.newID()
		if err != nil {
			return albums.Created{}, err
		}
		m := &store.Meta{Format: store.MetaFormat, ID: id, Owner: owner, Created: now, Updated: now, Expires: expires, Draft: rev}
		if req.Password != "" {
			if m.Password, err = s.newPassword(id, req.Password); err != nil {
				return albums.Created{}, err
			}
		}
		inserted, err := s.insertNew(lim, m)
		if err != nil {
			return albums.Created{}, err
		}
		if inserted {
			return albums.Created{ID: id, URL: s.url(id), ExpiresAt: expires, Missing: missingOf(rev, nil)}, nil
		}
	}
	return albums.Created{}, errors.New("album: не удалось выдать свободный id")
}

// insertNew создаёт каталог и meta нового альбома и кладёт его в индекс. false без ошибки — id занят
// (в индексе или каталогом на диске): розыгрыш повторяют.
func (s *Service) insertNew(lim OwnerLimits, m *store.Meta) (bool, error) {
	defer s.lock(m.ID, true)()
	if s.lookup(m.ID) != nil {
		return false, nil
	}
	usage := usageOf(m)
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if s.count[m.Owner] >= lim.MaxAlbums {
		return false, &TooLargeError{Reason: ReasonAlbums}
	}
	if s.used[m.Owner]+usage > lim.QuotaBytes {
		return false, &TooLargeError{Reason: ReasonQuota}
	}
	if err := s.st.CreateAlbum(m); err != nil {
		switch {
		case errors.Is(err, store.ErrExists):
			return false, nil
		case errors.Is(err, store.ErrNotDurable):
			s.log.Warn("meta записана, но не доказана на диске", slog.String(logx.KeyOp, "create"), s.tagger.Attr(m.ID))
		default:
			return false, wrapStore("создание альбома", err)
		}
	}
	a := &album{usage: usage}
	a.meta.Store(m)
	s.idxMu.Lock()
	s.idx[m.ID] = a
	s.idxMu.Unlock()
	s.used[m.Owner] += usage
	s.count[m.Owner]++
	return true, nil
}

// commit записывает новую meta альбома и публикует её в памяти. Вызывать под мьютексом альбома (на запись).
// checkQuota — проверить квоту владельца, если вклад альбома вырос. Ошибка записи оставляет прежнее состояние.
func (s *Service) commit(a *album, m *store.Meta, checkQuota bool) error {
	usage := usageOf(m)
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if checkQuota && usage > a.usage && s.used[m.Owner]-a.usage+usage > s.cfg.Owners[m.Owner].QuotaBytes {
		return &TooLargeError{Reason: ReasonQuota}
	}
	if err := s.st.WriteMeta(m); err != nil {
		if !errors.Is(err, store.ErrNotDurable) {
			return wrapStore("запись meta", err)
		}
		s.log.Warn("meta записана, но не доказана на диске", slog.String(logx.KeyOp, "meta"), s.tagger.Attr(m.ID))
	}
	s.used[m.Owner] += usage - a.usage
	a.usage = usage
	a.meta.Store(m)
	return nil
}

// prune удаляет файлы страниц вне published ∪ draft. Ошибка не отменяет уже записанную meta: в журнал.
func (s *Service) prune(m *store.Meta) {
	keep := keepSet(m)
	_, err := s.st.PrunePages(m.Owner, m.ID, func(name string) bool { _, ok := keep[name]; return ok })
	if err != nil {
		s.log.Warn("файлы прежней редакции не удалены", slog.String(logx.KeyOp, "prune"), s.tagger.Attr(m.ID),
			slog.Any(logx.KeyErr, err))
	}
}

// Update — PUT /v1/albums/{id}: новая редакция (draft). Прежняя published видна клиенту до publish. Время
// первой неопубликованной правки не обновляется (иначе правку можно держать вечно). Возвращает номера
// страниц новой редакции, которых на диске ещё нет.
func (s *Service) Update(owner, id string, man albums.Manifest) ([]int, error) {
	if err := s.checkLimits(&man); err != nil {
		return nil, err
	}
	if err := albums.Validate(&man, s.cfg.Limits); err != nil {
		return nil, &BadManifestError{Err: err}
	}
	if err := s.checkRules(&man); err != nil {
		return nil, err
	}
	var missing []int
	err := s.owned(owner, id, true, func(a *album, m *store.Meta) error {
		now := s.now()
		rev := &store.Revision{At: now, Manifest: fill(man)}
		if m.Draft != nil {
			rev.At = m.Draft.At
		}
		if err := s.checkDisk(newBytes(rev, keepSet(m))); err != nil {
			return err
		}
		c := *m
		c.Draft, c.Updated = rev, now
		if err := s.commit(a, &c, true); err != nil {
			return err
		}
		s.prune(&c)
		present, err := s.presentFiles(owner, id)
		if err != nil {
			return err
		}
		missing = missingOf(rev, present)
		return nil
	})
	return missing, err
}

// Publish — POST /v1/albums/{id}/publish: draft становится published, файлы, которых нет в новой published,
// удаляются. Без draft — повторный publish: 200, если все файлы текущей редакции на месте (так «неполный»
// опубликованный альбом выкладывают заново: страницы догружают в published, затем publish). Не все файлы —
// MissingError.
func (s *Service) Publish(owner, id string) (albums.Published, error) {
	var out albums.Published
	err := s.owned(owner, id, true, func(a *album, m *store.Meta) error {
		present, err := s.presentFiles(owner, id)
		if err != nil {
			return err
		}
		if miss := missingOf(currentRev(m), present); len(miss) > 0 {
			return &MissingError{Missing: miss}
		}
		if m.Draft != nil {
			now := s.now()
			c := *m
			c.Published = &store.Revision{At: now, Manifest: m.Draft.Manifest}
			c.Draft, c.Updated = nil, now
			if err := s.commit(a, &c, false); err != nil {
				return err
			}
			s.prune(&c)
			m = &c
		}
		a.incomplete.Store(false)
		out = albums.Published{URL: s.url(id), ExpiresAt: m.Expires}
		return nil
	})
	return out, err
}

// Patch — PATCH /v1/albums/{id}: срок, пароль ("" — снять), сброс входов. Новый пароль получает новую соль, а
// соль входит в подпись cookie: смена пароля закрывает прежние входы сама (design §5.3).
func (s *Service) Patch(owner, id string, p *albums.Patch) (albums.Album, error) {
	now := s.now()
	if err := albums.ValidatePatch(p, now); err != nil {
		return albums.Album{}, &BadManifestError{Err: err}
	}
	if p.Password != nil && *p.Password != "" {
		if _, err := access.Prepare(*p.Password); err != nil {
			return albums.Album{}, &BadManifestError{Err: err}
		}
	}
	var out albums.Album
	err := s.owned(owner, id, true, func(a *album, m *store.Meta) error {
		c := *m
		c.Updated = now
		if p.ExpiresAt != nil {
			c.Expires = s.clamp(*p.ExpiresAt, now)
		}
		if p.Password != nil {
			c.Password = nil
			if *p.Password != "" {
				pw, err := s.newPassword(id, *p.Password)
				if err != nil {
					return err
				}
				c.Password = pw
			}
		}
		if p.RevokeSessions {
			c.Epoch++
		}
		if err := s.commit(a, &c, false); err != nil {
			return err
		}
		out = s.albumView(&c)
		return nil
	})
	return out, err
}

// Delete — DELETE /v1/albums/{id}: альбом удалён сразу, все адреса — 404.
func (s *Service) Delete(owner, id string) error {
	return s.owned(owner, id, true, func(a *album, m *store.Meta) error { return s.drop(a, m) })
}

// drop убирает альбом: каталог — в корзину и на удаление, запись — из индекса и из учёта квоты. Под
// мьютексом альбома (на запись). Ошибка переноса в корзину оставляет альбом на месте.
func (s *Service) drop(a *album, m *store.Meta) error {
	if err := s.st.DeleteAlbum(m.Owner, m.ID); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return wrapStore("удаление альбома", err)
	}
	s.idxMu.Lock()
	delete(s.idx, m.ID)
	s.idxMu.Unlock()
	s.quotaMu.Lock()
	s.used[m.Owner] -= a.usage
	s.count[m.Owner]--
	s.quotaMu.Unlock()
	return nil
}

// albumView — альбом в списке и ответе PATCH: название и число страниц — из published, иначе из draft.
func (s *Service) albumView(m *store.Meta) albums.Album {
	rev := m.Published
	if rev == nil {
		rev = m.Draft
	}
	v := albums.Album{ID: m.ID, URL: s.url(m.ID), ExpiresAt: m.Expires, HasPassword: m.Password != nil, Published: m.Published != nil}
	if rev != nil {
		v.Title, v.Pages = rev.Manifest.Title, len(rev.Manifest.Pages)
	}
	return v
}

// List — GET /v1/albums: свои альбомы без истёкших, по id. Читает неизменяемые снимки meta без мьютексов альбомов.
func (s *Service) List(owner string) []albums.Album {
	now := s.now()
	out := []albums.Album{}
	for _, id := range s.ids() {
		a := s.lookup(id)
		if a == nil {
			continue
		}
		if m := a.meta.Load(); m.Owner == owner && now.Before(m.Expires) {
			out = append(out, s.albumView(m))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
