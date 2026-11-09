package album

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

var (
	d1, d2, d3, d4 = blob(1, 400), blob(2, 400), blob(3, 400), blob(4, 400)
	idRE           = regexp.MustCompile(`^[a-z2-7]{26}$`)
)

func TestCreateMissing(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("Свадьба", d1, nil, d3, d4), "")
	if !reflect.DeepEqual(cr.Missing, []int{1, 3, 4}) {
		t.Fatalf("missing при создании: %v", cr.Missing)
	}
	if cr.URL != "https://albums.test/a/"+cr.ID+"/" {
		t.Fatalf("ссылка: %q", cr.URL)
	}
	if want := t0.Add(48 * time.Hour); !cr.ExpiresAt.Equal(want) {
		t.Fatalf("срок: %v, ждали %v", cr.ExpiresAt, want)
	}
	// До publish на /a/ — ничего: опубликованной редакции нет.
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("до publish Access: %v", err)
	}
	if _, err := e.svc.PublishedManifest(cr.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("до publish манифест: %v", err)
	}
	e.mustPut(pOne, cr.ID, d1)
	missing, err := e.svc.Update(pOne, cr.ID, book("Свадьба", d1, nil, d3, d4))
	if err != nil || !reflect.DeepEqual(missing, []int{3, 4}) {
		t.Fatalf("missing после одной страницы: %v, %v", missing, err)
	}
	m, err := e.st.ReadMeta(pOne, cr.ID)
	if err != nil || m.Draft == nil || m.Published != nil || m.Owner != pOne {
		t.Fatalf("meta на диске: %+v, %v", m, err)
	}
	for _, p := range m.Draft.Manifest.Pages {
		if !p.Blank && p.File != p.SHA256+".jpg" {
			t.Fatalf("имя файла даёт сервис: %q", p.File)
		}
	}
}

func TestIDFormat(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for range 300 {
		id, err := e.svc.newID()
		if err != nil {
			t.Fatal(err)
		}
		if !idRE.MatchString(id) || albums.CheckID(id) != nil {
			t.Fatalf("id вне формата: %q", id)
		}
		if seen[id] {
			t.Fatalf("id повторился: %q", id)
		}
		seen[id] = true
	}
}

// scripted выдаёт заранее заданные куски байт, затем случайные: проверка розыгрыша при совпадении id.
type scripted struct {
	chunks [][]byte
	rest   io.Reader
}

func (s *scripted) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return s.rest.Read(p)
	}
	n := copy(p, s.chunks[0])
	s.chunks = s.chunks[1:]
	return n, nil
}

func TestIDCollisionRedraw(t *testing.T) {
	zero := make([]byte, idLen)
	seq := make([]byte, idLen)
	for i := range seq {
		seq[i] = byte(i + 1)
	}
	e := newEnv(t, func(_ *Config, o *Options) {
		o.Rand = &scripted{chunks: [][]byte{zero, zero, zero, seq}, rest: bytes.NewReader(bytes.Repeat([]byte{7}, 1<<12))}
	})
	first := e.create(pOne, book("A", d1, d2), "")
	if first.ID != strings.Repeat("a", idLen) {
		t.Fatalf("первый id: %q", first.ID)
	}
	second := e.create(pOne, book("B", d1, d2), "") // два розыгрыша дают занятый id
	if second.ID == first.ID || !idRE.MatchString(second.ID) {
		t.Fatalf("второй id: %q", second.ID)
	}
}

func TestUpdateKeepsPublished(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("Версия 1", d1, d2), d1, d2)
	before, err := e.svc.PublishedManifest(cr.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := e.svc.Update(pOne, cr.ID, book("Версия 2", d1, d3))
	if err != nil || !reflect.DeepEqual(missing, []int{2}) {
		t.Fatalf("Update: %v, %v", missing, err)
	}
	after, err := e.svc.PublishedManifest(cr.ID, nil)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("до publish клиент видит прежнюю редакцию: %s, %v", after, err)
	}
	if _, err := e.svc.Access(cr.ID); err != nil {
		t.Fatalf("альбом с правкой остаётся доступным: %v", err)
	}
	e.mustPut(pOne, cr.ID, d3)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	now, _ := e.svc.PublishedManifest(cr.ID, nil)
	if !strings.Contains(string(now), "Версия 2") || bytes.Equal(before, now) {
		t.Fatalf("после publish — новая редакция: %s", now)
	}
}

