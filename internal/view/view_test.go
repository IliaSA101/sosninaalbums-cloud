package view

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/web/viewer"
)

const ghostID = "qqqqqqqqqqqqqqqqqqqqqqqqqq" // по грамматике, но такого альбома нет

// want404 — ответ равен эталонному 404 целиком: код, заголовки и байты тела.
func want404(t *testing.T, ref, got response, what string) {
	t.Helper()
	if got.status != http.StatusNotFound {
		t.Errorf("%s: код %d, нужно 404 (тело %.60q)", what, got.status, got.body)
		return
	}
	if !bytes.Equal(got.body, ref.body) {
		t.Errorf("%s: тело 404 отличается от эталона", what)
	}
	for k, v := range ref.hdr {
		if strings.Join(got.hdr[k], "|") != strings.Join(v, "|") {
			t.Errorf("%s: заголовок %s: %q, у эталона %q", what, k, got.hdr[k], v)
		}
	}
	if len(got.hdr) != len(ref.hdr) {
		t.Errorf("%s: набор заголовков %v, у эталона %v", what, got.hdr, ref.hdr)
	}
}

func TestViewMatrix(t *testing.T) {
	e := newEnv(t)
	ref := e.get("/a/" + ghostID + "/")
	nf, err := fs.ReadFile(viewer.Pages, "notfound.html")
	if err != nil {
		t.Fatal(err)
	}
	if ref.status != 404 || !bytes.Equal(ref.body, nf) {
		t.Fatalf("эталон 404: %d, тело не notfound.html", ref.status)
	}
	sha := strings.Repeat("ab", 32)
	addrs := func(id, file string) []string {
		return []string{"/a/" + id + "/", "/a/" + id, "/a/" + id + "/manifest.json", "/a/" + id + "/view.json", "/a/" + id + "/" + file, "/a/" + id + "/login"}
	}

	// Невидимые состояния: все адреса — один и тот же 404.
	unpublished := e.create("Т", "")

	expired := e.published("")
	expiredFile := expired.file

	deleted := e.published("")
	if err := e.svc.Delete(owner, deleted.id); err != nil {
		t.Fatal(err)
	}

	incomplete := e.published("")
	e.removeFile(incomplete.id, incomplete.file)
	if r := e.get("/a/" + incomplete.id + "/" + incomplete.file); r.status != 404 { // отдача замечает пропажу файла
		t.Fatalf("файл неполного альбома: %d", r.status)
	}

	cases := map[string][]string{
		"нет такого":        addrs(ghostID, sha+".jpg"),
		"не опубликован":    addrs(unpublished.id, sha+".jpg"),
		"удалён":            addrs(deleted.id, deleted.file),
		"неполный":          addrs(incomplete.id, incomplete.file),
		"id 25 знаков":      addrs(ghostID[:25], sha+".jpg"),
		"id 27 знаков":      addrs(ghostID+"q", sha+".jpg"),
		"id заглавными":     addrs(strings.ToUpper(ghostID), sha+".jpg"),
		"id с дефисом":      addrs(ghostID[:25]+"-", sha+".jpg"),
		"id с цифрой 1":     addrs(ghostID[:25]+"1", sha+".jpg"),
		"id из 22 знаков":   addrs(strings.Repeat("a", 22), sha+".jpg"),
		"id очень длинный":  addrs(strings.Repeat("a", 3000), sha+".jpg"),
		"id с точкой":       addrs(ghostID[:25]+".", sha+".jpg"),
		"пустой id":         {"/a/", "/a//", "/a//manifest.json"},
		"прочие корни":      {"/", "/favicon.ico", "/meta/x", "/meta.json", "/robots.txt", "/v2/albums", "/A/" + ghostID + "/"},
		"viewer: не то":     {"/viewer/", "/viewer", "/viewer/zzzzzzzz/app.js", "/viewer/00000000/app.js"},
		"viewer: каталог":   {"/viewer/" + e.h.version + "/", "/viewer/" + e.h.version, "/viewer/" + e.h.version + "/vendor", "/viewer/" + e.h.version + "/vendor/"},
		"viewer: обходы":    {"/viewer/" + e.h.version + "/../app.js", "/viewer/" + e.h.version + "/%2e%2e/app.js", "/viewer/" + e.h.version + "/./app.js", "/viewer/" + e.h.version + "//app.js"},
		"viewer: не файл":   {"/viewer/" + e.h.version + "/embed.go", "/viewer/" + e.h.version + "/index.html", "/viewer/" + e.h.version + "/login.html", "/viewer/" + e.h.version + "/notfound.html", "/viewer/" + e.h.version + "/app.js.map"},
		"viewer: регистр":   {"/viewer/" + e.h.version + "/APP.JS", "/Viewer/" + e.h.version + "/app.js"},
		"viewer: кодировка": {"/viewer/" + e.h.version + "/app%2ejs", "/viewer/" + e.h.version + "%2fapp.js", "/viewer/%2e%2e/a/" + ghostID + "/"},
	}
	for name, targets := range cases {
		for _, target := range targets {
			want404(t, ref, e.get(target), name+" "+redact(target))
		}
	}

	// Истёкший: сразу после срока 404 на всех адресах, не дожидаясь уборки.
	for _, target := range addrs(expired.id, expiredFile) {
		if r := e.get(target); r.status == 404 {
			t.Fatalf("альбом до срока не виден: %s", redact(target))
		}
	}
	e.clk.advance(49 * time.Hour)
	for _, target := range addrs(expired.id, expiredFile) {
		want404(t, ref, e.get(target), "истёк "+redact(target))
	}

	// Видимый альбом: всё, что не из списка адресов или методов, — тот же 404.
	vis := e.published("")
	bad := []string{
		"/a/" + vis.id + "/meta.json", "/a/" + vis.id + "/../" + vis.id + "/", "/a/" + vis.id + "/%2e%2e/meta.json",
		"/a/" + vis.id + "/..%2fmeta.json", "/a//" + vis.id + "/", "/a/" + vis.id + "//", "/a/" + vis.id + "/manifest.json/",
		"/a/" + vis.id + "/login/", "/a/" + vis.id + "/x/y", "/a/" + vis.id + "/" + sha + ".png", "/a/" + vis.id + "/" + strings.ToUpper(sha) + ".jpg",
		"/a/" + vis.id + "/" + sha[:60] + ".jpg", "/a/" + vis.id + "/" + sha + ".jpg.bak", "/a/" + vis.id + "/.hidden", "/a/" + vis.id + "/Manifest.json",
		"/a/" + vis.id + "/%6danifest.json", "/a/" + vis.id + "\\manifest.json", "/A/" + vis.id + "/",
		"/a/" + vis.id + "/" + sha + ".jpg", // нет такого файла в published
	}
	for _, target := range bad {
		want404(t, ref, e.get(target), "видимый альбом, адрес "+redact(target))
	}
	for _, m := range []string{"PUT", "DELETE", "PATCH", "OPTIONS", "TRACE", "CONNECT", "PROPFIND"} {
		want404(t, ref, e.do(m, "/a/"+vis.id+"/", nil), "метод "+m)
		want404(t, ref, e.do(m, "/a/"+vis.id+"/manifest.json", nil), "метод "+m)
	}
	want404(t, ref, e.do("POST", "/a/"+vis.id+"/", strings.NewReader("x=1")), "POST на страницу")
	want404(t, ref, e.do("POST", "/a/"+vis.id+"/manifest.json", strings.NewReader("x=1")), "POST на манифест")
	want404(t, ref, e.do("POST", "/viewer/"+e.h.version+"/app.js", nil), "POST на листалку")
	want404(t, ref, e.do("PUT", "/a/"+vis.id+"/login", nil), "PUT на вход")
}

