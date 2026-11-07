package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// Размеры секрета. Файл — base64url, после декодирования не меньше MinSecretBytes; выдаёт -gen-secret
// ровно SecretBytes.
const (
	SecretBytes    = 32
	MinSecretBytes = 32
	maxSecretFile  = 4 << 10
	keyBytes       = 32
)

// Метки HKDF (необратимо: смена метки делает негодными все пароли и входы).
const (
	LabelPassword = "pw/1"
	LabelCookie   = "cookie/1"
	LabelLog      = "log/1"
)

// Keys — ключи, выведенные из мастер-секрета. Каждый служит одной цели, поэтому утечка одного (например,
// метки журнала) не открывает подпись cookie.
type Keys struct {
	Password []byte // k_pw: HMAC пароля листалки
	Cookie   []byte // k_cookie: подпись cookie входа
	Log      []byte // k_log: метка id в журнале
}

// LoadSecret читает файл секрета: права шире 0600 — отказ, хвостовые пробелы и перевод строки срезаются.
// Ошибки не содержат ни содержимого файла, ни его пути.
func LoadSecret(path string) ([]byte, error) {
	data, _, err := readSmall(path, maxSecretFile, true)
	if err != nil {
		return nil, fmt.Errorf("секрет: %w", err)
	}
	return ParseSecret(string(data))
}

// ParseSecret разбирает содержимое файла секрета: base64url (без дополнения или с ним), не меньше
// MinSecretBytes байт. Нестрогое декодирование не допускается: два текста — один секрет, а подпись
// должна зависеть от точных байт.
func ParseSecret(text string) ([]byte, error) {
	s := strings.TrimRight(text, " \t\r\n")
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "\r\n") { // декодер Go молча пропускает переводы строк внутри
		return nil, errors.New("секрет: не base64url")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, errors.New("секрет: не base64url")
	}
	if len(b) < MinSecretBytes {
		return nil, fmt.Errorf("секрет: меньше %d байт", MinSecretBytes)
	}
	return b, nil
}

// DeriveKeys выводит ключи HKDF-SHA256 из мастер-секрета с метками «pw/1», «cookie/1», «log/1».
func DeriveKeys(master []byte) (*Keys, error) {
	if len(master) < MinSecretBytes {
		return nil, fmt.Errorf("секрет: меньше %d байт", MinSecretBytes)
	}
	derive := func(label string) ([]byte, error) {
		k, err := hkdf.Key(sha256.New, master, nil, label, keyBytes)
		if err != nil {
			return nil, fmt.Errorf("hkdf %s: %w", label, err)
		}
		return k, nil
	}
	var k Keys
	var err error
	if k.Password, err = derive(LabelPassword); err != nil {
		return nil, err
	}
	if k.Cookie, err = derive(LabelCookie); err != nil {
		return nil, err
	}
	if k.Log, err = derive(LabelLog); err != nil {
		return nil, err
	}
	return &k, nil
}

// GenSecret — новый мастер-секрет: SecretBytes случайных байт из r (в работе — crypto/rand), base64url
// без дополнения. Печатается на машине владельца, не в юните: иначе значение попало бы в journald.
func GenSecret(r io.Reader) (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("случайные байты: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Format не раскрывает ключи ни под каким глаголом (%v, %+v, %#v, %s, %x): случайный `log.Printf("%v", keys)`
// или `slog.Any("keys", keys)` не должен выдать k_pw и k_cookie.
func (Keys) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, logx.Redacted) }

// LogValue — то же для slog: значение подменяется до того, как до обработчика дойдут байты ключа.
func (Keys) LogValue() slog.Value { return slog.StringValue(logx.Redacted) }
