package album

import (
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// Сроки приёма. Один источник для api (срок чтения тела) и для уборки (возраст «идущего» .part): уборка не
// должна резать файл медленного, но живого клиента.
const (
	readBase   = 60 * time.Second // база срока чтения тела
	readRate   = 32 << 10         // байт в секунду: наименьшая скорость, которую ждём от клиента (design §4)
	WriteExtra = 30 * time.Second // на ответ после чтения тела
	activeGap  = 60 * time.Second // запас уборки сверх самого долгого запроса
)

// MaxWriteBudget — потолок срока записи ответа: меньше `write` у Caddy (600 с), иначе Caddy оборвёт раньше.
const MaxWriteBudget = 540 * time.Second

// transferTime — время передачи n байт на наименьшей ожидаемой скорости (32 КиБ/с); общее для чтения тела
// страницы (api) и записи картинки клиенту (view).
func transferTime(n int64) time.Duration { return time.Duration(n/readRate) * time.Second }

// ReadBudget — срок чтения тела размером n байт: 60 с + n / 32 КБ/с (≤ 540 с при 15 МБ).
func ReadBudget(n int64) time.Duration { return readBase + transferTime(n) }

// WriteBudget — срок записи ответа размером n байт клиенту: 30 с + n / 32 КиБ/с, не больше MaxWriteBudget.
func WriteBudget(n int64) time.Duration { return min(MaxWriteBudget, WriteExtra+transferTime(n)) }

// ActiveAge — возраст, до которого .part и каталог без meta считаются идущими в работе: чтение самой большой
// страницы (page_bytes из конфига) + ответ + запас. Для 15 МБ — 630 с. Отдаётся в store.Options.ActiveAge.
func ActiveAge(pageBytes int64) time.Duration {
	return ReadBudget(pageBytes) + WriteExtra + activeGap
}

// DiskError переводит «нет места на диске» (ENOSPC, квота ФС) в TooLargeError{ReasonDisk}: клиенту это 413, а
// не 500. Прочие ошибки возвращаются как есть. Нужна api для ошибок записи тела в временный файл.
func DiskError(err error) error {
	if store.IsNoSpace(err) {
		return &TooLargeError{Reason: ReasonDisk}
	}
	return err
}
