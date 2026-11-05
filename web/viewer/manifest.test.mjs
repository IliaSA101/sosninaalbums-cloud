// Проверка manifest.js: `node --test web/viewer/manifest.test.mjs`.
import assert from 'node:assert/strict'
import test from 'node:test'
import { clip, counterText, displayPages, indexOfPage, parseManifest, sameBook, visibleIndexes, wantedIndexes } from './manifest.js'

const hash = (c) => c.repeat(64) + '.jpg'
// Обложка, форзац (белая стр. 2), разворот 3–4, белая 5, задняя 6: K = 2N + 2, N = 2.
const raw = () => ({
  schema: 1,
  title: 'T',
  book: { cover: true, page_mm: { w: 300, h: 300 } },
  pages: [
    { n: 1, role: 'front', file: hash('a') },
    { n: 2, role: 'page', blank: true },
    { n: 3, role: 'page', file: hash('b') },
    { n: 4, role: 'page', file: hash('c') },
    { n: 5, role: 'page', blank: true },
    { n: 6, role: 'back', file: hash('d') },
  ],
})

test('разбор и порядок', () => {
  const m = parseManifest(raw())
  assert.equal(m.pages.length, 6)
  assert.equal(m.ratio, 1)
  assert.equal(displayPages(m, false).length, 6)
  assert.deepEqual(displayPages(m, true).map((p) => p.n), [1, 3, 4, 6]) // вертикально белые пропущены
  assert.equal(indexOfPage(displayPages(m, true), 2), 0) // белая → ближайшая предыдущая
  assert.equal(indexOfPage(displayPages(m, true), 4), 2)
})

test('чужие имена файлов и пути отвергаются', () => {
  for (const file of ['../x.jpg', 'http://evil/x.jpg', '//evil/x.jpg', hash('a') + '?x=1', 'a.jpg', hash('A'), '/' + hash('a')]) {
    const r = raw()
    r.pages[2].file = file
    assert.throws(() => parseManifest(r), /имя файла/, file)
  }
})

test('структура: схема, номера, роли, обложка', () => {
  const mut = (f) => {
    const r = raw()
    f(r)
    return r
  }
  assert.throws(() => parseManifest(mut((r) => (r.schema = 2))))
  assert.throws(() => parseManifest(mut((r) => (r.pages[1].n = 7))))
  assert.throws(() => parseManifest(mut((r) => (r.pages[2].role = 'x'))))
  assert.throws(() => parseManifest(mut((r) => (r.book.cover = false))))
  assert.throws(() => parseManifest(mut((r) => (r.pages[0].blank = true))))
  assert.throws(() => parseManifest(mut((r) => (r.book.page_mm.w = 0))))
  assert.throws(() => parseManifest(null))
  assert.throws(() => parseManifest(mut((r) => (r.pages = []))))
})

test('подписи и видимые страницы', () => {
  const m = parseManifest(raw())
  const l = m.pages
  assert.deepEqual(visibleIndexes(m, l, 0, false), [0])
  assert.deepEqual(visibleIndexes(m, l, 1, false), [1, 2])
  assert.deepEqual(visibleIndexes(m, l, 5, false), [5])
  assert.deepEqual(visibleIndexes(m, l, 2, true), [2])
  assert.equal(counterText(m, [l[0]]), 'Обложка')
  assert.equal(counterText(m, [l[5]]), 'Задняя обложка')
  assert.equal(counterText(m, [l[1], l[2]]), '1–2 из 4')
  assert.equal(counterText(m, [l[2]]), '2 из 4')
})

test('ленивая загрузка и сравнение книг', () => {
  assert.deepEqual(wantedIndexes(100, 50, false)[0], 50) // текущая — первой
  assert.ok(wantedIndexes(100, 50, false).length <= 7)
  assert.deepEqual(wantedIndexes(3, 0, true), [0, 1, 2])
  assert.ok(sameBook(parseManifest(raw()), parseManifest(raw())))
  const r = raw()
  r.pages[3].file = hash('e')
  assert.ok(!sameBook(parseManifest(raw()), parseManifest(r)))
})

test('без обложки: пары с первой страницы, одиночных нет', () => {
  const r = raw()
  r.book.cover = false
  r.pages = r.pages.slice(1, 5).map((p, i) => ({ ...p, n: i + 1, role: 'page' }))
  const m = parseManifest(r)
  assert.equal(m.cover, false)
  assert.deepEqual(visibleIndexes(m, m.pages, 0, false), [0, 1])
  assert.deepEqual(visibleIndexes(m, m.pages, 2, false), [2, 3])
  assert.equal(counterText(m, [m.pages[0], m.pages[1]]), '1–2 из 4')
  assert.equal(counterText(m, [m.pages[3]]), '4 из 4')
})

test('clip режет по знакам, а не по половинкам эмодзи', () => {
  const s = '😀'.repeat(3)
  assert.equal(clip(s, 2), '😀😀')
  assert.equal(clip('abc', 5), 'abc')
  const r = raw()
  r.title = '😀'.repeat(250)
  assert.equal(Array.from(parseManifest(r).title).length, 200)
})
