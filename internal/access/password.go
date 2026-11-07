package access

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/IliaSA101/sosninaalbums/api/albums"
	"golang.org/x/text/unicode/norm"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// SaltBytes — длина соли пароля альбома.
const SaltBytes = 16

// ErrPassword — пароль не принят при установке; текст без значения пароля.
var ErrPassword = errors.New("пароль не принят")

// Normalize — вид, в котором пароль участвует в HMAC: TrimSpace, затем NFC. Так «é» из двух знаков и из
// одного — один пароль, а пробел по краям (копирование из мессенджера) не мешает входу.
func Normalize(p string) string { return norm.NFC.String(strings.TrimSpace(p)) }

// Passwords — HMAC паролей листалки на ключе k_pw. Медленный KDF не нужен: без ключа (он вне данных и
// бэкапа) офлайн-перебор невозможен (решение по стеку §5); от онлайн-перебора — лимит попыток.
type Passwords struct {
	key []byte
	// onHash вызывается перед каждым хэшированием; поле нужно тесту «пределы до хэша».
	onHash func()
}

// NewPasswords — по ключу k_pw из HKDF с меткой «pw/1».
func NewPasswords(kpw []byte) *Passwords { return &Passwords{key: append([]byte(nil), kpw...)} }

// NewSalt — 16 случайных байт из r (в работе crypto/rand). Новая соль — при каждой смене пароля: соль
// входит в подпись cookie, так что смена пароля закрывает прежние входы сама.
func NewSalt(r io.Reader) ([]byte, error) {
	s := make([]byte, SaltBytes)
	if _, err := io.ReadFull(r, s); err != nil {
		return nil, fmt.Errorf("случайные байты: %w", err)
	}
	return s, nil
}

// Prepare проверяет пароль при установке: CheckPassword контракта, затем обрезка и NFC; после обрезки
// меньше MinPassword знаков — отказ. Возвращает нормализованный пароль для Hash.
func Prepare(p string) (string, error) {
	if err := albums.CheckPassword(p); err != nil {
		return "", ErrPassword
	}
	n := Normalize(p)
	if utf8.RuneCountInString(n) < albums.MinPassword {
		return "", ErrPassword
	}
	return n, nil
}

// Hash — HMAC-SHA256(k_pw, salt ‖ id ‖ пароль); пароль уже нормализован (Prepare). Соль — ровно SaltBytes.
func (h *Passwords) Hash(id string, salt []byte, normalized string) ([]byte, error) {
	if !names.ValidID(id) { // salt ‖ id ‖ пароль однозначно делится на части только при id фиксированной формы
		return nil, errors.New("id не по грамматике")
	}
	if len(salt) != SaltBytes {
		return nil, errors.New("соль не 16 байт")
	}
	if h.onHash != nil {
		h.onHash()
	}
	m := hmac.New(sha256.New, h.key)
	m.Write(salt)
	m.Write([]byte(id))
	m.Write([]byte(normalized))
	return m.Sum(nil), nil
}

// Verify сверяет введённый пароль с сохранённым хэшем. Предел длины и вид (CheckPassword) проверяются до
// нормализации и хэширования: огромное тело формы не доходит до NFC. Пустой пароль, ошибка вида и
// короткий после обрезки — просто «неверно»; сравнение hmac.Equal.
func (h *Passwords) Verify(id string, salt, stored []byte, password string) bool {
	if password == "" || albums.CheckPassword(password) != nil {
		return false
	}
	n := Normalize(password)
	if utf8.RuneCountInString(n) < albums.MinPassword {
		return false
	}
	got, err := h.Hash(id, salt, n)
	if err != nil {
		return false
	}
	return hmac.Equal(got, stored)
}

// Format не раскрывает ключ ни под каким глаголом (%v, %+v, %#v, %s, %x).
func (Passwords) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, logx.Redacted) }

// LogValue — то же для slog.
func (Passwords) LogValue() slog.Value { return slog.StringValue(logx.Redacted) }