// redact — адрес для сообщения теста без id (на случай, если он реальный).
func redact(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// Файл прежней редакции после publish — 404 только ему; альбом и новые файлы живы.
func TestOldManifestFile404(t *testing.T) {
	e := newEnv(t)
	f := e.published("")
	old := f.file
	m := book("Т", blob(1), nil, blob(30), blob(4)) // страница 3 другая
	if _, err := e.svc.Update(owner, f.id, m); err != nil {
		t.Fatal(err)
	}
	e.upload(f.id, blob(30))
	if r := e.get("/a/" + f.id + "/" + old); r.status != 200 {
		t.Fatalf("до publish прежняя редакция должна отдаваться: %d", r.status)
	}
	if _, err := e.svc.Publish(owner, f.id); err != nil {
		t.Fatal(err)
	}
	gone := e.fileName(f.id, 1) // страница 3 новой редакции
	if r := e.get("/a/" + f.id + "/" + gone); r.status != 200 {
		t.Fatalf("новый файл: %d", r.status)
	}
	// Страница 3 прежней редакции: пропала из published и с диска.
	oldPage3 := pageOf(3, blob(3)).SHA256 + ".jpg"
	ref := e.get("/a/" + ghostID + "/")
	want404(t, ref, e.get("/a/"+f.id+"/"+oldPage3), "файл прежней редакции")
	if r := e.get("/a/" + f.id + "/"); r.status != 200 { // альбом не стал «неполным»
		t.Fatalf("страница альбома после запроса старого файла: %d", r.status)
	}
	if r := e.get("/a/" + f.id + "/" + old); r.status != 200 {
		t.Fatalf("неизменённая страница: %d", r.status)
	}
}

func TestViewServes(t *testing.T) {
	e := newEnv(t)
	f := e.published("")
	// Страница: листалка с подписью студии, без названия.
	r := e.get("/a/" + f.id + "/")
	if r.status != 200 || !strings.Contains(string(r.body), studioName) || !strings.Contains(string(r.body), "/viewer/"+e.h.version+"/app.js") {
		t.Fatalf("страница: %d %.200s", r.status, r.body)
	}
	// manifest.json: тот, что у сервиса альбомов; название — только здесь.
	mf := e.get("/a/" + f.id + "/manifest.json")
	var man albums.Manifest
	if mf.status != 200 || json.Unmarshal(mf.body, &man) != nil || man.Title != "Секретное название" || len(man.Pages) != 4 {
		t.Fatalf("manifest: %d %.200s", mf.status, mf.body)
	}
	for _, p := range man.Pages {
		if !p.Blank && p.File == "" {
			t.Fatalf("у страницы %d нет file", p.N)
		}
	}
	// view.json
	vj := e.get("/a/" + f.id + "/view.json")
	if vj.status != 200 || string(vj.body) != `{"studio":"`+studioName+`"}` || vj.hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("view.json: %d %s %q", vj.status, vj.body, vj.hdr.Get("Content-Type"))
	}
	// Картинка: байты, тип, ETag и 304, диапазон.
	img := e.get("/a/" + f.id + "/" + f.file)
	if img.status != 200 || !bytes.Equal(img.body, blob(1)) || img.hdr.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("картинка: %d %q", img.status, img.hdr.Get("Content-Type"))
	}
	etag := img.hdr.Get("Etag")
	if etag == "" || etag != `"`+strings.TrimSuffix(f.file, ".jpg")+`"` {
		t.Fatalf("ETag %q", etag)
	}
	if r := e.get("/a/"+f.id+"/"+f.file, withHeader("If-None-Match", etag)); r.status != 304 || len(r.body) != 0 {
		t.Fatalf("If-None-Match: %d, тело %d байт", r.status, len(r.body))
	}
	if r := e.get("/a/"+f.id+"/"+f.file, withHeader("Range", "bytes=0-9")); r.status != 206 || !bytes.Equal(r.body, blob(1)[:10]) {
		t.Fatalf("Range: %d", r.status)
	}
	if r := e.get("/a/"+f.id+"/"+f.file, withHeader("Range", "bytes=999999-")); r.status != 416 || r.hdr.Get("Cache-Control") == cacheImage {
		t.Fatalf("Range за пределом: %d, Cache-Control %q", r.status, r.hdr.Get("Cache-Control"))
	}
	// HEAD — те же заголовки, без тела.
	for _, target := range []string{"/a/" + f.id + "/", "/a/" + f.id + "/manifest.json", "/a/" + f.id + "/" + f.file} {
		g, h := e.get(target), e.do("HEAD", target, nil)
		if h.status != g.status || len(h.body) != 0 || h.hdr.Get("Content-Type") != g.hdr.Get("Content-Type") || h.hdr.Get("Content-Length") != g.hdr.Get("Content-Length") {
			t.Errorf("HEAD %s: %d, тело %d, тип %q", redact(target), h.status, len(h.body), h.hdr.Get("Content-Type"))
		}
	}
	// Без слэша — 308 на адрес со слэшем, только для видимого альбома.
	r = e.get("/a/" + f.id)
	if r.status != 308 || r.hdr.Get("Location") != "/a/"+f.id+"/" {
		t.Fatalf("без слэша: %d %q", r.status, r.hdr.Get("Location"))
	}
	if g := e.get("/a/" + ghostID); g.status != 404 {
		t.Fatalf("без слэша у несуществующего: %d", g.status)
	}
	// GET на вход перенаправляет на страницу (форма живёт там).
	if r := e.get("/a/" + f.id + "/login"); r.status != 303 || r.hdr.Get("Location") != "/a/"+f.id+"/" {
		t.Fatalf("GET login: %d %q", r.status, r.hdr.Get("Location"))
	}
	// Строка запроса игнорируется.
	if r := e.get("/a/" + f.id + "/manifest.json?x=1&token=y"); r.status != 200 {
		t.Fatalf("строка запроса: %d", r.status)
	}
}

