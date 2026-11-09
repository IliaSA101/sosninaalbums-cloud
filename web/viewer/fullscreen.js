// Полный экран: кнопка нужна только там, где браузер умеет requestFullscreen (на iPhone элементы в полный экран
// не открываются — кнопки там просто нет). Чистые функции от переданных объектов: проверяются из Node.

// canFullscreen — есть ли смысл показывать кнопку.
export function canFullscreen(doc, el) {
  return Boolean(doc) && doc.fullscreenEnabled !== false && typeof el.requestFullscreen === 'function'
}

// toggleFullscreen — вход или выход. Отказ браузера (жест не признан, запрет политикой) глотаем: кнопка остаётся
// как была, а необработанный отказ попал бы в консоль.
export function toggleFullscreen(doc, el) {
  const done = doc.fullscreenElement ? doc.exitFullscreen() : el.requestFullscreen()
  return Promise.resolve(done).catch(() => {})
}
