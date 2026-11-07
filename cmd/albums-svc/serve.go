package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/api"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/view"
)

// timeouts — тайм-ауты http.Server (design §4): медленный клиент не держит соединение вечно (Slowloris), а
// заголовки не растут без предела. Чтение тела страницы — отдельный срок на запрос (ResponseController в api),
// поэтому общий ReadTimeout короткий.
type timeouts struct {
	readHeader, read, write, idle time.Duration
	maxHeader                     int
}

var defaultTimeouts = timeouts{
	readHeader: 10 * time.Second,
	read:       30 * time.Second,
	write:      120 * time.Second, // ответ страницы клиенту; PUT страницы продлевает свой срок сам
	idle:       120 * time.Second,
	maxHeader:  16 << 10,
}

// newServer — единственное место, где создаётся http.Server: голый ListenAndServe запрещён линтером.
func newServer(h http.Handler, log *slog.Logger, t timeouts) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: t.readHeader,
		ReadTimeout:       t.read,
		WriteTimeout:      t.write,
		IdleTimeout:       t.idle,
		MaxHeaderBytes:    t.maxHeader,
		ErrorLog:          slog.NewLogLogger(quiet{log.Handler()}, slog.LevelError),
	}
}

// quiet отдаёт журналу только факт ошибки сервера: текст ошибок net/http содержит адрес клиента и значения
// паники (в них бывает путь), а в журнал им нельзя (design §7).
type quiet struct{ next slog.Handler }

func (q quiet) Enabled(ctx context.Context, l slog.Level) bool { return q.next.Enabled(ctx, l) }

func (q quiet) Handle(ctx context.Context, r slog.Record) error {
	return q.next.Handle(ctx, slog.NewRecord(r.Time, r.Level, "ошибка http-сервера", 0))
}

func (q quiet) WithAttrs([]slog.Attr) slog.Handler { return q }
func (q quiet) WithGroup(string) slog.Handler      { return q }

// newRouter — вход сервиса: каноничный /v1/* — api; всё прочее — view: /a/ и /viewer/ обслуживаются там, а
// любой другой путь (в том числе неканонический /v1/%2e%2e/…, регистр, «//») получает тот же 404, что и
// несуществующий альбом, до всяких маршрутов и без перенаправлений ServeMux.
func newRouter(v1, view http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.EscapedPath()
		if names.CanonicalPath(p) && strings.HasPrefix(p, "/v1/") {
			v1.ServeHTTP(w, r)
			return
		}
		view.ServeHTTP(w, r)
	})
}

// assemble собирает обработчик сервиса из готовых частей: api (/v1) и view (/a/, /viewer/) разделяют сервис
// альбомов, но не знают друг друга. Отдельная функция, чтобы тесты поднимали ровно ту же сборку, что и serve.
func assemble(cfg *config.Config, keys *config.Keys, svc *album.Service, tokens *access.Tokens, log *slog.Logger, tagger *logx.Tagger) (http.Handler, error) {
	lim, err := access.NewLimiter(loginLimits(cfg), access.LimiterOptions{Now: svc.Now})
	if err != nil {
		return nil, fmt.Errorf("лимит попыток входа: %w", err)
	}
	studios := make(map[string]string, len(cfg.Owners))
	for _, o := range cfg.Owners {
		studios[o.Slug] = o.Studio
	}
	v, err := view.New(svc, access.NewPasswords(keys.Password), access.NewCookies(keys.Cookie), lim, view.Options{
		PublicBase: cfg.PublicBase, Studios: studios, TrustedProxy: cfg.TrustedProxy, Log: log, Tagger: tagger,
	})
	if err != nil {
		return nil, fmt.Errorf("листалка: %w", err)
	}
	return newRouter(api.New(svc, tokens, cfg.Limits, api.Options{Log: log, Tagger: tagger}), v), nil
}

// loginLimits — пределы попыток входа из конфига.
func loginLimits(c *config.Config) access.LimitConfig {
	rate := func(r config.Rate) access.Rate { return access.Rate{Count: r.Count, Window: r.Window} }
	return access.LimitConfig{PerAlbumIP: rate(c.Login.PerAlbumIP), PerIP: rate(c.Login.PerIP), PerAlbum: rate(c.Login.PerAlbum)}
}

// albumConfig — настройки жизненного цикла из конфига процесса.
func albumConfig(c *config.Config) album.Config {
	owners := make(map[string]album.OwnerLimits, len(c.Owners))
	for _, o := range c.Owners {
		owners[o.Slug] = album.OwnerLimits{QuotaBytes: o.QuotaBytes, MaxAlbums: o.MaxAlbums}
	}
	return album.Config{
		PublicBase: c.PublicBase, Limits: c.Limits, AcceptWebP: c.AcceptWebP,
		UnpublishedTTL: time.Duration(c.UnpublishedTTL), DiskReserve: c.DiskReserve, Owners: owners,
	}
}

// storeOptions — настройки хранилища из конфига: возраст «идущего» .part считается из page_bytes (самый долгий
// приём страницы), а не берётся константой.
func storeOptions(c *config.Config, tagger *logx.Tagger, log *slog.Logger) store.Options {
	return store.Options{Tagger: tagger, Log: log, ActiveAge: album.ActiveAge(c.Limits.PageBytes)}
}

