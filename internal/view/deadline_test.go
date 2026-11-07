package view

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Срок записи картинки продлевается по размеру: у сервера WriteTimeout 1 с, а медленный клиент читает тело
// дольше — оно всё равно приходит целиком. Без продления соединение рвалось бы на заполненном буфере.
func TestImageWriteDeadlineExtended(t *testing.T) {
	e := newEnv(t, func(o *envOpts) { o.pageBytes = 16 << 20 })
	big := bytes.Repeat([]byte{0xAB}, 14<<20) // больше буферов сокета loopback: отдача упрётся в медленное чтение
	copy(big, []byte{0xFF, 0xD8, 0xFF, 0xE0})
	f := e.publishedWith("", [][]byte{big, nil})

	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/a/"+f.id+"/"+f.file, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	head := make([]byte, 64<<10)
	if _, err := io.ReadFull(res.Body, head); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // втрое дольше WriteTimeout сервера: читаем медленно
	rest, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("тело оборвано на %d байтах: %v", len(head)+len(rest), err)
	}
	if got := append(head, rest...); !bytes.Equal(got, big) {
		t.Fatalf("тело пришло не целиком: %d из %d байт", len(got), len(big))
	}
}

// После 413 на форме входа сервер закрывает соединение: непрочитанный остаток тела не остаётся в нём.
func TestLoginTooBigClosesConnection(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	// Тело неизвестной длины (chunked): предел срабатывает при чтении, а не по Content-Length.
	req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/a/"+f.id+"/login", io.NopCloser(strings.NewReader("password="+strings.Repeat("a", 100000))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ClientIPHeader, clientIP)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusRequestEntityTooLarge || !res.Close {
		t.Fatalf("413: код %d, закрытие соединения %v", res.StatusCode, res.Close)
	}
}
