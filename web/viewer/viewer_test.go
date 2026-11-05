package viewer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

const prefix = "/viewer/abcd1234/"

// Единственная разрешённая внешняя ссылка: «Исходный код» (AGPL-3.0 §13). Это переход по клику, не загрузка
// ресурса; форк меняет адрес в самих шаблонах.
const sourceLink = `<a href="https://github.com/IliaSA101/sosninaalbums-cloud" rel="noopener noreferrer">Исходный код</a>`

func render(t *testing.T, name string, data PageData) string {
	t.Helper()
	tpl, err := template.ParseFS(Pages, name)
	if err != nil {
		t.Fatalf("разбор %s: %v", name, err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		t.Fatalf("подстановка %s: %v", name, err)
	}
	return buf.String()
}

// Название студии приходит из конфига, но шаблон обязан экранировать его как любую строку.
var evil = `<b>x</b><script>alert(1)</script>`

func TestTemplatesEscapeAndNoPD(t *testing.T) {
	idx := render(t, "index.html", PageData{Assets: prefix, Studio: evil})
	login := render(t, "login.html", PageData{Assets: prefix, Studio: evil, Message: "Неверный пароль"})
	for name, out := range map[string]string{"index.html": idx, "login.html": login} {
		if strings.Contains(out, "<b>x</b>") || strings.Contains(out, "<script>alert") {
			t.Errorf("%s: подпись студии не экранирована", name)
		}
		if !strings.Contains(out, "&lt;b&gt;x&lt;/b&gt;") {
			t.Errorf("%s: экранированной подписи нет в странице", name)
		}
		for _, want := range []string{`name="robots" content="noindex, nofollow"`, `name="referrer" content="no-referrer"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: нет %s", name, want)
			}
		}
		if strings.Count(out, sourceLink) != 1 {
			t.Errorf("%s: ссылка «Исходный код» должна быть ровно одна", name)
		}
		rest := strings.Replace(out, sourceLink, "", 1) // остальное — без единого внешнего адреса
		for _, bad := range []string{"og:", "twitter:", "<style", "style=", "http://", "https://", "<iframe", "<img"} {
			if strings.Contains(rest, bad) {
				t.Errorf("%s: нашлось %q", name, bad)
			}
		}
	}
	if !strings.Contains(login, "Неверный пароль") || !strings.Contains(login, `method="post" action="login"`) {
		t.Error("login.html: нет сообщения или формы входа")
	}
	if strings.Contains(login, `type="text"`) || !strings.Contains(login, `autocomplete="current-password"`) {
		t.Error("login.html: поле пароля")
	}
	if !strings.Contains(idx, `<h1 id="title" class="bar-title"></h1>`) {
		t.Error("index.html: название альбома должно рисоваться скриптом, а не лежать в HTML")
	}
	// Без студии и без сообщения — страницы всё равно собираются.
	_ = render(t, "index.html", PageData{Assets: prefix})
	if out := render(t, "login.html", PageData{Assets: prefix}); strings.Contains(out, "card-error") {
		t.Error("login.html: сообщение без причины")
	}
}

var scriptTag = regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`)

func TestNoInlineScripts(t *testing.T) {
	for _, name := range []string{"index.html", "login.html", "notfound.html"} {
		out := render(t, name, PageData{Assets: prefix})
		for _, m := range scriptTag.FindAllStringSubmatch(out, -1) {
			if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
				t.Errorf("%s: встроенный скрипт %q", name, m[0])
			}
		}
		if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(out) {
			t.Errorf("%s: обработчик в атрибуте", name)
		}
	}
}

// notfound.html отдаёт и Caddy, поэтому это не шаблон и у него нет ссылок на каталог с хэшем.
func TestNotFoundStatic(t *testing.T) {
	b, err := fs.ReadFile(Pages, "notfound.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Count(s, sourceLink) != 1 {
		t.Error("notfound.html: ссылка «Исходный код» должна быть ровно одна")
	}
	rest := strings.Replace(s, sourceLink, "", 1)
	for _, bad := range []string{"{{", "<script", "<link", "<style", "src=", "href=", "http://", "https://"} {
		if strings.Contains(rest, bad) {
			t.Errorf("notfound.html: нашлось %q", bad)
		}
	}
}

var (
	importRe = regexp.MustCompile(`(?:from|import)\s*\(?\s*['"](\.[^'"]+)['"]`)
	urlRe    = regexp.MustCompile(`(?:src|href)="` + regexp.QuoteMeta(prefix) + `([^"]+)"`)
	newURLRe = regexp.MustCompile(`new URL\('([^']+)', import\.meta\.url\)`)
)

// Всё, на что ссылаются шаблоны и модули, лежит в Assets: иначе листалка при первом же открытии даст 404.
func TestAssetsReferencesExist(t *testing.T) {
	exists := func(from, ref string) {
		p := path.Join(path.Dir(from), ref)
		if _, err := fs.Stat(Assets, p); err != nil {
			t.Errorf("%s ссылается на %s: %v", from, ref, err)
		}
	}
	for _, name := range []string{"index.html", "login.html"} {
		out := render(t, name, PageData{Assets: prefix})
		for _, m := range urlRe.FindAllStringSubmatch(out, -1) {
			exists(".", m[1])
		}
	}
	err := fs.WalkDir(Assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") || strings.HasPrefix(p, "vendor/") {
			return err
		}
		b, err := fs.ReadFile(Assets, p)
		if err != nil {
			return err
		}
		for _, m := range importRe.FindAllStringSubmatch(string(b), -1) {
			exists(p, m[1])
		}
		for _, m := range newURLRe.FindAllStringSubmatch(string(b), -1) {
			exists(p, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Версия библиотеки и лицензия — как записано в vendor/SOURCE.txt; правка вендора без замены версии — провал.
func TestVendorPinned(t *testing.T) {
	src, err := fs.ReadFile(Assets, "vendor/SOURCE.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"page-flip-2.0.7.browser.js", "LICENSE-page-flip.txt", "page-flip-2.0.7.css"} {
		b, err := fs.ReadFile(Assets, "vendor/"+name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if !strings.Contains(string(src), hex.EncodeToString(sum[:])) {
			t.Errorf("vendor/%s: sha256 %x не записан в SOURCE.txt", name, sum)
		}
	}
	lic, err := fs.ReadFile(Assets, "vendor/LICENSE-page-flip.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(lic), "MIT License") {
		t.Error("лицензия StPageFlip не MIT")
	}
}

func TestAssetsHaveNoPages(t *testing.T) {
	err := fs.WalkDir(Assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".html") {
			t.Errorf("%s: шаблоны не должны раздаваться как файлы листалки", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	v1, err := Version()
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := Version()
	if v1 != v2 || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(v1) {
		t.Errorf("Version() = %q и %q: ждали постоянные 8 hex", v1, v2)
	}
}
