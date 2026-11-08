package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
)

// syncBuf — буфер, в который пишут из нескольких горутин (журнал сервера и тест).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServerTimeouts(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	srv := newServer(http.NotFoundHandler(), log, defaultTimeouts)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Fatalf("у сервера нет тайм-аута или предела заголовков: %+v", srv)
	}
	if srv.ReadHeaderTimeout != 10*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 16<<10 || srv.ReadTimeout != 30*time.Second {
		t.Fatalf("значения не по design §4: %v %v %v %d", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout < srv.ReadTimeout {
		t.Fatalf("WriteTimeout %v меньше ReadTimeout %v", srv.WriteTimeout, srv.ReadTimeout)
	}

	// Поведение с короткими сроками: медленный клиент (Slowloris) отключается, а не держит соединение.
	fast := newServer(http.NotFoundHandler(), log, timeouts{readHeader: 150 * time.Millisecond, read: time.Second, write: time.Second, idle: time.Second, maxHeader: 1 << 10})
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fast.Serve(ln) }()
	t.Cleanup(func() { _ = fast.Close() })
	dial := func() net.Conn {
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	slow := dial()
	if _, err := io.WriteString(slow, "GET /v1/health HTTP/1.1\r\nHost: x\r\nX-Slow: "); err != nil { // заголовки не закончены
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := io.ReadAll(slow); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("соединение не закрыто сервером: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("медленный клиент держал соединение %v", time.Since(start))
	}
	big := dial()
	if _, err := io.WriteString(big, "GET / HTTP/1.1\r\nHost: x\r\nX-Big: "+strings.Repeat("a", 8<<10)+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(big), nil)
	if err != nil || res.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("заголовки больше предела: %v %v", res, err)
	}
	_ = res.Body.Close()
}

// ErrorLog сервера не несёт текст ошибки net/http: там бывают адрес клиента и значение паники.
func TestServerErrorLogQuiet(t *testing.T) {
	var buf bytes.Buffer
	srv := newServer(http.NotFoundHandler(), logx.New(&buf), defaultTimeouts)
	srv.ErrorLog.Printf("http: panic serving 203.0.113.9:5555: /a/%s/ token=%s", strings.Repeat("q", 26), "секрет")
	out := buf.String()
	if !strings.Contains(out, "ошибка http-сервера") || strings.Contains(out, "203.0.113") || strings.Contains(out, "секрет") || strings.Contains(out, "qqqq") {
		t.Fatalf("журнал ошибок сервера: %s", out)
	}
}

func TestRouter(t *testing.T) {
	reached := ""
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = name + " " + r.URL.Path
			w.WriteHeader(http.StatusTeapot)
		})
	}
	h := newRouter(mark("api"), mark("view"))
	id := "/a/" + strings.Repeat("q", 26) + "/"
	for target, want := range map[string]string{
		"/v1/albums":           "api /v1/albums",
		"/v1/health":           "api /v1/health",
		"/v1/%2e%2e/v1/albums": "view /v1/../v1/albums", // неканонический путь не доходит до api: 404 из view
		"/v1/../v1/albums":     "view /v1/../v1/albums",
		"/v1//albums":          "view /v1//albums",
		"/V1/albums":           "view /V1/albums",
		"/v1":                  "view /v1",
		"/":                    "view /",
		id:                     "view " + id,
		"/viewer/x/app.js":     "view /viewer/x/app.js",
		"/a/%2e%2e/v1/albums":  "view /a/../v1/albums",
	} {
		reached = ""
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", target, nil))
		if rec.Code != http.StatusTeapot || reached != want {
			t.Fatalf("%s: дошло до %q (ждали %q), код %d", target, reached, want, rec.Code)
		}
	}
}

