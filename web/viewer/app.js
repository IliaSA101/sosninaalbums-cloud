// Листалка альбома: читает manifest.json и view.json рядом со страницей (/a/<id>/), собирает книгу и
// ведёт управление. Доступ (пароль, срок) проверяет сервер — здесь только показ. Всё из манифеста идёт в
// textContent и в src по проверенному шаблону; ничего не исполняется и не уводит на другой адрес.
import { mountBook, stageSize } from './book.js'
import { loadFlipLib } from './lib.js'
import {
  clip,
  counterText,
  displayPages,
  indexOfPage,
  ManifestError,
  parseManifest,
  sameBook,
  visibleIndexes,
} from './manifest.js'

const LIB_URL = new URL('vendor/page-flip-2.0.7.browser.js', import.meta.url).href
const $ = (id) => document.getElementById(id)
const el = { title: $('title'), studio: $('studio'), stage: $('stage'), status: $('status'), prev: $('prev'), next: $('next'), counter: $('counter') }
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')

class HttpError extends Error {
  constructor(status) {
    super(`HTTP ${status}`)
    this.status = status
  }
}

// getJSON — адрес без строки запроса (design §5.6); no-cache: перечитывать у сервера, не из кэша браузера.
async function getJSON(url) {
  const r = await fetch(url, { cache: 'no-cache', credentials: 'same-origin', referrerPolicy: 'no-referrer' })
  if (!r.ok) throw new HttpError(r.status)
  return r.json()
}

function say(text) {
  el.status.textContent = text
  el.status.hidden = text === ''
}

const state = { m: null, St: null, book: null, list: [], idx: 0 }

// Книга вертикальная (одна страница), если окно узкое или выше, чем шире страницы.
function wantPortrait(m) {
  const { w, h } = stageSize(el.stage)
  return w < 560 || w / Math.max(h, 1) < m.ratio
}

function build(startN) {
  if (state.book) state.book.destroy()
  const portrait = wantPortrait(state.m)
  state.list = displayPages(state.m, portrait)
  state.book = mountBook({
    St: state.St,
    stage: el.stage,
    list: state.list,
    ratio: state.m.ratio,
    cover: state.m.cover,
    portrait,
    start: indexOfPage(state.list, startN),
    reduced: reducedMotion.matches,
    onFlip: show,
    onBroken: broken,
  })
}

// show — подпись и кнопки по текущей странице.
function show(idx) {
  const { m, list, book } = state
  if (!book || list.length === 0) return
  state.idx = idx
  const at = visibleIndexes(m, list, idx, book.portrait)
  el.counter.textContent = counterText(m, at.map((i) => list[i]))
  el.prev.disabled = at[0] === 0
  el.next.disabled = at[at.length - 1] === list.length - 1
}

// Потерянная картинка: возможно, правку опубликовали и файл ушёл вместе с прежней редакцией — тогда манифест
// новый. Перечитываем его; изменился — пересобираем книгу на том же месте, нет — страница действительно недоступна.
const lost = new Set() // номера страниц (n), а не индексы: после пересборки индексы другие
let refreshing = false

function broken(n) {
  lost.add(n)
  if (!refreshing) refresh()
}

async function refresh() {
  refreshing = true
  const idx = state.idx
  const startN = state.list[idx] ? state.list[idx].n : 1
  try {
    const fresh = parseManifest(await getJSON('manifest.json'))
    if (!sameBook(state.m, fresh)) {
      state.m = fresh
      lost.clear()
      build(startN)
      return
    }
  } catch (e) {
    // Вход пропал или альбом закрыт посреди чтения: заново открыть адрес — сервер покажет форму или 404.
    if (e instanceof HttpError && (e.status === 401 || e.status === 404)) {
      window.location.reload()
      return
    }
  } finally {
    refreshing = false
  }
  for (const n of lost) state.book.fail(n)
  lost.clear()
}

function onResize() {
  const { book, m } = state
  if (!book) return
  const portrait = wantPortrait(m)
  book.fit()
  const w = book.holderWidth()
  const outOfRange = book.portrait && (w < book.minWidth || w >= 2 * book.minWidth)
  if (portrait !== book.portrait || outOfRange) {
    build(state.list[state.idx] ? state.list[state.idx].n : 1)
  }
}

function onKey(e) {
  if (!state.book || e.altKey || e.ctrlKey || e.metaKey) return
  const keys = {
    ArrowRight: () => state.book.next(),
    PageDown: () => state.book.next(),
    ArrowLeft: () => state.book.prev(),
    PageUp: () => state.book.prev(),
    Home: () => state.book.first(),
    End: () => state.book.last(),
  }
  const act = keys[e.key]
  if (act) {
    e.preventDefault()
    act()
  }
}

async function start() {
  say('Загружаем альбом…')
  let m
  let view = {}
  try {
    const [raw, v] = await Promise.all([getJSON('manifest.json'), getJSON('view.json').catch(() => ({}))])
    m = parseManifest(raw)
    if (v && typeof v.studio === 'string') view = { studio: clip(v.studio, 200) }
  } catch (e) {
    say(e instanceof ManifestError ? 'Этот альбом не удалось открыть.' : 'Не удалось загрузить альбом. Обновите страницу.')
    return
  }
  el.title.textContent = m.title
  el.studio.textContent = view.studio || ''
  document.title = [m.title, view.studio].filter(Boolean).join(' · ')
  try {
    state.St = await loadFlipLib(LIB_URL)
  } catch {
    say('Не удалось загрузить листалку. Обновите страницу.')
    return
  }
  state.m = m
  say('')
  el.prev.addEventListener('click', () => state.book.prev())
  el.next.addEventListener('click', () => state.book.next())
  document.addEventListener('keydown', onKey)
  // До первой сборки: обработчик resize у StPageFlip ставится при сборке и должен идти после нашего, который
  // сперва подгоняет размер книги под окно.
  window.addEventListener('resize', onResize)
  build(1)
}

start()
