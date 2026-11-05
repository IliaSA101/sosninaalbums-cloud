package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// MetaFormat — формат meta.json (design §2.1, 001-14).
const MetaFormat = 1

// maxMetaSize — предел чтения meta.json: манифест на 300 страниц — сотни килобайт, а не мегабайты.
const maxMetaSize = 4 << 20

// Meta — источник правды об альбоме (design §2.1). Ссылка, открытый пароль и IP не хранятся. Неизвестные
// поля (как здесь, так и во вложенных Revision и Password) сохраняются при перезаписи: откат образа не
// теряет поля новой версии. Исключение — Manifest: это тип контракта, он сериализуется заново.
type Meta struct {
	Format    int       `json:"format"`
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Expires   time.Time `json:"expires"`
	Published *Revision `json:"published"`
	Draft     *Revision `json:"draft"`
	Password  *Password `json:"password"`
	Epoch     int64     `json:"epoch"`
	extra     map[string]json.RawMessage
}

// Revision — редакция альбома: когда появилась и манифест с заполненными `file`.
type Revision struct {
	At       time.Time       `json:"at"`
	Manifest albums.Manifest `json:"manifest"`
	extra    map[string]json.RawMessage
}

// Password — запись пароля листалки: ничего открытого, только соль и HMAC (design §5.3).
type Password struct {
	Alg   string `json:"alg"`
	Key   string `json:"key"`
	Salt  string `json:"salt"` // base64url, 16 байт
	Hash  string `json:"hash"` // base64url
	extra map[string]json.RawMessage
}

type (
	plainMeta     Meta
	plainRevision Revision
	plainPassword Password
)

// MarshalJSON пишет известные поля и сохранённые неизвестные.
func (m Meta) MarshalJSON() ([]byte, error) { return encodeKeep(plainMeta(m), m.extra) }

// UnmarshalJSON разбирает известные поля и запоминает прочие.
func (m *Meta) UnmarshalJSON(b []byte) error {
	var p plainMeta
	extra, err := decodeKeep(b, &p)
	if err != nil {
		return err
	}
	*m = Meta(p)
	m.extra = extra
	return nil
}

// MarshalJSON — см. Meta.MarshalJSON.
func (r Revision) MarshalJSON() ([]byte, error) { return encodeKeep(plainRevision(r), r.extra) }

// UnmarshalJSON — см. Meta.UnmarshalJSON.
func (r *Revision) UnmarshalJSON(b []byte) error {
	var p plainRevision
	extra, err := decodeKeep(b, &p)
	if err != nil {
		return err
	}
	*r = Revision(p)
	r.extra = extra
	return nil
}

// MarshalJSON — см. Meta.MarshalJSON.
func (p Password) MarshalJSON() ([]byte, error) { return encodeKeep(plainPassword(p), p.extra) }

// UnmarshalJSON — см. Meta.UnmarshalJSON.
func (p *Password) UnmarshalJSON(b []byte) error {
	var q plainPassword
	extra, err := decodeKeep(b, &q)
	if err != nil {
		return err
	}
	*p = Password(q)
	p.extra = extra
	return nil
}

// ReadMeta читает meta.json альбома. Нет файла — ошибка, для которой errors.Is(err, fs.ErrNotExist).
// Не разбирается, или id/owner не сошлись с путём, — ErrCorruptMeta; format новее своего — ErrNewerFormat.
func (s *Store) ReadMeta(owner, id string) (*Meta, error) {
	if err := checkNames(owner, id); err != nil {
		return nil, err
	}
	f, err := s.root.Open(albumPath(owner, id) + "/meta.json")
	if err != nil {
		return nil, s.opErr("read meta", id, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMetaSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, s.opErr("read meta", id, err)
	}
	if len(data) > maxMetaSize {
		return nil, s.opErr("read meta", id, fmt.Errorf("%w: слишком велик", ErrCorruptMeta))
	}
	// Сначала только формат: новее своего — отказ старта, даже если остальное в новом виде не разобралось бы.
	var h struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, s.opErr("read meta", id, fmt.Errorf("%w: не разобран", ErrCorruptMeta))
	}
	if h.Format > MetaFormat {
		return nil, s.opErr("read meta", id, ErrNewerFormat)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		// Текст ошибки json может цитировать содержимое — в ошибку идёт только причина.
		return nil, s.opErr("read meta", id, fmt.Errorf("%w: не разобран", ErrCorruptMeta))
	}
	switch {
	case m.Format > MetaFormat:
		return nil, s.opErr("read meta", id, ErrNewerFormat)
	case m.Format < MetaFormat:
		return nil, s.opErr("read meta", id, fmt.Errorf("%w: нет формата", ErrCorruptMeta))
	case m.ID != id || m.Owner != owner:
		return nil, s.opErr("read meta", id, fmt.Errorf("%w: id или владелец не сошлись с путём", ErrCorruptMeta))
	}
	return &m, nil
}

// WriteMeta атомарно перезаписывает meta.json существующего альбома. Каталог не создаётся: у удалённого
// альбома запись заканчивается ошибкой (fs.ErrNotExist), а не воскрешает каталог (design §2, «сироты
// не воскресают»). Вызывать под мьютексом альбома.
func (s *Store) WriteMeta(m *Meta) error {
	if m == nil {
		return errors.New("store: пустая meta")
	}
	if err := checkNames(m.Owner, m.ID); err != nil {
		return err
	}
	c := *m // формат ставится в копии: вызывающий может держать m под своим мьютексом, а не под нашим
	c.Format = MetaFormat
	b, err := json.Marshal(&c)
	if err != nil {
		return s.opErr("marshal meta", m.ID, errors.New("meta не собрана"))
	}
	return s.writeAtomic(albumPath(m.Owner, m.ID)+"/meta.json", b, m.ID)
}

func albumsPath(owner string) string { return ownersDir + "/" + owner + "/albums" }

func albumPath(owner, id string) string { return albumsPath(owner) + "/" + id }
