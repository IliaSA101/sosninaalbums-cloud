// Package config — чтение и проверка конфига сервиса (design §6) и файла секрета с ключами HKDF.
// Ошибки называют поле, но не значение: в конфиге рядом лежат хэши токенов, а в файле секрета — ключ.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

// Предел размера файла конфига: он читается целиком в память до разбора.
const maxConfigSize = 1 << 20

// Значения по умолчанию (design §6, спека «Технические заметки»): уточняются замером реальных выгрузок.
const (
	DefaultMaxDays        = 366 // 001-12: «Год» в программе — 366 суток, в модуле по умолчанию 365
	DefaultQuotaBytes     = int64(10) << 30
	DefaultMaxAlbums      = 500
	DefaultDiskReserve    = int64(2) << 30
	DefaultUnpublishedTTL = 24 * time.Hour
)

var tokenHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Config — конфиг сервиса. Неизвестное поле в файле — ошибка: опечатка в имени предела не должна
// молча оставить предел по умолчанию.
type Config struct {
	Listen         string        `json:"listen"`
	TrustedProxy   string        `json:"trusted_proxy"`
	PublicBase     string        `json:"public_base"`
	DataDir        string        `json:"data_dir"`
	SecretFile     string        `json:"secret_file"`
	Limits         albums.Limits `json:"limits"`
	AcceptWebP     bool          `json:"accept_webp"`
	UnpublishedTTL Duration      `json:"unpublished_ttl"`
	DiskReserve    int64         `json:"disk_reserve_bytes"`
	Login          Login         `json:"login"`
	Owners         []Owner       `json:"owners"`
}

// Login — пределы неудачных попыток входа (design §5.5).
type Login struct {
	PerAlbumIP Rate `json:"per_album_ip"`
	PerIP      Rate `json:"per_ip"`
	PerAlbum   Rate `json:"per_album"`
}

// Owner — фотограф: slug (каталог на диске, в адрес не входит), подпись студии, сайт студии, квоты и токены.
type Owner struct {
	Slug   string `json:"slug"`
	Studio string `json:"studio"`
	// Site — необязательный адрес сайта студии: подпись в листалке станет ссылкой. Правило — checkSite.
	Site       string  `json:"site"`
	QuotaBytes int64   `json:"quota_bytes"`
	MaxAlbums  int     `json:"max_albums"`
	Tokens     []Token `json:"tokens"`
}

// Token — запись токена загрузки: имя для человека и sha256 токена в hex. Сам токен не хранится.
type Token struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Duration — длительность строкой «24h» в JSON.
type Duration time.Duration

// UnmarshalJSON разбирает строку вида «24h».
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("длительность — строка вида \"24h\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil || v <= 0 {
		return errors.New("длительность не разобрана или не положительна")
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON — обратное к UnmarshalJSON.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Rate — предел «N/окно»: не больше N неудач за окно («5/15m»).
type Rate struct {
	Count  int
	Window time.Duration
}

// UnmarshalJSON разбирает строку «5/15m».
func (r *Rate) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("предел — строка вида \"5/15m\"")
	}
	n, w, ok := strings.Cut(s, "/")
	if !ok {
		return errors.New("предел — строка вида \"5/15m\"")
	}
	cnt, err := strconv.Atoi(n)
	win, err2 := time.ParseDuration(w)
	if err != nil || err2 != nil || cnt <= 0 || win <= 0 {
		return errors.New("предел не разобран: нужны положительные число и окно")
	}
	r.Count, r.Window = cnt, win
	return nil
}

// MarshalJSON — обратное к UnmarshalJSON.
func (r Rate) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.Itoa(r.Count) + "/" + r.Window.String())
}

// Load читает и проверяет файл конфига. Читается через os.Root каталога файла, размер ограничен.
func Load(path string) (*Config, error) {
	data, _, err := readSmall(path, maxConfigSize, false)
	if err != nil {
		return nil, fmt.Errorf("конфиг: %w", err)
	}
	return Parse(data)
}

// Parse разбирает и проверяет конфиг, подставляя значения по умолчанию вместо нулевых.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("конфиг не разобран: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("конфиг: после объекта есть лишнее")
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Limits.MaxDays == 0 {
		c.Limits.MaxDays = DefaultMaxDays
	}
	titleOver := c.Limits.TitleLen > albums.MaxTitle
	c.Limits = c.Limits.OrDefault()
	if titleOver {
		c.Limits.TitleLen = albums.MaxTitle + 1 // Validate отвергнет, а не зажмёт молча
	}
	if c.UnpublishedTTL == 0 {
		c.UnpublishedTTL = Duration(DefaultUnpublishedTTL)
	}
	if c.DiskReserve == 0 {
		c.DiskReserve = DefaultDiskReserve
	}
	def := func(r *Rate, n int, w time.Duration) {
		if r.Count == 0 {
			*r = Rate{Count: n, Window: w}
		}
	}
	def(&c.Login.PerAlbumIP, 5, 15*time.Minute)
	def(&c.Login.PerIP, 20, time.Hour)
	def(&c.Login.PerAlbum, 20, 24*time.Hour)
	for i := range c.Owners {
		o := &c.Owners[i]
		if o.QuotaBytes == 0 {
			o.QuotaBytes = DefaultQuotaBytes
		}
		if o.MaxAlbums == 0 {
			o.MaxAlbums = DefaultMaxAlbums
		}
	}
}

