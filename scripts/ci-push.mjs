// Ветка проверок для GitHub Actions: `node scripts/ci-push.mjs <имя> [--ref <ref>] [--push] [--repo <папка>]`.
// Строит из дерева ref (по умолчанию HEAD) коммит-сироту (без родителей; автор и коммиттер — из git config)
// и печатает команду отправки в origin: git push origin +<sha>:refs/heads/ci/<имя>; сам отправляет только с --push.
// `--delete <имя> [--push]` — удаление ветки: git push origin :refs/heads/ci/<имя> (с --push выполняет).
// Рабочую копию и индекс не трогает.
//
// Почему сирота. Публичный репозиторий не должен получить историю dev: в ней сообщения, пути и решения
// приватной оснастки. Хук pre-push пускает в refs/heads/ci/* только коммит без родителей; этот скрипт —
// единственный штатный способ его построить. Принудительная отправка (+) нужна потому, что ветка
// одноразовая и каждый раз пересоздаётся новым сиротой.
// Почему защита от приватного здесь, а не только в хуках. Хук pre-push смотрит дерево уже в момент push;
// отказ до построения коммита дешевле и объясняет причину по-русски. Но содержимое в итоге проверяет
// только pre-push со стражем, поэтому --push без подключённых хуков отказывает.
//
// Коды выхода: 0 — готово, 2 — неверные аргументы, 1 — отказ (грязное дерево, приватный путь, подмена
// объектов, нет хуков) или ошибка git.
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { privateIn } from './private-paths.mjs'

// Список приватных путей — общий со стражем публичного дерева и pre-commit (владелец 31.10.2026 по аудиту:
// два списка разошлись). privateIn — приватные пути в списке файлов дерева.
export { PRIVATE_PATH, privateIn } from './private-paths.mjs'

export const NAME_RE = /^[\w-]+(?:[./][\w-]+)*$/

// validName — имя ветки без ci/: сегменты из [\w-], без «..», без сегмента на .lock (git такие ссылки не принимает).
export function validName(name) {
  return typeof name === 'string' && NAME_RE.test(name) && !name.includes('..') && !/\.lock(\/|$)/.test(name)
}

// parseArgs — разбор командной строки; ошибка — исключение с текстом для кода 2.
export function parseArgs(argv) {
  const o = { name: null, ref: null, push: false, del: false, repo: null }
  // Значение флага не может начинаться с «-»: иначе `--ref --push` молча съел бы следующий флаг.
  const need = (i, flag) => {
    const v = argv[i + 1]
    if (v === undefined || v.startsWith('-')) throw new Error(`${flag} требует значение (не начинающееся с «-»)`)
    return v
  }
  let pos = null
  let delName = null
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--push') o.push = true
    else if (a === '--ref') o.ref = need(i++, a)
    else if (a === '--repo') o.repo = need(i++, a)
    else if (a === '--delete') {
      if (delName !== null) throw new Error('--delete указан дважды')
      o.del = true
      delName = need(i++, a)
    } else if (a.startsWith('-')) throw new Error(`неизвестный флаг: ${a}`)
    else if (pos === null) pos = a
    else throw new Error(`лишний аргумент: ${a}`)
  }
  if (o.del && (pos !== null || o.ref !== null)) throw new Error('--delete принимает только имя ветки (без второго имени и --ref)')
  o.name = o.del ? delName : pos
  if (o.name === null) throw new Error('не указано имя ветки')
  if (!validName(o.name)) throw new Error(`недопустимое имя ветки «${o.name}»: сегменты из букв, цифр, _ и -, разделители . и /, без «..» и «.lock»`)
  o.ref ??= 'HEAD'
  return o
}

