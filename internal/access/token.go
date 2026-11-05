// Package access — доступ: токены загрузки, пароль листалки, cookie входа и лимит попыток входа.
// Все сравнения секретов — за постоянное время.
package access

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// TokenBytes — длина нового токена: 256 бит, как у выдачи ключей.
const TokenBytes = 32

// TokenEntry — запись токена из конфига: владелец, имя для человека, sha256 токена в hex.
type TokenEntry struct {
	Owner  string
	Name   string
	SHA256 string
}

type token struct {
	owner, name string
	sum         [sha256.Size]byte
}

// Tokens — набор токенов всех владельцев. Токен однозначно определяет владельца; альбом принадлежит
// владельцу, а не токену, так что отозвать токен ноутбука можно, не теряя альбомов.
type Tokens struct {
	entries []token
	// equal — сравнение двух хэшей; поле нужно тесту «сверка со всеми записями».
	equal func(a, b []byte) int
}

// NewTokens строит набор из записей конфига. Хэш — 64 hex; повтор хэша — ошибка (токен не должен
// указывать на двух владельцев).
func NewTokens(entries []TokenEntry) (*Tokens, error) {
	t := &Tokens{equal: subtle.ConstantTimeCompare}
	seen := map[[sha256.Size]byte]bool{}
	for i, e := range entries {
		raw, err := hex.DecodeString(e.SHA256)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("токен %d: sha256 — 64 hex", i)
		}
		var sum [sha256.Size]byte
		copy(sum[:], raw)
		if seen[sum] {
			return nil, fmt.Errorf("токен %d: хэш повторяется", i)
		}
		seen[sum] = true
		t.entries = append(t.entries, token{owner: e.Owner, name: e.Name, sum: sum})
	}
	return t, nil
}

// HashToken — sha256 токена в hex: то, что записывается в конфиг.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// NewToken выдаёт токен (32 байта из r, base64url) и строку sha256 для конфига. В работе r — crypto/rand.
func NewToken(r io.Reader) (tok, sha string, err error) {
	b := make([]byte, TokenBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", "", fmt.Errorf("случайные байты: %w", err)
	}
	tok = base64.RawURLEncoding.EncodeToString(b)
	return tok, HashToken(tok), nil
}

// GenToken — NewToken на crypto/rand.
func GenToken() (tok, sha string, err error) { return NewToken(rand.Reader) }

// ErrNoToken — токена нет, чужой или негодного вида: для клиента всё это один и тот же 401.
var ErrNoToken = errors.New("токен не принят")

// Bearer достаёт токен из заголовка Authorization. Не «Bearer», пусто, лишние слова — ошибка.
func Bearer(header string) (string, error) {
	const scheme = "Bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", ErrNoToken
	}
	return header[len(scheme):], nil
}

// Match — владелец и имя токена. Ничего секретного.
type Match struct{ Owner, Name string }

// Lookup сверяет токен со всеми записями без раннего выхода и без ветвления по результату сверки:
// время ответа не зависит ни от места совпавшей записи, ни от того, есть ли она. Токен негодного вида
// (CheckToken) всё равно хэшируется и сверяется — отказ неотличим по времени от «чужого».
func (t *Tokens) Lookup(tok string) (Match, error) {
	sum := sha256.Sum256([]byte(tok))
	found := 0
	idx := 0
	for i := range t.entries {
		eq := t.equal(sum[:], t.entries[i].sum[:])
		idx = subtle.ConstantTimeSelect(eq, i, idx)
		found |= eq
	}
	if albums.CheckToken(tok) != nil || found != 1 {
		return Match{}, ErrNoToken
	}
	e := t.entries[idx]
	return Match{Owner: e.owner, Name: e.name}, nil
}

// Authenticate — Bearer + Lookup: один вход для обработчиков.
func (t *Tokens) Authenticate(authorization string) (Match, error) {
	tok, err := Bearer(authorization)
	if err != nil {
		// Тот же объём работы, что и при настоящем токене.
		_, _ = t.Lookup("")
		return Match{}, ErrNoToken
	}
	return t.Lookup(tok)
}