func tokenEntries(c *config.Config) []access.TokenEntry {
	var out []access.TokenEntry
	for _, o := range c.Owners {
		for _, t := range o.Tokens {
			out = append(out, access.TokenEntry{Owner: o.Slug, Name: t.Name, SHA256: t.SHA256})
		}
	}
	return out
}

// onListening вызывается, когда сервер начал слушать: тест ждёт этого, вместо опроса сокета.
var onListening = func(net.Listener) {}

// serve — запуск сервера: конфиг и секрет → хранилище (flock: второй экземпляр не стартует) → сервис (индекс,
// уборка при старте, сторож часов) → слушатель → http.Server. Остановка по ctx (SIGTERM): дообслужить запросы,
// остановить уборку, закрыть хранилище. Ошибки старта — в stderr без значений секретов и путей данных.
func serve(ctx context.Context, cfgPath string, stdout, stderr io.Writer) int {
	fail := func(what string, err error) int {
		_, _ = fmt.Fprintf(stderr, "albums-svc: %s: %v\n", what, err)
		return exitFail
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fail("конфиг", err)
	}
	master, err := config.LoadSecret(cfg.SecretFile)
	if err != nil {
		return fail("секрет", err)
	}
	keys, err := config.DeriveKeys(master)
	if err != nil {
		return fail("секрет", err)
	}
	log := logx.New(stdout)
	tagger := logx.NewTagger(keys.Log)
	st, err := store.Open(cfg.DataDir, storeOptions(cfg, tagger, log))
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return fail("данные", errors.New("каталог занят другим экземпляром сервиса"))
		}
		return fail("данные", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("хранилище закрыто с ошибкой", slog.String(logx.KeyOp, "close"), slog.Any(logx.KeyErr, err))
		}
	}()
	svc, err := album.New(st, albumConfig(cfg), album.Options{Passwords: access.NewPasswords(keys.Password), Tagger: tagger, Log: log})
	if err != nil {
		return fail("альбомы", err)
	}
	tokens, err := access.NewTokens(tokenEntries(cfg))
	if err != nil {
		return fail("токены", err)
	}
	handler, err := assemble(cfg, keys, svc, tokens, log, tagger)
	if err != nil {
		return fail("сборка", err)
	}

	ln, err := listen(ctx, cfg.Listen)
	if err != nil {
		return fail("слушатель", err)
	}
	srv := newServer(handler, log, defaultTimeouts)
	onListening(ln)

	var wg sync.WaitGroup
	sweepCtx, stopSweep := context.WithCancel(ctx)
	wg.Add(1)
	go func() { defer wg.Done(); svc.Run(sweepCtx, album.SweepEvery) }()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("сервис запущен", slog.String(logx.KeyOp, "start"))

	code := exitOK
	select {
	case <-ctx.Done():
	case err := <-served:
		log.Error("сервер остановился", slog.String(logx.KeyOp, "serve"), slog.Any(logx.KeyErr, err))
		code = exitFail
	}
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("остановка сервера", slog.String(logx.KeyOp, "shutdown"), slog.Any(logx.KeyErr, err))
		_ = srv.Close() // дообслужить не удалось: закрываем соединения, чтобы не висеть
		code = exitFail
	}
	stopSweep()
	wg.Wait()
	log.Info("сервис остановлен", slog.String(logx.KeyOp, "stop"))
	return code
}

// listen открывает слушателя из конфига. unix-сокет создаётся сразу с правами 0660 (umask 0117 на время
// создания, затем chmod): окна, в котором сокет доступен всем, нет. Остаток сокета от упавшего процесса убирается:
// если на подключение никто не отвечает, слушать на этом пути больше некому.
func listen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	if hostport, ok := strings.CutPrefix(addr, "tcp:"); ok {
		ln, err := lc.Listen(ctx, "tcp", hostport)
		if err != nil {
			return nil, fmt.Errorf("tcp: %w", err)
		}
		return ln, nil
	}
	path, ok := strings.CutPrefix(addr, "unix:")
	if !ok {
		return nil, errors.New("listen: unix:/путь или tcp:127.0.0.1:порт")
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("по пути сокета лежит не сокет")
		}
		// Сокет мог остаться от упавшего процесса, а мог принадлежать живому экземпляру с другими данными и тем
		// же listen (flock этого не покажет). Ответил на подключение — слушает живой: не удаляем.
		if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path); err == nil {
			_ = conn.Close()
			return nil, errors.New("по этому адресу уже работает другой экземпляр")
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("старый сокет не удалён: %w", err)
		}
	}
	old := syscall.Umask(0o117)
	ln, err := lc.Listen(ctx, "unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("unix: %w", err)
	}
	// Сокет 0660: писать в него должна группа Caddy (design §1). Каталог сокета закрыт для остальных.
	if err := os.Chmod(path, 0o660); err != nil { //nolint:gosec // G302: именно 0660, не 0600 — доступ группе Caddy (design §1)
		_ = ln.Close()
		return nil, fmt.Errorf("права сокета: %w", err)
	}
	return ln, nil
}
