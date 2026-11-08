package config

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

func TestSecretFormat(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 32)
	enc := base64.RawURLEncoding.EncodeToString(raw)
	cases := []struct {
		name string
		text string
		ok   bool
	}{
		{"без дополнения", enc, true},
		{"с переводом строки", enc + "\n", true},
		{"с пробелами и CRLF в хвосте", enc + " \t\r\n", true},
		{"с дополнением =", base64.URLEncoding.EncodeToString(append(raw, 1, 2)) + "\n", true},
		{"31 байт", base64.RawURLEncoding.EncodeToString(raw[:31]), false},
		{"пусто", "", false},
		{"стандартный алфавит (+/)", strings.NewReplacer("-", "+", "_", "/").Replace(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xff}, 20))), false},
		{"перевод строки внутри", enc[:20] + "\n" + enc[20:], false},
		{"пробел внутри", enc[:20] + " " + enc[20:], false},
		{"не base64", strings.Repeat("!", 50), false},
		{"ненулевые хвостовые биты", func() string { // «нестрогий» разбор принял бы
			s := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0}, 34))
			return s[:len(s)-1] + "B"
		}(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := ParseSecret(tc.text)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
			if err != nil && strings.Contains(err.Error(), tc.text) && tc.text != "" {
				t.Fatalf("содержимое секрета в ошибке: %v", err)
			}
			if err == nil && len(b) < MinSecretBytes {
				t.Fatalf("короткий секрет принят: %d", len(b))
			}
		})
	}
}

func TestSecretFilePerms(t *testing.T) {
	dir := t.TempDir()
	good := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) + "\n"
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(good), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // umask мог урезать права
			t.Fatal(err)
		}
		return p
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		if _, err := LoadSecret(write("ok", mode)); err != nil {
			t.Fatalf("права %o: %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666} {
		_, err := LoadSecret(write("wide", mode))
		if err == nil {
			t.Fatalf("права %o приняты", mode)
		}
		if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), strings.TrimSpace(good)) {
			t.Fatalf("в ошибке путь или секрет: %v", err)
		}
	}
	if _, err := LoadSecret(filepath.Join(dir, "нет")); err == nil {
		t.Fatal("нет файла — должна быть ошибка")
	}
	if _, err := LoadSecret(dir); err == nil {
		t.Fatal("каталог принят как секрет")
	}
	// Символическая ссылка наружу каталога файла — os.Root её не пропустит.
	outside := t.TempDir()
	target := filepath.Join(outside, "s")
	if err := os.WriteFile(target, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecret(link); err == nil {
		t.Fatal("ссылка за пределы каталога прочитана")
	}
}

// hkdfRef — независимая реализация RFC 5869 (соль нулевая, один блок на 32 байта).
func hkdfRef(ikm []byte, info string) []byte {
	ext := hmac.New(sha256.New, make([]byte, sha256.Size))
	ext.Write(ikm)
	prk := ext.Sum(nil)
	exp := hmac.New(sha256.New, prk)
	exp.Write([]byte(info))
	exp.Write([]byte{1})
	return exp.Sum(nil)
}

func TestDeriveKeys(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	k, err := DeriveKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]struct {
		key   []byte
		label string
	}{"pw": {k.Password, "pw/1"}, "cookie": {k.Cookie, "cookie/1"}, "log": {k.Log, "log/1"}} {
		if !bytes.Equal(got.key, hkdfRef(master, got.label)) {
			t.Fatalf("ключ %s не совпал с RFC 5869 для метки %s", name, got.label)
		}
	}
	if bytes.Equal(k.Password, k.Cookie) || bytes.Equal(k.Cookie, k.Log) || bytes.Equal(k.Password, k.Log) {
		t.Fatal("ключи разных меток совпали")
	}
	k2, err := DeriveKeys(append(bytes.Clone(master[:31]), 0x43))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k.Password, k2.Password) {
		t.Fatal("ключ не зависит от секрета")
	}
	if _, err := DeriveKeys(master[:31]); err == nil {
		t.Fatal("короткий секрет принят")
	}
}

func TestGenSecret(t *testing.T) {
	a, err := GenSecret(bytes.NewReader(bytes.Repeat([]byte{1}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := ParseSecret(a); err != nil || len(b) != SecretBytes {
		t.Fatalf("выданный секрет не разбирается обратно: %v %d", err, len(b))
	}
	if _, err := GenSecret(iotest.ErrReader(errors.New("нет энтропии"))); err == nil {
		t.Fatal("отказ источника случайных байт проглочен")
	}
	if _, err := GenSecret(bytes.NewReader(make([]byte, 5))); err == nil {
		t.Fatal("короткое чтение проглочено")
	}
}

// FIFO вместо файла секрета или конфига не вешает старт: до open() видно, что это не обычный файл.
func TestFIFONotOpened(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "secret")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo недоступен: %v", err)
	}
	done := make(chan error, 2)
	go func() { _, err := LoadSecret(fifo); done <- err }()
	go func() { _, err := Load(fifo); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "не обычный файл") {
				t.Fatalf("FIFO принят или ошибка не та: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("чтение FIFO повисло")
		}
	}
}
