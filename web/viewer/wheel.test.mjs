// Проверка wheel.js без браузера: `node --test 'web/viewer/*.test.mjs'`. Время подаём руками, поэтому инерцию тачпада
// (убывающие delta каждые 16 мс) можно воспроизвести точно. Настоящие пороги на тачпаде Mac — ручная проверка (005-27).
import assert from 'node:assert/strict'
import test from 'node:test'

import { createWheel, isZoomed, QUIET, THRESHOLD } from './wheel.js'

const ev = (deltaY, extra = {}) => ({ deltaX: 0, deltaY, deltaMode: 0, ctrlKey: false, zoomed: false, ...extra })

// run подаёт серию событий и возвращает все ответы, кроме 'eat'.
function run(w, events) {
  return events.map(([e, t]) => w.feed(e, t)).filter((a) => a !== 'eat')
}

// Жест тачпада: delta затухает от start к нулю каждые 16 мс в течение ms.
function inertia(sign, start, ms, t0) {
  const out = []
  for (let t = 0; t <= ms; t += 16) {
    const d = Math.round(start * (1 - t / ms))
    if (d !== 0) out.push([ev(sign * d), t0 + t])
  }
  return out
}

test('щелчок мыши: вниз — вперёд, вверх — назад', () => {
  assert.equal(createWheel().feed(ev(100), 0), 'next')
  assert.equal(createWheel().feed(ev(-100), 0), 'prev')
  assert.equal(createWheel().feed(ev(THRESHOLD), 0), 'next')
})

test('ниже порога — ничего, но событие гасится', () => {
  const w = createWheel()
  assert.equal(w.feed(ev(THRESHOLD - 1), 0), 'eat')
  assert.equal(w.feed(ev(0), 20), 'eat')
})

test('мелкие события копятся до порога', () => {
  const w = createWheel()
  assert.deepEqual(run(w, [[ev(10), 0], [ev(10), 16], [ev(10), 32], [ev(10), 48]]), ['next'])
})

test('жест с инерцией 1,5 с — одна страница', () => {
  const w = createWheel()
  assert.deepEqual(run(w, inertia(1, 60, 1500, 1000)), ['next'])
  assert.deepEqual(run(createWheel(), inertia(-1, 60, 1500, 1000)), ['prev'])
})

test('колесо без паузы — одна страница до паузы', () => {
  const w = createWheel()
  const evs = []
  for (let i = 0; i < 20; i++) evs.push([ev(100), i * 100]) // 2 с щелчков каждые 100 мс
  assert.deepEqual(run(w, evs), ['next'])
})

test('два жеста с паузой не меньше quiet — две страницы', () => {
  const w = createWheel()
  const first = inertia(1, 60, 600, 0)
  const lastT = first[first.length - 1][1]
  const second = inertia(1, 60, 600, lastT + QUIET)
  assert.deepEqual(run(w, [...first, ...second]), ['next', 'next'])
})

test('пауза короче quiet жест не завершает', () => {
  const w = createWheel()
  assert.deepEqual(run(w, [[ev(100), 0], [ev(100), QUIET - 1], [ev(100), 2 * QUIET - 2]]), ['next'])
})

test('пауза сбрасывает и накопленное ниже порога', () => {
  const w = createWheel()
  assert.equal(w.feed(ev(30), 0), 'eat')
  assert.equal(w.feed(ev(30), QUIET), 'eat') // 30 + 30 было бы 60, но это уже другой жест
  assert.equal(w.feed(ev(30), QUIET + 16), 'next')
})

test('ctrlKey и zoomed — пропуск и состояние не трогают', () => {
  const w = createWheel()
  assert.equal(w.feed(ev(500, { ctrlKey: true }), 0), 'pass')
  assert.equal(w.feed(ev(500, { zoomed: true }), 1), 'pass')
  assert.equal(w.feed(ev(500, { ctrlKey: true, zoomed: true }), 2), 'pass')
  assert.equal(w.feed(ev(100), 3), 'next') // пропущенные события ничего не накопили и не заперли
})

