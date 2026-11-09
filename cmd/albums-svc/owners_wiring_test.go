package main

import (
	"strings"
	"testing"
)

// Проводка владельцев (serve.go → view.Options.Owners): подпись и сайт из конфига доходят до view.json своего
// владельца и не доходят до чужого; владелец без сайта получает прежние байты. Сборка — assemble, та же, что
// вызывает serve.
func TestServeOwnersWiring(t *testing.T) {
	const site = "https://studio.example/albums"
	s := newStack(t, func(owners []any) {
		owners[0].(map[string]any)["site"] = site // у второго владельца сайта нет
	})
	one := s.publish(s.tok1, "Первый", "")
	two := s.publish(s.tok2, "Второй", "")

	r := s.do("GET", "/a/"+one.id+"/view.json", "", nil, nil)
	if r.status != 200 || string(r.body) != `{"studio":"Студия Один","site":"`+site+`"}` {
		t.Fatalf("view.json владельца с сайтом: %d %s", r.status, r.body)
	}
	r = s.do("GET", "/a/"+two.id+"/view.json", "", nil, nil)
	if r.status != 200 || string(r.body) != `{"studio":"Студия Два"}` {
		t.Fatalf("view.json владельца без сайта: %d %s", r.status, r.body)
	}
	// Сайта нет в HTML страницы альбома: ссылку строит листалка.
	if r = s.do("GET", "/a/"+one.id+"/", "", nil, nil); r.status != 200 || strings.Contains(string(r.body), "studio.example") {
		t.Fatalf("страница альбома: %d, сайт в HTML: %v", r.status, strings.Contains(string(r.body), "studio.example"))
	}
}