// actionsUrl — страница Actions из адреса origin (https или ssh); null, если адрес не github.com.
export function actionsUrl(remote) {
  const m = /github\.com[:/]([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i.exec(remote.trim())
  return m ? `https://github.com/${m[1]}/${m[2]}/actions` : null
}

// hooksProblem — почему отправка без хуков недопустима; '' — всё подключено. git запускает pre-push в корне
// дерева, поэтому относительный core.hooksPath читается от корня.
export function hooksProblem(repo, hooksPath) {
  const want = path.join(repo, 'scripts', 'git-hooks')
  if (!hooksPath || path.resolve(repo, hooksPath) !== want) return 'core.hooksPath не указывает на scripts/git-hooks'
  if (!fs.existsSync(path.join(repo, '.claude', 'hooks', ['public', 'guard.mjs'].join('-')))) return 'нет стража публичного дерева'
  return ''
}

function main(argv) {
  let o
  try {
    o = parseArgs(argv)
  } catch (e) {
    console.error(`ci-push: ${e.message}`)
    console.error('использование: node scripts/ci-push.mjs <имя> [--ref <ref>] [--push] [--repo <папка>]   |   --delete <имя> [--push]')
    return 2
  }
  const repo = o.repo ? path.resolve(o.repo) : path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
  // GIT_NO_REPLACE_OBJECTS: подмена объектов (refs/replace) заставила бы ls-tree показать одно дерево, а push отправить другое.
  // GIT_OPTIONAL_LOCKS=0: status не должен писать в индекс — скрипт ничего в рабочей копии не меняет.
  const env = { ...process.env, GIT_NO_REPLACE_OBJECTS: '1', GIT_OPTIONAL_LOCKS: '0' }
  const git = (a) => execFileSync('git', ['-c', 'core.quotePath=false', ...a], { cwd: repo, encoding: 'utf8', env, stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  const branch = `ci/${o.name}`
  const link = () => {
    try {
      const u = actionsUrl(git(['remote', 'get-url', 'origin']))
      if (u) console.log(`прогоны: ${u}`)
    } catch {
      // origin не настроен — ссылки нет, отправка и так упала бы раньше
    }
  }
  try {
    if (git(['for-each-ref', 'refs/replace'])) {
      console.error('ci-push: в репозитории есть refs/replace (подмена объектов): проверенное дерево могло бы отличаться от отправленного.')
      console.error('Уберите подмены (git replace -d …) и повторите.')
      return 1
    }
    if (o.push) {
      let hp = ''
      try {
        hp = git(['config', 'core.hooksPath'])
      } catch {
        // ключа нет — hooksProblem скажет об этом
      }
      const why = hooksProblem(repo, hp)
      if (why) {
        console.error(`ci-push: ${why}: содержимое проверяет только pre-push, без хуков не публикую.`)
        console.error('Нужны: git config core.hooksPath scripts/git-hooks и страж в основной рабочей копии (не в worktree агента).')
        return 1
      }
    }
    let cmd
    if (o.del) {
      cmd = ['push', 'origin', `:refs/heads/${branch}`]
    } else {
      const sha = git(['rev-parse', '--verify', `${o.ref}^{commit}`])
      // Сирота берёт закоммиченное дерево. Если ref — это HEAD, а рабочая копия правлена, проверять придётся не то, что видит автор.
      if (sha === git(['rev-parse', '--verify', 'HEAD^{commit}'])) {
        const lines = git(['status', '--porcelain', '--untracked-files=normal']).split('\n').filter(Boolean)
        const dirty = lines.filter((l) => !l.startsWith('??'))
        if (dirty.length) {
          console.error('ci-push: рабочее дерево не чистое — в сироту попадёт закоммиченное, а не текущее:')
          console.error(dirty.map((l) => `  ${l}`).join('\n'))
          console.error('Закоммитьте или уберите правки (или укажите другой --ref).')
          return 1
        }
        const fresh = lines.filter((l) => l.startsWith('??'))
        // Не отказ: новые файлы часто и должны остаться за бортом; но автор должен об этом знать.
        if (fresh.length) console.error(`ci-push: предупреждение: неотслеживаемые файлы в сироту не попадут:\n${fresh.map((l) => `  ${l.slice(3)}`).join('\n')}`)
      }
      // -z: имена с переводом строки и кавычками не ломают разбор.
      const files = git(['ls-tree', '-r', '-z', '--name-only', `${sha}^{tree}`]).split('\0').filter(Boolean)
      const bad = privateIn(files)
      if (bad.length) {
        console.error('ci-push: в дереве есть приватное, в публичный репозиторий оно не пойдёт:')
        console.error(bad.slice(0, 20).map((f) => `  ${f}`).join('\n'))
        return 1
      }
      // useConfigOnly: без user.name/user.email в конфиге git откажет, а не подставит имя по логину и хосту машины.
      const commit = git(['-c', 'user.useConfigOnly=true', 'commit-tree', `${sha}^{tree}`, '-m', `ci: ${o.name} из ${git(['rev-parse', '--short', sha])}`])
      console.log(`коммит-сирота ${commit.slice(0, 7)} из дерева ${sha.slice(0, 7)}`)
      cmd = ['push', 'origin', `+${commit}:refs/heads/${branch}`]
    }
    console.log(`git ${cmd.join(' ')}`)
    if (o.push) {
      execFileSync('git', cmd, { cwd: repo, env, stdio: 'inherit' })
      link()
    }
    return 0
  } catch (e) {
    const err = String(e.stderr || '').trim() || e.message
    console.error(`ci-push: ${err}`)
    return 1
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exit(main(process.argv.slice(2)))
