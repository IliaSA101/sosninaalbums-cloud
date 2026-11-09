// Проверка fullscreen.js без браузера: `node --test web/viewer/fullscreen.test.mjs`.
import assert from 'node:assert/strict'
import test from 'node:test'
import { canFullscreen, toggleFullscreen } from './fullscreen.js'

test('кнопка полного экрана нужна только там, где есть requestFullscreen', () => {
  assert.equal(canFullscreen({ fullscreenEnabled: true }, {}), false) // нет API (iPhone) — кнопки нет
  assert.equal(canFullscreen({ fullscreenEnabled: true }, { requestFullscreen() {} }), true)
  assert.equal(canFullscreen({ fullscreenEnabled: false }, { requestFullscreen() {} }), false) // запрещён политикой
  assert.equal(canFullscreen(undefined, { requestFullscreen() {} }), false)
})

test('вход и выход; отказ браузера не превращается в ошибку', async () => {
  const calls = []
  const el = { requestFullscreen: () => Promise.resolve(calls.push('enter')) }
  const doc = { fullscreenElement: null, exitFullscreen: () => Promise.resolve(calls.push('exit')) }
  await toggleFullscreen(doc, el)
  doc.fullscreenElement = el
  await toggleFullscreen(doc, el)
  assert.deepEqual(calls, ['enter', 'exit'])
  const denied = { requestFullscreen: () => Promise.reject(new Error('denied')) }
  await assert.doesNotReject(toggleFullscreen({ fullscreenElement: null }, denied))
})
