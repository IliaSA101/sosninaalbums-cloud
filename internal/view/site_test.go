package view

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const siteURL = "https://studio.example/albums/2026"

// withOwners пересобирает обработчик с другим списком владельцев.
func (e *env) withOwners(owners map[string]Owner) http.Handler {
	e.t.Helper()
	e.opt.Owners = owners
	h, err := New(e.svc, e.pw, e.ck, e.h.lim, e.opt)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

// view.json (005-2, 005-5): с сайтом — {"studio","site"}; без — байты ровно как в v0.1.1; чужой владелец и
// владелец вне конфига сайта не получают. В HTML сайта нет нигде: ссылку строит листалка, а не шаблон.
func TestViewJSONSite(t *testing.T) {
	e := newEnv(t)
	const pw = "Пароль-для-сайта-7731"
	open, locked := e.published(""), e.published(pw)
	ck := withCookie(e.signIn(locked.id, pw))

	with := e.withOwners(map[string]Owner{owner: {Studio: studioName, Site: siteURL}})
	want := `{"studio":"` + studioName + `","site":"` + siteURL + `"}`
	if r := e.doTo(with, "GET", "/a/"+open.id+"/view.json", nil); r.status != 200 || string(r.body) != want {
		t.Fatalf("view.json с сайтом: %d %s", r.status, r.body)
	}
	if r := e.doTo(with, "GET", "/a/"+locked.id+"/view.json", nil, ck); r.status != 200 || string(r.body) != want {
		t.Fatalf("view.json закрытого альбома с входом: %d %s", r.status, r.body)
	}
	if r := e.doTo(with, "GET", "/a/"+locked.id+"/view.json", nil); r.status != 401 || strings.Contains(string(r.body), "studio") {
		t.Fatalf("view.json без входа: %d %q", r.status, r.body)
	}

	// Сайта нет ни в одной HTML-странице: индекс, форма входа, «неверный пароль», страница с входом.
	for name, r := range map[string]response{
		"индекс":        e.doTo(with, "GET", "/a/"+open.id+"/", nil),
		"форма":         e.doTo(with, "GET", "/a/"+locked.id+"/", nil),
		"неверный":      e.doTo(with, "POST", "/a/"+locked.id+"/login", strings.NewReader("password=x")),
		"страница вход": e.doTo(with, "GET", "/a/"+locked.id+"/", nil, ck),
	} {
		if r.status == 0 || strings.Contains(string(r.body), "studio.example") {
			t.Errorf("%s: сайт студии в HTML (%d)", name, r.status)
		}
	}

	// Без сайта — ровно прежние байты; владелец вне конфига — пустая подпись и без сайта (чужой не подставляется).
	without := e.withOwners(map[string]Owner{owner: {Studio: studioName}})
	if r := e.doTo(without, "GET", "/a/"+open.id+"/view.json", nil); string(r.body) != `{"studio":"`+studioName+`"}` {
		t.Fatalf("view.json без сайта: %s", r.body)
	}
	anon := e.withOwners(map[string]Owner{"p9": {Studio: "Чужая", Site: "https://chuzhaya.example"}})
	for name, r := range map[string]response{
		"view.json": e.doTo(anon, "GET", "/a/"+open.id+"/view.json", nil),
		"индекс":    e.doTo(anon, "GET", "/a/"+open.id+"/", nil),
		"форма":     e.doTo(anon, "GET", "/a/"+locked.id+"/", nil),
	} {
		if strings.Contains(string(r.body), "chuzhaya") || strings.Contains(string(r.body), "Чужая") {
			t.Errorf("%s: данные чужого владельца", name)
		}
	}
	if r := e.doTo(anon, "GET", "/a/"+open.id+"/view.json", nil); string(r.body) != `{"studio":""}` {
		t.Fatalf("view.json владельца вне конфига: %s", r.body)
	}

	// Подпись и сайт кодируются JSON-ом как данные: разбор возвращает значения в точности.
	var vj struct{ Studio, Site string }
	r := e.doTo(with, "GET", "/a/"+open.id+"/view.json", nil)
	if err := json.Unmarshal(r.body, &vj); err != nil || vj.Studio != studioName || vj.Site != siteURL {
		t.Fatalf("разбор view.json: %v %+v", err, vj)
	}
}
