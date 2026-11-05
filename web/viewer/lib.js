// Загрузка StPageFlip без правки его файла и два обхода его повадок (версия 2.0.7, vendor/SOURCE.txt).
//
// 1. При загрузке библиотека добавляет в document.head <style> со своими правилами. CSP сервиса (style-src без
//    unsafe-inline) такой <style> блокирует и пишет нарушение в консоль, поэтому правила лежат в vendor/*.css,
//    а на время выполнения скрипта document.head подменяется отдельным узлом, куда библиотека и пишет.
// 2. Её цикл отрисовки (requestAnimationFrame) не останавливается ни destroy(), ни удалением страницы: каждая
//    пересборка книги (поворот телефона) оставила бы вечный цикл. Колбэки, созданные во время сборки, помечаются
//    поколением; закрытое поколение на следующем кадре молча обрывается.

const native = window.requestAnimationFrame.bind(window)
const generation = new WeakMap() // колбэк библиотеки → поколение (колбэк цикла — одна и та же функция каждый кадр)
const closed = new Set()
let current = 0
let next = 0

window.requestAnimationFrame = (cb) => {
  if (!generation.has(cb)) generation.set(cb, current)
  const g = generation.get(cb)
  return native((t) => {
    if (!closed.has(g)) cb(t)
  })
}

// beginBuild открывает поколение: всё, что библиотека запросит у requestAnimationFrame до endBuild, — его.
export function beginBuild() {
  current = ++next
  return current
}

export function endBuild() {
  current = 0
}

// closeBuild гасит цикл отрисовки поколения (вызывается после destroy книги).
export function closeBuild(g) {
  closed.add(g)
}

// loadFlipLib подключает скрипт библиотеки и отдаёт её глобальный объект St.
export function loadFlipLib(src) {
  if (window.St) return Promise.resolve(window.St)
  return new Promise((resolve, reject) => {
    const head = document.head
    const sink = document.createElement('head')
    Object.defineProperty(document, 'head', { configurable: true, get: () => sink })
    const restore = () => {
      delete document.head
    }
    const script = document.createElement('script')
    script.src = src
    script.addEventListener('load', () => {
      restore()
      if (window.St && window.St.PageFlip) resolve(window.St)
      else reject(new Error('нет St.PageFlip'))
    })
    script.addEventListener('error', () => {
      restore()
      reject(new Error('библиотека не загрузилась'))
    })
    head.appendChild(script)
  })
}
