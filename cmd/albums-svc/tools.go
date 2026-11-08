package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// subFlags — разбор флагов подкоманды: у каждой свой FlagSet. Позиционных аргументов нет.
func subFlags(name string, args []string, stderr io.Writer, define func(*flag.FlagSet)) (ok bool, code int) {
	fs := flag.NewFlagSet("albums-svc "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, exitOK
		}
		return false, exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "albums-svc %s: лишние аргументы\n", name)
		return false, exitUsage
	}
	return true, exitOK
}

// cmdHealth — `albums-svc health -config <файл>`: GET /v1/health по тому же адресу, что слушает сервис
// (сокет внутри контейнера с Network=none). Код 0 — сервис ответил 200 и «api: v1».
func cmdHealth(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfgPath string
	if ok, code := subFlags("health", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&cfgPath, "config", "", "конфиг сервиса (нужен адрес listen)")
	}); !ok {
		return code
	}
	if cfgPath == "" {
		_, _ = fmt.Fprintln(stderr, "albums-svc health: нужен -config")
		return exitUsage
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc health: конфиг:", err)
		return exitFail
	}
	if err := checkHealth(ctx, cfg.Listen); err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc health:", err)
		return exitFail
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return exitOK
}

func checkHealth(ctx context.Context, listen string) error {
	network, addr, ok := "", "", false
	if p, found := strings.CutPrefix(listen, "unix:"); found {
		network, addr, ok = "unix", p, true
	} else if hp, found := strings.CutPrefix(listen, "tcp:"); found {
		network, addr, ok = "tcp", hp, true
	}
	if !ok {
		return errors.New("listen: нужен адрес unix:/путь или tcp:127.0.0.1:порт")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://albums-svc/v1/health", nil)
	if err != nil {
		return fmt.Errorf("запрос: %w", err)
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("сервис не отвечает: %w", unwrapURL(err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("статус %d", res.StatusCode)
	}
	var h albums.Health
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&h); err != nil || h.API != "v1" {
		return errors.New("ответ не по контракту")
	}
	return nil
}

func unwrapURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		return ue.Unwrap()
	}
	return err
}

// cmdVerify — `albums-svc verify -data <копия>`: только чтение (store.OpenReadOnly: без замка, без записи,
// без создания каталогов). Код 0 — проблем нет. id альбомов не печатаются: id — ключ доступа.
func cmdVerify(args []string, stdout, stderr io.Writer) int {
	var dir string
	if ok, code := subFlags("verify", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&dir, "data", "", "каталог data/ (или его копия)")
	}); !ok {
		return code
	}
	if dir == "" {
		_, _ = fmt.Fprintln(stderr, "albums-svc verify: нужен -data")
		return exitUsage
	}
	st, err := store.OpenReadOnly(dir, store.Options{})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc verify:", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	rep, err := album.Verify(st)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc verify:", err)
		return exitFail
	}
	for _, p := range rep.Problems {
		_, _ = fmt.Fprintf(stdout, "проблема: владелец %s, альбом %s: %s\n", p.Owner, p.Ref, p.What)
	}
	_, _ = fmt.Fprintf(stdout, "проверено альбомов: %d, проблем: %d\n", rep.Albums, len(rep.Problems))
	if !rep.OK() {
		return exitFail
	}
	return exitOK
}

// openStopped открывает data/ на запись под flock для разовых команд: сервис должен быть остановлен (занят —
// отказ). Нет LAYOUT — это не data/: store.Open создал бы там пустую раскладку, а команде это не нужно.
func openStopped(dir string) (*store.Store, error) {
	if dir == "" {
		return nil, errors.New("нужен -data")
	}
	if _, err := os.Stat(filepath.Join(dir, "LAYOUT")); err != nil {
		return nil, errors.New("в каталоге нет LAYOUT: это не data/")
	}
	st, err := store.Open(dir, store.Options{})
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return nil, errors.New("каталог занят: остановите сервис")
		}
		return nil, err
	}
	return st, nil
}

// cmdAfterRestore — `albums-svc after-restore -data <dir>`, только при остановленном сервисе: flock на LOCK
// (занят — отказ, иначе запись meta из памяти сервиса затёрла бы результат), epoch++ у всех альбомов (снимок
// мог быть до revoke_sessions или смены пароля), last_seen ← now (иначе снимок месячной давности включит
// сторож часов). Мусор не убирается: это дело старта сервиса.
func cmdAfterRestore(args []string, stdout, stderr io.Writer) int {
	var dir string
	if ok, code := subFlags("after-restore", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&dir, "data", "", "каталог data/, восстановленный из копии")
	}); !ok {
		return code
	}
	if dir == "" {
		_, _ = fmt.Fprintln(stderr, "albums-svc after-restore: нужен -data")
		return exitUsage
	}
	st, err := openStopped(dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc after-restore:", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	metas, err := st.LoadAll()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc after-restore:", err)
		return exitFail
	}
	done := 0
	for _, m := range metas {
		c := *m
		c.Epoch++
		if err := st.WriteMeta(&c); err != nil && !errors.Is(err, store.ErrNotDurable) {
			_, _ = fmt.Fprintf(stderr, "albums-svc after-restore: после %d альбомов: %v\n", done, err)
			return exitFail
		}
		done++
	}
	l := st.Layout()
	l.LastSeen = time.Now().UTC()
	if err := st.SetLayout(l); err != nil && !errors.Is(err, store.ErrNotDurable) {
		_, _ = fmt.Fprintln(stderr, "albums-svc after-restore: last_seen:", err)
		return exitFail
	}
	_, _ = fmt.Fprintf(stdout, "готово: epoch увеличен у %d альбомов, last_seen обновлён\n", done)
	if l.ClockHold != nil {
		_, _ = fmt.Fprintln(stdout, "пауза сторожа часов стоит: снимите её, когда убедитесь в часах (albums-svc -clock-ok -data …)")
	}
	return exitOK
}

// cmdClockOK — `albums-svc -clock-ok -data <dir>`: разовый запуск при остановленном сервисе по runbook, после
// того как владелец убедился, что часы исправны. Под flock чистит clock_hold, ставит last_seen ← now, выходит.
// В юнит Quadlet флаг не пишется: пауза, снятая юнитом, снялась бы первым же перезапуском.
func cmdClockOK(dir string, stdout, stderr io.Writer) int {
	st, err := openStopped(dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc -clock-ok:", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	prev := st.Layout().ClockHold
	if err := st.SetLayout(store.Layout{LastSeen: time.Now().UTC()}); err != nil && !errors.Is(err, store.ErrNotDurable) {
		_, _ = fmt.Fprintln(stderr, "albums-svc -clock-ok:", err)
		return exitFail
	}
	if prev == nil {
		_, _ = fmt.Fprintln(stdout, "паузы не было; last_seen обновлён")
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "пауза снята (причина: %s); last_seen обновлён\n", prev.Reason)
	return exitOK
}
