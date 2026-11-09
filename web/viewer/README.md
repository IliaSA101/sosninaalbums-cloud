# Листалка (`web/viewer`)

HTML, CSS и ES-модули без сборщика; встраивается в бинарник (`embed.go`), раздаёт её `internal/view`
(спека 001, design §1, §5). Из сети — только `manifest.json` и `view.json` альбома и его картинки по
относительным адресам; сторонних запросов, встроенных скриптов и стилей нет. Внешних ссылок в шаблонах и
`notfound.html` нет вовсе; ссылку на сайт студии рисует `studio.js` из `view.json` (`site`), только по правилу.

## Для `internal/view` (Г3)

- `viewer.Assets` — файлы по `/viewer/<viewer.Version()>/…` (`Cache-Control: immutable`). Версия — хэш всего
  содержимого `Assets`, поэтому относительные импорты модулей и `vendor/…` остаются внутри каталога. Скрытых
  имён нет: отдавать только то, что есть в `Assets` (`.html` туда не входят).
- `viewer.Pages` — `index.html` и `login.html` — шаблоны `html/template`; данные — `viewer.PageData`:
  `Assets` (строка `/viewer/<версия>/` со слэшем на конце), `Studio`, у входа ещё `Message`.
  `notfound.html` — не шаблон, байты те же, что отдаёт Caddy (`handle_errors`); без стилей и ссылок.
- CSP как в design §5.6: `script-src` и `style-src` — `<public_base>/viewer/`, `img-src 'self'`,
  `connect-src 'self'`, `form-action 'self'`. Стили StPageFlip вынесены в `vendor/page-flip-2.0.7.css`
  (подробности — `vendor/SOURCE.txt`); `element.style` из JS политика допускает.
- Иконка `icon.svg` нужна для того, чтобы браузер не просил `/favicon.ico` (его 404 попадал бы в консоль).

## Проверки

- `go test ./web/...` — шаблоны (экранирование, без ПД и инлайна), ссылки, версия, вендор по `SOURCE.txt`.
- `node --test 'web/viewer/*.test.mjs'` (кавычки: glob раскрывает сам node; каталогом в Node 24 не запустить) — разбор манифеста,
  гашение цикла отрисовки, сборка книги (поддельные DOM и StPageFlip), `wheel.js` (жест колеса и тачпада без DOM),
  `studio.js` (ссылка на сайт студии по общему файлу случаев `internal/config/testdata/site_cases.json`),
  `fullscreen.js` (`fullscreen.test.mjs`: кнопка полного экрана есть, только если у браузера есть `requestFullscreen`).
- Браузер: телефон 390×844 и ПК 1440×900, консоль без ошибок — по сценарию спеки 001 («Листалка»).
