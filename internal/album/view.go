package album

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// Всё, что нужно /a/<id>/… (пакет view, Г3): снимок доступа, манифест и открытие файла. Невидимый альбом —
// несуществующий, не опубликованный, неполный, истёкший — всегда один и тот же ErrNotFound: по времени
// запроса, а не по уборке (design §3, §5.1).

// Access — снимок доступа к опубликованному альбому. Пароль — декодированные соль и хэш; nil — пароля нет.
type Access struct {
	Owner    string
	Expires  time.Time
	Epoch    int64
	Password *PasswordRecord
}

// PasswordRecord — соль и HMAC пароля альбома.
type PasswordRecord struct{ Salt, Hash []byte }

// File — открытый файл страницы для http.ServeContent: отдача идёт из дескриптора после снятия мьютекса.
type File struct {
	io.ReadSeekCloser
	Type string // Content-Type из манифеста
	Size int64  // длина по манифесту, байт (для срока записи ответа)
}

// visible — опубликован, полон и не истёк по времени запроса. Без мьютекса: meta — неизменяемый снимок.
func (s *Service) visible(id string) (*album, *store.Meta, error) {
	a := s.lookup(id)
	if a == nil {
		return nil, nil, ErrNotFound
	}
	m := a.meta.Load()
	if m.Published == nil || a.incomplete.Load() || !s.now().Before(m.Expires) {
		return nil, nil, ErrNotFound
	}
	return a, m, nil
}

// ErrDenied — вход не подтверждён: проверка allow из PublishedManifest и OpenPublished отказала. Клиенту это 401
// независимо от того, есть ли в альбоме запрошенный файл (иначе 404 вместо 401 подтверждал бы догадку о хэше).
var ErrDenied = errors.New("вход не подтверждён")

// snapshot — видимый альбом и его снимок доступа из одной и той же meta. Негодная запись пароля (не base64, не
// те длины) — отказ как у несуществующего альбома, а не открытый альбом: ошибка записи не должна снимать пароль.
func (s *Service) snapshot(id string) (*album, *store.Meta, Access, error) {
	a, m, err := s.visible(id)
	if err != nil {
		return nil, nil, Access{}, err
	}
	out := Access{Owner: m.Owner, Expires: m.Expires, Epoch: m.Epoch}
	if m.Password != nil {
		enc := base64.RawURLEncoding.Strict()
		salt, err1 := enc.DecodeString(m.Password.Salt)
		hash, err2 := enc.DecodeString(m.Password.Hash)
		if err1 != nil || err2 != nil || len(salt) == 0 || len(hash) == 0 {
			s.log.Error("запись пароля альбома негодна", slog.String(logx.KeyOp, "access"), s.tagger.Attr(id))
			return nil, nil, Access{}, ErrNotFound
		}
		out.Password = &PasswordRecord{Salt: salt, Hash: hash}
	}
	return a, m, out, nil
}

// Access — снимок для проверки входа.
func (s *Service) Access(id string) (Access, error) {
	_, _, acc, err := s.snapshot(id)
	return acc, err
}

// PublishedManifest — manifest.json опубликованной редакции: повторная сериализация типа контракта (лишних
// полей входа в нём нет), file заполнены сервисом. allow получает снимок доступа из той же meta, по которой
// строится ответ: пароль, поставленный между проверкой входа и отдачей, не пропустит ответ (nil — без проверки).
func (s *Service) PublishedManifest(id string, allow func(Access) bool) ([]byte, error) {
	_, m, acc, err := s.snapshot(id)
	if err != nil {
		return nil, err
	}
	if allow != nil && !allow(acc) {
		return nil, ErrDenied
	}
	b, err := json.Marshal(m.Published.Manifest)
	if err != nil {
		return nil, fmt.Errorf("манифест не собран: %w", err)
	}
	return b, nil
}

// OpenPublished открывает файл страницы опубликованной редакции (001-24). Под RLock альбома: видимость
// проверяется заново, allow проверяет вход по снимку из той же meta (до поиска имени: отказ не зависит от того,
// есть ли файл), имя ищется в текущей published (имя прежней редакции после publish — 404 только ему),
// дескриптор открывается до снятия мьютекса. ENOENT у файла текущей published — альбом «неполный»: 404 на
// всех адресах, пока его не опубликуют заново. Size — длина файла по манифесту.
func (s *Service) OpenPublished(id, name string, allow func(Access) bool) (*File, error) {
	defer s.lock(id, false)()
	a, m, acc, err := s.snapshot(id)
	if err != nil {
		return nil, err
	}
	if allow != nil && !allow(acc) {
		return nil, ErrDenied
	}
	typ, size := "", int64(0)
	for _, p := range m.Published.Manifest.Pages {
		if !p.Blank && p.File == name {
			typ, size = p.Type, p.Bytes
			break
		}
	}
	if typ == "" {
		return nil, ErrNotFound
	}
	f, err := s.st.OpenPage(m.Owner, id, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !a.incomplete.Swap(true) {
				s.log.Error("у опубликованного альбома нет файла страницы", slog.String(logx.KeyOp, "serve"), s.tagger.Attr(id))
			}
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("открытие страницы: %w", err)
	}
	return &File{ReadSeekCloser: f, Type: typ, Size: size}, nil
}
