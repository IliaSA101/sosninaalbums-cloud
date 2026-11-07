// Проверка lib.js и book.js без браузера: `node --test web/viewer/engine.test.mjs`.
// Окружение поддельное: очередь кадров, минимальный DOM и StPageFlip-заглушка. Настоящий браузер — по сценарию стенда.
import assert from 'node:assert/strict'
import test from 'node:test'

// --- поддельное окружение (до импорта модулей: lib.js подменяет requestAnimationFrame при загрузке) ---
const frames = []
globalThis.window = globalThis
globalThis.requestAnimationFrame = (cb) => frames.push(cb)
const flush = () => frames.splice(0).forEach((cb) => cb(0))

class Node {
  constructor(tag) {
    this.tag = tag
    this.children = []
    this.attrs = {}
    this.handlers = {}
    this.dataset = {}
    this.style = {}
    this.className = ''
    const set = new Set()
    this.classList = { add: (c) => set.add(c), has: (c) => set.has(c) }
  }
  get src() {
    return this.attrs.src
  }
  set src(v) {
    this.attrs.src = v
  }
  removeAttribute(k) {
    delete this.attrs[k]
  }
  appendChild(c) {
    this.children.push(c)
  }
  addEventListener(ev, fn) {
    this.handlers[ev] = fn
  }
  remove() {
    this.removed = true
  }
}
globalThis.document = { createElement: (t) => new Node(t) }
globalThis.getComputedStyle = () => ({ paddingLeft: '12px', paddingRight: '12px', paddingTop: '8px', paddingBottom: '8px' })

const { beginBuild, closeBuild, endBuild } = await import('./lib.js')
const { fitSize, mountBook, stageSize } = await import('./book.js')

test('lib: цикл закрытого поколения обрывается, чужие живут', () => {
  let a = 0
  let b = 0
  let other = 0
  const loopA = () => {
    a++
    requestAnimationFrame(loopA)
  }
  const loopB = () => {
    b++
    requestAnimationFrame(loopB)
  }
  const loopOther = () => {
    other++
    requestAnimationFrame(loopOther)
  }
  const ga = beginBuild()
  requestAnimationFrame(loopA)
  endBuild()
  const gb = beginBuild()
  requestAnimationFrame(loopB)
  endBuild()
  requestAnimationFrame(loopOther) // вне сборки — поколение 0, не закрывается
  flush()
  flush()
  assert.deepEqual([a, b, other], [2, 2, 2])
  closeBuild(ga)
  flush()
  flush()
  assert.deepEqual([a, b, other], [2, 4, 4])
  closeBuild(gb)
  flush()
  assert.deepEqual([a, b, other], [2, 4, 5])
  assert.notEqual(ga, gb)
})

// --- book.js ---
const stage = new Node('main')
stage.clientWidth = 400
stage.clientHeight = 300
let created = []
class FakeFlip {
  constructor(block, opts) {
    this.opts = opts
    created.push(this)
  }
  loadFromHTML() {}
  on() {}
  destroy() {}
  getCurrentPageIndex() {
    return 0
  }
}
const page = (n, extra = {}) => ({ n, role: 'page', blank: false, file: `${String(n).padStart(64, '0')}.jpg`, ...extra })
const mount = (list, onBroken, portrait = false) =>
  mountBook({ St: { PageFlip: FakeFlip }, stage, list, ratio: 1, cover: false, portrait, start: 0, reduced: false, onFlip() {}, onBroken })

test('book: размер без отступов сцены', () => {
  assert.deepEqual(stageSize(stage), { w: 376, h: 284 })
  assert.deepEqual(fitSize(376, 284, 1, false), { w: 376, h: 188 })
  assert.deepEqual(fitSize(376, 284, 1, true), { w: 284, h: 284 })
})

test('book: ошибки по номеру страницы, после destroy не учитываются', () => {
  const list = [page(1), page(2, { blank: true, file: '' }), page(3)]
  const broken = []
  const book = mount(list, (n) => broken.push(n))
  const imgs = []
  const collect = (node) => {
    if (node.tag === 'img') imgs.push(node)
    node.children.forEach(collect)
  }
  collect(stage)
  assert.equal(imgs.length, 2)
  imgs[1].handlers.error()
  assert.deepEqual(broken, [3]) // номер страницы, а не индекс 2
  book.fail(3)
  book.fail(99) // нет такой — тихо
  book.fail(2) // белая — тоже
  book.destroy()
  assert.ok(imgs.every((i) => i.src === undefined)) // догрузка погашена
  imgs[0].handlers.error()
  assert.deepEqual(broken, [3]) // ошибка старой сборки проигнорирована
  assert.doesNotThrow(() => book.fail(3))
})

test('book: список короче прежнего — fail по номеру не падает', () => {
  const portraitList = [page(1), page(3), page(4)] // белые вертикально выброшены
  const book = mount(portraitList, () => {}, true)
  assert.doesNotThrow(() => book.fail(5))
  assert.equal(created.at(-1).opts.usePortrait, true)
})
