package access

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

const (
	// CookieName — имя cookie входа: префикс __Secure- заставляет браузер принимать её только с Secure.
	// __Host- не годится: он требует Path=/, а у каждого альбома свой путь (design §5.4, 001-4).
	CookieName = "__Secure-sa_session"
	// CookieMaxAge — наибольший срок входа; не дольше срока самого альбома.
	CookieMaxAge = 30 * 24 * time.Hour
	// MaxCookies — сколько одноимённых cookie проверяется: остальные игнорируются, чтобы заголовок с сотней
	// копий не множил работу (design §5.4).
	MaxCookies = 4

	cookieVersion = "1"
	maxCookieLen  = 160 // «1.» + epoch + «.» + exp + «.» + 43 знака подписи — с запасом
)

// Session — то, от чего зависит годность входа: id альбома, соль пароля и epoch из meta. Смена пароля меняет
// соль, revoke_sessions увеличивает epoch — прежние cookie перестают сходиться без списка отозванных.
type Session struct {
	ID    string
	Salt  []byte
	Epoch int64
}

// Cookies выдаёт и проверяет cookie входа: HMAC-SHA256 на ключе k_cookie (HKDF «cookie/1»).
type Cookies struct{ key []byte }

// NewCookies — по ключу k_cookie.
func NewCookies(kcookie []byte) *Cookies { return &Cookies{key: append([]byte(nil), kcookie...)} }

// mac — base64url без заполнения от HMAC("1|" id "|" salt "|" epoch "|" exp). Длины id и соли фиксированы
// (names.ValidID, SaltBytes), поэтому разделители не дают двух разных разборов одной строки.
func (c *Cookies) mac(s Session, exp int64) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(cookieVersion + "|" + s.ID + "|"))
	m.Write(s.Salt)
	m.Write([]byte("|" + strconv.FormatInt(s.Epoch, 10) + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s Session) valid() bool { return names.ValidID(s.ID) && len(s.Salt) == SaltBytes && s.Epoch >= 0 }

// Issue выдаёт значение cookie и её срок. Срок входа — min(30 дней, до expires) с точностью до секунды; если
// до конца альбома меньше секунды, входа нет (ok=false).
func (c *Cookies) Issue(s Session, now, albumExpires time.Time) (value string, maxAge time.Duration, ok bool) {
	if !s.valid() {
		return "", 0, false
	}
	exp := min(now.Add(CookieMaxAge).Unix(), albumExpires.Unix())
	if exp <= now.Unix() {
		return "", 0, false
	}
	value = cookieVersion + "." + strconv.FormatInt(s.Epoch, 10) + "." + strconv.FormatInt(exp, 10) + "." + c.mac(s, exp)
	return value, time.Duration(exp-now.Unix()) * time.Second, true
}

// Valid — годна ли хоть одна из присланных cookie (не больше MaxCookies первых): r.Cookie берёт только
// первую, а браузер при нескольких одноимённых шлёт их все (design §5.4).
func (c *Cookies) Valid(s Session, now time.Time, values []string) bool {
	if !s.valid() {
		return false
	}
	ok := false
	for i, v := range values {
		if i >= MaxCookies {
			break
		}
		if c.check(s, now, v) {
			ok = true // без раннего выхода: время не зависит от места годной cookie
		}
	}
	return ok
}

// check: строгий разбор epoch и exp (десятичные без знака и ведущих нулей), exp > now, затем строка строится
// заново из epoch и соли meta и сравнивается с присланной целиком за постоянное время. Нестрогого разбора
// base64 нет: прототип показал, что он пропускает ~6 % подделок с теми же байтами подписи.
func (c *Cookies) check(s Session, now time.Time, v string) bool {
	if len(v) > maxCookieLen {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 4 || parts[0] != cookieVersion {
		return false
	}
	if _, ok := strictNumber(parts[1]); !ok {
		return false
	}
	exp, ok := strictNumber(parts[2])
	if !ok || exp <= now.Unix() {
		return false
	}
	want := cookieVersion + "." + strconv.FormatInt(s.Epoch, 10) + "." + parts[2] + "." + c.mac(s, exp)
	return subtle.ConstantTimeCompare([]byte(want), []byte(v)) == 1
}

// strictNumber разбирает неотрицательное десятичное число в канонической записи: без знака, пробелов, плюса
// и ведущих нулей («0» — можно), не длиннее 18 знаков.
func strictNumber(s string) (int64, bool) {
	if s == "" || len(s) > 18 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Format не раскрывает ключ ни под каким глаголом (%v, %+v, %#v, %s, %x).
func (Cookies) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, logx.Redacted) }

// LogValue — то же для slog.
func (Cookies) LogValue() slog.Value { return slog.StringValue(logx.Redacted) }
