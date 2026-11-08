package access

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	cookieNow  = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cookieKey  = bytes.Repeat([]byte{0x42}, 32)
	cookieSalt = bytes.Repeat([]byte{7}, SaltBytes)
)

func newSession() Session { return Session{ID: albumID, Salt: cookieSalt, Epoch: 3} }

func issue(t *testing.T, s Session, expires time.Time) string {
	t.Helper()
	v, _, ok := NewCookies(cookieKey).Issue(s, cookieNow, expires)
	if !ok {
		t.Fatal("cookie не выдана")
	}
	return v
}

func TestCookieRoundTrip(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	v, maxAge, ok := c.Issue(s, cookieNow, cookieNow.Add(90*24*time.Hour))
	if !ok || maxAge != CookieMaxAge {
		t.Fatalf("выдача: %v %v", ok, maxAge)
	}
	if !strings.HasPrefix(v, "1.3.") || len(v) > maxCookieLen {
		t.Fatalf("вид значения: %q", v)
	}
	if !c.Valid(s, cookieNow, []string{v}) {
		t.Fatal("своя cookie не принята")
	}
	// Срок альбома короче 30 дней: вход не дольше альбома.
	_, maxAge, ok = c.Issue(s, cookieNow, cookieNow.Add(36*time.Hour+500*time.Millisecond))
	if !ok || maxAge != 36*time.Hour {
		t.Fatalf("срок по альбому: %v %v", ok, maxAge)
	}
	// Срок уже вышел или меньше секунды — входа нет.
	for _, exp := range []time.Time{cookieNow, cookieNow.Add(-time.Hour), cookieNow.Add(900 * time.Millisecond)} {
		if _, _, ok := c.Issue(s, cookieNow, exp); ok {
			t.Fatalf("cookie выдана при сроке %v", exp)
		}
	}
	// Негодная сессия (id вне грамматики, соль не 16 Б) не подписывается и не проверяется.
	for _, bad := range []Session{{ID: "x", Salt: cookieSalt}, {ID: albumID, Salt: cookieSalt[:5]}, {ID: albumID, Salt: cookieSalt, Epoch: -1}} {
		if _, _, ok := c.Issue(bad, cookieNow, cookieNow.Add(time.Hour)); ok || c.Valid(bad, cookieNow, []string{v}) {
			t.Fatalf("сессия %+v принята", bad)
		}
	}
}