func TestPublishPagesMissing(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("A", d1, nil, d3, d4), "")
	e.mustPut(pOne, cr.ID, d1)
	_, err := e.svc.Publish(pOne, cr.ID)
	var me *MissingError
	if !errors.As(err, &me) || !reflect.DeepEqual(me.Missing, []int{3, 4}) {
		t.Fatalf("publish без страниц: %v", err)
	}
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("неудавшийся publish не открывает альбом")
	}
	e.mustPut(pOne, cr.ID, d3, d4)
	for i := range 2 { // повторный publish без правки — тоже успех
		if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
			t.Fatalf("publish #%d: %v", i, err)
		}
	}
	m, _ := e.st.ReadMeta(pOne, cr.ID)
	if m.Published == nil || m.Draft != nil {
		t.Fatalf("после publish: published есть, draft нет: %+v", m)
	}
}

func TestPublishRemovesOld(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	// Правка: страница 2 заменена, d3 загружена, но затем правка заменена снова — d3 никому не нужна.
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d3)); err != nil {
		t.Fatal(err)
	}
	e.mustPut(pOne, cr.ID, d3)
	if !e.hasFile(pOne, cr.ID, d2) || !e.hasFile(pOne, cr.ID, d3) {
		t.Fatalf("published ∪ draft хранятся: %v", e.files(pOne, cr.ID))
	}
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d4)); err != nil {
		t.Fatal(err)
	}
	if e.hasFile(pOne, cr.ID, d3) || !e.hasFile(pOne, cr.ID, d2) {
		t.Fatalf("замена правки удаляет файлы прежней правки, но не published: %v", e.files(pOne, cr.ID))
	}
	e.mustPut(pOne, cr.ID, d4)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	if e.hasFile(pOne, cr.ID, d2) || !e.hasFile(pOne, cr.ID, d1) || !e.hasFile(pOne, cr.ID, d4) {
		t.Fatalf("после publish файлов прежней редакции нет: %v", e.files(pOne, cr.ID))
	}
}

func TestExpiryClamp(t *testing.T) {
	e := newEnv(t)
	limit := t0.Add(366 * 24 * time.Hour)
	cr, err := e.svc.Create(pOne, &albums.CreateRequest{Manifest: book("A", d1, d2), ExpiresAt: t0.Add(1000 * 24 * time.Hour)})
	if err != nil || !cr.ExpiresAt.Equal(limit) {
		t.Fatalf("срок зажат к max_days: %v, %v", cr.ExpiresAt, err)
	}
	soon := t0.Add(72*time.Hour + 500*time.Millisecond)
	a, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{ExpiresAt: &soon})
	if err != nil || !a.ExpiresAt.Equal(soon.Truncate(time.Second)) {
		t.Fatalf("PATCH срока: %v, %v", a.ExpiresAt, err)
	}
	far := t0.Add(5000 * 24 * time.Hour)
	if a, err = e.svc.Patch(pOne, cr.ID, &albums.Patch{ExpiresAt: &far}); err != nil || !a.ExpiresAt.Equal(limit) {
		t.Fatalf("PATCH зажат: %v, %v", a.ExpiresAt, err)
	}
	past := t0.Add(-time.Hour)
	if _, err = e.svc.Patch(pOne, cr.ID, &albums.Patch{ExpiresAt: &past}); err == nil {
		t.Fatal("срок в прошлом принят")
	}
}

