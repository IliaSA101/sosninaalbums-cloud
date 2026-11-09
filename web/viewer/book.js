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

// MAX_SPREAD_W — предел ширины разворота: две страницы по ~1600 px; шире картинки только мылились бы, а для
// StPageFlip (maxWidth 4000 на страницу) это запас.
export const MAX_SPREAD_W = 3200

// fitSize — самый большой прямоугольник книги нужной пропорции внутри сцены: разворот — две страницы в ряд.
export function fitSize(stageW, stageH, ratio, portrait) {
  const aspect = portrait ? ratio : ratio * 2
  const w = Math.max(1, Math.min(stageW, stageH * aspect, portrait ? Infinity : MAX_SPREAD_W))
  return { w, h: w / aspect }
}

// Доля ширины разворота, на которую сдвигается книга, чтобы одиночная обложка стояла по центру сцены, как у
// закрытой книги. StPageFlip с showCover ставит переднюю обложку в правую половину разворота, заднюю — в левую.
const COVER_SHIFT = 0.25

// coverShift — сдвиг книги (доля ширины разворота) для разворота spread (индексы страниц; null — неизвестен):
// передняя обложка одна справа → влево, задняя одна слева → вправо, остальное — на месте. Вертикально
// страница и так одна по центру, без обложки одиноких страниц нет.
export function coverShift(cover, portrait, count, spread) {
  if (!cover || portrait || count < 2 || !spread || spread.length !== 1) return 0
  if (spread[0] === 0) return -COVER_SHIFT
  return spread[0] === count - 1 ? COVER_SHIFT : 0
}

// mountBook собирает книгу в stage. opts: St, stage, list (страницы показа), ratio, cover, portrait, start (индекс),
// reduced (prefers-reduced-motion), onFlip(idx), onBroken(n — номер страницы книги, не индекс: индексы
// меняются при пересборке).
export function mountBook(opts) {
  const { St, stage, list, ratio, cover, portrait, reduced } = opts
  const holder = document.createElement('div')
  holder.className = 'book-holder'
  if (!portrait) holder.classList.add('book-spread') // корешок рисуем только у разворота: одиночная страница — не разворот
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
    maxShadowOpacity: 0.35,
    flippingTime: reduced ? 200 : 900,
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
  const slide = coverSlide()
  slide.settle(true) // сразу, не дожидаясь init: иначе кадр-другой обложка стояла бы справа
  flip.on('flip', (e) => {
    slide.settle()
    sync(e.data)
  })
  flip.on('init', (e) => {
    slide.settle(true)
    sync(e.data.page)
  })
  flip.on('changeState', (e) => slide.state(e.data))

  function fit() {
    const room = stageSize(stage)
    const s = fitSize(room.w, room.h, ratio, portrait)
    holder.style.width = `${s.w}px`
    holder.style.height = `${s.h}px`
    return s
  }

  // coverSlide двигает holder по горизонтали вместе с переворотом обложки. Книгу сдвигаем transform'ом у holder:
  // StPageFlip считает координаты мыши и касаний через getBoundingClientRect, так что клики по уголкам и свайп
  // остаются точными; .stage обрезает по краю, но обложка в центре и её тень внутри сцены. Сдвиг привязан к
  // положению страницы (calc.getPosition), а не ко времени: так он идёт ровно с переворотом, в том числе при
  // перетаскивании пальцем и отмене жеста. Без обложки и вертикально сдвига нет; при prefers-reduced-motion
  // (reduced) покадрового движения нет: книга встаёт на место сразу.
  function coverSlide() {
    const live = cover && !portrait
    let tracking = false
    let queued = false // кадр уже заказан: второй не ставим, иначе остановка и новый жест удвоили бы цикл
    let route = null // {from, to} — доли сдвига начала и конца текущего переворота
    const spreads = () => {
      const pc = flip.getPageCollection()
      return { all: pc.getSpread(), at: pc.getCurrentSpreadIndex() }
    }
    const shiftAt = (all, i) => coverShift(cover, portrait, list.length, all[i])
    // Тень книги — filter: drop-shadow у holder (viewer.css): она идёт по видимым пикселям и не ложится под пустую
    // половину разворота, поэтому от сдвига ничего больше не зависит.
    const place = (frac, glide) => {
      holder.classList.toggle('book-glide', glide)
      holder.style.transform = frac === 0 ? '' : `translateX(${frac * 100}%)`
    }
    const rest = (glide) => {
      const { all, at } = spreads()
      place(shiftAt(all, at), glide)
    }
    // routeOf — откуда и куда едет книга: направление знает только расчёт переворота, он появляется чуть позже
    // смены состояния.
    const routeOf = (calc) => {
      const { all, at } = spreads()
      return { from: shiftAt(all, at), to: shiftAt(all, at + (calc.getDirection() === 0 ? 1 : -1)) }
    }
    const schedule = () => {
      if (queued) return
      queued = true
      requestAnimationFrame(frame)
    }
    const frame = () => {
      queued = false
      tick()
    }
    const tick = () => {
      if (!tracking || destroyed) return
      schedule()
      const calc = flip.getFlipController().getCalculation()
      if (!calc || !calc.getPosition()) return
      route = route || routeOf(calc)
      const half = flip.getRender().getRect().pageWidth
      // x страницы: pageWidth — лежит, -pageWidth — перевёрнута (в обе стороны листания).
      const t = Math.min(1, Math.max(0, (half - calc.getPosition().x) / (2 * half)))
      place(route.from + (route.to - route.from) * t, false)
    }
    return {
      // state — события состояния StPageFlip: покадровое слежение только пока страница в движении.
      // fold_corner (подгиб уголка при наведении) — не переворот.
      state(s) {
        if (!live || reduced || destroyed) return
        const moving = s === 'flipping' || s === 'user_fold'
        if (moving && !tracking) {
          tracking = true
          route = null
          schedule()
        } else if (!moving && tracking) {
          tracking = false
          rest(false)
        }
      },
      // settle — страница встала (переворот закончен, переход Home/End, первая сборка). Первая сборка (instant)
      // и конец покадрового переворота встают сразу; плавный съезд — только при переходе без листа.
      settle(instant) {
        if (!live || destroyed) return
        rest(!instant && !reduced && !tracking)
      },
    }
  }

  function pageElement(p, i) {
    const el = document.createElement('div')
    el.className = 'page'
    byN.set(p.n, el)
    if (p.role !== 'page') el.dataset.density = 'hard' // обложка — твёрдая
    if (p.fill) el.classList.add('page-fill') // пропорция картинки почти как у листа: заполняем, без белых полос
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
    // update — пересчёт StPageFlip после смены размера без события resize (полный экран).
    update: () => {
      if (!destroyed) flip.update()
    },
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
