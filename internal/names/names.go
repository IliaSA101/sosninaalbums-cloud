// Package names — грамматика имён, которые доходят до пути на диске или в журнал: id альбома, slug
// владельца, имя файла страницы. Единственный источник: config, store, access и logx проверяют одним и тем же
// шаблоном, поэтому правка в одном месте не может разойтись с другим. Пакет без зависимостей.
package names

import (
	"regexp"
	"strings"
)

var (
	idRE   = regexp.MustCompile(`^[a-z2-7]{26}$`)
	slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	pageRE = regexp.MustCompile(`^[0-9a-f]{64}\.(jpg|webp)$`)
)

// ValidID — id альбома, выданный сервисом: 26 знаков a–z2–7 (130 бит). Всё прочее до пути не допускается.
func ValidID(id string) bool { return idRE.MatchString(id) }

// ValidSlug — slug владельца из конфига: `^[a-z0-9][a-z0-9-]{0,31}$`.
func ValidSlug(slug string) bool { return slugRE.MatchString(slug) }

// ValidPageName — имя файла страницы: <sha256 hex>.jpg (или .webp, когда он включён).
func ValidPageName(name string) bool { return pageRE.MatchString(name) }

// CanonicalPath — путь запроса (в закодированном виде, как его прислал клиент) уже канонический: начинается
// с «/», без «%», обратной косой, управляющих и не-ASCII знаков, без пробелов, пустых и точечных сегментов
// (последний сегмент может быть пустым — завершающая «/»). Законный адрес сервиса (/v1/…, /a/<id>/…,
// /viewer/…) состоит только из знаков, которым кодирование не нужно, поэтому %2e, %2f, «..», «//» — не вид
// записи, а попытка обхода: такой запрос получает 404 до маршрутизатора, а не 301 ServeMux.
func CanonicalPath(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x21 || c >= 0x7f || c == '%' || c == '\\' {
			return false
		}
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		last := i == len(segs)-1
		if (s == "" && !last) || s == "." || s == ".." {
			return false
		}
	}
	return true
}
