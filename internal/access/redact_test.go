package access

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestPasswordsNotRevealed(t *testing.T) {
	key := bytes.Repeat([]byte{0x6b}, 32)
	p := NewPasswords(key)
	var texts []string
	for _, v := range []any{p, *p} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
			texts = append(texts, fmt.Sprintf(verb, v))
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("пароли", "p", p)
	texts = append(texts, buf.String())
	for _, text := range texts {
		for _, l := range []string{hex.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key), fmt.Sprintf("%v", key)} {
			if strings.Contains(text, l) {
				t.Fatalf("ключ раскрыт: %q", text)
			}
		}
	}
	if !strings.Contains(buf.String(), "[redacted]") {
		t.Fatalf("журнал: %q", buf.String())
	}
}

// salt ‖ id ‖ пароль однозначен только при id фиксированной формы: иначе перенос границы даёт тот же хэш.
func TestPasswordHashRejectsBadID(t *testing.T) {
	h := pw(t)
	salt := bytes.Repeat([]byte{3}, SaltBytes)
	for _, id := range []string{"", "abc", albumID + "a", strings.ToUpper(albumID), albumID[:25] + "1", "../" + albumID[:23], albumID[:25] + "\n"} {
		if _, err := h.Hash(id, salt, "pass"); err == nil {
			t.Errorf("id %q принят", id)
		}
		if h.Verify(id, salt, make([]byte, 32), "password") {
			t.Errorf("Verify принял id %q", id)
		}
	}
	if _, err := h.Hash(albumID[:25], salt, "x"+"pass"); err == nil {
		t.Fatal("сдвиг границы id/пароля даёт хэш")
	}
}
