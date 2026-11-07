package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const h1 = "1111111111111111111111111111111111111111111111111111111111111111"
const h2 = "2222222222222222222222222222222222222222222222222222222222222222"

// validCfg — минимальный годный конфиг; тесты портят в нём одно поле.
func validCfg() map[string]any {
	return map[string]any{
		"listen":      "unix:/run/albums-svc/http.sock",
		"public_base": "https://albums.example.org",
		"data_dir":    "/data",
		"secret_file": "/run/secrets/albums_master",
		"owners": []any{
			map[string]any{"slug": "p1", "studio": "Студия Пример",
				"tokens": []any{map[string]any{"name": "ноутбук", "sha256": h1}}},
		},
	}
}

func parseMap(t *testing.T, m map[string]any) (*Config, error) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(b)
}

func owner(m map[string]any) map[string]any { return m["owners"].([]any)[0].(map[string]any) }

func TestConfigDefaults(t *testing.T) {
	c, err := parseMap(t, validCfg())
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.PageBytes != 15<<20 || c.Limits.Pages != 300 || c.Limits.AlbumBytes != 1<<30 || c.Limits.MaxDays != 366 {
		t.Fatalf("пределы по умолчанию: %+v", c.Limits)
	}
	if c.DiskReserve != 2<<30 || time.Duration(c.UnpublishedTTL) != 24*time.Hour {
		t.Fatalf("запас места и срок брошенной выгрузки: %d %v", c.DiskReserve, c.UnpublishedTTL)
	}
	if c.Login.PerAlbumIP.Count != 5 || c.Login.PerAlbumIP.Window != 15*time.Minute || c.Login.PerAlbum.Window != 24*time.Hour {
		t.Fatalf("лимиты входа: %+v", c.Login)
	}
	if o := c.Owners[0]; o.QuotaBytes != 10<<30 || o.MaxAlbums != 500 {
		t.Fatalf("квоты владельца: %+v", o)
	}
	if c.AcceptWebP {
		t.Fatal("WebP в 0.1 выключен по умолчанию")
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m map[string]any)
		want string // фрагмент ошибки; пусто — конфиг годный
	}{
		{"годный", func(map[string]any) {}, ""},
		{"loopback http", func(m map[string]any) { m["public_base"] = "http://127.0.0.1:8080" }, ""},
		{"tcp loopback с прокси", func(m map[string]any) {
			m["listen"], m["trusted_proxy"] = "tcp:127.0.0.1:8080", "127.0.0.1"
		}, ""},
		{"http не loopback", func(m map[string]any) { m["public_base"] = "http://albums.example.org" }, "public_base"},
		{"public_base с завершающим слэшем", func(m map[string]any) { m["public_base"] = "https://albums.example.org/" }, "public_base"},
		{"путь в public_base", func(m map[string]any) { m["public_base"] = "https://albums.example.org/x" }, "public_base"},
		{"запрос в public_base", func(m map[string]any) { m["public_base"] = "https://albums.example.org?a=1" }, "public_base"},
		{"пользователь в public_base", func(m map[string]any) { m["public_base"] = "https://u:p@albums.example.org" }, "public_base"},
		{"public_base пуст", func(m map[string]any) { m["public_base"] = "" }, "public_base"},
		{"listen неизвестный", func(m map[string]any) { m["listen"] = "0.0.0.0:80" }, "listen"},
		{"tcp не loopback", func(m map[string]any) {
			m["listen"], m["trusted_proxy"] = "tcp:0.0.0.0:8080", "127.0.0.1"
		}, "listen"},
		{"tcp без trusted_proxy", func(m map[string]any) { m["listen"] = "tcp:127.0.0.1:8080" }, "trusted_proxy"},
		{"unix относительный", func(m map[string]any) { m["listen"] = "unix:run/x.sock" }, "listen"},
		{"data_dir пуст", func(m map[string]any) { delete(m, "data_dir") }, "data_dir"},
		{"secret_file пуст", func(m map[string]any) { delete(m, "secret_file") }, "secret_file"},
		{"нет владельцев", func(m map[string]any) { m["owners"] = []any{} }, "owners"},
		{"slug с заглавной", func(m map[string]any) { owner(m)["slug"] = "P1" }, "slug"},
		{"slug с ..", func(m map[string]any) { owner(m)["slug"] = ".." }, "slug"},
		{"slug слишком длинный", func(m map[string]any) { owner(m)["slug"] = strings.Repeat("a", 33) }, "slug"},
		{"slug повторяется", func(m map[string]any) {
			m["owners"] = []any{owner(m), map[string]any{"slug": "p1", "studio": "Б",
				"tokens": []any{map[string]any{"name": "x", "sha256": h2}}}}
		}, "slug"},
		{"studio пуста", func(m map[string]any) { owner(m)["studio"] = "" }, "studio"},
		{"studio с управляющим", func(m map[string]any) { owner(m)["studio"] = "а\nб" }, "studio"},
		{"studio длиннее 100", func(m map[string]any) { owner(m)["studio"] = strings.Repeat("я", 101) }, "studio"},
		{"нет токенов", func(m map[string]any) { owner(m)["tokens"] = []any{} }, "tokens"},
		{"sha256 не hex", func(m map[string]any) {
			owner(m)["tokens"] = []any{map[string]any{"name": "x", "sha256": "zz"}}
		}, "sha256"},
		{"sha256 заглавными", func(m map[string]any) {
			owner(m)["tokens"] = []any{map[string]any{"name": "x", "sha256": strings.ToUpper(h1[:63]) + "A"}}
		}, "sha256"},
		{"sha256 повторяется у другого владельца", func(m map[string]any) {
			m["owners"] = []any{owner(m), map[string]any{"slug": "p2", "studio": "Б",
				"tokens": []any{map[string]any{"name": "x", "sha256": h1}}}}
		}, "повторяется"},
		{"отрицательная квота", func(m map[string]any) { owner(m)["quota_bytes"] = -1 }, "quota_bytes"},
		{"title_len больше контракта", func(m map[string]any) {
			m["limits"] = map[string]any{"title_len": 201}
		}, "title_len"},
		{"страница больше альбома", func(m map[string]any) {
			m["limits"] = map[string]any{"page_bytes": 2 << 30, "album_bytes": 1 << 30}
		}, "page_bytes"},
		{"отрицательный запас", func(m map[string]any) { m["disk_reserve_bytes"] = -5 }, "disk_reserve_bytes"},
		{"неизвестное поле", func(m map[string]any) { m["lisen"] = "x" }, "lisen"},
		{"предел входа без окна", func(m map[string]any) {
			m["login"] = map[string]any{"per_ip": "20"}
		}, "предел"},
		{"предел входа нулевой", func(m map[string]any) {
			m["login"] = map[string]any{"per_ip": "0/1h"}
		}, "предел"},
		{"срок не строка", func(m map[string]any) { m["unpublished_ttl"] = 5 }, "длительность"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validCfg()
			tc.mut(m)
			_, err := parseMap(t, m)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("годный конфиг отвергнут: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("ждали ошибку про %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("ошибка %q не называет %q", err, tc.want)
			}
		})
	}
}

func TestConfigErrorsHaveNoValues(t *testing.T) {
	m := validCfg()
	m["public_base"] = "http://секретный-хост.example.org"
	owner(m)["slug"] = "ПЛОХОЙ-slug"
	owner(m)["tokens"] = []any{map[string]any{"name": "x", "sha256": "Zsecretvalue"}}
	_, err := parseMap(t, m)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	for _, leak := range []string{"секретный-хост", "ПЛОХОЙ", "Zsecretvalue"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("значение поля попало в ошибку: %q", err)
		}
	}
}

func TestConfigTrailingGarbage(t *testing.T) {
	b, _ := json.Marshal(validCfg())
	if _, err := Parse(append(b, []byte(` {"x":1}`)...)); err == nil {
		t.Fatal("лишний объект после конфига принят")
	}
	if _, err := Parse([]byte(`{`)); err == nil {
		t.Fatal("битый JSON принят")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(validCfg())
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "нет.json")); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("ошибка должна быть и не называть путь: %v", err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxConfigSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(big); err == nil {
		t.Fatal("конфиг больше предела прочитан")
	}
}
