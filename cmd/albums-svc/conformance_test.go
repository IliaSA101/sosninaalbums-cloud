package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/IliaSA101/sosninaalbums/api/albums"
	"github.com/IliaSA101/sosninaalbums/api/albums/albumstest"
)

// TestConformance — albumstest.Conformance целиком (приём, просмотр, пароль, сброс входов, срок, удаление) на
// той же сборке, что и в работе: assemble — api и view за имитацией Caddy. Токен второго владельца включает
// проверки чужого альбома. Один прогон: тут же проверяется журнал — ни токены, ни cookie, ни пароли после
// полного прогона в него не попали.
func TestConformance(t *testing.T) {
	s := newStack(t)
	albumstest.Conformance(t, s.srv.URL, s.tok1, albumstest.WithOtherToken(s.tok2))
	out := s.log.String()
	if !strings.Contains(out, `"route":"POST /a/{id}/login"`) || !strings.Contains(out, `"route":"GET /a/{id}/{file}"`) {
		t.Fatalf("в журнале нет запросов просмотра: %.300s", out)
	}
	for name, secret := range map[string]string{"токен 1": s.tok1, "токен 2": s.tok2, "пароль": "Пароль-Conf-7391", "cookie": "__Secure-sa_session", "cookie-имя": "sa_session"} {
		if strings.Contains(out, secret) {
			t.Errorf("в журнале есть %s", name)
		}
	}
	if m := realPath.FindString(out); m != "" {
		t.Errorf("в журнале есть путь запроса: %q", m)
	}
}

// realPath — настоящий путь запроса в строке журнала: после /a/, /viewer/ или /v1/albums/ идёт не «{» шаблона
// маршрута («POST /a/{id}/login»), а что-то иное.
var realPath = regexp.MustCompile(`/(a|viewer|v1/albums)/[^{]`)

// allowedKeys — поля строки журнала (design §7); всё, что вне списка, в строку попасть не должно.
var allowedKeys = map[string]bool{
	"time": true, "level": true, "msg": true, "route": true, "status": true, "code": true, "bytes": true, "ms": true,
	"owner": true, "album": true, "op": true, "err": true, "reason": true, "count": true, "kind": true, "step": true,
}