func TestViewAssets(t *testing.T) {
	e := newEnv(t)
	base := "/viewer/" + e.h.version + "/"
	for file, typ := range map[string]string{
		"app.js": "text/javascript; charset=utf-8", "viewer.css": "text/css; charset=utf-8", "icon.svg": "image/svg+xml",
		"vendor/page-flip-2.0.7.browser.js": "text/javascript; charset=utf-8", "vendor/page-flip-2.0.7.css": "text/css; charset=utf-8",
		"vendor/LICENSE-page-flip.txt": "text/plain; charset=utf-8",
	} {
		r := e.get(base + file)
		want, err := fs.ReadFile(viewer.Assets, file)
		if err != nil {
			t.Fatal(err)
		}
		if r.status != 200 || !bytes.Equal(r.body, want) || r.hdr.Get("Content-Type") != typ {
			t.Errorf("%s: %d %q", file, r.status, r.hdr.Get("Content-Type"))
		}
		if cc := r.hdr.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", file, cc)
		}
		if r.hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: нет nosniff", file)
		}
		if r.hdr.Get("Set-Cookie") != "" {
			t.Errorf("%s: cookie у статики", file)
		}
	}
	if h := e.do("HEAD", base+"app.js", nil); h.status != 200 || len(h.body) != 0 {
		t.Fatalf("HEAD: %d", h.status)
	}
	// Листалка доступна без входа и без альбома: в ней нет данных клиента.
	pw := e.published("Пароль-для-листалки-1")
	if r := e.get(base + "app.js"); r.status != 200 {
		t.Fatalf("листалка при альбоме с паролем: %d", r.status)
	}
	_ = pw
}