// Validate проверяет конфиг по design §6. Сообщения — без значений полей.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if err := checkPublicBase(c.PublicBase); err != nil {
		bad("public_base: %w", err)
	}
	if err := checkListen(c.Listen, c.TrustedProxy); err != nil {
		bad("listen: %w", err)
	}
	if c.DataDir == "" {
		bad("data_dir: не задан")
	}
	if c.SecretFile == "" {
		bad("secret_file: не задан")
	}
	l := c.Limits
	if l.PageBytes <= 0 || l.Pages <= 0 || l.AlbumBytes <= 0 || l.MaxDays <= 0 || l.TitleLen <= 0 {
		bad("limits: все пределы положительны")
	}
	if l.TitleLen > albums.MaxTitle {
		bad("limits.title_len: больше %d", albums.MaxTitle)
	}
	if l.PageBytes > l.AlbumBytes {
		bad("limits: page_bytes больше album_bytes")
	}
	if c.DiskReserve < 0 {
		bad("disk_reserve_bytes: отрицательный")
	}
	if len(c.Owners) == 0 {
		bad("owners: нужен хотя бы один")
	}
	errs = append(errs, c.checkOwners()...)
	return errors.Join(errs...)
}

func (c *Config) checkOwners() []error {
	var errs []error
	slugs := map[string]bool{}
	hashes := map[string]bool{}
	for i, o := range c.Owners {
		at := fmt.Sprintf("owners[%d]", i)
		bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(at+"."+format, a...)) }
		if !names.ValidSlug(o.Slug) {
			bad("slug: не по шаблону ^[a-z0-9][a-z0-9-]{0,31}$")
		} else if slugs[o.Slug] {
			bad("slug: повторяется")
		}
		slugs[o.Slug] = true
		if n := utf8.RuneCountInString(o.Studio); n < 1 || n > 100 || !utf8.ValidString(o.Studio) || hasInvisible(o.Studio) {
			bad("studio: 1…100 знаков без управляющих")
		}
		if o.Site != "" {
			if err := checkSite(o.Site); err != nil {
				bad("site: %w", err) // текст ошибки — без значения поля
			}
		}
		if o.QuotaBytes <= 0 || o.MaxAlbums <= 0 {
			bad("quota_bytes и max_albums: положительные")
		}
		if len(o.Tokens) == 0 {
			bad("tokens: нужен хотя бы один")
		}
		for j, t := range o.Tokens {
			if t.Name == "" || utf8.RuneCountInString(t.Name) > 64 || hasInvisible(t.Name) {
				bad("tokens[%d].name: 1…64 знака без управляющих", j)
			}
			switch {
			case !tokenHashRE.MatchString(t.SHA256):
				bad("tokens[%d].sha256: 64 hex строчными", j)
			case hashes[t.SHA256]:
				bad("tokens[%d].sha256: повторяется (хэши уникальны среди всех владельцев)", j)
			}
			hashes[t.SHA256] = true
		}
	}
	return errs
}

func hasInvisible(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return true
		}
	}
	return false
}

func checkPublicBase(s string) error {
	if s == "" || len(s) > albums.MaxBase {
		return errors.New("пуст или длиннее предела")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("не разобран как адрес")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return errors.New("только схема и хост: без пользователя, пути, запроса и якоря")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("нет хоста")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(host) {
			return errors.New("http допустим только для loopback-стенда")
		}
	default:
		return errors.New("схема — https")
	}
	return nil
}

// Предел длины site и части правила для проверки: те же, что у роли Ansible и листалки, —
// все три читают testdata/site_cases.json, расхождение ловит тест, а не простой сервиса.
const maxSite = 200

var (
	siteLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// Последняя метка начинается с буквы: «127.1», «0x7f.1», «0177.0.0.1» браузер читает как IPv4, а Go — как имя.
	siteTLDRE  = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	sitePathRE = regexp.MustCompile(`^(/([A-Za-z0-9._~/-]|%[0-9A-Fa-f]{2})*)?$`)
)