func TestExpired404BeforeSweep(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	e.clk.advance(49 * time.Hour) // срок (48 ч) истёк, уборки ещё не было
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Access после срока: %v", err)
	}
	if _, err := e.svc.OpenPublished(cr.ID, fileOf(page(1, d1)), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("файл после срока: %v", err)
	}
	if got := e.svc.List(pOne); len(got) != 0 {
		t.Fatalf("истёкший в списке: %+v", got)
	}
	for name, err := range map[string]error{
		"Update":  func() error { _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d2)); return err }(),
		"Publish": func() error { _, err := e.svc.Publish(pOne, cr.ID); return err }(),
		"Delete":  e.svc.Delete(pOne, cr.ID),
		"Pages":   func() error { _, err := e.svc.CurrentPages(pOne, cr.ID); return err }(),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s истёкшего: %v", name, err)
		}
	}
	if len(e.files(pOne, cr.ID)) != 2 || !e.inIndex(cr.ID) {
		t.Fatal("файлы удаляет уборка, а не запрос")
	}
	st, err := e.svc.Sweep()
	if err != nil || st.Expired != 1 || e.inIndex(cr.ID) {
		t.Fatalf("уборка: %+v, %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "owners", pOne, "albums", cr.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("каталог после уборки: %v", err)
	}
}

func TestUnpublishedTTL(t *testing.T) {
	e := newEnv(t)
	never := e.create(pOne, book("Брошен", d1, d2), "")
	pub := e.published(pOne, book("Жив", d3, d4), d3, d4)
	e.clk.advance(23 * time.Hour)
	if st, err := e.svc.Sweep(); err != nil || st.Abandoned != 0 {
		t.Fatalf("через 23 ч уборка ничего не трогает: %+v, %v", st, err)
	}
	e.clk.advance(2 * time.Hour)
	e.cfgLongExpiry(pub.ID)
	st, err := e.svc.Sweep()
	if err != nil || st.Abandoned != 1 || e.inIndex(never.ID) {
		t.Fatalf("ни разу не опубликованный через сутки удалён: %+v, %v", st, err)
	}
	if !e.inIndex(pub.ID) {
		t.Fatal("опубликованный остаётся")
	}
}

// cfgLongExpiry продлевает альбом, чтобы срок не мешал проверке брошенных выгрузок.
func (e *env) cfgLongExpiry(id string) {
	e.t.Helper()
	far := e.clk.Wall().Add(100 * 24 * time.Hour)
	if _, err := e.svc.Patch(pOne, id, &albums.Patch{ExpiresAt: &far}); err != nil {
		e.t.Fatal(err)
	}
}

func TestDraftTTLFromFirstEdit(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	e.cfgLongExpiry(cr.ID)
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d3)); err != nil { // первая правка: t0
		t.Fatal(err)
	}
	e.mustPut(pOne, cr.ID, d3)
	e.clk.advance(20 * time.Hour)
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d4)); err != nil { // замена: время первой правки не обновляется
		t.Fatal(err)
	}
	e.mustPut(pOne, cr.ID, d4)
	m, _ := e.st.ReadMeta(pOne, cr.ID)
	if !m.Draft.At.Equal(t0) {
		t.Fatalf("draft.at — время первой правки: %v", m.Draft.At)
	}
	e.clk.advance(5 * time.Hour) // 25 ч от первой правки, 5 ч от последней
	st, err := e.svc.Sweep()
	if err != nil || st.Drafts != 1 {
		t.Fatalf("уборка правки: %+v, %v", st, err)
	}
	m, _ = e.st.ReadMeta(pOne, cr.ID)
	if m.Draft != nil || m.Published == nil {
		t.Fatalf("правка отброшена, опубликованная редакция осталась: %+v", m)
	}
	if e.hasFile(pOne, cr.ID, d4) || !e.hasFile(pOne, cr.ID, d2) {
		t.Fatalf("файлы правки удалены, published — на месте: %v", e.files(pOne, cr.ID))
	}
	if _, err := e.svc.Access(cr.ID); err != nil {
		t.Fatalf("альбом доступен: %v", err)
	}
}

