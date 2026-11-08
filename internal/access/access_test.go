package access

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// Токены для тестов — выдуманные.
const (
	tokA = "tok-a-0123456789abcdef-XYZ"
	tokB = "tok-b-0123456789abcdef-XYZ"
	tokC = "tok-c-0123456789abcdef-XYZ"
)

func newTokens(t *testing.T) *Tokens {
	t.Helper()
	tk, err := NewTokens([]TokenEntry{
		{Owner: "p1", Name: "ноутбук", SHA256: HashToken(tokA)},
		{Owner: "p1", Name: "студия", SHA256: HashToken(tokB)},
		{Owner: "p2", Name: "ноутбук", SHA256: HashToken(tokC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestTokenLookup(t *testing.T) {
	tk := newTokens(t)
	for tok, want := range map[string]Match{
		tokA: {"p1", "ноутбук"}, tokB: {"p1", "студия"}, tokC: {"p2", "ноутбук"},
	} {
		got, err := tk.Lookup(tok)
		if err != nil || got != want {
			t.Fatalf("Lookup(%s…) = %v, %v; ждали %v", tok[:5], got, err, want)
		}
	}
}

// TestTokenAllCompared: сверка идёт со всеми записями при любом исходе — ранний выход выдал бы по времени,
// какая запись совпала.
func TestTokenAllCompared(t *testing.T) {
	for name, tok := range map[string]string{
		"первый": tokA, "последний": tokC, "чужой": "tok-z-0123456789abcdef-XYZ",
		"короткий": "x", "с пробелом": "tok a 0123456789abcdef", "пустой": "",
	} {
		t.Run(name, func(t *testing.T) {
			tk := newTokens(t)
			calls := 0
			tk.equal = func(a, b []byte) int { calls++; return subtle.ConstantTimeCompare(a, b) }
			_, _ = tk.Lookup(tok)
			if calls != len(tk.entries) {
				t.Fatalf("сверок %d, записей %d", calls, len(tk.entries))
			}
		})
	}
	tk := newTokens(t)
	calls := 0
	tk.equal = func(a, b []byte) int { calls++; return subtle.ConstantTimeCompare(a, b) }
	if _, err := tk.Authenticate("Basic xyz"); err == nil {
		t.Fatal("не Bearer принят")
	}
	if calls != len(tk.entries) {
		t.Fatalf("отказ по схеме должен стоить столько же сверок: %d", calls)
	}
}

// TestToken401Same: нет, чужой, плохой вид, не Bearer — одна и та же ошибка, без подробностей.
func TestToken401Same(t *testing.T) {
	tk := newTokens(t)
	for _, h := range []string{
		"", "Bearer", "Bearer ", "Bearer tok-z-0123456789abcdef-XYZ", "Bearer x", "Bearer " + tokA + " лишнее",
		"Bearer tok a 0123456789abcdef", "Basic " + tokA, tokA, "Bearer " + strings.Repeat("a", 600),
		"Bearer " + tokA[:len(tokA)-1],
	} {
		_, err := tk.Authenticate(h)
		if !errors.Is(err, ErrNoToken) || err.Error() != ErrNoToken.Error() {
			t.Fatalf("заголовок %q: %v", h, err)
		}
	}
	if m, err := tk.Authenticate("bearer " + tokB); err != nil || m.Owner != "p1" {
		t.Fatalf("схема без учёта регистра: %v %v", m, err)
	}
	if err := albums.CheckToken(tokA); err != nil {
		t.Fatalf("тестовый токен негоден контракту: %v", err)
	}
}

func TestNewTokensRejects(t *testing.T) {
	good := HashToken(tokA)
	for name, e := range map[string][]TokenEntry{
		"не hex":    {{Owner: "p1", SHA256: strings.Repeat("g", 64)}},
		"коротко":   {{Owner: "p1", SHA256: good[:63]}},
		"повтор":    {{Owner: "p1", SHA256: good}, {Owner: "p2", SHA256: good}},
		"пустой":    {{Owner: "p1", SHA256: ""}},
		"заглавные": {{Owner: "p1", SHA256: strings.ToUpper(good)}},
	} {
		_, err := NewTokens(e)
		if name == "заглавные" { // hex.Decode принимает оба регистра: это допустимо
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: принят", name)
		}
	}
}

func TestNewToken(t *testing.T) {
	tok, sha, err := NewToken(bytes.NewReader(bytes.Repeat([]byte{5}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := albums.CheckToken(tok); err != nil {
		t.Fatalf("токен не проходит контракт: %v", err)
	}
	if len(tok) != 43 || sha != HashToken(tok) || len(sha) != 64 {
		t.Fatalf("длина токена %d, sha %q", len(tok), sha)
	}
	if _, _, err := NewToken(iotest.ErrReader(errors.New("нет энтропии"))); err == nil {
		t.Fatal("отказ источника проглочен")
	}
	a, _, _ := GenToken()
	b, _, _ := GenToken()
	if a == b || a == "" {
		t.Fatal("GenToken повторяется")
	}
}

func pw(t *testing.T) *Passwords {
	t.Helper()
	return NewPasswords(bytes.Repeat([]byte{0x11}, 32))
}

const albumID = "abcdefghijklmnopqrstuvwxyz"

func setPassword(t *testing.T, h *Passwords, salt []byte, p string) []byte {
	t.Helper()
	n, err := Prepare(p)
	if err != nil {
		t.Fatalf("Prepare(%q): %v", p, err)
	}
	hash, err := h.Hash(albumID, salt, n)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestPasswordNFC(t *testing.T) {
	h := pw(t)
	salt := bytes.Repeat([]byte{3}, SaltBytes)
	composed := "café-секрет"    // é одним знаком
	decomposed := "café-секрет" // e + combining acute
	stored := setPassword(t, h, salt, composed)
	if !h.Verify(albumID, salt, stored, decomposed) {
		t.Fatal("NFD-вариант не принят: пароль зависит от формы Unicode")
	}
	if !h.Verify(albumID, salt, setPassword(t, h, salt, decomposed), composed) {
		t.Fatal("NFC-вариант не принят при установке в NFD")
	}
	if h.Verify(albumID, salt, stored, "cafe-секрет") {
		t.Fatal("другой пароль принят")
	}
}

func TestPasswordTrim(t *testing.T) {
	h := pw(t)
	salt := bytes.Repeat([]byte{3}, SaltBytes)
	stored := setPassword(t, h, salt, "  correct horse \u00a0")
	for _, in := range []string{"correct horse", " correct horse", "correct horse ", "\u00a0correct horse  "} {
		if !h.Verify(albumID, salt, stored, in) {
			t.Fatalf("%q не принят: пробелы по краям должны срезаться", in)
		}
	}
	if h.Verify(albumID, salt, stored, "correcthorse") || h.Verify(albumID, salt, stored, "correct  horse") {
		t.Fatal("пробел внутри обязан учитываться")
	}
	// После обрезки меньше 4 знаков — отказ при установке.
	for _, p := range []string{"   ab   ", "abc", "    "} {
		if _, err := Prepare(p); !errors.Is(err, ErrPassword) {
			t.Fatalf("Prepare(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "a\x00bcd", "ab\u200bcd", "\xff\xfe\xfd\xfc", strings.Repeat("я", 65)} {
		if _, err := Prepare(p); !errors.Is(err, ErrPassword) {
			t.Fatalf("Prepare(%q) принят", p)
		}
	}
}

func TestPasswordNewSalt(t *testing.T) {
	h := pw(t)
	s1, err := NewSalt(bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := NewSalt(bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
	h1 := setPassword(t, h, s1, "same password")
	h2 := setPassword(t, h, s2, "same password")
	if bytes.Equal(h1, h2) {
		t.Fatal("тот же пароль с новой солью дал тот же хэш: прежние входы не умрут")
	}
	if h.Verify(albumID, s2, h1, "same password") {
		t.Fatal("хэш со старой солью прошёл проверку с новой")
	}
	if len(s1) != SaltBytes || len(h1) != 32 {
		t.Fatalf("размеры соли %d, хэша %d", len(s1), len(h1))
	}
	if _, err := NewSalt(iotest.ErrReader(errors.New("нет энтропии"))); err == nil {
		t.Fatal("отказ источника проглочен")
	}
	if _, err := h.Hash(albumID, s1[:15], "pass"); err == nil {
		t.Fatal("короткая соль принята")
	}
	// Хэш привязан к id альбома и ключу.
	if h.Verify("zzzzzzzzzzzzzzzzzzzzzzzzzz", s1, h1, "same password") {
		t.Fatal("хэш перенёсся на другой id")
	}
	if NewPasswords(bytes.Repeat([]byte{0x22}, 32)).Verify(albumID, s1, h1, "same password") {
		t.Fatal("хэш проверился другим ключом")
	}
}

// TestPasswordLimitsBeforeHash: предельные и негодные вводы отсекаются до NFC и HMAC.
func TestPasswordLimitsBeforeHash(t *testing.T) {
	h := pw(t)
	salt := bytes.Repeat([]byte{3}, SaltBytes)
	stored := setPassword(t, h, salt, "valid password")
	calls := 0
	h.onHash = func() { calls++ }
	for name, in := range map[string]string{
		"пусто": "", "огромный": strings.Repeat("a", 1<<20), "не UTF-8": "\xff\xfe\xfd\xfc\xfb",
		"управляющий": "valid\x00password", "невидимый": "valid\u200bpassword",
		"65 знаков": strings.Repeat("я", 65), "короткий": "  ab ",
	} {
		if h.Verify(albumID, salt, stored, in) {
			t.Fatalf("%s: принят", name)
		}
	}
	if calls != 0 {
		t.Fatalf("хэшировалось %d раз до проверки пределов", calls)
	}
	if !h.Verify(albumID, salt, stored, "valid password") || calls != 1 {
		t.Fatalf("верный пароль: calls=%d", calls)
	}
}