// checkSite — правило поля site (005-4, 005-22): https-адрес сайта студии в каноническом виде. Строже, чем
// нужно Go: ссылку строит браузер, и любое расхождение разбора (WHATWG URL против net/url) — дыра. Поэтому
// только печатный ASCII, DNS-имя без IP и порта, безопасный путь, u.String() == s. Тексты ошибок —
// без значения: правило пакета одно, в конфиге рядом лежат хэши токенов.
func checkSite(s string) error {
	if len(s) > maxSite {
		return errors.New("длиннее 200 байт")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e || s[i] == '\\' {
			return errors.New("только печатный ASCII без пробелов и обратной косой (кириллица — punycode)")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("не разобран как адрес")
	}
	if u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("только https://хост/путь: без пользователя, запроса и якоря")
	}
	if strings.Contains(u.Host, ":") {
		return errors.New("без порта и IP-адреса")
	}
	labels := strings.Split(u.Host, ".")
	if len(labels) < 2 || !siteTLDRE.MatchString(labels[len(labels)-1]) {
		return errors.New("хост — DNS-имя с зоной, последняя метка с буквы")
	}
	for _, l := range labels[:len(labels)-1] {
		if !siteLabelRE.MatchString(l) {
			return errors.New("хост — строчные a-z, цифры и дефис, метка до 63 знаков")
		}
	}
	// Путь берётся из самой строки (после схемы и хоста): u.Path уже раскодирован и маскирует %2F.
	path := strings.TrimPrefix(s, "https://"+u.Host)
	if !sitePathRE.MatchString(path) || hasDotSegment(path) {
		return errors.New("путь — из A-Za-z0-9._~/- и %XX, без сегментов «.» и «..»")
	}
	if u.String() != s {
		return errors.New("не в каноническом виде")
	}
	return nil
}

// hasDotSegment — есть ли в пути сегмент «.» или «..», в том числе записанный как %2e: браузер сворачивает
// такие сегменты, и href разошёлся бы со значением.
func hasDotSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		switch strings.ReplaceAll(strings.ToLower(seg), "%2e", ".") {
		case ".", "..":
			return true
		}
	}
	return false
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

func checkListen(listen, trusted string) error {
	switch {
	case strings.HasPrefix(listen, "unix:"):
		p := strings.TrimPrefix(listen, "unix:")
		if !filepath.IsAbs(p) || p != filepath.Clean(p) {
			return errors.New("путь unix-сокета — абсолютный и чистый")
		}
		return nil
	case strings.HasPrefix(listen, "tcp:"):
		ap, err := netip.ParseAddrPort(strings.TrimPrefix(listen, "tcp:"))
		if err != nil || !ap.Addr().IsLoopback() {
			return errors.New("tcp — только loopback-адрес с портом (127.0.0.1:порт)")
		}
		if trusted == "" {
			return errors.New("для tcp нужен trusted_proxy")
		}
		if _, err := netip.ParseAddr(trusted); err != nil {
			return errors.New("trusted_proxy — IP-адрес прокси")
		}
		return nil
	}
	return errors.New("unix:/путь или tcp:127.0.0.1:порт")
}

// readSmall читает обычный файл не больше limit байт через os.Root его каталога: ни символическая ссылка
// наружу, ни «..» не сработают. Права и тип смотрятся по открытому дескриптору (без гонки проверка/открытие).
// strict — отказ, если файл доступен кому-то кроме владельца (секрет).
func readSmall(path string, limit int64, strict bool) ([]byte, os.FileInfo, error) {
	if path == "" {
		return nil, nil, errors.New("путь не задан")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("каталог файла: %w", unwrapPath(err))
	}
	defer func() { _ = root.Close() }()
	base := filepath.Base(path)
	// Сначала Stat без открытия: open() FIFO без писателя вешает старт навсегда. O_NONBLOCK не годится: слой
	// пакетов (depguard) не пускает syscall в config. Гонка проверка/открытие здесь — только у владельца
	// каталога, а он и так хозяин секрета; тип открытого файла всё равно сверяется ниже по дескриптору.
	pre, err := root.Stat(base)
	if err != nil {
		return nil, nil, fmt.Errorf("файл не прочитан: %w", unwrapPath(err))
	}
	if !pre.Mode().IsRegular() {
		return nil, nil, errors.New("не обычный файл")
	}
	f, err := root.Open(base)
	if err != nil {
		return nil, nil, fmt.Errorf("файл не открыт: %w", unwrapPath(err))
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("файл не прочитан: %w", unwrapPath(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, errors.New("не обычный файл")
	}
	if strict && fi.Mode().Perm()&0o077 != 0 {
		return nil, nil, errors.New("права шире 0600")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("файл не прочитан: %w", unwrapPath(err))
	}
	if int64(len(data)) > limit {
		return nil, nil, errors.New("файл слишком велик")
	}
	return data, fi, nil
}

// unwrapPath убирает из ошибки ФС имя файла: остаётся errno (в тексте ошибки пути секрета не нужны).
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