// Заголовки каждого вида ответа (design §5.6) и согласие с Caddyfile: тот ставит свои только там, где сервис
// не поставил (`?`), поэтому значения у 404 сервиса и 404 Caddy должны совпадать.
func TestViewHeaders(t *testing.T) {
	e := newEnv(t)
	open := e.published("")
	locked := e.published("Пароль-для-заголовков-1")
	ck := e.signIn(locked.id, "Пароль-для-заголовков-1")
	htmlCSP := "default-src 'none'; script-src " + publicBase + "/viewer/; style-src " + publicBase + "/viewer/; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	const sandbox = "default-src 'none'; sandbox"
	type want struct {
		status     int
		csp, cache string
	}
	for name, c := range map[string]struct {
		r response
		w want
	}{
		"страница":        {e.get("/a/" + open.id + "/"), want{200, htmlCSP, "private, no-cache"}},
		"manifest":        {e.get("/a/" + open.id + "/manifest.json"), want{200, sandbox, "private, no-cache"}},
		"view.json":       {e.get("/a/" + open.id + "/view.json"), want{200, sandbox, "private, no-cache"}},
		"картинка":        {e.get("/a/" + open.id + "/" + open.file), want{200, sandbox, "private, max-age=86400, immutable"}},
		"форма входа":     {e.get("/a/" + locked.id + "/"), want{401, htmlCSP, "no-store"}},
		"401 манифеста":   {e.get("/a/" + locked.id + "/manifest.json"), want{401, sandbox, "no-store"}},
		"401 картинки":    {e.get("/a/" + locked.id + "/" + locked.file), want{401, sandbox, "no-store"}},
		"со входом":       {e.get("/a/"+locked.id+"/", withCookie(ck)), want{200, htmlCSP, "private, no-cache"}},
		"404":             {e.get("/a/" + ghostID + "/"), want{404, htmlCSP, "no-store"}},
		"308":             {e.get("/a/" + open.id), want{308, sandbox, "no-store"}},
		"GET login":       {e.get("/a/" + open.id + "/login"), want{303, sandbox, "no-store"}},
		"неверный пароль": {e.login(locked.id, "не тот пароль"), want{401, htmlCSP, "no-store"}},
		"верный вход":     {e.login(locked.id, "Пароль-для-заголовков-1"), want{303, sandbox, "no-store"}},
	} {
		r := c.r
		if r.status != c.w.status || r.hdr.Get("Content-Security-Policy") != c.w.csp || r.hdr.Get("Cache-Control") != c.w.cache {
			t.Errorf("%s: %d, CSP %q, Cache-Control %q", name, r.status, r.hdr.Get("Content-Security-Policy"), r.hdr.Get("Cache-Control"))
		}
		for k, v := range map[string]string{
			"X-Robots-Tag": "noindex, nofollow", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff",
			"Cross-Origin-Resource-Policy": "same-origin", "X-Frame-Options": "DENY",
		} {
			if got := r.hdr.Get(k); got != v {
				t.Errorf("%s: %s = %q", name, k, got)
			}
		}
		if strings.Contains(strings.ToLower(string(r.body)), "og:image") {
			t.Errorf("%s: og:image в теле", name)
		}
	}

	// Согласие с Caddyfile: значения после «?» — те же, что у сервиса.
	raw, err := os.ReadFile("../../deploy/caddy/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	// «?»-поле — в блоке header { … } или отдельной директивой «header ?Имя значение» (так в Caddyfile после стенда П1).
	re := regexp.MustCompile(`(?m)^\s*(?:header\s+)?\?([A-Za-z-]+)\s+(.+?)\s*$`)
	found := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		found[textproto.CanonicalMIMEHeaderKey(m[1])] = strings.ReplaceAll(strings.Trim(m[2], `"`), "https://{$ALBUMS_DOMAIN}", publicBase)
	}
	if len(found) < 6 {
		t.Fatalf("в Caddyfile нашлось %d заголовков «?»: %v", len(found), found)
	}
	nf := e.get("/a/" + ghostID + "/")
	for k, v := range found {
		if got := nf.hdr.Get(k); got != v {
			t.Errorf("Caddyfile ставит %s: %q, а сервис — %q", k, v, got)
		}
	}
}

