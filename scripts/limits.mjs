// Пределы размера файлов — единственный источник. Читают: scripts/check.sh и scripts/git-hooks/pre-commit
// (CLI), scripts/precheck.mjs и хук размера при правке (import). Менять пределы — только здесь и с согласия
// владельца: исполнитель, упёршийся в предел, склонен его поднять вместо того, чтобы разрезать файл.
//
// Go: пакет — единица смысла, счётчика файлов в папке нет; файл кода — предупреждение 600, провал 800 строк,
// без предела в КБ (кириллица в комментариях — 2 байта на знак); тест — 1200 строк.
// Просмотрщик (HTML/JS/CSS без сборщика) и скрипты — 600 строк / 30 КБ; тест скрипта — 900 строк.
// Чужое (вендор StPageFlip), тестовые данные и приватная зона не проверяются.
//
// CLI: node scripts/limits.mjs sizes [--cached] — пусто и код 0, если чисто; иначе список и код 1.
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const KB = 1024
const SKIP = /(^|\/)(testdata|vendor|node_modules|\.gotmp|docs\/private|deploy\/inventory)\/|\.min\.js$/
const CODE = /\.(go|js|mjs|html|css|py|sh)$/
const TEST = /(_test\.go|\.test\.mjs|(^|\/)test_[^/]*\.py)$/

// sizeRule — пределы файла: fail — провал проверки, warn — предупреждение хука при правке; null — не проверяется.
export function sizeRule(file) {
  const f = file.replace(/\\/g, '/')
  if (SKIP.test(f) || !CODE.test(f)) return null
  if (f.endsWith('.go')) return TEST.test(f) ? { fail: { lines: 1200 } } : { fail: { lines: 800 }, warn: { lines: 600 } }
  if (TEST.test(f)) return { fail: { lines: 900 } }
  return { fail: { lines: 600, bytes: 30 * KB } }
}

// countLines — хвостовой перевод строки не считается.
export function countLines(text) {
  return text.length === 0 ? 0 : text.replace(/\r?\n$/, '').split(/\r?\n/).length
}

// over — превышен ли предел lim ({lines, bytes}).
export function over(lim, lines, bytes) {
  return !!lim && ((!!lim.lines && lines > lim.lines) || (!!lim.bytes && bytes > lim.bytes))
}

export const limText = (lim) => `${lim.lines} строк` + (lim.bytes ? ` / ${lim.bytes / KB} КБ` : '')

// checkSizes — нарушения провального предела у файлов (пути от root).
export function checkSizes(files, root) {
  const bad = []
  for (const f of files) {
    const r = sizeRule(f)
    const p = path.join(root, f)
    if (!r || !fs.existsSync(p)) continue
    const buf = fs.readFileSync(p)
    const lines = countLines(buf.toString('utf8'))
    if (over(r.fail, lines, buf.length)) bad.push(`${f} (${lines} строк, ${(buf.length / KB).toFixed(1)} КБ; предел ${limText(r.fail)})`)
  }
  return bad
}

// gitFiles — файлы репозитория: индекс (cachedOnly) или индекс + новые неигнорируемые.
export function gitFiles(root, cachedOnly = false) {
  // quotePath=false: иначе кириллическое имя приходит в кавычках с кодами и файл молча выпадает из проверки.
  const ls = ['-c', 'core.quotePath=false', 'ls-files', '--cached']
  const args = cachedOnly ? ls : [...ls, '--others', '--exclude-standard']
  return execFileSync('git', args, { cwd: root, encoding: 'utf8', maxBuffer: 64 * KB * KB })
    .split('\n')
    .map((s) => s.trim())
    .filter((f) => f && fs.existsSync(path.join(root, f)))
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
  const [cmd, ...rest] = process.argv.slice(2)
  if (cmd !== 'sizes') {
    console.error('использование: node scripts/limits.mjs sizes [--cached]')
    process.exit(2)
  }
  const bad = checkSizes(gitFiles(root, rest.includes('--cached')), root)
  for (const b of bad) console.log(b)
  process.exit(bad.length ? 1 : 0)
}
