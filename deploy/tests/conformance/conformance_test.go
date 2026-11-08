// Package conformance гоняет проверку любой реализации контракта /v1 (albumstest.Conformance модуля контракта)
// через Caddy локального стенда: https, TLS от Caddy, два токена (чужой альбом проверяется 404 как у
// несуществующего). Адрес и токены — из окружения, их выставляет deploy/tests/stand.sh test; без них тест
// пропускается, так что в обычном `go test ./...` он ничего не делает и в сеть не ходит.
package conformance

import (
	"os"
	"testing"

	"github.com/IliaSA101/sosninaalbums/api/albums/albumstest"
)

func TestConformanceViaCaddy(t *testing.T) {
	base, tok1, tok2 := os.Getenv("SA_BASE"), os.Getenv("SA_TOKEN1"), os.Getenv("SA_TOKEN2")
	if base == "" || tok1 == "" || tok2 == "" {
		t.Skip("не на стенде: нужны SA_BASE, SA_TOKEN1, SA_TOKEN2 (bash deploy/tests/stand.sh test)")
	}
	albumstest.Conformance(t, base, tok1, albumstest.WithOtherToken(tok2))
}