// Название альбома — ПД: в HTML страницы, формы и 404 его нет, боты и предпросмотр ссылок его не получат.
func TestViewNoTitleInHTML(t *testing.T) {
	e := newEnv(t)
	const title = "Секретное название"
	open := e.published("")
	locked := e.published("Пароль-без-названия-1")
	for name, r := range map[string]response{
		"страница": e.get("/a/" + open.id + "/"), "форма": e.get("/a/" + locked.id + "/"),
		"неверный пароль": e.login(locked.id, "x"), "404": e.get("/a/" + ghostID + "/"),
		"страница со входом": e.get("/a/"+locked.id+"/", withCookie(e.signIn(locked.id, "Пароль-без-названия-1"))),
	} {
		low := strings.ToLower(string(r.body))
		if strings.Contains(string(r.body), title) || strings.Contains(low, "og:") || strings.Contains(low, "twitter:") || strings.Contains(low, "<img") {
			t.Errorf("%s: название или метаданные предпросмотра в HTML", name)
		}
		if !strings.Contains(low, `name="robots" content="noindex, nofollow"`) {
			t.Errorf("%s: нет noindex", name)
		}
	}
	// Подпись студии из конфига экранируется шаблоном.
	e.opt.Studios = map[string]string{owner: `<b>x</b><script>alert(1)</script>`}
	h, err := New(e.svc, e.pw, e.ck, e.h.lim, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]response{
		"страница": e.doTo(h, "GET", "/a/"+open.id+"/", nil), "форма": e.doTo(h, "GET", "/a/"+locked.id+"/", nil),
		"view.json": e.doTo(h, "GET", "/a/"+open.id+"/view.json", nil),
	} {
		if strings.Contains(string(r.body), "<b>x</b>") || strings.Contains(string(r.body), "<script>alert") {
			t.Errorf("%s: подпись студии не экранирована: %.200s", name, r.body)
		}
	}
	var vj struct{ Studio string }
	if r := e.doTo(h, "GET", "/a/"+open.id+"/view.json", nil); json.Unmarshal(r.body, &vj) != nil || vj.Studio != `<b>x</b><script>alert(1)</script>` {
		t.Errorf("view.json теряет подпись: %s", r.body)
	}
}

