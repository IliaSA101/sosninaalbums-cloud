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
    this.classList = {
      add: (c) => set.add(c),
      has: (c) => set.has(c),
      toggle: (c, on) => (on ? set.add(c) : set.delete(c)),
    }
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
const { coverShift, fitSize, MAX_SPREAD_W, mountBook, stageSize } = await import('./book.js')

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
  on(ev, fn) {
    this.handlers = this.handlers || {}
    this.handlers[ev] = fn
  }
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
  // широкий монитор: разворот не шире предела, портрет предела не знает
  assert.deepEqual(fitSize(5000, 5000, 1, false), { w: MAX_SPREAD_W, h: MAX_SPREAD_W / 2 })
  assert.deepEqual(fitSize(5000, 5000, 1, true), { w: 5000, h: 5000 })
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

// --- сдвиг обложки (закрытая книга по центру) ---
// Поддельная StPageFlip со стопкой разворотов: при cover — [0], [1,2], …, [n-1] для чётного n.
class CoverFlip extends FakeFlip {
  constructor(block, opts) {
    super(block, opts)
    this.spreadIndex = opts.startPage === 0 ? 0 : 1
    this.calc = null
    this.n = 0
  }
  loadFromHTML(pages) {
    this.n = pages.length
    this.spreads = [[0]]
    for (let i = 1; i + 1 < this.n; i += 2) this.spreads.push([i, i + 1])
    this.spreads.push([this.n - 1])
  }
  getPageCollection() {
    return { getSpread: () => this.spreads, getCurrentSpreadIndex: () => this.spreadIndex }
  }
  getFlipController() {
    return { getCalculation: () => this.calc }
  }
  getRender() {
    return { getRect: () => ({ pageWidth: 100 }) }
  }
  go(spreadIndex) {
    this.spreadIndex = spreadIndex
    this.handlers.flip({ data: this.spreads[spreadIndex][0] })
  }
}
const coverList = (n) => Array.from({ length: n }, (_, i) => page(i + 1, { role: i === 0 ? 'front' : i === n - 1 ? 'back' : 'page' }))
const mountCover = (n, extra = {}) => {
  const book = mountBook({
    St: { PageFlip: CoverFlip },
    stage,
    list: coverList(n),
    ratio: 1,
    cover: true,
    portrait: false,
    start: 0,
    reduced: false,
    onFlip() {},
    onBroken() {},
    ...extra,
  })
  const flip = created.at(-1)
  const holder = stage.children.at(-1)
  if (!extra.noInit) flip.handlers.init({ data: { page: extra.start || 0 } })
  return { book, flip, holder }
}

test('coverShift: одиночная передняя — влево, задняя — вправо, остальное на месте', () => {
  assert.equal(coverShift(true, false, 8, [0]), -0.25)
  assert.equal(coverShift(true, false, 8, [1, 2]), 0)
  assert.equal(coverShift(true, false, 8, [5, 6]), 0)
  assert.equal(coverShift(true, false, 8, [7]), 0.25)
  assert.equal(coverShift(false, false, 8, [0]), 0) // без обложки
  assert.equal(coverShift(true, true, 8, [0]), 0) // вертикально
  assert.equal(coverShift(true, false, 8, null), 0)
  assert.equal(coverShift(true, false, 1, [0]), 0)
})

test('book: сдвиг на позициях 0, 1, n-1 (последний разворот) и n (задняя обложка)', () => {
  const { holder, flip } = mountCover(8)
  assert.equal(holder.style.transform, 'translateX(-25%)') // позиция 0: передняя обложка по центру
  assert.ok(!holder.classList.has('book-glide')) // первая сборка — без съезда
  flip.go(1) // позиция 1: разворот 2–3
  assert.equal(holder.style.transform, '')
  assert.ok(holder.classList.has('book-glide')) // переход без листа (Home/End) — плавно
  flip.go(flip.spreads.length - 2) // последний разворот (n-1 страниц слева)
  assert.equal(holder.style.transform, '')
  flip.go(flip.spreads.length - 1) // позиция n: задняя обложка одна, слева
  assert.equal(holder.style.transform, 'translateX(25%)')
  flip.go(0)
  assert.equal(holder.style.transform, 'translateX(-25%)')
})

test('book: сдвиг идёт вместе с переворотом, отмена жеста возвращает', () => {
  const { flip, holder } = mountCover(8)
  const raf = frames.length
  flip.handlers.changeState({ data: 'fold_corner' }) // подгиб уголка — не переворот
  assert.equal(frames.length, raf)
  flip.handlers.changeState({ data: 'user_fold' })
  assert.equal(frames.length, raf + 1)
  flush() // calc ещё нет — ждём
  flip.calc = { getDirection: () => 0, getPosition: () => ({ x: 0, y: 0 }) } // середина: x = 0 при pageWidth = 100
  flush()
  assert.equal(holder.style.transform, 'translateX(-12.5%)') // полпути от -25% до 0
  flip.calc = { getDirection: () => 0, getPosition: () => ({ x: 100, y: 0 }) } // вернули на место
  flush()
  assert.equal(holder.style.transform, 'translateX(-25%)')
  flip.handlers.changeState({ data: 'read' }) // отпустили, обложка осталась
  assert.equal(holder.style.transform, 'translateX(-25%)')
  assert.ok(!holder.classList.has('book-glide')) // конец покадрового переворота встаёт сразу
  flush()
  assert.equal(frames.length, raf) // цикл слежения остановлен (остался чужой цикл из первого теста)
})

