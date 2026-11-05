package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
	"github.com/IliaSA101/sosninaalbums/api/albums"
)

func TestGenSecret(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-gen-secret"}, &out, &errb, rand.Reader); code != exitOK {
		t.Fatalf("код %d, stderr %q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("лишнее в stderr: %q", errb.String())
	}
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("ждали одну строку: %q", line)
	}
	b, err := config.ParseSecret(line)
	if err != nil || len(b) != config.SecretBytes {
		t.Fatalf("выданный секрет не проходит разбор: %v (%d байт)", err, len(b))
	}
	var out2 bytes.Buffer
	run([]string{"-gen-secret"}, &out2, &errb, rand.Reader)
	if out.String() == out2.String() {
		t.Fatal("два секрета совпали")
	}
}

func TestGenToken(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-gen-token"}, &out, &errb, rand.Reader); code != exitOK {
		t.Fatalf("код %d, stderr %q", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "token:  ") || !strings.HasPrefix(lines[1], "sha256: ") {
		t.Fatalf("формат вывода: %q", out.String())
	}
	tok, sum := strings.TrimPrefix(lines[0], "token:  "), strings.TrimPrefix(lines[1], "sha256: ")
	if err := albums.CheckToken(tok); err != nil {
		t.Fatalf("токен не проходит контракт: %v", err)
	}
	if access.HashToken(tok) != sum {
		t.Fatal("sha256 не от выданного токена")
	}
	// Строка sha256 годится прямо в конфиг, а токен — в Lookup.
	tk, err := access.NewTokens([]access.TokenEntry{{Owner: "p1", Name: "t", SHA256: sum}})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := tk.Authenticate("Bearer " + tok); err != nil || m.Owner != "p1" {
		t.Fatalf("выданный токен не принят: %v", err)
	}
}

func TestGenFailures(t *testing.T) {
	for _, flagName := range []string{"-gen-secret", "-gen-token"} {
		var out, errb bytes.Buffer
		code := run([]string{flagName}, &out, &errb, iotest.ErrReader(errors.New("нет энтропии")))
		if code != exitFail || out.Len() != 0 || errb.Len() == 0 {
			t.Fatalf("%s: код %d, stdout %q, stderr %q", flagName, code, out.String(), errb.String())
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"-gen-secret", "-gen-token"}, {"-gen-secret", "лишнее"}, {"-нет-такого"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb, rand.Reader); code != exitUsage || out.Len() != 0 {
			t.Fatalf("%v: код %d, stdout %q", args, code, out.String())
		}
	}
}
