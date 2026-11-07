// Package view — всё, что видит клиент альбома: /a/<id>/… (страница, manifest.json, view.json, картинки, вход
// по паролю) и /viewer/<версия>/… (встроенная листалка). Пакет не знает о диске: альбомы и файлы — через
// internal/album. Любой невидимый альбом (нет, не опубликован, неполный, истёк, отозван) отвечает одним и тем
// же 404, байт в байт, как у несуществующего (design §5).
package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
	"github.com/IliaSA101/sosninaalbums-cloud/web/viewer"
)

// ClientIPHeader — заголовок, в котором Caddy передаёт адрес клиента. Входящие X-Sa-* Caddy срезает, а сервис
// слушает unix-сокет в каталоге, закрытом от прочих процессов: подделать заголовок некому (design §1).
// Никаким другим заголовкам (X-Forwarded-For, Forwarded, X-Real-IP) сервис не верит.
const ClientIPHeader = "X-Sa-Client-IP"

// maxLoginBody — предел формы входа: пароль ≤ 256 знаков, остальное — имя поля. Caddy режет на 4 КиБ раньше;
// предел сервиса нужен на случай, если сервис окажется без Caddy (design §5.2).
const maxLoginBody = 4 << 10

// Тексты формы входа.
const (
	msgWrong   = "Неверный пароль"
	msgLimited = "Слишком много попыток"
	msgTooBig  = "Запрос слишком большой"
)

// Options — зависимости обработчика.
type Options struct {
	// PublicBase — https://домен без пути: в CSP листалка и стили разрешены только оттуда (design §5.6).
	PublicBase string
	// Studios — подпись студии по slug владельца (из конфига). Нет владельца в списке — страницы без подписи.
	Studios map[string]string
	// TrustedProxy — IP прокси при TCP-варианте связи: заголовок клиента принимается только от него. Пусто —
	// сокет unix, доверие к заголовку даёт сам сокет.
	TrustedProxy string
	Log          *slog.Logger // журнал запросов (через logx); по умолчанию — никуда
	Tagger       *logx.Tagger // метка альбома в журнале
	// Now — часы для cookie и лимита попыток; по умолчанию часы сервиса альбомов.
	Now func() time.Time
}

// Handler — обработчик /a/ и /viewer/.
type Handler struct {
	svc     *album.Service
	pw      *access.Passwords
	ck      *access.Cookies
	lim     *access.Limiter
	log     *slog.Logger
	tagger  *logx.Tagger
	now     func() time.Time
	trusted netip.Addr // нулевой — доверие сокету

	version  string // имя каталога листалки, 8 hex
	notFound []byte // notfound.html, те же байты, что отдаёт Caddy
	pages    map[string]*ownerPages
	anon     *ownerPages // для владельца вне конфига

	htmlCSP, sandboxCSP string
}

// ownerPages — страницы одного владельца, собранные заранее: подпись студии в них одна и та же для всех
// альбомов владельца, а значит шаблон исполняется один раз при старте, а не на каждый запрос.
type ownerPages struct {
	index    []byte
	viewJSON []byte
	login    map[string][]byte // сообщение → страница
}

// New собирает обработчик: версия листалки, страницы по владельцам, notfound.html.
func New(svc *album.Service, pw *access.Passwords, ck *access.Cookies, lim *access.Limiter, opt Options) (*Handler, error) {
	ver, err := viewer.Version()
	if err != nil {
		return nil, fmt.Errorf("версия листалки: %w", err)
	}
	nf, err := fs.ReadFile(viewer.Pages, "notfound.html")
	if err != nil {
		return nil, fmt.Errorf("notfound.html: %w", err)
	}
	h := &Handler{
		svc: svc, pw: pw, ck: ck, lim: lim, log: opt.Log, tagger: opt.Tagger, now: opt.Now,
		version: ver, notFound: nf, pages: map[string]*ownerPages{},
		htmlCSP: "default-src 'none'; script-src " + opt.PublicBase + "/viewer/; style-src " + opt.PublicBase +
			"/viewer/; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		sandboxCSP: "default-src 'none'; sandbox",
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	if h.now == nil {
		h.now = svc.Now
	}
	if opt.TrustedProxy != "" {
		a, err := netip.ParseAddr(opt.TrustedProxy)
		if err != nil {
			return nil, errors.New("trusted_proxy — не IP-адрес")
		}
		h.trusted = a.Unmap()
	}
	tpl, err := template.ParseFS(viewer.Pages, "index.html", "login.html")
	if err != nil {
		return nil, fmt.Errorf("шаблоны листалки: %w", err)
	}
	assets := "/viewer/" + ver + "/"
	for slug, studio := range opt.Studios {
		if h.pages[slug], err = renderPages(tpl, assets, studio); err != nil {
			return nil, err
		}
	}
	if h.anon, err = renderPages(tpl, assets, ""); err != nil {
		return nil, err
	}
	return h, nil
}

func renderPages(tpl *template.Template, assets, studio string) (*ownerPages, error) {
	p := &ownerPages{login: map[string][]byte{}}
	run := func(name, msg string) ([]byte, error) {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, name, viewer.PageData{Assets: assets, Studio: studio, Message: msg}); err != nil {
			return nil, fmt.Errorf("шаблон %s: %w", name, err)
		}
		return buf.Bytes(), nil
	}
	var err error
	if p.index, err = run("index.html", ""); err != nil {
		return nil, err
	}
	for _, msg := range []string{"", msgWrong, msgLimited, msgTooBig} {
		if p.login[msg], err = run("login.html", msg); err != nil {
			return nil, err
		}
	}
	if p.viewJSON, err = json.Marshal(struct {
		Studio string `json:"studio"`
	}{studio}); err != nil {
		return nil, fmt.Errorf("view.json: %w", err)
	}
	return p, nil
}