// TestLogNoSecrets — полный прогон выгрузки и просмотра с отказом диска и паникой: ни пароль, ни cookie, ни
// токен, ни id альбома, ни название, ни адрес клиента, ни путь в журнал не попадают; id встречается только
// как метка HMAC `log/1`.
func TestLogNoSecrets(t *testing.T) {
	s := newStack(t)
	const (
		pw    = "Пароль-для-журнала-4096"
		title = "Секретное название свадьбы"
	)
	open := s.publish(s.tok1, title, "")
	locked := s.publish(s.tok1, title, pw)
	other := s.publish(s.tok2, title, "")

	form := func(id, p string) reply {
		return s.do("POST", "/a/"+id+"/login", "", strings.NewReader(url.Values{"password": {p}}.Encode()),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	}
	// Вход, просмотр с cookie, неверные пароли до 429, запросы с «секретами» в строке запроса и в заголовках.
	r := form(locked.id, pw)
	cookie := ""
	for _, c := range (&http.Response{Header: r.hdr}).Cookies() {
		cookie = c.Name + "=" + c.Value
	}
	if r.status != 303 || cookie == "" {
		t.Fatalf("вход: %d", r.status)
	}
	cookieValue := strings.SplitN(cookie, "=", 2)[1]
	for _, p := range []string{"/", "/manifest.json", "/view.json", "/" + locked.file} {
		if g := s.do("GET", "/a/"+locked.id+p+"?token="+s.tok1+"&password="+pw, "", nil, map[string]string{"Cookie": cookie, "Referer": "https://x/a/" + locked.id + "/"}); g.status != 200 {
			t.Fatalf("просмотр %s: %d", p, g.status)
		}
	}
	got429 := false
	for i := range 8 {
		if form(locked.id, fmt.Sprintf("неверный-%d-%s", i, pw[:6])).status == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("429 не наступил")
	}
	for _, tg := range []string{"/a/" + locked.id + "/../meta.json", "/a/%2e%2e/" + open.id + "/", "/a/" + open.id + "/" + strings.Repeat("0", 64) + ".jpg", "/a/qqqqqqqqqqqqqqqqqqqqqqqqqq/", "/viewer/x/y"} {
		s.do("GET", tg, "", nil, nil)
	}
	// Обновление, сброс входов, удаление чужими и своими руками.
	s.jsonReq("PATCH", "/v1/albums/"+locked.id, s.tok1, albums.Patch{RevokeSessions: true})
	s.do("DELETE", "/v1/albums/"+other.id, s.tok1, nil, nil) // чужой: 404
	s.do("DELETE", "/v1/albums/"+open.id, s.tok1, nil, map[string]string{"X-Sa-Client-IP": "198.51.100.77"})

	// Отказ диска: каталог временных файлов и каталог альбома закрыты на запись — приём страницы и правка
	// падают с 500, а в журнале причина без имён файлов.
	if os.Geteuid() == 0 {
		t.Log("root: отказ диска через права не воспроизвести, шаг пропущен")
	} else {
		albumDir := filepath.Join(s.data, "owners", "p1", "albums", locked.id)
		tmpDir := filepath.Join(s.data, "tmp")
		for _, d := range []string{tmpDir, albumDir} {
			if err := os.Chmod(d, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		page := s.do("PUT", "/v1/albums/"+locked.id+"/pages/1", s.tok1, bytes.NewReader(locked.data[0]), map[string]string{"Content-Type": albums.TypeJPEG})
		patch := s.jsonReq("PATCH", "/v1/albums/"+locked.id, s.tok1, albums.Patch{RevokeSessions: true})
		for _, d := range []string{tmpDir, albumDir} {
			if err := os.Chmod(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if page.status < 500 && patch.status < 500 {
			t.Fatalf("отказ диска не сработал: %d и %d", page.status, patch.status)
		}
	}
	// Нет места: запас не оставляет ни байта — 413, не 500.
	s.free.Store(1)
	if g := s.do("PUT", "/v1/albums/"+locked.id+"/pages/3", s.tok1, bytes.NewReader(locked.data[2]), map[string]string{"Content-Type": albums.TypeJPEG}); g.status != 413 && g.status != 204 && g.status != 404 {
		t.Fatalf("нет места: %d", g.status)
	}
	s.free.Store(0)

	// Паника в обработчиках (часы паникуют значением с секретами): 500, процесс жив, значения в журнале нет.
	boom := "паника " + locked.id + " " + pw + " " + s.tok1
	s.panicNow.Store(&boom)
	for _, req := range [][3]string{{"GET", "/a/" + locked.id + "/", ""}, {"POST", "/a/" + locked.id + "/login", pw}, {"GET", "/v1/albums", s.tok1}, {"GET", "/a/" + other.id + "/manifest.json", ""}} {
		tok := ""
		if strings.HasPrefix(req[1], "/v1/") {
			tok = req[2]
		}
		g := s.do(req[0], req[1], tok, strings.NewReader("password="+pw), nil)
		if g.status != 500 {
			t.Fatalf("паника %s: %d", req[0], g.status)
		}
		if bytes.Contains(g.body, []byte(locked.id)) || bytes.Contains(g.body, []byte(pw)) {
			t.Fatal("значение паники в ответе")
		}
	}
	s.panicNow.Store(nil)
	if g := s.do("GET", "/v1/health", "", nil, nil); g.status != 200 {
		t.Fatalf("после паники: %d", g.status)
	}

	out := s.log.String()
	secrets := map[string]string{
		"токен 1": s.tok1, "токен 2": s.tok2, "пароль": pw, "часть пароля": pw[:12], "cookie": cookieValue,
		"подпись cookie": cookieValue[strings.LastIndex(cookieValue, ".")+1:], "имя cookie": "sa_session",
		"название": title, "каталог данных": s.data, "127.0.0.1": "127.0.0.1", "заявленный адрес": "198.51.100.77",
		"строка запроса": "token=",
		"имя файла":      locked.file[:16], "Authorization": "Bearer",
	}
	for _, u := range []uploaded{open, locked, other} {
		secrets["id "+u.id[:4]] = u.id
		secrets["префикс id "+u.id[:4]] = u.id[:10]
		secrets["файл "+u.file[:6]] = u.file
	}
	for name, v := range secrets {
		if strings.Contains(out, v) {
			t.Errorf("в журнале есть %s", name)
		}
	}
	if m := realPath.FindString(out); m != "" {
		t.Errorf("в журнале есть путь запроса: %q", m)
	}
	// Каждая строка — JSON с разрешёнными полями; id — только метка из HMAC.
	tags := map[string]bool{}
	for _, u := range []uploaded{open, locked, other} {
		tags[s.tagger.Tag(u.id)] = true
	}
	seenTag := false
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("строка журнала не JSON: %.80q", line)
		}
		for k, v := range m {
			if !allowedKeys[k] {
				t.Errorf("в журнале поле %q", k)
			}
			if k == "album" {
				tag, _ := v.(string)
				if len(tag) != 8 {
					t.Errorf("метка альбома %q", tag)
				}
				if tags[tag] {
					seenTag = true
				}
			}
		}
	}
	if !seenTag {
		t.Error("метки HMAC альбомов в журнале нет: проверка id без контрольного случая")
	}
	for _, want := range []string{`"status":500`, `"code":"login_limited"`, `"code":"login_failed"`, `"status":401`, `"status":404`} {
		if !strings.Contains(out, want) {
			t.Errorf("в журнале нет %s", want)
		}
	}
}
