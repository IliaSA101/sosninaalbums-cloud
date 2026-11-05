package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// metaRaw — meta.json «будущей» версии сервиса: известные поля плюс неизвестные на трёх уровнях.
func metaRaw(owner, id string, extra string) string {
	return `{"format":1,"id":"` + id + `","owner":"` + owner + `",
"created":"2026-10-31T12:00:00Z","updated":"2026-10-31T12:00:00Z","expires":"2026-11-01T12:00:00Z",
"published":{"at":"2026-10-31T12:00:00Z","manifest":{"schema":1,"generator":"g","title":"T","book":{"cover":false,"page_mm":{"w":200,"h":200}},"pages":[]},"revision_note":"из будущего"},
"draft":null,
"password":{"alg":"hmac-sha256","key":"pw/1","salt":"c2FsdHNhbHRzYWx0c2FsdA","hash":"aGFzaA","kdf_cost":7},
"epoch":3,"views":{"total":5,"last":"2026-10-31T12:00:00Z"}` + extra + `}`
}

func TestMetaUnknownFieldsKept(t *testing.T) {
	e := open(t)
	rel := "owners/p1/albums/" + idA + "/meta.json"
	e.write(t, rel, metaRaw("p1", idA, `,"новое_поле":[1,2,{"x":null}]`))
	m, err := e.ReadMeta("p1", idA)
	if err != nil {
		t.Fatal(err)
	}
	if m.Epoch != 3 || m.Published == nil || m.Draft != nil || m.Password == nil || m.Password.Key != "pw/1" {
		t.Fatalf("известные поля: %+v", m)
	}
	m.Epoch++
	m.Updated = m.Updated.Add(time.Hour)
	if err := e.WriteMeta(m); err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.read(t, rel)), &back); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"views": `{"total":5,"last":"2026-10-31T12:00:00Z"}`, "новое_поле": `[1,2,{"x":null}]`, "epoch": "4", "draft": "null",
	} {
		if got := strings.Join(strings.Fields(string(back[k])), ""); got != want {
			t.Fatalf("поле %s: %s, ждали %s", k, got, want)
		}
	}
	var pub, pw map[string]json.RawMessage
	if err := json.Unmarshal(back["published"], &pub); err != nil || string(pub["revision_note"]) != `"из будущего"` {
		t.Fatalf("неизвестное поле внутри published потеряно: %s", back["published"])
	}
	if err := json.Unmarshal(back["password"], &pw); err != nil || string(pw["kdf_cost"]) != "7" {
		t.Fatalf("неизвестное поле внутри password потеряно: %s", back["password"])
	}
	// Второй круг чтения и записи ничего не теряет и ничего не дублирует.
	m2, err := e.ReadMeta("p1", idA)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.WriteMeta(m2); err != nil {
		t.Fatal(err)
	}
	if strings.Count(e.read(t, rel), "kdf_cost") != 1 || strings.Count(e.read(t, rel), `"epoch"`) != 1 {
		t.Fatalf("поля задвоились: %s", e.read(t, rel))
	}
}

// Ключ, совпадающий с известным без учёта регистра (encoding/json их сопоставляет), не становится
// «неизвестным полем»: после записи двойника нет, и значение читается так же.
func TestMetaUnknownKeyCaseDoesNotShadow(t *testing.T) {
	e := open(t)
	rel := "owners/p1/albums/" + idA + "/meta.json"
	e.write(t, rel, metaRaw("p1", idA, `,"EPOCH":99`))
	m, err := e.ReadMeta("p1", idA)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.WriteMeta(m); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.ToLower(e.read(t, rel)), `"epoch"`); n != 1 {
		t.Fatalf("ключ epoch записан %d раз: %s", n, e.read(t, rel))
	}
	m2, err := e.ReadMeta("p1", idA)
	if err != nil || m2.Epoch != m.Epoch {
		t.Fatalf("epoch после круга: %d != %d (%v)", m2.Epoch, m.Epoch, err)
	}
}

func TestMetaNewerFormatRefuses(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	e.write(t, "owners/p1/albums/"+idB+"/meta.json", strings.Replace(metaRaw("p1", idB, ""), `"format":1`, `"format":2`, 1))
	_, err := e.ReadMeta("p1", idB)
	if !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("ReadMeta format 2: %v", err)
	}
	if _, err := e.LoadAll(); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("LoadAll должен отказать: %v", err)
	}
	// Отказ — не чистка: ничего не перенесено и не удалено.
	if !e.exists("owners/p1/albums/"+idB+"/meta.json") || !e.exists("owners/p1/albums/"+idA+"/meta.json") {
		t.Fatal("отказ по формату тронул каталоги")
	}
	if len(e.names(t, "quarantine")) != 0 {
		t.Fatal("новый формат ушёл в карантин")
	}
}