func (h *Handler) pagesOf(owner string) *ownerPages {
	if p, ok := h.pages[owner]; ok {
		return p
	}
	return h.anon
}

// Версия листалки — для проверок снаружи (адрес /viewer/<версия>/).
func (h *Handler) Version() string { return h.version }

// kind — что за адрес.
type kind int

const (
	kNone      kind = iota // не адрес сервиса
	kSlashless             // /a/<id>
	kIndex                 // /a/<id>/
	kManifest              // /a/<id>/manifest.json
	kViewJSON              // /a/<id>/view.json
	kFile                  // /a/<id>/<sha256>.jpg
	kLogin                 // /a/<id>/login
	kAsset                 // /viewer/<версия>/<файл>
)

// Маршруты (метка — для журнала; в журнал идёт шаблон, не путь).
const (
	routeIndex    = "GET /a/{id}/"
	routeManifest = "GET /a/{id}/manifest.json"
	routeViewJSON = "GET /a/{id}/view.json"
	routeFile     = "GET /a/{id}/{file}"
	routeLoginGet = "GET /a/{id}/login"
	routeLogin    = "POST /a/{id}/login"
	routeSlash    = "GET /a/{id}"
	routeAsset    = "GET /viewer/{version}/{file}"
	routeNone     = "-"
)

// parse разбирает путь строго: id по грамматике, остальное — из списка. Имя файла страницы проверяется той же
// грамматикой, что и при записи (names.ValidPageName); путь к файлу из ввода не склеивается.
func parse(p string) (k kind, id, name string) {
	if rest, ok := strings.CutPrefix(p, "/viewer/"); ok {
		return kAsset, "", rest
	}
	rest, ok := strings.CutPrefix(p, "/a/")
	if !ok {
		return kNone, "", ""
	}
	id, tail, slash := strings.Cut(rest, "/")
	if !names.ValidID(id) {
		return kNone, "", ""
	}
	switch {
	case !slash:
		return kSlashless, id, ""
	case tail == "":
		return kIndex, id, ""
	case tail == "manifest.json":
		return kManifest, id, ""
	case tail == "view.json":
		return kViewJSON, id, ""
	case tail == "login":
		return kLogin, id, ""
	case names.ValidPageName(tail):
		return kFile, id, tail
	}
	return kNone, "", ""
}

// ServeHTTP — вход: путь → метод → альбом → вход по паролю → ответ. Что бы ни было не так до альбома (путь,
// метод, id), ответ — тот же 404, что у несуществующего альбома.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := &call{h: h, w: &statusWriter{ResponseWriter: w}, r: r, route: routeNone, start: time.Now()}
	defer c.finish()

	p := r.URL.EscapedPath()
	if !names.CanonicalPath(p) { // %2e, «..», «//», регистр /A/: 404, а не перенаправление
		c.missing()
		return
	}
	k, id, name := parse(p)
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	post := k == kLogin && r.Method == http.MethodPost // единственный адрес, принимающий не чтение
	switch {
	case k == kNone, !read && !post:
		c.missing()
		return
	case k == kAsset:
		c.route = routeAsset
		h.asset(c, name)
		return
	}
	c.id = id
	c.route = map[kind]string{kSlashless: routeSlash, kIndex: routeIndex, kManifest: routeManifest, kViewJSON: routeViewJSON,
		kFile: routeFile, kLogin: routeLoginGet}[k]
	if k == kLogin && r.Method == http.MethodPost {
		c.route = routeLogin
	}
	acc, err := h.svc.Access(id)
	if err != nil {
		if !errors.Is(err, album.ErrNotFound) {
			c.err = errors.New("доступ к альбому не определён")
		}
		c.id = "" // метка альбома — только у существующего: нет смысла писать хэш чужих догадок
		c.missing()
		return
	}
	c.owner = acc.Owner
	switch k {
	case kSlashless:
		c.redirect(http.StatusPermanentRedirect, "/a/"+id+"/")
	case kLogin:
		if r.Method == http.MethodPost {
			h.login(c, acc)
		} else { // форма живёт на /a/<id>/: после неверного пароля обновление страницы не должно ломаться
			c.redirect(http.StatusSeeOther, "/a/"+id+"/")
		}
	default:
		h.view(c, k, name, acc)
	}
}

