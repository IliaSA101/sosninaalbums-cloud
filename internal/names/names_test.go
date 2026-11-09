package names

import (
	"strings"
	"testing"
)

func TestGrammar(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		fn   func(string) bool
		in   string
		ok   bool
	}{
		{"id", ValidID, strings.Repeat("q", 26), true},
		{"id", ValidID, "abcdefghijklmnopqrstuvwxy2", true},
		{"id", ValidID, strings.Repeat("q", 25), false},
		{"id", ValidID, strings.Repeat("q", 27), false},
		{"id", ValidID, strings.Repeat("Q", 26), false},
		{"id", ValidID, strings.Repeat("q", 25) + "1", false},
		{"id", ValidID, strings.Repeat("q", 25) + "\n", false},
		{"id", ValidID, "../" + strings.Repeat("q", 23), false},
		{"slug", ValidSlug, "p1", true},
		{"slug", ValidSlug, "a-b", true},
		{"slug", ValidSlug, strings.Repeat("a", 32), true},
		{"slug", ValidSlug, strings.Repeat("a", 33), false},
		{"slug", ValidSlug, "-a", false},
		{"slug", ValidSlug, "A", false},
		{"slug", ValidSlug, "a\n", false},
		{"slug", ValidSlug, "..", false},
		{"page", ValidPageName, sha + ".jpg", true},
		{"page", ValidPageName, sha + ".webp", true},
		{"page", ValidPageName, sha + ".png", false},
		{"page", ValidPageName, sha[:63] + ".jpg", false},
		{"page", ValidPageName, strings.ToUpper(sha) + ".jpg", false},
		{"page", ValidPageName, sha + ".jpg\n", false},
		{"page", ValidPageName, "../" + sha + ".jpg", false},
	} {
		if got := tc.fn(tc.in); got != tc.ok {
			t.Errorf("%s(%q) = %v", tc.name, tc.in, got)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"/", true},
		{"/v1/albums", true},
		{"/v1/albums/" + strings.Repeat("q", 26) + "/pages/3", true},
		{"/a/" + strings.Repeat("q", 26) + "/", true},
		{"/viewer/ab12cd34/app.js", true},
		{"", false},
		{"v1/albums", false},
		{"/v1/%2e%2e/albums", false},
		{"/v1/%2E%2e/albums", false},
		{"/v1/albums%2f", false},
		{"/v1/../albums", false},
		{"/v1/./albums", false},
		{"/v1/albums/..", false},
		{"/v1//albums", false},
		{"//v1/albums", false},
		{"/v1/albums//", false},
		{"/v1\\albums", false},
		{"/v1/al bums", false},
		{"/v1/albums\x00", false},
		{"/v1/albums\n", false},
		{"/v1/альбомы", false},
		{"/v1/albums\x7f", false},
	} {
		if got := CanonicalPath(tc.in); got != tc.ok {
			t.Errorf("CanonicalPath(%q) = %v, ждали %v", tc.in, got, tc.ok)
		}
	}
}