test('deltaMode 1 (строки, Firefox): 3 строки — страница', () => {
  assert.equal(createWheel().feed(ev(3, { deltaMode: 1 }), 0), 'next')
  assert.equal(createWheel().feed(ev(-3, { deltaMode: 1 }), 0), 'prev')
  assert.equal(createWheel().feed(ev(2, { deltaMode: 1 }), 0), 'eat') // 32 px < 40
})

test('deltaMode 2 (страницы): множитель — высота страницы', () => {
  assert.equal(createWheel({ pagePx: 800 }).feed(ev(1, { deltaMode: 2 }), 0), 'next')
  assert.equal(createWheel().feed(ev(0.2, { deltaMode: 2, pagePx: 300 }), 0), 'next') // ev.pagePx важнее: 0,2 × 300 = 60 px
  assert.equal(createWheel().feed(ev(0.1, { deltaMode: 2, pagePx: 300 }), 0), 'eat') // 30 px < 40
})

test('доминирующая ось: больше по модулю', () => {
  const w = () => createWheel()
  assert.equal(w().feed({ ...ev(5), deltaX: 60 }, 0), 'next')
  assert.equal(w().feed({ ...ev(5), deltaX: -60 }, 0), 'prev')
  assert.equal(w().feed({ ...ev(60), deltaX: 5 }, 0), 'next')
  assert.equal(w().feed({ ...ev(-60), deltaX: 5 }, 0), 'prev')
  // горизонтальная мелочь при вертикальном жесте не листает в другую сторону
  assert.equal(w().feed({ ...ev(60), deltaX: -50 }, 0), 'next')
})

test('диагональный жест: оси не складываются', () => {
  const w = createWheel()
  // по 10 по обеим осям с разными знаками: вертикаль доминирует (10 > 9), и до порога по ней копится
  const got = run(w, [0, 16, 32, 48].map((t) => [{ ...ev(10), deltaX: -9 }, t]))
  assert.deepEqual(got, ['next'])
})

test('смена знака внутри жеста — копим заново, до тишины не листаем', () => {
  const w = createWheel()
  assert.equal(w.feed(ev(30), 0), 'eat')
  assert.equal(w.feed(ev(-30), 16), 'eat') // не 0 и не -… : копим заново с -30
  assert.equal(w.feed(ev(-30), 32), 'prev') // -60
  // после срабатывания замок: встречное движение внутри того же жеста ничего не листает
  assert.equal(w.feed(ev(100), 48), 'eat')
  assert.equal(w.feed(ev(100), 64), 'eat')
})

test('нулевые события не продлевают жест', () => {
  const w = createWheel()
  assert.equal(w.feed(ev(100), 0), 'next')
  assert.equal(w.feed(ev(0), QUIET - 10), 'eat')
  assert.equal(w.feed(ev(100), QUIET + 1), 'next') // тишина считается от последнего ненулевого события
})

test('мусор в delta не ломает машину', () => {
  const w = createWheel()
  assert.equal(w.feed({ deltaX: NaN, deltaY: undefined, deltaMode: 0 }, 0), 'eat')
  assert.equal(w.feed(ev(100), 1), 'next')
})

test('свои пороги', () => {
  const w = createWheel({ threshold: 10, quiet: 100 })
  assert.equal(w.feed(ev(10), 0), 'next')
  assert.equal(w.feed(ev(10), 50), 'eat')
  assert.equal(w.feed(ev(10), 150), 'next')
})

test('isZoomed: только заметное увеличение', () => {
  assert.equal(isZoomed(undefined), false)
  assert.equal(isZoomed(null), false)
  assert.equal(isZoomed({ scale: 1 }), false)
  assert.equal(isZoomed({ scale: 1.0001 }), false)
  assert.equal(isZoomed({ scale: 1.5 }), true)
})
