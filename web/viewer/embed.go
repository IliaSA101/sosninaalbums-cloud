// Package viewer — листалка альбома (HTML, CSS, ES-модули, StPageFlip), встроенная в бинарник сервиса.
// Здесь только встраивание и экспорт файловых систем: раздачу, шаблоны и заголовки делает internal/view.
package viewer

import (
	"embed"
	"io/fs"
)

//go:embed index.html login.html notfound.html
var pages embed.FS

//go:embed *.js *.css *.svg vendor
var assets embed.FS

// PageData — данные шаблонов index.html и login.html: ровно эти поля, других шаблоны не читают.
type PageData struct {
	Assets  string // префикс каталога листалки с версией и слэшем на конце: «/viewer/<Version()>/»
	Studio  string // подпись студии из конфига; пусто — подписи нет
	Message string // только login.html: «Неверный пароль», «Слишком много попыток» или пусто
}

var (
	// Pages — страницы: index.html и login.html — шаблоны html/template (подстановки описаны в начале каждого),
	// notfound.html — готовый HTML «Альбом не найден», тот же файл копируется в Caddy. Клиенту как есть не отдаются.
	Pages fs.FS = pages
	// Assets — то, что сервис отдаёт по /viewer/<Version>/…: модули, стили, vendor/ (StPageFlip с лицензией).
	Assets fs.FS = assets
)
