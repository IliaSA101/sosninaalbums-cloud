// Быстрая предпроверка для исполнителей перед отчётом: `node scripts/precheck.mjs [--base <rev>]`.
// Смотрит только изменённое относительно base (по умолчанию HEAD: незакоммиченное и новые файлы) и ловит то,
// что иначе падает лишь на полном scripts/check.sh: gofmt, go vet, golangci-lint (только новое),
// go test -short изменённых пакетов, синтаксис ES-модулей просмотрщика, shellcheck, пределы размера.
// Полный go test с -race, govulncheck и проверки всего дерева — только в scripts/check.sh.
// Пределы — scripts/limits.mjs (один источник с check.sh и pre-commit).
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { checkSizes } from './limits.mjs'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
process.chdir(root)
fs.mkdirSync(path.join(root, '.gotmp'), { recursive: true })

const argv = process.argv.slice(2)
const bi = argv.indexOf('--base')
const base = bi >= 0 ? argv[bi + 1] : 'HEAD'
if (!base || base.startsWith('-')) {
  console.error('использование: node scripts/precheck.mjs [--base <rev>]')
  process.exit(2)
}

const run = (cmd, args, opts = {}) => spawnSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 << 20, ...opts })
const lines = (s) => s.split('\n').map((x) => x.trim()).filter(Boolean)
// quotePath=false: иначе кириллическое имя приходит в кавычках с кодами и выпадает из проверок.
const git = (args) => run('git', ['-c', 'core.quotePath=false', ...args])
// gofmt того же тулчейна, что go (toolchain в go.mod): другая версия форматирует иначе.
const gofmt = () => {
  const p = path.join(run('go', ['env', 'GOROOT']).stdout?.trim() || '', 'bin', 'gofmt')
  return fs.existsSync(p) ? p : 'gofmt'
}
// Инструмент из PATH или из GOPATH/bin (golangci-lint ставят через go install).
const tool = (name) => {
  if (run('sh', ['-c', `command -v ${name}`]).status === 0) return name
  const gopath = run('go', ['env', 'GOPATH']).stdout?.trim()
  const p = gopath ? path.join(gopath, 'bin', name) : ''
  return p && fs.existsSync(p) ? p : null
}

// В репозитории без коммитов HEAD нет — тогда изменено всё отслеживаемое и новое.
const hasBase = git(['rev-parse', '--verify', '--quiet', base]).status === 0
const diff = hasBase
  ? git(['diff', '--name-only', '--diff-filter=ACMR', base])
  : git(['ls-files', '--cached'])
if (diff.status !== 0) {
  console.error(`precheck: список изменённых файлов не получен: ${diff.stderr.trim()}`)
  process.exit(2)
}
const changed = [...new Set([...lines(diff.stdout), ...lines(git(['ls-files', '--others', '--exclude-standard']).stdout)])]
  .filter((f) => fs.existsSync(f))

const fails = []
const ok = []
const step = (name, res) => {
  if (res === true) return ok.push(name)
  fails.push(`FAIL ${name}\n${String(res).trim().split('\n').slice(-30).map((l) => `     ${l}`).join('\n')}`)
}
// Шаг провален при ненулевом коде или (emptyOut) при любом выводе.
const exec = (name, cmd, args, emptyOut = false) => {
  const r = run(cmd, args)
  const out = `${r.stdout ?? ''}${r.stderr ?? ''}${r.error ? r.error.message : ''}`
  step(name, r.status === 0 && !(emptyOut && out.trim()) ? true : out || `код ${r.status}`)
}

const goFiles = changed.filter((f) => f.endsWith('.go'))
const goTouched = goFiles.length > 0 || changed.some((f) => /(^|\/)go\.(mod|sum)$/.test(f))
// Пока в репозитории нет ни одного .go-файла, vet и линтер падают на «no packages» — шаги Go не к чему применять.
const goAny = lines(git(['ls-files', '--cached', '--others', '--exclude-standard', '--', '*.go']).stdout).length > 0
if (goTouched && goAny && fs.existsSync('go.mod')) {
  if (goFiles.length) exec('gofmt', gofmt(), ['-l', ...goFiles], true)
  exec('go vet', 'go', ['vet', './...'])
  const gcl = tool('golangci-lint')
  if (!gcl) step('golangci-lint', 'не установлен')
  else exec('golangci-lint (новое)', gcl, hasBase ? ['run', `--new-from-rev=${base}`, './...'] : ['run', './...'])
  const pkgs = [...new Set(goFiles.map((f) => `./${path.dirname(f)}`))]
  if (pkgs.length) exec('go test -short', 'go', ['test', '-short', ...pkgs])
}

// `node --check файл.js` на ES-модуле молча возвращает 0 даже для битого файла — только через stdin с --input-type=module.
const js = changed.filter((f) => /^web\/.*\.m?js$/.test(f) && !f.includes('/vendor/'))
if (js.length) {
  const bad = []
  for (const f of js) {
    const r = run('node', ['--check', '--input-type=module'], { input: fs.readFileSync(f) })
    if (r.status !== 0) bad.push(`${f}: ${lines(r.stderr).slice(0, 4).join(' | ')}`)
  }
  step('синтаксис web', bad.length ? bad.join('\n') : true)
}

const sh = changed.filter((f) => f.endsWith('.sh') || f.startsWith('scripts/git-hooks/'))
if (sh.length) {
  const sc = tool('shellcheck')
  // -x: по одному изменённому файлу shellcheck иначе не видит подключаемые (`. ops/guard.sh`) и даёт SC1091/SC2034,
  // которых нет в полном прогоне check.sh, где файлы идут все сразу («да» владельца 31.10).
  if (sc) exec('shellcheck', sc, ['-x', ...sh])
  else step('shellcheck', 'не установлен')
}

const big = checkSizes(changed, root)
step('размер файлов', big.length ? big.join('\n') : true)

if (fails.length) {
  console.log(fails.join('\n'))
  console.log(`precheck: FAIL (${fails.length}), OK: ${ok.join(', ') || '—'}`)
  process.exit(1)
}
console.log(`precheck: OK (${ok.join(', ')}; файлов изменено: ${changed.length})`)