func TestQuota(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Options) {
		c.Owners[pOne] = OwnerLimits{QuotaBytes: 1300, MaxAlbums: 2}
	})
	first := e.create(pOne, book("A", d1, d2), "") // 800 из 1300
	_, err := e.svc.Create(pOne, &albums.CreateRequest{Manifest: book("B", d3, d4), ExpiresAt: t0.Add(time.Hour)})
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Reason != ReasonQuota {
		t.Fatalf("квота: %v", err)
	}
	// У другого владельца свой счёт.
	e.create(pTwo, book("C", d3, d4), "")
	// Одинаковые страницы одного альбома считаются один раз.
	e.create(pOne, book("D", d3, d3), "")
	_, err = e.svc.Create(pOne, &albums.CreateRequest{Manifest: book("E", d1, d2), ExpiresAt: t0.Add(time.Hour)})
	if !errors.As(err, &tl) || tl.Reason != ReasonAlbums {
		t.Fatalf("число альбомов: %v", err)
	}
	if err := e.svc.Delete(pOne, first.ID); err != nil {
		t.Fatal(err)
	}
	e.create(pOne, book("F", d1, d2), "") // удаление вернуло квоту и место в числе альбомов
	if e.svc.used[pOne] != 400+800 || e.svc.count[pOne] != 2 {
		t.Fatalf("учёт: %d байт, %d альбомов", e.svc.used[pOne], e.svc.count[pOne])
	}
}

func TestQuotaReplacePending(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Options) {
		c.Owners[pOne] = OwnerLimits{QuotaBytes: 1300, MaxAlbums: 5}
	})
	cr := e.create(pOne, book("A", d1, d2), "") // draft 800
	// Замена ожидающей правки не складывается с прежней: 800 → 800, а не 1600.
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d4)); err != nil {
		t.Fatalf("замена draft: %v", err)
	}
	if e.svc.used[pOne] != 800 {
		t.Fatalf("после замены учтено %d", e.svc.used[pOne])
	}
	e.mustPut(pOne, cr.ID, d3, d4)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	// published 800 + новая правка с двумя другими страницами = 1600 > 1300.
	_, err := e.svc.Update(pOne, cr.ID, book("A", d1, d2))
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Reason != ReasonQuota {
		t.Fatalf("правка сверх квоты: %v", err)
	}
	// Правка, которая делит страницу с published, — 800 + 400.
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d1)); err != nil {
		t.Fatalf("правка с общей страницей: %v", err)
	}
	if e.svc.used[pOne] != 1200 {
		t.Fatalf("published ∪ draft: %d", e.svc.used[pOne])
	}
	// Отказ ничего не меняет на диске и в памяти.
	if m, _ := e.st.ReadMeta(pOne, cr.ID); usageOf(m) != 1200 {
		t.Fatalf("meta после отказа: %d", usageOf(m))
	}
}

func TestDiskReserve(t *testing.T) {
	// Свободное место подставное: тест не зависит от реального диска.
	var avail atomic.Uint64
	avail.Store(10 << 20)
	e := newEnv(t, func(c *Config, o *Options) {
		c.DiskReserve = 9 << 20
		o.Free = func() (uint64, error) { return avail.Load(), nil }
	})
	req := &albums.CreateRequest{Manifest: book("A", d1, d2), ExpiresAt: t0.Add(time.Hour)} // новых байт: 800
	cr, err := e.svc.Create(pOne, req)
	if err != nil {
		t.Fatalf("Create при запасе: %v", err)
	}
	avail.Store(9<<20 + 799) // после записи 800 байт запас был бы нарушен на байт
	var tl *TooLargeError
	if _, err := e.svc.Create(pOne, req); !errors.As(err, &tl) || tl.Reason != ReasonDisk {
		t.Fatalf("Create при нехватке места: %v", err)
	}
	if err := e.svc.CheckSpace(800); !errors.As(err, &tl) {
		t.Fatalf("CheckSpace: %v", err)
	}
	avail.Store(9<<20 + 800) // ровно хватает
	if err := e.svc.CheckSpace(800); err != nil {
		t.Fatalf("CheckSpace на границе: %v", err)
	}
	if e.svc.count[pOne] != 1 {
		t.Fatal("отказ не должен оставлять альбом")
	}
	avail.Store(9 << 20)
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d3, d4)); !errors.As(err, &tl) {
		t.Fatalf("Update при нехватке места: %v", err)
	}
	// Правка без новых байт запаса не требует.
	if _, err := e.svc.Update(pOne, cr.ID, book("Переименован", d1, d2)); err != nil {
		t.Fatalf("Update без новых файлов: %v", err)
	}
	// Ошибка Statfs — не «мало места», а внутренняя ошибка.
	o := e.svc.free
	e.svc.free = func() (uint64, error) { return 0, errors.New("statfs") }
	if err := e.svc.CheckSpace(1); err == nil || errors.As(err, &tl) {
		t.Fatalf("отказ Statfs: %v", err)
	}
	e.svc.free = o
}

