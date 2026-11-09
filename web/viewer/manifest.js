// Разбор manifest.json и порядок страниц. Чистые функции без DOM: проверяются из Node.
// Манифест приходит из сети — ему не доверяем: имена файлов только по шаблону сервиса (design §5.1), чтобы
// путь из манифеста не мог увести ни на другой адрес, ни в соседний каталог; текст — только в textContent.

const SCHEMA = 1
const MAX_PAGES = 1000 // контракт — 300, запас на рост; больше — явно не наш манифест
const FILL_TOLERANCE = 0.03 // картинка заполняет лист (cover), если её пропорция отличается от листа не больше чем на 3 %
const MAX_SIDE = 100000
const FILE_RE = /^[0-9a-f]{64}\.(jpg|webp)$/

// clip — не больше n знаков (по кодовым точкам: slice по UTF-16 рвёт эмодзи пополам).
export function clip(s, n) {
  return Array.from(s).slice(0, n).join('')
}

export class ManifestError extends Error {}

function bad(why) {
  throw new ManifestError(why)
}

// fillsPage — заполнять ли лист картинкой целиком (object-fit: cover). Обложки выгружаются чуть другой пропорции,
// чем страницы (доли процента); contain оставил бы на них полосы бумаги. При заметной разнице картинку не режем.
// w и h — размеры картинки из манифеста (нет или не число — 0, тогда contain).
export function fillsPage(w, h, ratio) {
  if (!(w > 0 && h > 0 && ratio > 0)) return false
  return Math.abs(w / h / ratio - 1) <= FILL_TOLERANCE
}

// parseManifest — манифест → {title, cover, ratio, pages}; всё лишнее отбрасывается.
export function parseManifest(raw) {
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) bad('манифест не объект')
  if (raw.schema !== SCHEMA) bad('неизвестная версия схемы')
  const book = raw.book
  if (book === null || typeof book !== 'object') bad('нет book')
  const mm = book.page_mm
  if (mm === null || typeof mm !== 'object') bad('нет page_mm')
  const w = Number(mm.w)
  const h = Number(mm.h)
  if (!(w >= 10 && w <= 2000 && h >= 10 && h <= 2000)) bad('page_mm вне пределов')
  const cover = book.cover === true
  if (!Array.isArray(raw.pages) || raw.pages.length === 0 || raw.pages.length > MAX_PAGES) bad('pages')
  const k = raw.pages.length
  const pages = raw.pages.map((p, i) => {
    if (p === null || typeof p !== 'object') bad('страница не объект')
    if (p.n !== i + 1) bad('номера страниц не подряд')
    if (p.role !== 'front' && p.role !== 'back' && p.role !== 'page') bad('роль')
    const blank = p.blank === true
    if (blank && p.role !== 'page') bad('белой бывает только страница')
    if (!blank && !(typeof p.file === 'string' && FILE_RE.test(p.file))) bad('имя файла')
    const side = (v) => (Number.isFinite(v) && v > 0 && v <= MAX_SIDE ? v : 0)
    const fill = !blank && fillsPage(side(p.w), side(p.h), w / h)
    return { n: p.n, role: p.role, blank, file: blank ? '' : p.file, fill }
  })
  if (cover !== (pages[0].role === 'front' && pages[k - 1].role === 'back')) bad('обложка и роли не сходятся')
  const title = typeof raw.title === 'string' ? clip(raw.title, 200) : ''
  return { title, cover, ratio: w / h, pages }
}

// displayPages — страницы в порядке показа. Вертикально (одна страница на экране) белые листы пропускаются: пустой
// экран при листании только сбивает; на развороте они нужны, иначе пары съедут.
export function displayPages(m, portrait) {
  return portrait ? m.pages.filter((p) => !p.blank) : m.pages
}

// indexOfPage — индекс в списке показа для страницы с номером n; если её нет (белая вертикально) — ближайшая
// предыдущая показываемая, чтобы при повороте телефона читатель остался на том же месте.
export function indexOfPage(list, n) {
  let at = 0
  for (let i = 0; i < list.length; i++) {
    if (list[i].n <= n) at = i
  }
  return at
}

// visibleIndexes — индексы страниц на экране при левой странице idx. Разворот: с обложкой первая и последняя
// стоят одни, иначе пары (0,1), (2,3)…; вертикально — одна.
export function visibleIndexes(m, list, idx, portrait) {
  if (portrait) return [idx]
  const last = list.length - 1
  if (m.cover && (idx === 0 || idx === last)) return [idx]
  return idx + 1 <= last ? [idx, idx + 1] : [idx]
}

// counterParts — подпись под книгой из двух строк: крупная и мелкая. Обложки в счёт не входят, белые листы входят
// (это страницы книги). Альбомом считаем развороты («2 / 30» — «разворот»), вертикально — страницы.
export function counterParts(m, shown, portrait) {
  const first = shown[0]
  const last = shown[shown.length - 1]
  if (first.role === 'front') return { main: 'Обложка', sub: 'листайте' }
  if (last.role === 'back') return { main: 'Конец', sub: 'задняя обложка' }
  const shift = m.cover ? 1 : 0
  const total = m.pages.length - 2 * shift
  const a = first.n - shift
  if (portrait) return { main: `${a} / ${total}`, sub: 'страница' }
  return { main: `${Math.ceil(a / 2)} / ${Math.ceil(total / 2)}`, sub: 'разворот' }
}

// wantedIndexes — какие страницы держать загруженными: текущие, соседние развороты в обе стороны.
// Страницы грузятся по мере листания, а не все сразу (медленная сеть, до 300 страниц).
export function wantedIndexes(count, idx, portrait) {
  const behind = portrait ? 1 : 2
  const ahead = portrait ? 2 : 4
  const out = []
  for (let i = Math.max(0, idx - behind); i <= Math.min(count - 1, idx + ahead); i++) out.push(i)
  // Ближние первыми: сначала текущая страница, потом вперёд, потом назад.
  return out.sort((a, b) => Math.abs(a - idx) - Math.abs(b - idx) || b - a)
}

// sameBook — тот же ли манифест (по файлам и порядку): листалка перечитывает его при потерянной картинке.
export function sameBook(a, b) {
  return a.pages.length === b.pages.length && a.cover === b.cover && a.pages.every((p, i) => p.file === b.pages[i].file && p.blank === b.pages[i].blank)
}
