// Пути, которым нечего делать в публичном репозитории, — единственный список. Читают: pre-commit (через запуск
// ниже), pre-push (страж публичного дерева) и scripts/ci-push.mjs. Владелец 31.10.2026 по аудиту безопасности:
// у стража и ci-push было по своему списку, они разошлись, и ci-push пропустил бы то, что отклонит pre-push.
//
// Сверх .gitignore — пути в любой глубине (`sub/CLAUDE.md`, `x/.env`) и имена приватных документов процесса
// по любому пути: такой файл в публичном каталоге — всегда ошибка переноса. Регистр не учитывается: на macOS
// и Windows `.Claude` — тот же каталог, что `.claude`.
//
// Запуск: `node scripts/private-paths.mjs` — имена файлов на stdin (по строке или через NUL), печатает приватные
// и выходит с кодом 1, если они есть; 0 — чисто.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const PRIVATE_PATH = new RegExp(
  [
    String.raw`(^|/)CLAUDE\.md$`, // пути машины и ссылки на приватные документы
    String.raw`^\.claude/`,
    String.raw`^docs/(private|archive)/`,
    String.raw`^\.gotmp/`, // временные деревья тестов; исключение — KEEP
    String.raw`^(bin|dist|coverage)/`,
    String.raw`^\.(obsidian|vscode|idea)/`,
    String.raw`^deploy/inventory(?!\.example(/|$))`, // хосты, домен, секреты; пример — deploy/inventory.example/
    String.raw`^data/`, // альбомы локального стенда
    String.raw`(^|/)\.(venv|terraform)/`,
    String.raw`(^|/)\.env`,
    String.raw`\.tfstate`, // состояние OpenTofu — адреса и ключи
    String.raw`\.tfvars(\.json)?$`, // значения переменных — секреты в открытом виде
    String.raw`\.tfplan$`,
    String.raw`(^|/)\.vault-pass`, // ключ ко всем секретам inventory
    String.raw`\.local\.(json|ya?ml)$`,
    String.raw`\.log$`,
    String.raw`(^|/)(BACKLOG|LOCAL|OPS|PROCESS|compact-next|decisions-process)\.md$`,
    String.raw`(^|/)plan-\d+\.\d+\.html$`, // страница хода
  ].join('|'),
  'i',
)

// Пустой .gotmp/.gitkeep публичный репозиторий держит сознательно: каталог для тестовых деревьев нужен сразу
// после клона.
export const KEEP = new Set(['.gotmp/.gitkeep'])

export const privatePath = (f) => !KEEP.has(f) && PRIVATE_PATH.test(String(f).replace(/\\/g, '/'))

export const privateIn = (files) => files.filter(privatePath)

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const input = readFileSync(0, 'utf8')
  const bad = privateIn(input.split(input.includes('\0') ? '\0' : '\n').filter(Boolean))
  if (bad.length) console.log(bad.join('\n'))
  process.exit(bad.length ? 1 : 0)
}
