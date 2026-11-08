package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"
)

// flockNB берёт исключительный замок без ожидания. Дескриптор файла — номер в таблице процесса, в int он
// помещается всегда.
func flockNB(f *os.File) error {
	fd := int(f.Fd())
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// Layout — содержимое data/LAYOUT (design §2): версия раскладки, последний момент, когда сервис видел
// исправные часы, и пауза сторожа часов. Неизвестные поля сохраняются.
type Layout struct {
	Layout    int        `json:"layout"`
	LastSeen  time.Time  `json:"last_seen"`
	ClockHold *ClockHold `json:"clock_hold"`
	extra     map[string]json.RawMessage
}

// ClockHold — пауза удалений по времени: причина и момент (001-19).
type ClockHold struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

type plainLayout Layout

// MarshalJSON пишет известные поля и сохранённые неизвестные.
func (l Layout) MarshalJSON() ([]byte, error) { return encodeKeep(plainLayout(l), l.extra) }

// UnmarshalJSON разбирает известные поля и запоминает прочие.
func (l *Layout) UnmarshalJSON(b []byte) error {
	var p plainLayout
	extra, err := decodeKeep(b, &p)
	if err != nil {
		return err
	}
	*l = Layout(p)
	l.extra = extra
	return nil
}

func (s *Store) loadLayout() error {
	b, err := s.root.ReadFile(layoutFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if s.ro {
			return ErrNoLayout
		}
		s.layout = Layout{Layout: LayoutVersion, LastSeen: s.clock().UTC()}
		return s.writeLayoutLocked()
	case err != nil:
		return s.opErr("read layout", "", err)
	}
	l, err := parseLayout(b)
	if err != nil {
		return err
	}
	s.layout = l
	return nil
}

// parseLayout сначала смотрит только на версию: LAYOUT новее своего — отказ старта (ErrNewerFormat), даже
// если остальные поля новой версии другого вида и полный разбор не прошёл бы.
func parseLayout(b []byte) (Layout, error) {
	var h struct {
		Layout int `json:"layout"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return Layout{}, fmt.Errorf("store: LAYOUT не разобран: %w", ErrCorruptMeta)
	}
	if h.Layout > LayoutVersion {
		return Layout{}, ErrNewerFormat
	}
	if h.Layout < LayoutVersion {
		return Layout{}, fmt.Errorf("store: LAYOUT без версии раскладки: %w", ErrCorruptMeta)
	}
	var l Layout
	if err := json.Unmarshal(b, &l); err != nil {
		return Layout{}, fmt.Errorf("store: LAYOUT не разобран: %w", ErrCorruptMeta)
	}
	return l, nil
}

// Layout — копия прочитанного LAYOUT.
func (s *Store) Layout() Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.layout
}

// SetLayout записывает LAYOUT атомарно. Версия раскладки и неизвестные поля сохраняются от прочитанного.
// Ошибка ErrNotDurable значит, что запись состоялась (rename прошёл): память при ней не откатывается.
func (s *Store) SetLayout(l Layout) error {
	if s.ro {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.layout
	s.layout = Layout{Layout: LayoutVersion, LastSeen: l.LastSeen.UTC(), ClockHold: l.ClockHold, extra: prev.extra}
	if err := s.writeLayoutLocked(); err != nil {
		if !errors.Is(err, ErrNotDurable) {
			s.layout = prev
		}
		return err
	}
	return nil
}

func (s *Store) writeLayoutLocked() error {
	b, err := json.Marshal(s.layout)
	if err != nil {
		return fmt.Errorf("store: LAYOUT не собран: %w", err)
	}
	return s.writeAtomic(layoutFile, b, "")
}

// decodeKeep разбирает data в dst (указатель на структуру с json-тегами) и возвращает поля, которых в
// структуре нет: откат образа не должен стирать поля новой версии.
func decodeKeep(data []byte, dst any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	known := knownKeys(reflect.TypeOf(dst).Elem())
	for k := range all {
		for _, kn := range known {
			if strings.EqualFold(k, kn) { // encoding/json сопоставляет без учёта регистра
				delete(all, k)
				break
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// encodeKeep пишет v и дописывает неизвестные поля, не затирая известные.
func encodeKeep(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, raw := range extra {
		if _, ok := m[k]; !ok {
			m[k] = raw
		}
	}
	return json.Marshal(m)
}

func knownKeys(t reflect.Type) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
	}
	return keys
}
