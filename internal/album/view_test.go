package album

import (
	"errors"
	"testing"
)

// Вход проверяется по тому же снимку, из которого берутся манифест и файл: allow получает Access из той же meta
// (пароль, соль, epoch), а отказ — ErrDenied раньше любой проверки имени файла (нет оракула «файл есть»).
func TestViewAllowSingleSnapshot(t *testing.T) {
	e := newEnv(t)
	d1, d2, d3 := blob(1, 600), blob(2, 600), blob(3, 600)
	cr := e.create(pOne, book("Т", d1, nil, d2, d3), "Пароль-снимка-77")
	e.mustPut(pOne, cr.ID, d1, d2, d3)
	if _, err := e.svc.Publish(pOne, cr.ID); err != nil {
		t.Fatal(err)
	}
	want, err := e.svc.Access(cr.ID)
	if err != nil || want.Password == nil {
		t.Fatalf("Access: %+v %v", want, err)
	}
	var got []Access
	record := func(ok bool) func(Access) bool {
		return func(a Access) bool { got = append(got, a); return ok }
	}
	name := fileOf(page(1, d1))
	if _, err := e.svc.PublishedManifest(cr.ID, record(true)); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.OpenPublished(cr.ID, name, record(true))
	if err != nil {
		t.Fatal(err)
	}
	if f.Size != int64(len(d1)) {
		t.Errorf("Size %d, нужно %d", f.Size, len(d1))
	}
	_ = f.Close()
	if len(got) != 2 {
		t.Fatalf("allow вызван %d раз", len(got))
	}
	for i, a := range got {
		if a.Owner != want.Owner || a.Epoch != want.Epoch || a.Password == nil || string(a.Password.Salt) != string(want.Password.Salt) || string(a.Password.Hash) != string(want.Password.Hash) {
			t.Errorf("снимок %d не тот, что Access: %+v", i, a)
		}
	}
	// Отказ — ErrDenied и для настоящего, и для несуществующего имени: файл не подтверждается без входа.
	for _, n := range []string{name, fileOf(page(9, blob(9, 600)))} {
		if _, err := e.svc.OpenPublished(cr.ID, n, record(false)); !errors.Is(err, ErrDenied) {
			t.Errorf("OpenPublished(%s…): %v, нужно ErrDenied", n[:6], err)
		}
	}
	if _, err := e.svc.PublishedManifest(cr.ID, record(false)); !errors.Is(err, ErrDenied) {
		t.Errorf("PublishedManifest: %v, нужно ErrDenied", err)
	}
	// Невидимый альбом — ErrNotFound, allow не вызывается.
	got = nil
	if _, err := e.svc.OpenPublished("qqqqqqqqqqqqqqqqqqqqqqqqqq", name, record(true)); !errors.Is(err, ErrNotFound) || len(got) != 0 {
		t.Errorf("нет альбома: %v, allow вызван %d раз", err, len(got))
	}
}
