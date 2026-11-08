package access

import (
	"fmt"
	"strings"
	"testing"
)

// Корзина «альбом» зависит от соли и epoch: смена пароля или сброс входов начинают счёт заново, а «альбом+IP»
// и «IP» остаются по id и адресу.
func TestLimiterAlbumGeneration(t *testing.T) {
	l, _ := newLimiter(t, testRates, 0)
	s := Session{ID: albumID, Salt: cookieSalt, Epoch: 1}
	for i := range 20 {
		if _, _, ok := l.Begin(s, addr(fmt.Sprintf("198.51.100.%d", i+1))); !ok {
			t.Fatalf("попытка %d отказана", i+1)
		}
	}
	if _, _, ok := l.Begin(s, addr("203.0.113.1")); ok {
		t.Fatal("21-я попытка на то же поколение прошла")
	}
	for name, ns := range map[string]Session{
		"новая соль":  {ID: albumID, Salt: append([]byte{9}, cookieSalt[1:]...), Epoch: 1},
		"новый epoch": {ID: albumID, Salt: cookieSalt, Epoch: 2},
	} {
		if _, _, ok := l.Begin(ns, addr("203.0.113.1")); !ok {
			t.Errorf("%s: вход остался закрыт", name)
		}
	}
	// В ключе соль только в виде hex, не сырыми байтами.
	if g := generation(s); !strings.HasPrefix(g, albumID+"|") || strings.Contains(g, string(cookieSalt)) {
		t.Errorf("ключ поколения: %q", g)
	}
}