// Любая правка любого знака значения — отказ: подпись сверяется целой строкой, а не декодированием, так что
// «тот же base64 другими знаками» (лишние биты последнего знака) тоже не проходит.
func TestCookieTamper(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	v := issue(t, s, cookieNow.Add(48*time.Hour))
	macStart := strings.LastIndexByte(v, '.') + 1
	if len(v)-macStart != base64.RawURLEncoding.EncodedLen(32) {
		t.Fatalf("длина подписи: %q", v)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := macStart; i < len(v); i++ {
		for _, r := range alphabet {
			if byte(r) == v[i] {
				continue
			}
			bad := v[:i] + string(r) + v[i+1:]
			if c.Valid(s, cookieNow, []string{bad}) {
				t.Fatalf("подделка принята: позиция %d, знак %q", i, r)
			}
		}
	}
	for _, bad := range []string{
		v + "=", v + "A", v[:len(v)-1], "", "1.3.", v[:macStart], v + ".x", "2" + v[1:], "0." + v[2:],
		strings.ReplaceAll(v, ".", "-"), strings.Repeat("A", 1000),
	} {
		if c.Valid(s, cookieNow, []string{bad}) {
			t.Fatalf("искажённая cookie принята: %q", bad)
		}
	}
	// Другой ключ — другая подпись.
	if NewCookies(bytes.Repeat([]byte{0x43}, 32)).Valid(s, cookieNow, []string{v}) {
		t.Fatal("cookie с чужим ключом принята")
	}
}

func TestCookieStrictNumbers(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	exp := cookieNow.Add(48 * time.Hour).Unix()
	mac := c.mac(s, exp)
	// Те же числа в неканонической записи с подписью, посчитанной под них же: строгий разбор отказывает до
	// сверки подписи (иначе ведущие нули и знак давали бы несколько строк на один вход).
	for name, v := range map[string]string{
		"эталон":           fmt.Sprintf("1.%d.%d.%s", s.Epoch, exp, mac),
		"ведущий ноль":     fmt.Sprintf("1.0%d.%d.%s", s.Epoch, exp, mac),
		"ноль у срока":     fmt.Sprintf("1.%d.0%d.%s", s.Epoch, exp, mac),
		"плюс":             fmt.Sprintf("1.+%d.%d.%s", s.Epoch, exp, mac),
		"плюс у срока":     fmt.Sprintf("1.%d.+%d.%s", s.Epoch, exp, mac),
		"пробел":           fmt.Sprintf("1. %d.%d.%s", s.Epoch, exp, mac),
		"минус":            fmt.Sprintf("1.-%d.%d.%s", s.Epoch, exp, mac),
		"дробь":            fmt.Sprintf("1.%d.%d.5.%s", s.Epoch, exp, mac),
		"шестнадцатерично": fmt.Sprintf("1.%d.0x%x.%s", s.Epoch, exp, mac),
		"пустой epoch":     fmt.Sprintf("1..%d.%s", exp, mac),
		"пустой срок":      fmt.Sprintf("1.%d..%s", s.Epoch, mac),
		"слишком длинный":  fmt.Sprintf("1.%d.%d.%s", s.Epoch, exp*1000000000, mac),
		"переполнение":     fmt.Sprintf("1.%d.99999999999999999999.%s", s.Epoch, mac),
	} {
		want := name == "эталон"
		if got := c.Valid(s, cookieNow, []string{v}); got != want {
			t.Errorf("%s: %q принято=%v, ждали %v", name, v, got, want)
		}
	}
	for in, want := range map[string]bool{"0": true, "7": true, "10": true, "00": false, "01": false, "": false, "1_0": false, "١٢": false, "-0": false, strings.Repeat("9", 19): false, strings.Repeat("9", 18): true} {
		if _, ok := strictNumber(in); ok != want {
			t.Errorf("strictNumber(%q) = %v", in, ok)
		}
	}
}

// Браузер шлёт все одноимённые cookie: годна хоть одна из первых MaxCookies; остальные не смотрятся.
func TestCookieManySameName(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	good := issue(t, s, cookieNow.Add(48*time.Hour))
	junk := "1.3.9999999999.AAAA"
	for name, vals := range map[string][]string{
		"годная первой":    {good, junk, junk},
		"годная последней": {junk, junk, junk, good},
		"годная пятой":     {junk, junk, junk, junk, good},
		"только мусор":     {junk, junk, junk, junk},
		"пусто":            nil,
	} {
		want := name != "годная пятой" && name != "только мусор" && name != "пусто"
		if got := c.Valid(s, cookieNow, vals); got != want {
			t.Errorf("%s: %v, ждали %v", name, got, want)
		}
	}
}

func TestCookieOtherAlbum(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	v := issue(t, s, cookieNow.Add(48*time.Hour))
	other := s
	other.ID = "bcdefghijklmnopqrstuvwxyza"
	if c.Valid(other, cookieNow, []string{v}) {
		t.Fatal("cookie одного альбома открыла другой")
	}
	// Соль — часть подписи: смена пароля (новая соль) закрывает прежние входы без списка отозванных.
	salted := s
	salted.Salt = bytes.Repeat([]byte{8}, SaltBytes)
	if c.Valid(salted, cookieNow, []string{v}) {
		t.Fatal("cookie пережила смену соли")
	}
}

func TestCookieExpired(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	v := issue(t, s, cookieNow.Add(48*time.Hour))
	exp := cookieNow.Add(48 * time.Hour)
	for d, want := range map[time.Duration]bool{-time.Second: true, 0: true, 48*time.Hour - time.Second: true, 48 * time.Hour: false, 49 * time.Hour: false, 1000 * time.Hour: false} {
		if got := c.Valid(s, cookieNow.Add(d), []string{v}); got != want {
			t.Errorf("через %v (срок %v): %v", d, exp, got)
		}
	}
	// Срок в cookie продлить нельзя: подпись покрывает его.
	parts := strings.Split(v, ".")
	parts[2] = strconv.FormatInt(exp.Add(24*time.Hour).Unix(), 10)
	if c.Valid(s, cookieNow, []string{strings.Join(parts, ".")}) {
		t.Fatal("срок продлён правкой cookie")
	}
}

func TestCookieEpoch(t *testing.T) {
	c := NewCookies(cookieKey)
	s := newSession()
	v := issue(t, s, cookieNow.Add(48*time.Hour))
	s.Epoch++ // revoke_sessions
	if c.Valid(s, cookieNow, []string{v}) {
		t.Fatal("cookie пережила revoke_sessions")
	}
	if !c.Valid(s, cookieNow, []string{issue(t, s, cookieNow.Add(48*time.Hour))}) {
		t.Fatal("новая cookie после сброса не принята")
	}
	// Подставить новый epoch в значение мало: подпись посчитана на старый.
	parts := strings.Split(v, ".")
	parts[1] = strconv.FormatInt(s.Epoch, 10)
	if c.Valid(s, cookieNow, []string{strings.Join(parts, ".")}) {
		t.Fatal("epoch подменён в значении")
	}
}

func TestCookiesNotRevealed(t *testing.T) {
	c := NewCookies(cookieKey)
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("c", "c", c)
	texts := []string{buf.String(), fmt.Sprintf("%v %+v %#v %s %x", c, c, c, c, c), fmt.Sprintf("%v", *c)}
	for _, text := range texts {
		for _, leak := range []string{fmt.Sprintf("%v", cookieKey), base64.RawURLEncoding.EncodeToString(cookieKey), strings.Repeat("42", 32), "[66 66"} {
			if strings.Contains(text, leak) {
				t.Fatalf("ключ cookie раскрыт: %q", text)
			}
		}
	}
}