test('book: вперёд с последнего разворота к задней обложке', () => {
  const { flip, holder } = mountCover(8, { start: 4 })
  assert.equal(holder.style.transform, '') // 3–4 из разворотов
  flip.spreadIndex = flip.spreads.length - 2
  flip.handlers.changeState({ data: 'flipping' })
  flip.calc = { getDirection: () => 0, getPosition: () => ({ x: 0, y: 0 }) }
  flush()
  assert.equal(holder.style.transform, 'translateX(12.5%)') // вперёд к задней: от 0 к +25%
  flip.go(flip.spreads.length - 1)
  flip.handlers.changeState({ data: 'read' })
  assert.equal(holder.style.transform, 'translateX(25%)')
})

test('book: листание назад — с задней обложки и с разворота 1 на переднюю', () => {
  const { flip, holder } = mountCover(8)
  const last = flip.spreads.length - 1
  flip.go(last) // стоим на задней обложке: +25 %
  assert.equal(holder.style.transform, 'translateX(25%)')
  flip.handlers.changeState({ data: 'flipping' })
  flip.calc = { getDirection: () => 1, getPosition: () => ({ x: 0, y: 0 }) } // назад, середина
  flush()
  assert.equal(holder.style.transform, 'translateX(12.5%)') // от +25 % к 0
  flip.calc = { getDirection: () => 1, getPosition: () => ({ x: -100, y: 0 }) } // лист перевёрнут
  flush()
  assert.equal(holder.style.transform, '')
  flip.go(last - 1)
  flip.handlers.changeState({ data: 'read' })
  assert.equal(holder.style.transform, '')

  flip.go(1) // разворот 1 → передняя обложка: 0 → −25 %
  flip.handlers.changeState({ data: 'flipping' })
  flip.calc = { getDirection: () => 1, getPosition: () => ({ x: 0, y: 0 }) }
  flush()
  assert.equal(holder.style.transform, 'translateX(-12.5%)')
  flip.calc = { getDirection: () => 1, getPosition: () => ({ x: -100, y: 0 }) }
  flush()
  assert.equal(holder.style.transform, 'translateX(-25%)')
  flip.go(0)
  flip.handlers.changeState({ data: 'read' })
  assert.equal(holder.style.transform, 'translateX(-25%)')
})

test('book: обложка по центру сразу при сборке, до init; цикл слежения не удваивается', () => {
  const { flip, holder } = mountCover(8, { noInit: true })
  assert.equal(holder.style.transform, 'translateX(-25%)')
  flush()
  const base = frames.length
  flip.handlers.changeState({ data: 'flipping' })
  flip.handlers.changeState({ data: 'read' }) // остановили, пока кадр заказан…
  flip.handlers.changeState({ data: 'flipping' }) // …и сразу новый жест
  assert.equal(frames.length, base + 1) // кадр один, а не два
  flip.handlers.changeState({ data: 'read' })
  flush()
})

test('book: корешок только у разворота, лист-заполнение по флагу страницы', () => {
  assert.ok(mountCover(8).holder.classList.has('book-spread'))
  assert.ok(!mountCover(8, { portrait: true }).holder.classList.has('book-spread'))
  const list = [page(1, { role: 'front', fill: true }), page(2), page(3, { role: 'back' })]
  mountBook({ St: { PageFlip: FakeFlip }, stage, list, ratio: 1, cover: false, portrait: false, start: 0, reduced: false, onFlip() {}, onBroken() {} })
  const book = stage.children.at(-1).children[0].children // страницы книги
  assert.ok(book[0].classList.has('page-fill'))
  assert.ok(!book[1].classList.has('page-fill'))
  assert.ok(!book[2].classList.has('page-fill'))
})

test('book: без обложки, вертикально и при reduced — без сдвига и без анимации', () => {
  const rafBefore = frames.length
  const plain = mount(coverList(8), () => {})
  assert.equal(stage.children.at(-1).style.transform, undefined)
  plain.destroy()
  const flipPlain = created.at(-1)
  assert.equal(flipPlain.opts.showCover, false)

  const portrait = mountCover(8, { portrait: true })
  portrait.flip.handlers.changeState({ data: 'flipping' })
  portrait.flip.go(1)
  assert.equal(portrait.holder.style.transform, undefined)
  assert.equal(frames.length, rafBefore)

  const reduced = mountCover(8, { reduced: true })
  assert.equal(reduced.holder.style.transform, 'translateX(-25%)') // позиция всё равно по центру…
  reduced.flip.handlers.changeState({ data: 'flipping' })
  assert.equal(frames.length, rafBefore) // …но покадрового движения нет
  reduced.flip.go(1)
  assert.equal(reduced.holder.style.transform, '')
  assert.ok(!reduced.holder.classList.has('book-glide')) // и плавного съезда нет
})

test('book: после destroy слежение молча обрывается', () => {
  const { book, flip, holder } = mountCover(8)
  flush() // остатки прежних тестов: остаётся только чужой цикл из первого теста
  const base = frames.length
  flip.handlers.changeState({ data: 'flipping' })
  flip.calc = { getDirection: () => 0, getPosition: () => ({ x: 0, y: 0 }) }
  book.destroy()
  flush()
  assert.equal(holder.style.transform, 'translateX(-25%)')
  assert.equal(frames.length, base)
})
