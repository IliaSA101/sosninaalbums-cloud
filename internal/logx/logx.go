// Package logx — политика журнала сервиса (design §7): JSON в поток, в строке только разрешённые поля.
// Токен, пароль, cookie, id альбома, путь, строка запроса, название, IP и тело в журнал не попадают:
// чужое поле не пропускается, а заменяется на «[redacted]», а id альбома входит только как 8 hex HMAC.
package logx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"regexp"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// Redacted — что остаётся от значения, которое журналу писать нельзя.
const Redacted = "[redacted]"

// Ключи полей журнала. Всё прочее (в том числе под группами) в строку не попадает.
const (
	KeyRoute  = "route"  // шаблон маршрута, не путь
	KeyStatus = "status" // код ответа
	KeyCode   = "code"   // код ошибки контракта
	KeyBytes  = "bytes"
	KeyMS     = "ms"
	KeyOwner  = "owner" // slug владельца из конфига
	KeyAlbum  = "album" // 8 hex HMAC(k_log, id)
	KeyOp     = "op"    // операция store
	KeyErr    = "err"   // только ошибка store/сервиса без имён файлов
	KeyReason = "reason"
)

var (
	plainKeys = map[string]bool{
		slog.TimeKey: true, slog.LevelKey: true, slog.MessageKey: true,
		KeyRoute: true, KeyStatus: true, KeyCode: true, KeyBytes: true, KeyMS: true,
		KeyOp: true, KeyErr: true, KeyReason: true,
		"count": true, "kind": true, "step": true,
	}
	tagRE = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

// New — журнал JSON в w. Значения полей вне политики заменяются, а не пропускаются: ошибка в вызове
// не должна становиться утечкой.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replace,
	}))
}

func replace(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return slog.String(a.Key, Redacted)
	}
	switch {
	case plainKeys[a.Key]:
		return a
	case a.Key == KeyAlbum:
		if s, ok := a.Value.Any().(string); ok && tagRE.MatchString(s) {
			return a
		}
	case a.Key == KeyOwner:
		if s, ok := a.Value.Any().(string); ok && names.ValidSlug(s) {
			return a
		}
	}
	return slog.String(a.Key, Redacted)
}

// Tagger превращает id альбома в метку для журнала: префикс id — часть ключа доступа, а HMAC с
// отдельным ключом (k_log) сопоставим между строками и ничего не раскрывает.
type Tagger struct{ key []byte }

// NewTagger — по ключу k_log из HKDF с меткой «log/1».
func NewTagger(klog []byte) *Tagger { return &Tagger{key: append([]byte(nil), klog...)} }

// Tag — 8 hex от HMAC-SHA256(k_log, id). У nil-метки результат «00000000»: сервис без ключа не пишет id.
func (t *Tagger) Tag(id string) string {
	if t == nil {
		return "00000000"
	}
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil)[:4])
}

// Attr — поле «album» для журнала.
func (t *Tagger) Attr(id string) slog.Attr { return slog.String(KeyAlbum, t.Tag(id)) }

// Format не раскрывает ключ k_log ни под каким глаголом (%v, %+v, %#v, %s, %x).
func (Tagger) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// LogValue — то же для slog.
func (Tagger) LogValue() slog.Value { return slog.StringValue(Redacted) }