func TestManifestReserialized(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("Название", d1, nil, d3, d4), d1, d3, d4)
	raw, err := e.svc.PublishedManifest(cr.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"password", "expires_at", "token", "owner", "id"} {
		if _, ok := generic[k]; ok {
			t.Fatalf("в manifest.json есть %q", k)
		}
	}
	var m albums.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Pages {
		switch {
		case p.Blank && p.File != "":
			t.Fatalf("у белой страницы есть файл: %+v", p)
		case !p.Blank && p.File != p.SHA256+".jpg":
			t.Fatalf("file ставит сервис: %+v", p)
		}
	}
	// Манифест пересобирается из типа: после перезапуска байты те же.
	e.reopen()
	again, err := e.svc.PublishedManifest(cr.ID, nil)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("после перезапуска манифест другой: %v", err)
	}
}

func TestDeleteImmediate(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	if err := e.svc.Delete(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Access: %v", err)
	}
	if _, err := e.svc.OpenPublished(cr.ID, fileOf(page(1, d1)), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("файл: %v", err)
	}
	if err := e.svc.Delete(pOne, cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "owners", pOne, "albums", cr.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("каталог остался: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "trash")); len(ents) != 0 {
		t.Fatalf("корзина не пуста: %v", ents)
	}
	if e.svc.used[pOne] != 0 || e.svc.count[pOne] != 0 {
		t.Fatalf("учёт: %d, %d", e.svc.used[pOne], e.svc.count[pOne])
	}
}

func TestIncomplete404(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	name := fileOf(page(1, d1))
	if err := os.Remove(filepath.Join(e.dir, "owners", pOne, "albums", cr.ID, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.OpenPublished(cr.ID, name, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("файла нет: %v", err)
	}
	// Неполным стал весь альбом: 404 и на манифесте, и на другом, целом файле.
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Access неполного: %v", err)
	}
	if _, err := e.svc.OpenPublished(cr.ID, fileOf(page(2, d2)), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("целый файл неполного: %v", err)
	}
	if !strings.Contains(e.log.String(), `"op":"serve"`) {
		t.Fatalf("пометка не попала в журнал: %s", e.log.String())
	}
	// Через /v1 им управляют как обычно: список, правка.
	if got := e.svc.List(pOne); len(got) != 1 || !got[0].Published {
		t.Fatalf("список: %+v", got)
	}
	// Страницу догружают в published и публикуют заново.
	_, err := e.svc.Publish(pOne, cr.ID)
	var me *MissingError
	if !errors.As(err, &me) || !reflect.DeepEqual(me.Missing, []int{1}) {
		t.Fatalf("publish неполного: %v", err)
	}
	e.mustPut(pOne, cr.ID, d1)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Access(cr.ID); err != nil {
		t.Fatalf("после новой публикации: %v", err)
	}
	// Неполный находится и при старте.
	if err := os.Remove(filepath.Join(e.dir, "owners", pOne, "albums", cr.ID, name)); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("после перезапуска: %v", err)
	}
}

func TestOldManifestFile404(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	oldName := fileOf(page(2, d2))
	if _, err := e.svc.Update(pOne, cr.ID, book("A", d1, d3)); err != nil {
		t.Fatal(err)
	}
	e.mustPut(pOne, cr.ID, d3)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	// Клиент со старым манифестом просит файл прежней редакции: 404 только ему, альбом не «неполный».
	if _, err := e.svc.OpenPublished(cr.ID, oldName, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("файл прежней редакции: %v", err)
	}
	if _, err := e.svc.Access(cr.ID); err != nil {
		t.Fatalf("альбом остался доступен: %v", err)
	}
	f, err := e.svc.OpenPublished(cr.ID, fileOf(page(2, d3)), nil)
	if err != nil {
		t.Fatalf("файл новой редакции: %v", err)
	}
	defer func() { _ = f.Close() }()
	if got, _ := io.ReadAll(f); !bytes.Equal(got, d3) || f.Type != albums.TypeJPEG {
		t.Fatalf("отдан не тот файл: %d байт, %q", len(got), f.Type)
	}
	// Имя, которого в манифесте нет вовсе, и имя не по грамматике.
	for _, n := range []string{fileOf(page(9, d4)), "../meta.json", "meta.json", ""} {
		if _, err := e.svc.OpenPublished(cr.ID, n, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("чужое имя %q: %v", n, err)
		}
	}
}

func TestRestoreFromMeta(t *testing.T) {
	e := newEnv(t)
	cr := e.create(pOne, book("С паролем", d1, d2), "секрет-1234")
	e.mustPut(pOne, cr.ID, d1, d2)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	other := e.published(pTwo, book("Чужой", d3, d4), d3, d4)
	list := e.svc.List(pOne)
	man, _ := e.svc.PublishedManifest(cr.ID, nil)
	acc, err := e.svc.Access(cr.ID)
	if err != nil || acc.Password == nil {
		t.Fatalf("Access: %+v, %v", acc, err)
	}
	e.reopen() // индекс восстанавливается только из meta на диске
	if got := e.svc.List(pOne); !reflect.DeepEqual(got, list) {
		t.Fatalf("список после перезапуска: %+v, было %+v", got, list)
	}
	if got, _ := e.svc.PublishedManifest(cr.ID, nil); !bytes.Equal(got, man) {
		t.Fatal("манифест после перезапуска другой")
	}
	acc2, err := e.svc.Access(cr.ID)
	if err != nil || !reflect.DeepEqual(acc, acc2) {
		t.Fatalf("пароль и срок после перезапуска: %+v, %v", acc2, err)
	}
	if e.svc.used[pOne] != 800 || e.svc.count[pTwo] != 1 {
		t.Fatalf("учёт квоты восстановлен: %d, %d", e.svc.used[pOne], e.svc.count[pTwo])
	}
	if _, err := e.svc.Access(other.ID); err != nil {
		t.Fatalf("альбом второго владельца: %v", err)
	}
	// Негодная meta — в карантин, остальное работает.
	bad := filepath.Join(e.dir, "owners", pOne, "albums", cr.ID, "meta.json")
	if err := os.WriteFile(bad, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	if _, err := e.svc.Access(cr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("альбом с негодной meta: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "quarantine")); len(ents) != 1 {
		t.Fatalf("карантин: %v", ents)
	}
	if _, err := e.svc.Access(other.ID); err != nil {
		t.Fatalf("соседний альбом: %v", err)
	}
}

func TestListOwnOnly(t *testing.T) {
	e := newEnv(t)
	mine := e.published(pOne, book("Мой", d1, d2), d1, d2)
	foreign := e.published(pTwo, book("Чужой", d3, d4), d3, d4)
	got := e.svc.List(pOne)
	if len(got) != 1 || got[0].ID != mine.ID || got[0].Title != "Мой" || got[0].Pages != 2 || !got[0].Published {
		t.Fatalf("список: %+v", got)
	}
	// Чужой и несуществующий неотличимы ни для одной операции.
	ghost := strings.Repeat("q", idLen)
	for _, id := range []string{foreign.ID, ghost} {
		ops := map[string]error{
			"Pages":   func() error { _, err := e.svc.CurrentPages(pOne, id); return err }(),
			"Update":  func() error { _, err := e.svc.Update(pOne, id, book("A", d1, d2)); return err }(),
			"Publish": func() error { _, err := e.svc.Publish(pOne, id); return err }(),
			"Patch":   func() error { _, err := e.svc.Patch(pOne, id, &albums.Patch{RevokeSessions: true}); return err }(),
			"Delete":  e.svc.Delete(pOne, id),
			"Commit":  e.put(pOne, id, d1),
		}
		for name, err := range ops {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s(%s): %v", name, map[bool]string{true: "чужой", false: "несуществующий"}[id == foreign.ID], err)
			}
		}
	}
	if !e.inIndex(foreign.ID) {
		t.Fatal("чужие операции не тронули альбом")
	}
}

func TestPasswordPatch(t *testing.T) {
	e := newEnv(t)
	cr := e.published(pOne, book("A", d1, d2), d1, d2)
	pw := "Пароль-для-теста"
	if _, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	m1, _ := e.st.ReadMeta(pOne, cr.ID)
	if m1.Password == nil || strings.Contains(m1.Password.Hash+m1.Password.Salt, pw) {
		t.Fatalf("запись пароля: %+v", m1.Password)
	}
	// Новая соль при каждой смене, в том числе на тот же пароль; epoch растёт только по revoke_sessions.
	if _, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{Password: &pw, RevokeSessions: true}); err != nil {
		t.Fatal(err)
	}
	m2, _ := e.st.ReadMeta(pOne, cr.ID)
	if m2.Password.Salt == m1.Password.Salt || m2.Password.Hash == m1.Password.Hash || m2.Epoch != m1.Epoch+1 {
		t.Fatalf("смена пароля: соль, хэш и epoch: %+v / %+v", m1, m2)
	}
	acc, _ := e.svc.Access(cr.ID)
	n, _ := e.opt.Passwords.Hash(cr.ID, acc.Password.Salt, "Пароль-для-теста")
	if !bytes.Equal(n, acc.Password.Hash) {
		t.Fatal("хэш не сходится с HMAC от соли, id и пароля")
	}
	short := "  abc "
	if _, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{Password: &short}); err == nil {
		t.Fatal("пароль короче 4 знаков после обрезки принят")
	}
	empty := ""
	if _, err := e.svc.Patch(pOne, cr.ID, &albums.Patch{Password: &empty}); err != nil {
		t.Fatal(err)
	}
	if m3, _ := e.st.ReadMeta(pOne, cr.ID); m3.Password != nil {
		t.Fatal("пароль не снят")
	}
}

