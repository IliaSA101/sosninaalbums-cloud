// Подпись студии в шапке: текст или ссылка на сайт студии. Ссылку строим только из view.json и только если
// адрес прошёл то же правило, что в конфиге сервиса и в роли (spec 005, design §1.1): листалка — третий рубеж,
// на случай если первые два пропустили или в кэш попал чужой ответ. Только DOM-свойства и textContent.

import { clip } from './manifest.js'

// Правило роли (albums_svc_site_re) без изменений: https, DNS-имя в ASCII с буквенной последней меткой,
// без userinfo, порта, запроса и якоря, путь из безопасного набора. В JS `$` без флага m — конец строки
// (хвостовой перевод строки не пропускает, в отличие от Python-овского `$`).
const SITE_RE = /^https:\/\/([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?(\/([A-Za-z0-9._~/-]|%[0-9A-Fa-f]{2})*)?$/
// Сегмент пути «.» или «..» (в том числе %2e): URL() сворачивает его, и href разошёлся бы со значением.
const DOT_SEGMENT_RE = /(^|\/)(\.|%2[eE]){1,2}(\/|$)/
const MAX_SITE = 200

// safeSite — строка сайта, если её можно сделать ссылкой, иначе ''. Возвращает href.
export function safeSite(site) {
  if (typeof site !== 'string' || site === '' || site.length > MAX_SITE || !SITE_RE.test(site)) return ''
  const slash = site.indexOf('/', 'https://'.length)
  if (slash >= 0 && DOT_SEGMENT_RE.test(site.slice(slash))) return ''
  let u
  try {
    u = new URL(site)
  } catch {
    return ''
  }
  // Каноническое равенство в самом браузере: числовые хосты, регистр, табы, порт дали бы другой href.
  if (u.protocol !== 'https:' || (u.href !== site && u.href !== `${site}/`)) return ''
  return u.href
}

// studioNode(doc, studio, site) → узел для #studio: ссылка, текст или пустой текст. Подпись всегда textContent.
export function studioNode(doc, studio, site) {
  const text = typeof studio === 'string' ? clip(studio, 200) : ''
  const href = text === '' ? '' : safeSite(site)
  if (href === '') return doc.createTextNode(text)
  const a = doc.createElement('a')
  a.href = href
  a.textContent = text
  // Новая вкладка (005-3): клиент не теряет место в альбоме. noopener — вкладка студии не получит window.opener,
  // noreferrer и referrerPolicy — адрес альбома (он и есть ключ доступа) не уйдёт в Referer.
  a.rel = 'noopener noreferrer'
  a.target = '_blank'
  a.referrerPolicy = 'no-referrer'
  return a
}
