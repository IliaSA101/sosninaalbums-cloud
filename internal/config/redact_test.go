package config

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// Ключи не раскрываются ни форматированием, ни журналом, ни JSON-обработчиком slog.
func TestKeysNotRevealed(t *testing.T) {
	k, err := DeriveKeys(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, v := range []any{k, *k} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q", "%d"} {
			texts = append(texts, fmt.Sprintf(verb, v))
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("ключи", "keys", k, "k", *k)
	jsonLine := strings.SplitN(buf.String(), "\n", 2)[0]
	slog.New(slog.NewTextHandler(&buf, nil)).Info("ключи", "keys", k)
	texts = append(texts, buf.String())
	for _, key := range [][]byte{k.Password, k.Cookie, k.Log} {
		leaks := []string{hex.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key), base64.StdEncoding.EncodeToString(key), fmt.Sprintf("%v", key)}
		for _, text := range texts {
			for _, l := range leaks {
				if strings.Contains(text, l) {
					t.Fatalf("ключ раскрыт: %q", text)
				}
			}
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonLine), &m); err != nil || m["keys"] != "[redacted]" || m["k"] != "[redacted]" {
		t.Fatalf("slog JSON: %v %v", m, err)
	}
}
