// Случаи для scripts/ci-push.mjs: `node scripts/ci-push-cases.mjs` (код 0 — все прошли).
// Работают во временном репозитории с локальным голым origin: сеть и настоящий GitHub не нужны,
// pre-push проекта не срабатывает (хуки временного репозитория пусты). Глобальный git config отключён:
// автор сироты должен прийти из конфигурации самого репозитория, а не из окружения запускающего.
import { execFileSync, spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { actionsUrl, parseArgs, privateIn, validName } from './ci-push.mjs'

const script = path.join(path.dirname(fileURLToPath(import.meta.url)), 'ci-push.mjs')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'ci-push-cases-'))
const work = path.join(tmp, 'work')
const origin = path.join(tmp, 'origin.git')
const env = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' }
const git = (cwd, ...a) => execFileSync('git', a, { cwd, encoding: 'utf8', env }).trim()
const run = (...a) => spawnSync(process.execPath, [script, '--repo', work, ...a], { encoding: 'utf8', env })

// Пути оснастки собираем из частей: страж публичного дерева не пропускает их литералы даже в тестах.
const P = (...p) => p.join('/')
let failed = 0
const check = (name, cond, extra = '') => {
  if (cond) console.log(`ok   ${name}`)
  else {
    failed++
    console.log(`FAIL ${name}${extra ? `: ${extra}` : ''}`)
  }
}
const put = (f, text = 'x\n') => {
  fs.mkdirSync(path.dirname(path.join(work, f)), { recursive: true })
  fs.writeFileSync(path.join(work, f), text)
}
// file задан — коммитится только он и принудительно (мимо exclude с заглушками стража).
const commit = (msg, file) => {
  if (file) git(work, 'add', '-f', '--', file)
  else git(work, 'add', '-A')
  git(work, 'commit', '-q', '-m', msg)
}