// fixture — конфиг, секрет и каталог данных для запуска сервера; сокет — в коротком каталоге (предел длины пути).
type fixture struct {
	cfgPath, data, sock, tok string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	short, err := os.MkdirTemp("", "sa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	dir := t.TempDir()
	f := &fixture{data: filepath.Join(dir, "data"), sock: filepath.Join(short, "http.sock")}
	secret, err := config.GenSecret(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, sum, err := access.GenToken()
	if err != nil {
		t.Fatal(err)
	}
	f.tok = tok
	cfg := map[string]any{
		"listen": "unix:" + f.sock, "public_base": "http://localhost:8080", "data_dir": f.data, "secret_file": secretFile,
		"disk_reserve_bytes": 1,
		"owners":             []any{map[string]any{"slug": "p1", "studio": "Студия", "tokens": []any{map[string]any{"name": "t", "sha256": sum}}}},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.cfgPath = filepath.Join(dir, "config.json")
	if err := os.WriteFile(f.cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// start запускает сервер и ждёт, пока он слушает; возвращает остановку (код выхода) и журнал.
func (f *fixture) start(t *testing.T) (stop func() int, log *syncBuf) {
	t.Helper()
	ready := make(chan struct{})
	onListening = func(net.Listener) { close(ready) }
	t.Cleanup(func() { onListening = func(net.Listener) {} })
	ctx, cancel := context.WithCancel(context.Background())
	log = &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- execute(ctx, []string{"-config", f.cfgPath}, log, log, rand.Reader) }()
	select {
	case <-ready:
	case code := <-done:
		cancel()
		t.Fatalf("сервер не запустился (код %d): %s", code, log.String())
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("сервер не начал слушать")
	}
	stopped := false
	stop = func() int {
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	return stop, log
}

func (f *fixture) get(t *testing.T, path, tok string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", f.sock)
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://x"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestServe(t *testing.T) {
	f := newFixture(t)
	stop, log := f.start(t)

	fi, err := os.Stat(f.sock)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Fatalf("сокет: %v, %v", fi, err)
	}
	if code, body := f.get(t, "/v1/health", ""); code != 200 || !strings.Contains(body, `"api":"v1"`) {
		t.Fatalf("health: %d %s", code, body)
	}
	if code, _ := f.get(t, "/v1/albums", ""); code != 401 {
		t.Fatalf("без токена: %d", code)
	}
	if code, body := f.get(t, "/v1/albums", f.tok); code != 200 || !strings.Contains(body, `"albums":[]`) {
		t.Fatalf("список с токеном: %d %s", code, body)
	}
	if code, _ := f.get(t, "/v1/%2e%2e/v1/albums", f.tok); code != 404 {
		t.Fatalf("неканонический путь: %d", code)
	}

	// health подкомандой по тому же конфигу — как в HEALTHCHECK юнита.
	var out, errb bytes.Buffer
	if code := run([]string{"health", "-config", f.cfgPath}, &out, &errb, rand.Reader); code != exitOK || !strings.Contains(out.String(), "ok") {
		t.Fatalf("health: код %d, %q %q", code, out.String(), errb.String())
	}

	// Второй экземпляр на тех же данных не стартует (flock) и не трогает первого.
	second := newFixture(t)
	cfg2, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(cfg2, &m); err != nil {
		t.Fatal(err)
	}
	m["listen"] = "unix:" + second.sock
	b, _ := json.Marshal(m)
	if err := os.WriteFile(second.cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"-config", second.cfgPath}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(errb.String(), "занят") {
		t.Fatalf("второй экземпляр: код %d, %q", code, errb.String())
	}
	if code, _ := f.get(t, "/v1/health", ""); code != 200 {
		t.Fatalf("первый экземпляр после попытки второго: %d", code)
	}

	if code := stop(); code != exitOK {
		t.Fatalf("остановка: код %d, журнал %s", code, log.String())
	}
	if _, err := os.Stat(f.sock); err == nil {
		t.Fatal("сокет остался после остановки")
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"health", "-config", f.cfgPath}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("health остановленного: код %d", code)
	}
	for _, secret := range []string{f.tok, f.data} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("в журнале есть %q", secret[:8])
		}
	}
	if !strings.Contains(log.String(), "сервис запущен") || !strings.Contains(log.String(), "сервис остановлен") {
		t.Fatalf("журнал: %s", log.String())
	}
}

// Остаток сокета от упавшего процесса не мешает запуску; чужой файл по пути сокета — отказ, а не удаление.
func TestServeStaleSocket(t *testing.T) {
	f := newFixture(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // «упавший» процесс: сокет-файл остаётся
	_ = ln.Close()
	if _, err := os.Stat(f.sock); err != nil {
		t.Fatal(err)
	}
	stop, _ := f.start(t)
	if code, _ := f.get(t, "/v1/health", ""); code != 200 {
		t.Fatalf("health: %d", code)
	}
	stop()

	if err := os.WriteFile(f.sock, []byte("не сокет"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-config", f.cfgPath}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(errb.String(), "не сокет") {
		t.Fatalf("чужой файл по пути сокета: код %d, %q", code, errb.String())
	}
	if b, err := os.ReadFile(f.sock); err != nil || string(b) != "не сокет" {
		t.Fatal("чужой файл удалён или изменён")
	}
}

func TestServeBadConfig(t *testing.T) {
	var out, errb bytes.Buffer
	for name, content := range map[string]string{"пустой файл": "", "не JSON": "{", "чужие поля": `{"x":1}`} {
		p := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errb.Reset()
		if code := run([]string{"-config", p}, &out, &errb, rand.Reader); code != exitFail {
			t.Fatalf("%s: код %d", name, code)
		}
	}
	if code := run([]string{"-config", filepath.Join(t.TempDir(), "нет.json")}, &out, &errb, rand.Reader); code != exitFail {
		t.Fatalf("нет файла: код %d", code)
	}
	for _, args := range [][]string{{"health"}, {"health", "-config"}, {"verify"}, {"after-restore"}, {"-clock-ok"}, {"нет-такой"}, {"verify", "-data", "x", "лишнее"}, {"-config", "x", "-gen-token"}} {
		out.Reset()
		errb.Reset()
		if code := run(args, &out, &errb, rand.Reader); code == exitOK {
			t.Fatalf("%v: код 0 при неверном вызове", args)
		}
	}
}

// Живой экземпляр с другими данными и тем же listen: сокет не удаляется, второй не стартует, первый работает.
func TestServeLiveSocketKept(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	second := newFixture(t)
	raw, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["data_dir"] = second.data // другие данные: flock не помешает, помешать должен только живой сокет
	b, _ := json.Marshal(m)
	if err := os.WriteFile(second.cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-config", second.cfgPath}, &out, &errb, rand.Reader); code != exitFail || !strings.Contains(errb.String(), "другой экземпляр") {
		t.Fatalf("второй экземпляр на живом сокете: код %d, %q", code, errb.String())
	}
	if code, _ := f.get(t, "/v1/health", ""); code != 200 {
		t.Fatalf("сокет первого экземпляра уничтожен: %d", code)
	}
}

// ActiveAge хранилища берётся из page_bytes конфига: большая страница — дольше «идущий» приём.
func TestServeActiveAgeFromConfig(t *testing.T) {
	parse := func(pageBytes int64) *config.Config {
		cfg := map[string]any{
			"listen": "unix:/run/x.sock", "public_base": "https://albums.example.org", "data_dir": "/d", "secret_file": "/s",
			"limits": map[string]any{"page_bytes": pageBytes, "album_bytes": pageBytes * 4},
			"owners": []any{map[string]any{"slug": "p1", "studio": "С", "tokens": []any{map[string]any{"name": "t", "sha256": strings.Repeat("a", 64)}}}},
		}
		b, _ := json.Marshal(cfg)
		c, err := config.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	small, big := storeOptions(parse(15<<20), nil, nil), storeOptions(parse(1<<30), nil, nil)
	if small.ActiveAge != 630*time.Second || big.ActiveAge < time.Hour || big.ActiveAge <= small.ActiveAge {
		t.Fatalf("ActiveAge: %v и %v", small.ActiveAge, big.ActiveAge)
	}
}
