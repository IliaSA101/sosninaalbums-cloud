// Проверка studio.js без браузера: `node --test 'web/viewer/*.test.mjs'`. Случаи сайта — общий файл
// internal/config/testdata/site_cases.json: его читают и Go-тест сервиса, и статика роли — расхождение
// трёх правил ловится тестом, а не простоем сервиса (spec 005, design §1.1).
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { safeSite, studioNode } from './studio.js'

const cases = JSON.parse(readFileSync(new URL('../../internal/config/testdata/site_cases.json', import.meta.url), 'utf8'))

// Минимальный DOM: узлы запоминают, что в них записали.
const doc = {
  createTextNode: (text) => ({ kind: 'text', text }),
  createElement: (tag) => ({ kind: 'el', tag }),
}

test('общий файл случаев прочитан и не пуст', () => {
  assert.ok(cases.length >= 50, `случаев ${cases.length}`)
  assert.ok(cases.some((c) => c.ok) && cases.some((c) => !c.ok))
})

test('site_cases.json: принятые — ссылка, отказные — текст', () => {
  for (const c of cases) {
    const n = studioNode(doc, 'Студия', c.site)
    const label = `${JSON.stringify(c.site.slice(0, 60))} (${c.why})`
    if (c.ok) {
      assert.equal(n.tag, 'a', `ждали ссылку: ${label}`)
      assert.equal(n.textContent, 'Студия')
      assert.equal(n.rel, 'noopener noreferrer')
      assert.equal(n.target, '_blank')
      assert.equal(n.referrerPolicy, 'no-referrer')
      assert.equal(new URL(n.href).protocol, 'https:')
      assert.ok(n.href === c.site || n.href === `${c.site}/`, `href разошёлся со значением: ${label}`)
    } else {
      assert.deepEqual(n, { kind: 'text', text: 'Студия' }, `ждали текст: ${label}`)
      assert.equal(safeSite(c.site), '', label)
    }
  }
})

test('без сайта и с не строкой — текст', () => {
  for (const site of [undefined, null, '', 42, {}, ['https://studio.example']]) {
    assert.deepEqual(studioNode(doc, 'Студия', site), { kind: 'text', text: 'Студия' })
  }
})

test('пустая подпись — пусто, даже при годном сайте', () => {
  assert.deepEqual(studioNode(doc, '', 'https://studio.example'), { kind: 'text', text: '' })
  assert.deepEqual(studioNode(doc, undefined, 'https://studio.example'), { kind: 'text', text: '' })
})

test('подпись режется до 200 знаков и идёт только в textContent', () => {
  const n = studioNode(doc, `<b>${'я'.repeat(300)}`, 'https://studio.example')
  assert.equal(n.tag, 'a')
  assert.equal(Array.from(n.textContent).length, 200)
  assert.ok(n.textContent.startsWith('<b>'))
  assert.equal(n.innerHTML, undefined)
})