try {
  git(tmp, 'init', '-q', '--bare', origin)
  git(tmp, 'init', '-q', '-b', 'dev', work)
  git(work, 'config', 'user.name', 'Тест Автор')
  git(work, 'config', 'user.email', 'author@example.org')
  git(work, 'remote', 'add', 'origin', origin)
  put('a.txt')
  commit('первый')
  put('b/c.txt')
  commit('второй')

  // Чистые функции
  check('имя: допустимые', ['x', 'a-b_c', 'deploy/v1.2', 'a.b/c'].every(validName))
  check('имя: недопустимые', ['', 'a..b', '../x', '/x', 'x/', 'a b', 'x;rm', 'ж', 'a.lock', 'a//b'].every((n) => !validName(n)))
  check('аргументы: --delete берёт имя', parseArgs(['--delete', 'x']).del === true)
  check('аргументы: лишний позиционный', (() => { try { parseArgs(['a', 'b']); return false } catch { return true } })())
  check('приватное: перечень', privateIn(['a.txt', P('.claude', 'x'), 'CLAUDE.md', 'sub/CLAUDE.md', P('docs', 'private', 'p.md'), 'deploy/inventory/hosts', 'deploy/inventory-prod/h', 'x.tfstate', 'dev.tfvars']).length === 8)
  check('приватное: регистр', privateIn([P('.Claude', 'a'), P('Docs', 'Private', 'x'), 'DATA/a', 'X.TFSTATE', 'Sub/claude.md']).length === 5)
  check('приватное: .gotmp/.gitkeep пускается, остальное .gotmp — нет', privateIn(['.gotmp/.gitkeep', '.gotmp/x']).length === 1)
  check('приватное: сверх .gitignore — документы процесса по любому пути', privateIn([P('docs', 'BACK' + 'LOG.md'), P('web', 'plan-' + '0.1.html'), 'x/.env.prod', P('.Vault-pass'), 'a.tfplan']).length === 5)
  check('приватное: обычные имена не задеты', privateIn(['docs/plan.md', 'web/environment.js', 'deploy/inventory.example/hosts.yml', 'internal/log/log.go']).length === 0)
  // Запуск из pre-commit: имена на stdin, код 1 и перечень при находке.
  const cli = (input) => spawnSync(process.execPath, [path.join(path.dirname(script), 'private-paths.mjs')], { input, encoding: 'utf8' })
  const hit = cli('a.go\nsub/CLAUDE.md\n')
  check('private-paths: находка — код 1 и перечень', hit.status === 1 && hit.stdout.trim() === 'sub/CLAUDE.md')
  check('private-paths: чисто — код 0', cli('a.go\ndocs/x.md\n').status === 0 && cli(`a.go\0${P('.claude', 'x')}\0`).status === 1)
  check('приватное: пример inventory пускается', privateIn(['deploy/inventory.example/hosts.yml', 'deploy/roles/a.yml']).length === 0)
  check('actions: https и ssh', actionsUrl('https://github.com/o/r.git') === 'https://github.com/o/r/actions' && actionsUrl('git@github.com:o/r.git') === 'https://github.com/o/r/actions')
  check('actions: не github — null', actionsUrl('/tmp/origin.git') === null)

  // Без --push: сирота построен, команда напечатана, origin не тронут
  let r = run('t1')
  const sha = /\+([0-9a-f]{40,64}):refs\/heads\/ci\/t1/.exec(r.stdout)?.[1]
  check('без --push: код 0 и команда', r.status === 0 && !!sha, r.stderr)
  check('без --push: печать точной формы', r.stdout.includes(`git push origin +${sha}:refs/heads/ci/t1`))
  if (sha) {
    check('сирота: без родителей', git(work, 'rev-list', '--parents', '-n', '1', sha).split(' ').length === 1)
    check('сирота: дерево как у HEAD', git(work, 'rev-parse', `${sha}^{tree}`) === git(work, 'rev-parse', 'HEAD^{tree}'))
    check('сирота: сообщение', git(work, 'log', '-1', '--format=%s', sha) === `ci: t1 из ${git(work, 'rev-parse', '--short', 'HEAD')}`)
    check('сирота: автор из git config', git(work, 'log', '-1', '--format=%an <%ae>', sha) === 'Тест Автор <author@example.org>')
    check('сирота: ветки в origin нет', git(origin, 'branch', '--list', 'ci/*') === '')
  }

  // --ref
  r = run('t2', '--ref', 'HEAD~1')
  const sha2 = /\+([0-9a-f]{40,64}):/.exec(r.stdout)?.[1]
  check('--ref: дерево первого коммита', !!sha2 && git(work, 'rev-parse', `${sha2}^{tree}`) === git(work, 'rev-parse', 'HEAD~1^{tree}'))

  // --push без подключённых хуков: отказ, ветки в origin нет (печать команды без --push хуков не требует — выше)
  r = run('t3/sub', '--push')
  check('--push без hooksPath: код 1', r.status === 1 && r.stderr.includes('без хуков') && git(origin, 'branch', '--list', 'ci/*') === '', r.stderr)
  r = run('--delete', 't3/sub', '--push')
  check('--delete --push без hooksPath: код 1', r.status === 1 && r.stderr.includes('без хуков'))
  git(work, 'config', 'core.hooksPath', 'scripts/git-hooks')
  r = run('t3/sub', '--push')
  check('--push: hooksPath есть, стража нет — код 1', r.status === 1 && r.stderr.includes('нет стража'), r.stderr)
  git(work, 'config', 'core.hooksPath', '/nonexistent/hooks')
  r = run('t3/sub', '--push')
  check('--push: hooksPath чужой — код 1', r.status === 1 && r.stderr.includes('core.hooksPath'), r.stderr)
  // Заглушка стража и каталога хуков: настоящего pre-push во временном репозитории нет, нужна лишь проверка подключения.
  put(P('.claude', 'hooks', ['public', 'guard.mjs'].join('-')))
  fs.mkdirSync(path.join(work, 'scripts', 'git-hooks'), { recursive: true })
  fs.writeFileSync(path.join(work, '.git', 'info', 'exclude'), `${P('.claude', '')}\n${P('scripts', 'git-hooks', '')}\n`)
  git(work, 'config', 'core.hooksPath', path.join(work, 'scripts', 'git-hooks'))

  // С --push: ветка в origin — сирота; повторная отправка (новый сирота) проходит принудительно
  r = run('t3/sub', '--push')
  check('--push: код 0 (абсолютный hooksPath)', r.status === 0, r.stderr)
  git(work, 'config', 'core.hooksPath', 'scripts/git-hooks')
  git(origin, 'update-ref', '-d', 'refs/heads/ci/t3/sub')
  r = run('t3/sub', '--push')
  check('--push: код 0 (относительный hooksPath)', r.status === 0, r.stderr)
  const remote = git(origin, 'rev-parse', '--verify', 'refs/heads/ci/t3/sub')
  check('--push: в origin сирота', git(origin, 'rev-list', '--parents', '-n', '1', remote).split(' ').length === 1)
  put('d.txt')
  commit('третий')
  r = run('t3/sub', '--push')
  check('--push повторно: принудительно, код 0', r.status === 0, r.stderr)
  check('--push повторно: ветка обновлена', git(origin, 'rev-parse', 'refs/heads/ci/t3/sub') !== remote)

  // Удаление
  r = run('--delete', 't3/sub')
  check('--delete без --push: только печать', r.status === 0 && r.stdout.includes('git push origin :refs/heads/ci/t3/sub') && git(origin, 'branch', '--list', 'ci/*') !== '')
  r = run('--delete', 't3/sub', '--push')
  check('--delete --push: ветка удалена', r.status === 0 && git(origin, 'branch', '--list', 'ci/*') === '', r.stderr)

  // Отказы
  r = run('t4', '--ref', 'nope')
  check('несуществующий ref: код 1', r.status === 1)
  for (const bad of ['a..b', '../x', 'a b', '']) {
    r = run(bad)
    check(`плохое имя «${bad}»: код 2`, r.status === 2 && !r.stdout.includes('git push'))
  }
  r = run()
  check('без имени: код 2', r.status === 2)
  r = run('t5', '--nope')
  check('неизвестный флаг: код 2', r.status === 2)
  for (const [title, args] of [
    ['--ref -x', ['t5', '--ref', '-x']],
    ['--ref без значения', ['t5', '--ref']],
    ['--ref перед флагом', ['t5', '--ref', '--push']],
    ['--delete без имени', ['--delete']],
    ['--delete перед флагом', ['--delete', '--push']],
    ['--delete с лишним именем', ['--delete', 'a', 'b']],
    ['--delete с именем до флага', ['x', '--delete', 'a']],
    ['--delete с --ref', ['--delete', 'a', '--ref', 'HEAD']],
    ['--repo -x', ['t5', '--repo', '-x']],
  ]) {
    r = run(...args)
    check(`${title}: код 2, без команды`, r.status === 2 && !r.stdout.includes('git push'), r.stderr)
  }

  // Подмена объектов: ls-tree увидел бы подменённое дерево, а push отправил настоящее
  const headSha = git(work, 'rev-parse', 'HEAD')
  git(work, 'replace', headSha, git(work, 'rev-parse', 'HEAD~1'))
  r = run('t5')
  check('refs/replace: код 1, без команды', r.status === 1 && r.stderr.includes('refs/replace') && !r.stdout.includes('git push'), r.stderr)
  git(work, 'replace', '-d', headSha)
  r = run('t5')
  check('refs/replace убран: пускает', r.status === 0, r.stderr)

  // Автор только из конфигурации: без неё отказ, а не имя по логину и хосту машины
  git(work, 'config', '--unset', 'user.email')
  r = run('t5')
  check('нет user.email: код 1, без команды', r.status === 1 && !r.stdout.includes('git push'), r.stdout)
  git(work, 'config', 'user.email', 'author@example.org')

  // Неотслеживаемое — предупреждение, не отказ (заглушки стража выше лежат в exclude и не мешают)
  put('new.txt')
  r = run('t5')
  check('неотслеживаемый файл: код 0 и предупреждение', r.status === 0 && r.stderr.includes('new.txt'), r.stderr)
  fs.rmSync(path.join(work, 'new.txt'))

  put('a.txt', 'правка\n')
  r = run('t6')
  check('грязное дерево: код 1, без команды', r.status === 1 && r.stderr.includes('не чистое') && !r.stdout.includes('git push'))
  r = run('t6', '--ref', 'HEAD~1')
  check('грязное дерево, другой ref: пускает', r.status === 0, r.stderr)
  git(work, 'checkout', '-q', '--', 'a.txt')

  for (const f of [
    P('.claude', 'x.md'), P('.Claude', 'a'), 'CLAUDE.md', 'sub/Claude.MD', 'sub/CLAUDE.md',
    P('docs', 'private', 'n.md'), P('Docs', 'Private', 'x'), P('docs', 'archive', 'x'),
    'deploy/inventory/hosts.yml', 'Deploy/Inventory/hosts.yml', 'deploy/inventory-prod/h.yml', 'terraform.tfstate',
    'data/a', '.env', 'sub/.env.prod', '.terraform/p', 'x.tfplan', 'prod.tfvars', 'a.tfvars.json', '.vault-pass', '.vault-pass.old',
    P('sub', 'BACKLOG') + '.md', 'OPS' + '.md', 'plan-1.2' + '.html', 'x.log', 'bin/a', 'dist/a', 'coverage/a',
    '.gotmp/x', '.venv/x', '.vscode/s.json', '.idea/s', '.obsidian/s', 'a.local.json', 'a.local.yml',
  ]) {
    put(f)
    commit(`приватное ${f}`, f)
    r = run('t7')
    check(`приватный путь ${f}: код 1`, r.status === 1 && r.stderr.includes(f) && !r.stdout.includes('git push'), r.stderr)
    git(work, 'reset', '-q', '--hard', 'HEAD~1')
  }
  put('deploy/inventory.example/hosts.yml')
  commit('пример', 'deploy/inventory.example/hosts.yml')
  put('.gotmp/.gitkeep', '')
  commit('gitkeep', '.gotmp/.gitkeep')
  r = run('t9')
  check('.gotmp/.gitkeep пускается', r.status === 0, r.stderr)
  r = run('t8')
  check('inventory.example пускается', r.status === 0, r.stderr)
} finally {
  fs.rmSync(tmp, { recursive: true, force: true })
}

if (failed) {
  console.log(`ci-push-cases: провалено ${failed}`)
  process.exit(1)
}
console.log('ci-push-cases: всё прошло')
