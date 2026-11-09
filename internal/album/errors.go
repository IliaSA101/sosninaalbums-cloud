package album

import (
	"errors"
	"fmt"
)

// ErrNotFound — альбома нет для этого вызывающего: не существует, чужой, истёк, удалён. Для клиента это
// всё один и тот же 404 (контракт: разницы в ответе быть не должно).
var ErrNotFound = errors.New("album: альбом не найден")

// ErrPageGone — страница уже не нужна альбому: пока читалось тело, правка заменила редакцию, и хэша нет ни в
// published, ни в draft. Клиенту — 409: список missing устарел, его надо запросить заново.
var ErrPageGone = errors.New("album: страница больше не нужна альбому")

// Причины отказа по пределу (TooLargeError.Reason). Тексты без значений: пределы и остатки места
// клиенту не раскрываются.
const (
	ReasonPages      = "число страниц больше предела"
	ReasonAlbumBytes = "размер альбома больше предела"
	ReasonQuota      = "квота владельца исчерпана"
	ReasonAlbums     = "число альбомов владельца исчерпано"
	ReasonDisk       = "на сервере не хватает места"
)

// TooLargeError — превышен предел или квота: клиенту 413 too_large (К1, К2 design §11).
type TooLargeError struct{ Reason string }

func (e *TooLargeError) Error() string { return "album: " + e.Reason }

// BadManifestError — запрос не прошёл проверку контракта или правил сервиса: клиенту 400 bad_manifest.
type BadManifestError struct{ Err error }

func (e *BadManifestError) Error() string { return "album: " + e.Err.Error() }

// Unwrap отдаёт причину (например, *albums.ValidationError).
func (e *BadManifestError) Unwrap() error { return e.Err }

func badManifest(format string, a ...any) error {
	return &BadManifestError{Err: fmt.Errorf(format, a...)}
}

// MissingError — publish без всех страниц: клиенту 409 pages_missing со списком номеров.
type MissingError struct{ Missing []int }

func (e *MissingError) Error() string {
	return "album: не все страницы загружены"
}
