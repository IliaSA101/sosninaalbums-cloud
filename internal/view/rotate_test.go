package view

import (
	"strconv"
	"testing"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// Ключ «альбом» — id + соль + epoch: 20 неверных паролей с разных адресов закрывают вход всем без cookie, но
// фотограф сменой пароля или сбросом входов открывает новую корзину (аудит Г3).
func TestLoginAlbumKeyRotates(t *testing.T) {
	e := newEnv(t)
	burn := func(id string) {
		t.Helper()
		for i := range 20 {
			if r := e.login(id, "перебор", fromIP("198.51.100."+strconv.Itoa(i+1))); r.status != 401 {
				t.Fatalf("попытка %d: %d", i+1, r.status)
			}
		}
		if r := e.login(id, loginPW, fromIP("203.0.113.200")); r.status != 429 {
			t.Fatalf("верный пароль после 20 неверных: %d, нужно 429", r.status)
		}
	}
	// Смена пароля.
	a := e.published(loginPW)
	burn(a.id)
	next := "Новый-пароль-3077"
	if _, err := e.svc.Patch(owner, a.id, &albums.Patch{Password: &next}); err != nil {
		t.Fatal(err)
	}
	if r := e.login(a.id, next, fromIP("203.0.113.201")); r.status != 303 {
		t.Fatalf("новый пароль после смены, новый адрес: %d", r.status)
	}
	// Сброс входов (тот же пароль, новый epoch).
	b := e.published(loginPW)
	burn(b.id)
	if _, err := e.svc.Patch(owner, b.id, &albums.Patch{RevokeSessions: true}); err != nil {
		t.Fatal(err)
	}
	if r := e.login(b.id, loginPW, fromIP("203.0.113.202")); r.status != 303 {
		t.Fatalf("пароль после сброса входов, новый адрес: %d", r.status)
	}
	// Блокировка альбома не переходит на соседний.
	c := e.published(loginPW)
	if r := e.login(c.id, loginPW, fromIP("203.0.113.203")); r.status != 303 {
		t.Fatalf("соседний альбом: %d", r.status)
	}
}
