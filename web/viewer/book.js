// Книга на StPageFlip: одна сборка = один экземпляр. Движок знает только страницы и их файлы; манифест,
// сеть и подписи — в app.js. Смена ориентации — это новая сборка (destroy у StPageFlip убирает и сам элемент
// книги, поэтому у каждой сборки свой), старый цикл отрисовки гасит lib.js.
import { beginBuild, closeBuild, endBuild } from './lib.js'
import { wantedIndexes } from './manifest.js'

// stageSize — свободное место сцены без её внутренних отступов (clientWidth/Height считают и padding).
export function stageSize(stage) {
  const cs = window.getComputedStyle(stage)
  const px = (v) => Number.parseFloat(v) || 0
  return {
    w: Math.max(0, stage.clientWidth - px(cs.paddingLeft) - px(cs.paddingRight)),
    h: Math.max(0, stage.clientHeight - px(cs.paddingTop) - px(cs.paddingBottom)),
  }
}

// fitSize — самый большой прямоугольник книги нужной пропорции внутри сцены: разворот — две страницы в ряд.
export function fitSize(stageW, stageH, ratio, portrait) {
  const aspect = portrait ? ratio : ratio * 2
  const w = Math.max(1, Math.min(stageW, stageH * aspect))
  return { w, h: w / aspect }
}

// mountBook собирает книгу в stage. opts: St, stage, list (страницы показа), ratio, cover, portrait, start (индекс),
// reduced (prefers-reduced-motion), onFlip(idx), onBroken(n — номер страницы книги, не индекс: индексы
// меняются при пересборке).
export function mountBook(opts) {
  const { St, stage, list, ratio, cover, portrait, reduced } = opts
  const holder = document.createElement('div')
  holder.className = 'book-holder'
  const block = document.createElement('div')
  block.className = 'book'
  holder.appendChild(block)
  stage.appendChild(holder)

  let destroyed = false
  const size = fit()
  const imgs = new Array(list.length).fill(null)
  const asked = new Array(list.length).fill(false)
  const byN = new Map() // номер страницы → элемент
  const pages = list.map((p, i) => pageElement(p, i))
  for (const el of pages) block.appendChild(el)

  // Вертикально библиотека переходит на одну страницу, когда ширина книги меньше 2·minWidth; ширину ставим
  // чуть меньше нынешней: в этой сборке режим не прыгает при небольших изменениях окна, а выход за границы
  // app.js замечает и пересобирает книгу.
  const minWidth = portrait ? Math.max(100, Math.ceil(size.w * 0.8)) : 100
  const flip = new St.PageFlip(block, {
    width: 1000,
    height: Math.round(1000 / ratio),
    size: 'stretch',
    minWidth,
    maxWidth: 4000,
    minHeight: 100,
    maxHeight: 4000,
    usePortrait: portrait,
    startPage: opts.start,
    showCover: cover,
    maxShadowOpacity: 0.4,
    flippingTime: reduced ? 200 : 800,
    showPageCorners: !reduced,
    mobileScrollSupport: false,
    swipeDistance: 30,
  })
  const gen = beginBuild()
  try {
    flip.loadFromHTML(pages)
  } finally {
    endBuild()
  }
  ensure(opts.start)
  flip.on('flip', (e) => sync(e.data))
  flip.on('init', (e) => sync(e.data.page))

  function fit() {
    const room = stageSize(stage)
    const s = fitSize(room.w, room.h, ratio, portrait)
    holder.style.width = `${s.w}px`
    holder.style.height = `${s.h}px`
    return s
  }

  function pageElement(p, i) {
    const el = document.createElement('div')
    el.className = 'page'
    byN.set(p.n, el)
    if (p.role !== 'page') el.dataset.density = 'hard' // обложка — твёрдая
    if (p.blank) {
      el.classList.add('page-blank')
      return el
    }
    const img = document.createElement('img')
    img.alt = `Страница ${p.n}`
    img.decoding = 'async'
    img.draggable = false
    img.addEventListener('load', () => el.classList.add('page-loaded'))
    // Ошибку старой сборки (после destroy картинка могла ещё догружаться) не учитываем: её страница уже не наша.
    img.addEventListener('error', () => {
      if (!destroyed) opts.onBroken(p.n)
    })
    el.appendChild(img)
    imgs[i] = img
    return el
  }

  // ensure запрашивает картинки вокруг текущей страницы; остальные не трогает, пока до них не долистают.
  function ensure(idx) {
    for (const i of wantedIndexes(list.length, idx, portrait)) {
      if (imgs[i] && !asked[i]) {
        asked[i] = true
        imgs[i].src = list[i].file // имя уже проверено по шаблону в manifest.js; путь относительный
      }
    }
  }

  function sync(idx) {
    if (destroyed) return
    ensure(idx)
    opts.onFlip(idx)
  }

  return {
    portrait,
    minWidth,
    holderWidth: () => holder.offsetWidth,
    fit,
    current: () => flip.getCurrentPageIndex(),
    next: () => flip.flipNext(),
    prev: () => flip.flipPrev(),
    first: () => {
      flip.turnToPage(0)
      sync(0)
    },
    last: () => {
      flip.turnToPage(list.length - 1)
      sync(flip.getCurrentPageIndex())
    },
    fail: (n) => {
      const page = byN.get(n)
      if (page && !destroyed) page.classList.add('page-failed')
    },
    ensure,
    destroy: () => {
      destroyed = true
      for (const img of imgs) if (img) img.removeAttribute('src') // не догружать страницы погашенной книги
      closeBuild(gen)
      flip.destroy()
      holder.remove()
    },
  }
}