// session — то, от чего зависит годность cookie: из снимка доступа альбома.
func session(id string, acc album.Access) access.Session {
	return access.Session{ID: id, Salt: acc.Password.Salt, Epoch: acc.Epoch}
}

// authorized — у альбома нет пароля или присланная cookie годна (хоть одна из одноимённых).
func (h *Handler) authorized(r *http.Request, id string, acc album.Access) bool {
	if acc.Password == nil {
		return true
	}
	var vals []string
	for _, ck := range r.CookiesNamed(access.CookieName) {
		vals = append(vals, ck.Value)
		if len(vals) == access.MaxCookies {
			break
		}
	}
	return h.ck.Valid(session(id, acc), h.now(), vals)
}

// clientIP — адрес клиента из заголовка Caddy. Нет, не один, не разбирается, либо (TCP-вариант) запрос не от
// доверенного прокси — не определён: вход отвечает 503, а не ставит общий ключ всем (001-17).
func (h *Handler) clientIP(r *http.Request) (netip.Addr, bool) {
	if h.trusted.IsValid() {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return netip.Addr{}, false
		}
		peer, err := netip.ParseAddr(host)
		if err != nil || peer.Unmap() != h.trusted {
			return netip.Addr{}, false
		}
	}
	vals := r.Header.Values(ClientIPHeader)
	if len(vals) != 1 {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(vals[0]))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap().WithZone(""), true
}

// Заголовки всех ответов /a/ (design §5.6): страницы с id в адресе не индексируются, не утекают в Referer, не
// встраиваются и не читаются чужими источниками.
func (h *Handler) common(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("X-Frame-Options", "DENY")
}

// Кэш (design §5.6).
const (
	cacheNone    = "no-store"          // форма, 401, 404, 429, перенаправления
	cachePrivate = "private, no-cache" // страница, manifest.json, view.json: перечитывать у сервиса
	cacheImage   = "private, max-age=86400, immutable"
)

// send отвечает телом целиком: Content-Length известен, ответ не рвётся посередине.
func (c *call) send(status int, ctype, csp, cache string, body []byte) {
	h := c.h
	h.common(c.w)
	hd := c.w.Header()
	hd.Set("Content-Type", ctype)
	hd.Set("Content-Security-Policy", csp)
	hd.Set("Cache-Control", cache)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	c.w.WriteHeader(status)
	if c.r.Method == http.MethodHead {
		return
	}
	if _, err := c.w.Write(body); err != nil {
		c.err = errors.New("ответ не записан клиенту")
	}
}

const htmlType = "text/html; charset=utf-8"

// missing — единственный 404: страница notfound.html и заголовки, как у handle_errors Caddy (design §5.1).
func (c *call) missing() {
	c.code = "not_found"
	c.send(http.StatusNotFound, htmlType, c.h.htmlCSP, cacheNone, c.h.notFound)
}

// html отвечает страницей листалки или формой входа.
func (c *call) html(status int, cache string, body []byte) {
	c.send(status, htmlType, c.h.htmlCSP, cache, body)
}

// redirect — перенаправление без тела.
func (c *call) redirect(status int, to string) {
	c.h.common(c.w)
	hd := c.w.Header()
	hd.Set("Location", to)
	hd.Set("Cache-Control", cacheNone)
	hd.Set("Content-Security-Policy", c.h.sandboxCSP)
	hd.Set("Content-Length", "0")
	c.w.WriteHeader(status)
}

// denied — 401 без входа: для страницы — форма, для остального — пустое тело (ни манифест, ни картинка не
// должны выдавать ничего, даже текст ошибки).
func (c *call) denied(k kind, acc album.Access) {
	c.code = "unauthorized"
	if k == kIndex {
		c.html(http.StatusUnauthorized, cacheNone, c.h.pagesOf(acc.Owner).login[""])
		return
	}
	c.send(http.StatusUnauthorized, "text/plain; charset=utf-8", c.h.sandboxCSP, cacheNone, nil)
}
