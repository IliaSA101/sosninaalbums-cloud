package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// siteCase — строка общего файла случаев: его читают этот тест, статика роли Ansible и тест листалки.
type siteCase struct {
	Site string `json:"site"`
	OK   bool   `json:"ok"`
	Why  string `json:"why"`
}

func loadSiteCases(t *testing.T) []siteCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "site_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []siteCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("в файле случаев %d записей: файл усечён?", len(cases))
	}
	return cases
}

// Правило site (005-4, 005-22): весь общий файл случаев, через Parse конфига; отказ называет поле, но не значение.
func TestConfigOwnerSite(t *testing.T) {
	okN, badN := 0, 0
	for _, tc := range loadSiteCases(t) {
		m := validCfg()
		owner(m)["site"] = tc.Site
		c, err := parseMap(t, m)
		if tc.OK {
			okN++
			if err != nil {
				t.Errorf("%s: принимаемый site отвергнут: %v", tc.Why, err)
			} else if c.Owners[0].Site != tc.Site {
				t.Errorf("%s: site изменён при разборе", tc.Why)
			}
			continue
		}
		badN++
		switch {
		case err == nil:
			t.Errorf("%s: site принят, ждали отказ", tc.Why)
		case !strings.Contains(err.Error(), "owners[0].site"):
			t.Errorf("%s: ошибка не называет поле: %v", tc.Why, err)
		case strings.Contains(err.Error(), tc.Site):
			t.Errorf("%s: отвергнутое значение попало в ошибку: %q", tc.Why, err)
		}
	}
	if okN < 5 || badN < 30 {
		t.Fatalf("случаев мало: принять %d, отказать %d", okN, badN)
	}
}

// Без site (ключа нет или он пуст) подпись остаётся текстом; не строка и опечатка в ключе — отказ.
func TestConfigOwnerSiteOptional(t *testing.T) {
	c, err := parseMap(t, validCfg())
	if err != nil || c.Owners[0].Site != "" {
		t.Fatalf("без site: %v %q", err, c.Owners[0].Site)
	}
	m := validCfg()
	owner(m)["site"] = ""
	if _, err := parseMap(t, m); err != nil {
		t.Fatalf("пустой site: %v", err)
	}
	m = validCfg()
	owner(m)["site"] = 12345678
	if _, err := parseMap(t, m); err == nil || strings.Contains(err.Error(), "12345678") {
		t.Fatalf("site не строка: %v", err)
	}
	m = validCfg()
	owner(m)["sitе"] = "https://studio.example" // кириллическая «е» в имени ключа
	if _, err := parseMap(t, m); err == nil {
		t.Fatal("опечатка в имени ключа владельца принята")
	}
}