func TestCreateRejects(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Options) { c.Limits.Pages = 4; c.Limits.AlbumBytes = 1500 })
	req := func(m albums.Manifest) *albums.CreateRequest {
		return &albums.CreateRequest{Manifest: m, ExpiresAt: t0.Add(time.Hour)}
	}
	var tl *TooLargeError
	var bm *BadManifestError
	_, err := e.svc.Create(pOne, req(book("A", d1, d2, d3, d4, d1, d2)))
	if !errors.As(err, &tl) || tl.Reason != ReasonPages {
		t.Fatalf("6 страниц при пределе 4: %v", err)
	}
	_, err = e.svc.Create(pOne, req(book("A", d1, d2, d3, d4))) // 1600 > 1500
	if !errors.As(err, &tl) || tl.Reason != ReasonAlbumBytes {
		t.Fatalf("сумма байт: %v", err)
	}
	web := book("A", d1, d2)
	web.Pages[0].Type = albums.TypeWebP
	if _, err = e.svc.Create(pOne, req(web)); !errors.As(err, &bm) {
		t.Fatalf("WebP выключен: %v", err)
	}
	dup := book("A", d1, d1)
	dup.Pages[1].W = 99
	if _, err = e.svc.Create(pOne, req(dup)); !errors.As(err, &bm) {
		t.Fatalf("один хэш — разные размеры: %v", err)
	}
	bad := book("A", d1, d2)
	bad.Pages[0].File = "../../x"
	if _, err = e.svc.Create(pOne, req(bad)); !errors.As(err, &bm) {
		t.Fatalf("file во входе: %v", err)
	}
	if _, err = e.svc.Create(pOne, &albums.CreateRequest{Manifest: book("A", d1, d2), ExpiresAt: t0.Add(time.Hour), Password: "  ab "}); !errors.As(err, &bm) {
		t.Fatalf("пароль после обрезки короче 4: %v", err)
	}
	if e.svc.count[pOne] != 0 || len(e.svc.idx) != 0 {
		t.Fatal("отказы не оставили альбомов")
	}
}
