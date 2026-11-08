package view

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

const loginPW = "Пароль-для-входа-5512"

func setCookie(t *testing.T, r response) *http.Cookie {
	t.Helper()
	res := http.Response{Header: r.hdr}
	cs := res.Cookies()
	if len(cs) != 1 {
		t.Fatalf("Set-Cookie: %d штук", len(cs))
	}
	return cs[0]
}

func TestLoginCookieAttrs(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	r := e.login(f.id, loginPW)
	if r.status != http.StatusSeeOther || r.hdr.Get("Location") != "/a/"+f.id+"/" {
		t.Fatalf("вход: %d %q", r.status, r.hdr.Get("Location"))
	}
	c := setCookie(t, r)
	if c.Name != "__Secure-sa_session" || c.Path != "/a/"+f.id+"/" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Domain != "" {
		t.Fatalf("атрибуты cookie: %+v", c)
	}
	if c.MaxAge != 48*3600 || !strings.HasPrefix(c.Value, "1.0.") {
		t.Fatalf("Max-Age %d, значение %.6q", c.MaxAge, c.Value)
	}
	if raw := r.hdr.Get("Set-Cookie"); !strings.Contains(raw, "Secure") || !strings.Contains(raw, "HttpOnly") || !strings.Contains(raw, "SameSite=Lax") || strings.Contains(raw, "Domain") {
		t.Fatalf("Set-Cookie: %.60q", raw)
	}
	// Срок альбома длиннее 30 дней: вход — 30 дней; короче — не дольше альбома.
	long := e.published(loginPW)
	far := e.clk.Wall().Add(100 * 24 * time.Hour)
	if _, err := e.svc.Patch(owner, long.id, &albums.Patch{ExpiresAt: &far}); err != nil {
		t.Fatal(err)
	}
	if c := setCookie(t, e.login(long.id, loginPW)); c.MaxAge != 30*24*3600 {
		t.Fatalf("Max-Age при сроке альбома 100 дней: %d", c.MaxAge)
	}
	short := e.published(loginPW)
	exp := e.clk.Wall().Add(36 * time.Hour)
	if _, err := e.svc.Patch(owner, short.id, &albums.Patch{ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	if c := setCookie(t, e.login(short.id, loginPW)); c.MaxAge != 36*3600 {
		t.Fatalf("Max-Age при сроке альбома 36 ч: %d", c.MaxAge)
	}
	if len(r.body) != 0 {
		t.Errorf("303 с телом: %d байт", len(r.body))
	}
	// Страница после входа открывается выданной cookie.
	if g := e.get("/a/"+f.id+"/", withCookie(c.Value)); g.status != 200 {
		t.Fatalf("страница с cookie: %d", g.status)
	}
	// Альбом без пароля: входить некуда — перенаправление без cookie.
	open := e.published("")
	if r := e.login(open.id, "что-угодно"); r.status != 303 || r.hdr.Get("Set-Cookie") != "" {
		t.Fatalf("вход в открытый альбом: %d, cookie %q", r.status, r.hdr.Get("Set-Cookie"))
	}
}

func TestLoginWrongNoCookie(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	for name, body := range map[string]string{
		"неверный": "password=" + "Не-тот-пароль-77", "пустой": "password=", "нет поля": "other=1", "пустое тело": "",
		"мусор": "%zz=%", "почти верный": "password=" + loginPW + "x", "регистр": "password=" + strings.ToLower(loginPW),
		"два поля": "password=1&password=2", "слишком короткий": "password=abc",
	} {
		r := e.do("POST", "/a/"+f.id+"/login", strings.NewReader(body))
		if r.status != http.StatusUnauthorized || r.hdr.Get("Set-Cookie") != "" || !strings.Contains(string(r.body), "Неверный пароль") {
			t.Errorf("%s: %d, cookie %q", name, r.status, r.hdr.Get("Set-Cookie"))
		}
		if strings.Contains(string(r.body), "Не-тот-пароль") || strings.Contains(string(r.body), "Секретное название") {
			t.Errorf("%s: в ответе пароль или название", name)
		}
		if r.hdr.Get("Cache-Control") != "no-store" || r.hdr.Get("Location") != "" {
			t.Errorf("%s: Cache-Control %q, Location %q", name, r.hdr.Get("Cache-Control"), r.hdr.Get("Location"))
		}
		e.clk.advance(time.Hour) // не упираться в лимит попыток
	}
	// Пароль с пробелами по краям и в другой нормализации — тот же пароль (TrimSpace, NFC).
	if r := e.login(f.id, "  "+loginPW+"\u00a0 "); r.status != 303 {
		t.Errorf("пробелы по краям: %d", r.status)
	}
}

// Пустой пароль, форма без поля и мусор засчитываются как попытки: после пяти — 429 даже с верным паролем.
func TestLoginEmptyCounted(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	for i, body := range []string{"password=", "", "x=1", "%zz", "password="} {
		if r := e.do("POST", "/a/"+f.id+"/login", strings.NewReader(body)); r.status != 401 {
			t.Fatalf("попытка %d: %d", i+1, r.status)
		}
	}
	if r := e.login(f.id, loginPW); r.status != http.StatusTooManyRequests || r.hdr.Get("Set-Cookie") != "" {
		t.Fatalf("шестая попытка с верным паролем: %d", r.status)
	}
}

func TestLogin429(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	for i := range 5 {
		if r := e.login(f.id, "неверный-"+strconv.Itoa(i)); r.status != 401 {
			t.Fatalf("попытка %d: %d", i+1, r.status)
		}
	}
	r := e.login(f.id, loginPW) // верный пароль не обходит предел и не подсказывает, верен ли он
	if r.status != 429 || r.hdr.Get("Retry-After") != strconv.Itoa(15*60) || !strings.Contains(string(r.body), "Слишком много попыток") {
		t.Fatalf("429: %d, Retry-After %q", r.status, r.hdr.Get("Retry-After"))
	}
	if r.hdr.Get("Cache-Control") != "no-store" || r.hdr.Get("Set-Cookie") != "" {
		t.Fatalf("429: Cache-Control %q, cookie %q", r.hdr.Get("Cache-Control"), r.hdr.Get("Set-Cookie"))
	}
	// Тот же предел не мешает другому адресу и другому альбому.
	if r := e.login(f.id, loginPW, fromIP("198.51.100.9")); r.status != 303 {
		t.Fatalf("другой адрес: %d", r.status)
	}
	g := e.published(loginPW)
	if r := e.login(g.id, loginPW); r.status != 303 {
		t.Fatalf("другой альбом: %d", r.status)
	}
	// Окно прошло — снова можно; верный вход сбрасывает счёт «альбом+IP».
	e.clk.advance(15*time.Minute + time.Second)
	for i := range 4 {
		if r := e.login(f.id, "опять-неверный-"+strconv.Itoa(i)); r.status != 401 {
			t.Fatalf("после окна, попытка %d: %d", i+1, r.status)
		}
	}
	if r := e.login(f.id, loginPW); r.status != 303 {
		t.Fatalf("верный пароль пятым: %d", r.status)
	}
	for i := range 5 {
		if r := e.login(f.id, "после-успеха-"+strconv.Itoa(i)); r.status != 401 {
			t.Fatalf("после успеха попытка %d: %d (счёт не сброшен)", i+1, r.status)
		}
	}
	// Предел по альбому: разные адреса не обходят его (20 за сутки).
	h := e.published(loginPW)
	limited := 0
	for i := range 30 {
		r := e.login(h.id, "перебор", fromIP("192.0.2."+strconv.Itoa(i+1)))
		if r.status == 429 {
			limited++
		}
	}
	if limited != 10 {
		t.Fatalf("429 при переборе с 30 адресов: %d, нужно 10 (предел 20)", limited)
	}
	// 429 не раскрывает больше, чем 401: тело — форма той же страницы.
	if r := e.login(h.id, loginPW, fromIP("192.0.2.200")); r.status != 429 {
		t.Fatalf("верный пароль при исчерпанном пределе альбома: %d", r.status)
	}
}

// Параллельный перебор не проходит за предел и не гоняется под -race.
func TestLoginConcurrent(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	var ok, limited atomic.Int32
	done := make(chan struct{})
	for range 40 {
		go func() {
			defer func() { done <- struct{}{} }()
			switch r := e.login(f.id, "перебор"); r.status {
			case 401:
				ok.Add(1)
			case 429:
				limited.Add(1)
			}
		}()
	}
	for range 40 {
		<-done
	}
	if ok.Load() != 5 || limited.Load() != 35 {
		t.Fatalf("неверных принято %d, отказов %d (ждали 5 и 35)", ok.Load(), limited.Load())
	}
}

func TestLoginNoClientIP503(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	for name, opts := range map[string][]reqOpt{
		"нет заголовка":      {noIP()},
		"пустой":             {fromIP("")},
		"не адрес":           {fromIP("не-адрес")},
		"адрес с портом":     {fromIP("203.0.113.7:5555")},
		"только X-Forwarded": {noIP(), withHeader("X-Forwarded-For", "203.0.113.7")},
		"только X-Real-IP":   {noIP(), withHeader("X-Real-IP", "203.0.113.7")},
		"только Forwarded":   {noIP(), withHeader("Forwarded", "for=203.0.113.7")},
		"два значения": {func(r *http.Request) {
			r.Header.Add(ClientIPHeader, "203.0.113.7")
			r.Header.Add(ClientIPHeader, "203.0.113.8")
		}},
	} {
		r := e.login(f.id, loginPW, opts...)
		if r.status != http.StatusServiceUnavailable || r.hdr.Get("Set-Cookie") != "" || r.hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d", name, r.status)
		}
	}
	if !strings.Contains(e.log.String(), `"code":"no_client_ip"`) || !strings.Contains(e.log.String(), `"level":"ERROR"`) {
		t.Fatalf("строки журнала про отсутствие адреса нет: %s", e.log.String())
	}
	// 503 не занимает попыток: общий ключ «без адреса» не создан, другие входы не затронуты.
	for range 10 {
		e.login(f.id, "x", noIP())
	}
	if r := e.login(f.id, loginPW); r.status != 303 {
		t.Fatalf("после 503: %d", r.status)
	}
	// Подмена не действует: чужой X-Forwarded-For не создаёт другой ключ. Перебор с одним настоящим адресом
	// и разными подделками упирается в тот же предел.
	g := e.published(loginPW)
	for i := range 5 {
		e.login(g.id, "x", withHeader("X-Forwarded-For", "192.0.2."+strconv.Itoa(i+1)), withHeader("Forwarded", "for=192.0.2.9"))
	}
	if r := e.login(g.id, "x", withHeader("X-Forwarded-For", "192.0.2.77")); r.status != 429 {
		t.Fatalf("подмена X-Forwarded-For обошла предел: %d", r.status)
	}
	// IPv4 в виде ::ffff:a.b.c.d — тот же ключ, что и a.b.c.d.
	if r := e.login(g.id, "x", fromIP("::ffff:"+clientIP)); r.status != 429 {
		t.Fatalf("::ffff:a.b.c.d обошёл предел: %d", r.status)
	}
}

// TCP-вариант: заголовок принимается только от доверенного прокси.
func TestLoginTrustedProxy(t *testing.T) {
	e := newEnv(t, func(o *envOpts) { o.trusted = "127.0.0.1" })
	f := e.published(loginPW)
	from := func(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }
	if r := e.login(f.id, loginPW, from("127.0.0.1:40000")); r.status != 303 {
		t.Fatalf("от доверенного прокси: %d", r.status)
	}
	for _, addr := range []string{"127.0.0.2:40000", "203.0.113.7:40000", "[::1]:40000", "не-адрес", ""} {
		if r := e.login(f.id, loginPW, from(addr)); r.status != 503 {
			t.Errorf("от %q: %d, нужно 503", addr, r.status)
		}
	}
	if r := e.login(f.id, loginPW, from("[::ffff:127.0.0.1]:40000")); r.status != 303 {
		t.Errorf("IPv4 прокси в виде ::ffff: %d", r.status)
	}
}

func TestLoginExpired404(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	ref := e.get("/a/" + ghostID + "/")
	if r := e.login(f.id, loginPW); r.status != 303 {
		t.Fatalf("до срока: %d", r.status)
	}
	e.clk.advance(48*time.Hour + time.Second)
	want404(t, ref, e.login(f.id, loginPW), "вход после срока")
	want404(t, ref, e.login(ghostID, loginPW), "вход в несуществующий")
	// Не опубликованный и удалённый — то же.
	un := e.create("Т", loginPW)
	want404(t, ref, e.login(un.id, loginPW), "вход в не опубликованный")
	// Отсутствие адреса клиента не отличает существующий альбом от несуществующего раньше времени:
	// 404 у невидимого приходит до 503.
	want404(t, ref, e.login(ghostID, loginPW, noIP()), "несуществующий без адреса")
}

func TestLoginBodyLimit(t *testing.T) {
	e := newEnv(t)
	f := e.published(loginPW)
	// Длина известна — отказ до чтения тела.
	var read atomic.Int64
	body := io.NopCloser(countingReader{r: strings.NewReader("password=" + strings.Repeat("a", 8000)), n: &read})
	req, err := http.NewRequestWithContext(t.Context(), "POST", "http://x/a/"+f.id+"/login", body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 8009
	req.Header.Set(ClientIPHeader, clientIP)
	rr := newCapture()
	e.h.ServeHTTP(rr, req)
	if rr.code != http.StatusRequestEntityTooLarge || read.Load() != 0 {
		t.Fatalf("известная длина: %d, прочитано %d байт", rr.code, read.Load())
	}
	// Длина неизвестна (chunked): режется на пределе, дальше предела не читается.
	read.Store(0)
	req, err = http.NewRequestWithContext(t.Context(), "POST", "http://x/a/"+f.id+"/login", io.NopCloser(countingReader{r: strings.NewReader("password=" + strings.Repeat("a", 100000)), n: &read}))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set(ClientIPHeader, clientIP)
	rr = newCapture()
	e.h.ServeHTTP(rr, req)
	if rr.code != http.StatusRequestEntityTooLarge || read.Load() > maxLoginBody+512 {
		t.Fatalf("без длины: %d, прочитано %d байт", rr.code, read.Load())
	}
	// Верный пароль в слишком большом теле не принимается и не засчитывается как вход.
	big := "junk=" + strings.Repeat("a", maxLoginBody) + "&password=" + loginPW
	r := e.do("POST", "/a/"+f.id+"/login", strings.NewReader(big))
	if r.status != 413 || r.hdr.Get("Set-Cookie") != "" {
		t.Fatalf("большое тело с верным паролем: %d", r.status)
	}
	// Ровно на пределе — разбирается как обычно.
	exact := "junk=" + strings.Repeat("a", maxLoginBody-len("junk=&password=")-len(loginPW)) + "&password=" + loginPW
	if len(exact) != maxLoginBody {
		t.Fatalf("тест: длина %d", len(exact))
	}
	if r := e.do("POST", "/a/"+f.id+"/login", strings.NewReader(exact)); r.status != 303 {
		t.Fatalf("тело ровно на пределе: %d", r.status)
	}
	// Слишком большие тела (пароль не читался) попытками не считаются: сколько бы их ни было, верный пароль входит.
	for i := range 8 {
		if r := e.do("POST", "/a/"+f.id+"/login", strings.NewReader(big)); r.status != 413 {
			t.Fatalf("большое тело %d: %d", i+1, r.status)
		}
	}
	if r := e.login(f.id, loginPW); r.status != 303 {
		t.Fatalf("верный пароль после больших тел: %d (они засчитаны как попытки)", r.status)
	}
	// Слишком длинный пароль в пределах тела — просто неверный (CheckPassword до NFC и хэша).
	if r := e.login(f.id, strings.Repeat("я", 300)); r.status != 401 {
		t.Fatalf("длинный пароль: %d", r.status)
	}
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// capture — минимальный ResponseWriter для запроса со своим телом.
type capture struct {
	code int
	hdr  http.Header
	body bytes.Buffer
}

func newCapture() *capture { return &capture{hdr: http.Header{}} }

func (c *capture) Header() http.Header { return c.hdr }
func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}
func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// Хэш пароля сверяется с хэшем: логин не зависит от формы записи пароля (NFC) — сквозная проверка с view.
func TestLoginPasswordNFC(t *testing.T) {
	e := newEnv(t)
	composed := "Пароль-é-ё-4411"
	decomposed := "Пароль-é-ё-4411"
	f := e.published(composed)
	if r := e.login(f.id, decomposed); r.status != 303 {
		t.Fatalf("тот же пароль в другой нормализации: %d", r.status)
	}
}