func TestMetaQuarantine(t *testing.T) {
	e := open(t)
	good := e.create(t, "p1", idA)
	addPage(t, e.Store, "p1", idA, []byte("целая страница"))
	idD := "dddddddddddddddddddddddddd"
	idE := "eeeeeeeeeeeeeeeeeeeeeeeeee"
	idF := strings.Repeat("f", 26)
	idG := "gggggggggggggggggggggggggg"
	cases := map[string]string{
		idB: "{ это не JSON, а секретное-слово",
		idC: metaRaw("p1", idD, ""),                                                 // id не сошёлся с путём
		idD: metaRaw("p2", idD, ""),                                                 // владелец не сошёлся
		idE: strings.Replace(metaRaw("p1", idE, ""), `"format":1`, `"format":0`, 1), // формата нет
		idF: `[1,2,3]`,
		idG: strings.Repeat(" ", maxMetaSize+10), // больше предела
	}
	for id, raw := range cases {
		e.write(t, "owners/p1/albums/"+id+"/meta.json", raw)
		e.write(t, "owners/p1/albums/"+id+"/"+pageName([]byte(id)), "страница "+id)
	}
	got, err := e.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("загружено %d альбомов, ждали один исправный", len(got))
	}
	q := e.names(t, "quarantine")
	if len(q) != len(cases) {
		t.Fatalf("в карантине %v, ждали %d каталогов", q, len(cases))
	}
	for id := range cases {
		if e.exists("owners/p1/albums/" + id) {
			t.Fatalf("негодный альбом %s остался на месте", id[:3])
		}
		var found bool
		for _, n := range q {
			if strings.HasPrefix(n, id+".") && len(n) == 26+1+16 {
				found = true
				if !e.exists("quarantine/" + n + "/" + pageName([]byte(id))) {
					t.Fatalf("страницы в карантине потеряны (%s)", id[:3])
				}
			}
		}
		if !found {
			t.Fatalf("каталог %s не в карантине: %v", id[:3], q)
		}
	}
	log := e.log.String()
	mustNotContain(t, log, idB, idC, idD, idE, idF, idG, "секретное-слово", e.dir)
	if strings.Count(log, "карантин") != len(cases) {
		t.Fatalf("в журнале ждали %d строк о карантине:\n%s", len(cases), log)
	}
	// Исправный альбом цел, повторный LoadAll ничего нового не делает.
	again, err := e.LoadAll()
	if err != nil || len(again) != 1 || len(e.names(t, "quarantine")) != len(cases) {
		t.Fatalf("повторный LoadAll: %d %v", len(again), err)
	}
}

func TestLoadAllSkipsStrangeNames(t *testing.T) {
	e := open(t)
	e.create(t, "p1", idA)
	e.write(t, "owners/p1/albums/not-an-id/meta.json", metaRaw("p1", "not-an-id", ""))
	e.write(t, "owners/Bad_Owner/albums/"+idB+"/meta.json", metaRaw("Bad_Owner", idB, ""))
	e.write(t, "owners/p1/albums/file.txt", "файл, не каталог")
	got, err := e.LoadAll()
	if err != nil || len(got) != 1 {
		t.Fatalf("LoadAll: %d %v", len(got), err)
	}
	if !e.exists("owners/p1/albums/not-an-id") || !e.exists("owners/Bad_Owner") {
		t.Fatal("посторонние имена не должны ни удаляться, ни переноситься")
	}
	if _, err := os.Stat(e.abs("quarantine")); err != nil || len(e.names(t, "quarantine")) != 0 {
		t.Fatal("посторонние имена попали в карантин")
	}
}

func TestLoadAllEmptyAndOrder(t *testing.T) {
	e := open(t)
	if got, err := e.LoadAll(); err != nil || len(got) != 0 {
		t.Fatalf("пустое хранилище: %v %v", got, err)
	}
	e.create(t, "p2", idC)
	e.create(t, "p1", idB)
	e.create(t, "p1", idA)
	got, err := e.LoadAll()
	if err != nil || len(got) != 3 {
		t.Fatal(err, len(got))
	}
	if got[0].ID != idA || got[1].ID != idB || got[2].ID != idC {
		t.Fatalf("порядок по владельцу и id: %s %s %s", got[0].ID, got[1].ID, got[2].ID)
	}
	if _, err := e.ReadMeta("p1", idC); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("чужой каталог владельца: %v", err)
	}
}
