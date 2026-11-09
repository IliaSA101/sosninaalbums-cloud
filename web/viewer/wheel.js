// Колесо мыши и свайп тачпада → «страница вперёд / назад». Машина состояний без DOM и без часов: время
// подаёт вызывающий, поэтому жест с инерцией проверяется в node, а не только вручную (спека 005, design §2).
//
// Зачем замок. Тачпад Mac после отрыва пальцев шлёт убывающие delta сотни миллисекунд (инерция), колесо
// мыши — серию щелчков. Если листать по порогу на каждое событие, один свайп перевернёт десяток страниц.
// Поэтому после срабатывания копление запирается до тишины (quiet мс без событий): один жест — одна страница.

// Стартовые значения — гипотеза до ручной проверки на тачпаде и мыши (005-8): Chrome под Windows даёт 100 px
// на щелчок, Firefox — 3 строки (48 px), так что щелчок = страница. Правка констант — следующим выпуском.
export const THRESHOLD = 40
export const QUIET = 450
const LINE_PX = 16 // deltaMode 1 (строки) → пиксели
const ZOOM_EPS = 1.01 // visualViewport.scale иногда 1.0000x и без увеличения

// isZoomed — вид увеличен щипком: тогда два пальца двигают вид, а не листают (005-23).
export function isZoomed(vv) {
  return Boolean(vv) && vv.scale > ZOOM_EPS
}

// createWheel: threshold — порог в пикселях, quiet — тишина в мс, pagePx — высота «страницы» для deltaMode 2.
// feed(ev, now) → 'next' | 'prev' | 'eat' | 'pass'. Всё, кроме 'pass', вызывающий гасит через preventDefault:
// это же отменяет жест «назад по истории» на Mac. ev: { deltaX, deltaY, deltaMode, ctrlKey, zoomed, pagePx? }.
export function createWheel({ threshold = THRESHOLD, quiet = QUIET, pagePx = 800 } = {}) {
  let accX = 0
  let accY = 0
  let locked = false
  let last = null // время предыдущего события с ненулевым смещением

  const scale = (mode, px) => (mode === 1 ? LINE_PX : mode === 2 ? px : 1)

  function feed(ev, now) {
    // Щипок тачпада Mac и Ctrl+колесо приходят как wheel с ctrlKey — это масштаб браузера, не листание.
    if (ev.ctrlKey || ev.zoomed) return 'pass'
    const k = scale(ev.deltaMode, ev.pagePx || pagePx)
    const dx = (Number(ev.deltaX) || 0) * k
    const dy = (Number(ev.deltaY) || 0) * k
    // Нулевые события (конец инерции) жест не продлевают и не начинают.
    if (dx === 0 && dy === 0) return 'eat'
    if (last !== null && now - last >= quiet) {
      accX = 0
      accY = 0
      locked = false
    }
    last = now
    if (locked) return 'eat'
    // Доминирующая ось: диагональный жест не должен складывать обе оси в одну сумму.
    const horizontal = Math.abs(dx) > Math.abs(dy)
    const v = horizontal ? dx : dy
    // Смена знака внутри жеста — копим заново: палец вернулся назад, прежний путь не в счёт.
    let acc = horizontal ? accX : accY
    acc = acc !== 0 && Math.sign(acc) !== Math.sign(v) ? v : acc + v
    if (horizontal) accX = acc
    else accY = acc
    if (Math.abs(acc) < threshold) return 'eat'
    accX = 0
    accY = 0
    locked = true
    return acc > 0 ? 'next' : 'prev'
  }

  return { feed }
}