// Альбом владельца вне конфига открывается без подписи (а не 500 и не чужая подпись).
func TestViewUnknownOwnerStudio(t *testing.T) {
	e := newEnv(t)
	f := e.published("")
	e.opt.Studios = map[string]string{"p9": "Чужая студия"}
	h, err := New(e.svc, e.pw, e.ck, e.h.lim, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	r := e.doTo(h, "GET", "/a/"+f.id+"/", nil)
	if r.status != 200 || strings.Contains(string(r.body), "Чужая студия") {
		t.Fatalf("%d %.200s", r.status, r.body)
	}
	if v := e.doTo(h, "GET", "/a/"+f.id+"/view.json", nil); string(v.body) != `{"studio":""}` {
		t.Fatalf("view.json: %s", v.body)
	}
}

// Пароль: без cookie закрыто всё (страница — форма, остальное — пустой 401), с годной — открыто; cookie
// другого альбома, подделка, сброс входов, смена и снятие пароля.
func TestViewPasswordAccess(t *testing.T) {
	e := newEnv(t)
	const pw = "Пароль-доступа-4821"
	a, b := e.published(pw), e.published(pw)
	targets := func(f fixture) []string {
		return []string{"/a/" + f.id + "/", "/a/" + f.id + "/manifest.json", "/a/" + f.id + "/view.json", "/a/" + f.id + "/" + f.file}
	}
	ckA := e.signIn(a.id, pw)
	for _, tg := range targets(a) {
		if r := e.get(tg); r.status != 401 {
			t.Errorf("без входа %s: %d", redact(tg), r.status)
		}
		if r := e.get(tg, withCookie(ckA)); r.status != 200 {
			t.Errorf("со входом %s: %d", redact(tg), r.status)
		}
		if r := e.get(tg, withCookie(tamper(ckA))); r.status != 401 {
			t.Errorf("подделка %s: %d", redact(tg), r.status)
		}
	}
	// Страница без входа — форма, остальное — пустое тело.
	if r := e.get(targets(a)[0]); !strings.Contains(string(r.body), `name="password"`) {
		t.Error("401 страницы без формы")
	}
	for _, tg := range targets(a)[1:] {
		if r := e.get(tg); len(r.body) != 0 {
			t.Errorf("401 %s с телом", redact(tg))
		}
	}
	// Cookie альбома A не открывает B.
	for _, tg := range targets(b) {
		if r := e.get(tg, withCookie(ckA)); r.status != 401 {
			t.Errorf("чужая cookie %s: %d", redact(tg), r.status)
		}
	}
	// Пять одноимённых cookie: годна первая или четвёртая — открыто, пятая — нет (проверяются первые четыре).
	junk := "1.0.9999999999.AAAA"
	many := func(vals ...string) reqOpt {
		return func(r *http.Request) {
			for _, v := range vals {
				r.AddCookie(&http.Cookie{Name: "__Secure-sa_session", Value: v})
			}
		}
	}
	if r := e.get(targets(a)[1], many(junk, junk, junk, ckA)); r.status != 200 {
		t.Errorf("годная четвёртой: %d", r.status)
	}
	if r := e.get(targets(a)[1], many(junk, junk, junk, junk, ckA)); r.status != 401 {
		t.Errorf("годная пятой: %d", r.status)
	}
	// Сброс входов: прежняя cookie негодна, новый вход работает.
	if _, err := e.svc.Patch(owner, a.id, &albums.Patch{RevokeSessions: true}); err != nil {
		t.Fatal(err)
	}
	if r := e.get(targets(a)[1], withCookie(ckA)); r.status != 401 {
		t.Errorf("после revoke: %d", r.status)
	}
	ckA2 := e.signIn(a.id, pw)
	if r := e.get(targets(a)[1], withCookie(ckA2)); r.status != 200 {
		t.Errorf("новый вход после revoke: %d", r.status)
	}
	// Смена пароля закрывает и такой вход, старый пароль не подходит.
	newPW := "Другой-пароль-9035"
	if _, err := e.svc.Patch(owner, a.id, &albums.Patch{Password: &newPW}); err != nil {
		t.Fatal(err)
	}
	if r := e.get(targets(a)[1], withCookie(ckA2)); r.status != 401 {
		t.Errorf("после смены пароля: %d", r.status)
	}
	if r := e.login(a.id, pw); r.status != 401 || sessionOf(t, r) != "" {
		t.Errorf("старый пароль после смены: %d", r.status)
	}
	if r := e.get(targets(a)[1], withCookie(e.signIn(a.id, newPW))); r.status != 200 {
		t.Errorf("новый пароль: %d", r.status)
	}
	// Снятие пароля — открыто без входа.
	none := ""
	if _, err := e.svc.Patch(owner, a.id, &albums.Patch{Password: &none}); err != nil {
		t.Fatal(err)
	}
	if r := e.get(targets(a)[1]); r.status != 200 {
		t.Errorf("после снятия пароля: %d", r.status)
	}
	// Срок: cookie не дольше альбома — после срока 404 даже с годной cookie.
	ckB := e.signIn(b.id, pw)
	e.clk.advance(49 * time.Hour)
	if r := e.get(targets(b)[1], withCookie(ckB)); r.status != 404 {
		t.Errorf("после срока с cookie: %d", r.status)
	}
}

// Любой путь — без паники, без 5xx и без утечки: невидимое всегда notfound.html.
func FuzzViewPath(f *testing.F) {
	for _, s := range []string{
		"/", "/a/", "/a/" + ghostID, "/a/" + ghostID + "/", "/a/" + ghostID + "/manifest.json", "/a/" + ghostID + "/login",
		"/a/../x", "/a/%2e%2e/", "/a/" + ghostID + "/../../meta.json", "/viewer/", "/viewer/x/y", "//a//", "/a/\x00", "/a/é/", "/%",
		"/a/" + strings.Repeat("a", 26) + "/" + strings.Repeat("0", 64) + ".jpg",
	} {
		f.Add(s)
	}
	e := newEnv(f)
	nf, err := fs.ReadFile(viewer.Pages, "notfound.html")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, target string) {
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://x/", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Адрес задаётся как есть, минуя разбор клиента: в нём бывают знаки, которых клиент не пропустил бы.
		req.URL.Path, req.URL.RawPath = target, target
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("%q: %d", target, rec.Code)
		}
		if rec.Code == 404 && !bytes.Equal(rec.Body.Bytes(), nf) {
			t.Fatalf("%q: 404 с чужим телом", target)
		}
		if rec.Code != 404 && !strings.HasPrefix(target, "/viewer/"+e.h.version+"/") {
			t.Fatalf("%q: %d, видимых адресов у пустого сервиса нет", target, rec.Code)
		}
	})
}
