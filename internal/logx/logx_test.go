package logx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("строка журнала не JSON: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestTagFormat(t *testing.T) {
	tg := NewTagger([]byte("ключ-журнала-для-теста-0123456789"))
	id := strings.Repeat("a", 26)
	a, b := tg.Tag(id), tg.Tag(id)
	if a != b || !tagRE.MatchString(a) {
		t.Fatalf("метка должна быть стабильной и 8 hex: %q %q", a, b)
	}
	if tg.Tag(strings.Repeat("b", 26)) == a {
		t.Fatal("разные id дали одну метку")
	}
	if other := NewTagger([]byte("другой-ключ-журнала-для-теста-012")).Tag(id); other == a {
		t.Fatal("метка не зависит от ключа")
	}
	if strings.Contains(id, a) || strings.Contains(a, id[:4]) {
		t.Fatal("метка не должна повторять префикс id")
	}
	var nilTagger *Tagger
	if got := nilTagger.Tag(id); !tagRE.MatchString(got) {
		t.Fatalf("nil-метка: %q", got)
	}
}

func TestPolicyRedacts(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	id := strings.Repeat("q", 26)
	l.Info("запрос",
		slog.String("route", "GET /a/{id}/"),
		slog.Int("status", 404),
		slog.String("authorization", "Bearer s3cret-token"),
		slog.String("password", "hunter2"),
		slog.String("cookie", "__Secure-sa_session=1.0.1.zzz"),
		slog.String("path", "/a/"+id+"/"),
		slog.String("title", "Свадьба Ивановых"),
		slog.String("ip", "203.0.113.7"),
		slog.String("album", id),         // сырой id вместо метки
		slog.String("owner", "../../p1"), // не slug
		slog.Group("g", slog.String("route", "x")),
	)
	out := buf.String()
	for _, bad := range []string{"s3cret-token", "hunter2", "__Secure-sa_session", id, "Свадьба", "203.0.113.7", "../../p1"} {
		if strings.Contains(out, bad) {
			t.Fatalf("в журнале %q: %s", bad, out)
		}
	}
	m := lines(t, &buf)[0]
	if m["route"] != "GET /a/{id}/" || m["status"] != float64(404) {
		t.Fatalf("разрешённые поля потерялись: %v", m)
	}
	for _, k := range []string{"authorization", "password", "cookie", "path", "title", "ip", "album", "owner"} {
		if m[k] != Redacted {
			t.Fatalf("поле %q не заменено: %v", k, m[k])
		}
	}
}

func TestPolicyKeepsTagAndOwner(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	tg := NewTagger([]byte("ключ-журнала-для-теста-0123456789"))
	l.Info("ок", tg.Attr(strings.Repeat("a", 26)), slog.String("owner", "p1"))
	m := lines(t, &buf)[0]
	if !tagRE.MatchString(m["album"].(string)) || m["owner"] != "p1" {
		t.Fatalf("метка или владелец заменены: %v", m)
	}
}

func TestTaggerKeyNotRevealed(t *testing.T) {
	key := []byte("ключ-журнала-для-теста-0123456789")
	tg := NewTagger(key)
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("метка", "t", tg)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x"} {
		fmt.Fprintf(&buf, verb, tg)
		fmt.Fprintf(&buf, verb, *tg)
	}
	if strings.Contains(buf.String(), string(key)) || strings.Contains(buf.String(), fmt.Sprintf("%v", key)) || !strings.Contains(buf.String(), Redacted) {
		t.Fatalf("ключ метки раскрыт: %q", buf.String())
	}
}
