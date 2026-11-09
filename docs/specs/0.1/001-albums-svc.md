# 001. Сервис листалок MVP (`albums-svc`)

Статус: draft

Техрешение — [001-albums-svc/design.md](001-albums-svc/design.md); развилки — [decisions.md](decisions.md),
раздел «спека 001». Контракт `/v1` — у программы: спека 146 (design §2.2, §2.3, §6, `security-review.md`) и
модуль `api/albums` (`v1/openapi.yaml`); здесь только ссылки. Решение по стеку — см. приватные заметки.

## Зачем

Фотограф нажимает «Выгрузить» в Sosnina Albums и получает ссылку на листалку для клиента. Сервис
принимает страницы по `/v1`, хранит на диске и показывает альбом по ссылке, по желанию — с паролем и
сроком. Сейчас — один фотограф; второй добавляется строкой конфига.

## Поведение (сценарии)

1. **Выгрузка.** `POST /v1/albums` → `PUT …/pages/{n}` для `missing` → `publish` → ссылка
   `https://<домен>/a/<id>/` (id — 26 знаков `a–z2–7`). До `publish` все адреса `/a/<id>/…` — 404.
2. **Обновление по той же ссылке.** `PUT /v1/albums/{id}` → в `missing` только изменённые страницы →
   `publish`. До `publish` клиент видит прежнюю редакцию.
3. **Клиент листает.** `/a/<id>/` — листалка из бинарника сервиса: название из манифеста, подпись студии
   из конфига, развороты по порядку, белые страницы, телефон и ПК. Скачать нельзя: кнопки нет.
4. **Пароль.** Без входа — форма (подпись студии, без названия); верный пароль — cookie до 30 дней (не
   дольше срока альбома); неверный — «Неверный пароль»; частые неудачи — 429 «Слишком много попыток».
5. **Фотограф меняет доступ.** `PATCH`: продлить, сменить или снять пароль, `revoke_sessions`. Смена
   пароля и сброс сразу закрывают все прежние входы.
6. **Срок.** После `expires_at` — 404 сразу; файлы удаляет уборка (до 5 минут).
7. **Отзыв.** `DELETE` — альбом удалён сразу, все адреса — 404.
8. **Брошенная выгрузка.** Ни разу не опубликованный альбом удаляется через сутки; неопубликованная правка
   отбрасывается через сутки от первой правки, опубликованная редакция остаётся.
9. **Краевые.** Нет, чужой, истёкший, отозванный — одинаковый 404 (`/v1` и `/a/`), неполный — на `/a/`
   (через `/v1` его можно выложить заново); плохой токен —
   одинаковый 401; предел и квота — 413; страница не та — 409 или 400 (design §4).

## Модель угроз (новое)

| Угроза | Мера (design) |
|---|---|
| Файл из сети: огромный, не картинка, с EXIF, обход пути | длина до чтения, `MaxBytesReader(bytes)`, хэш (409), сигнатура, белый список маркеров, размеры (400), имена от сервиса, `os.Root`, строгий шаблон пути (§2, §4, §5.1) |
| Диск забит украденным токеном | квота, число альбомов, запас места и на `PUT` страницы, правка чистит файлы прежней правки (§4, §3) |
| Гонки приёма с удалением и уборкой | финал страницы под мьютексом с повторной проверкой, каталог только в `POST` (§3) |
| Подбор пароля (~21 бит у «Придумать») | атомарный лимит по альбому+IP, IP, альбому; HMAC с солью и перцем (§5.2, §5.5) |
| Подделка или кража входа | `__Secure-` cookie с HMAC от id, соли, `epoch`, срока; сверка целой строкой (§5.4) |
| Чужой процесс на машине, подмена IP | unix-сокет, контейнер без сети, Caddy срезает входящие `X-Sa-*` (§1) |
| Утечка токена, пароля, ссылки | `sha256` токена в конфиге, сравнение за постоянное время; журналы без id, пути, IP (§4, §7) |
| ПД у ботов и в индексах | HTML без названия, нет `og:image`, `noindex`, `no-referrer`, CSP (§5.6) |
| Утёк секрет или бэкап | секрет вне `data/` и бэкапа, HKDF; бэкап шифрован, ≤ 30 дней (§6, §8) |

## Критерии приёмки

Go (`go test -race ./...`; картинки — генерируемые однотонные JPEG; часы — фейковые):

- [ ] `TestConformance`: `albumstest.Conformance` с `WithOtherToken` на `httptest`; он же с журналом в буфер.
- [ ] `store`: `TestStoreAtomicWrite`, `TestStoreCrashEachStep` (отказ ФС на каждом шаге и перезапуск: сирот
  нет, `meta` старая или новая), `TestStorePerms`, `TestStoreRootEscape`, `TestStoreLock`,
  `TestMetaUnknownFieldsKept`, `TestMetaNewerFormatRefuses`, `TestMetaQuarantine` (и неразбираемый), `TestSweepAtStart`,
  `TestSweepSparesActive` (идущий `PUT`, `writeAtomic`, окно `POST`), `TestTrashOnePurge`.
- [ ] `album`: `TestCreateMissing`, `TestIDFormat`, `TestUpdateKeepsPublished`, `TestPublishPagesMissing`,
  `TestPublishRemovesOld`, `TestExpiryClamp`, `TestExpired404BeforeSweep`, `TestUnpublishedTTL`,
  `TestDraftTTLFromFirstEdit`, `TestQuota`, `TestQuotaReplacePending`, `TestDiskReserve`,
  `TestManifestReserialized`, `TestDeleteImmediate`, `TestIncomplete404`, `TestOldManifestFile404`,
  `TestClockHoldPersists` (назад, вперёд, все удаления по времени), `TestClockOK`,
  `TestRestoreFromMeta`, `TestListOwnOnly`.
- [ ] Гонки: `TestPutPageRaceDelete`, `TestPutPageRaceUpdate`, `TestPutPageRaceSweep`,
  `TestPublishRacePut`, `TestSweepRacePatch`.
- [ ] Приём: `TestPutPageLengthBeforeRead`, `TestPutPageLengthMismatch409`, `TestPutPageStreamLimit`,
  `TestPutPageHash409`, `TestPutPageSignature400`, `TestPutPageExif400`, `TestPutPageTypeMismatch400`,
  `TestPutPageDims400`, `TestPutPageMarkers` (COM, APP2 — да; APP13 — 400), `TestPutPageNoLength`,
  `TestV1BadID404`, `TestPutPageBlank400`, `TestPutPageIdempotent`, `TestPutPageBusy429`,
  `TestWebPOff400`, `TestJSONBody413`, `TestPagesLimit413`, `TestNonCanonicalPath404`.
- [ ] `access`: `TestTokenAllCompared`, `TestToken401Same`, `TestPasswordNFC`, `TestPasswordTrim`,
  `TestPasswordNewSalt`, `TestPasswordLimitsBeforeHash`, `TestCookieRoundTrip`, `TestCookieTamper` (все
  позиции знака), `TestCookieStrictNumbers`, `TestCookieManySameName`, `TestCookieOtherAlbum`,
  `TestCookieExpired`, `TestCookieEpoch`, `TestLimiterKeys` (/64, `Unmap`), `TestLimiterConcurrent`
  (200 параллельных неверных — не больше предела), `TestLimiterBounded`, `TestLimiterRetryAfter`.
- [ ] `view`: `TestViewMatrix` (все 404 — одинаковые байты), `TestViewHeaders`, `TestViewNoTitleInHTML`,
  `TestLoginCookieAttrs`, `TestLoginWrongNoCookie`, `TestLoginEmptyCounted`, `TestLogin429`,
  `TestLoginNoClientIP503`, `TestLoginExpired404`, `TestLoginBodyLimit`.
- [ ] Прочее: `TestHealthLimits`, `TestErrorShape`, `TestServerTimeouts`, `TestConfigValidate`,
  `TestSecretFormat`, `TestGenToken`, `TestVerify`, `TestAfterRestoreEpoch`, `TestAfterRestoreLocked`, `TestLogNoSecrets` (с отказом
  диска и паникой), `FuzzViewPath`.

Матрица доступа (pytest + httpx, стенд Podman: Caddy + контейнер; оси — design §10):

- [ ] `test_access_matrix`, `test_headers`, `test_spoofed_headers`, `test_login_bruteforce`,
  `test_path_bypass`, `test_conformance_via_caddy`, `test_logs_clean` (и при 502).

Листалка (Playwright, телефон 390×844 и ПК 1440×900):

- [ ] Листается по порядку, обложка и белые страницы на месте; на телефоне — вертикально.
- [ ] Название и подпись студии видны; `<b>x</b><script>` в названии и в подписи — текстом.
- [ ] Нет `og:image`, кнопки скачивания, внешних запросов, нарушений CSP; к `manifest.json` нет `?`.
- [ ] Форма входа: неверный, верный, повторный заход без формы; 404 — нейтральная страница.
- [ ] Картинку убрала публикация правки — листалка перечитывает манифест.

## Не входит

- Счётчик просмотров, комментарии, «Утвердить» — этап 8 (тогда SQLite).
- Скачивание страниц и архива клиентом (решение владельца 31.10, п. 2).
- Метрики Prometheus — к этапу 7 (001-8); веб-админка; горячая перезагрузка конфига; WebP (001-6).
- Раскатка, механизм бэкапа, автообновления — этапы 4 и 7. Требования к ним: бэкап со страницами,
  шифрованный, ≤ 30 дней, без секрета (design §8); срок хранения журналов Caddy.
- Ссылки клиентам — только с конечного домена (001-22): временный поддомен пришлось бы держать с
  переадресацией весь срок альбомов.

## Технические заметки

- Пакеты и слои — design §9. Новая зависимость — `golang.org/x/text` (NFC); модуль контракта — по метке
  `api/albums/v0.1.0` (есть с 29.10), без `replace`.
- Пределы — из контракта (15 МБ, 300 страниц, 1 ГБ), `max_days` 366 (001-12; в модуле 365); квота 10 ГБ,
  запас 2 ГБ, лимиты попыток — стартовые значения конфига, уточнить замером реальных выгрузок.
- Сведения для контракта (К1, К2, К4–К7) — design §11; ответ программы 31.10: 0.1 не блокируют.

**Необратимое — второе мнение до кода:** формат ссылки (26 знаков base32); `data/LAYOUT` 1 и раскладка
`owners/<slug>/albums/<id>/`; `meta.json` формат 1; пароль (соль, HKDF `pw/1`, NFC); cookie
`__Secure-sa_session` значение `1.…`; формат файла секрета; состав бэкапа.

## Задачи по зонам

| № | Зона | Что | Готово, когда |
|---|---|---|---|
| Т0 | оркестратор (владелец сказал «да» 31.10) | depguard по design §9, зависимость `x/text` | правила в `dev` |
| Г1 | Go | `go get` модуля контракта `@v0.1.0`, `config` (секрет, HKDF), `-gen-secret`, `-gen-token`, `logx`, `store`, `imgcheck`, токены и HMAC пароля | тесты `store`, конфига, `imgcheck`, токена, пароля |
| Г2 | Go | `album`, `api`, уборка, сторож часов, `cmd/albums-svc`, `verify`, `after-restore` | критерии `album`, гонок, приёма |
| Г3 | Go | cookie, лимит попыток, `view`, встраивание листалки; `TestConformance`, `TestLogNoSecrets` | все Go-критерии |
| В1 | листалка | `web/viewer/` по `manifest.json` и `view.json`, StPageFlip от программы, телефон, белые страницы, CSS формы, 404 без стилей, без встроенных скриптов и стилей | критерии листалки |
| И1 | развёртывание | `deploy/`: Containerfile (без root, только бинарник), Quadlet (`Network=none`, `ReadOnly`, `DropCapability=ALL`, `NoNewPrivileges`, `UserNS`, `Memory`, `PidsLimit`, `Secret` файлом, образ по дайджесту), Caddyfile-шаблон (§1, §7, `notfound.html` из `web/viewer/`) | гипотеза сокета проверена заглушкой-слушателем, шаблоны проходят проверку синтаксиса |
| П1 | проверки | локальный стенд целиком (Caddy + контейнер), матрица pytest (`tests/access/`), листалка в браузере | критерии матрицы и листалки |

Порядок: Т0 → Г1 → Г2 ‖ В1 ‖ И1 → Г3 → П1 (стенд — после Г3, В1, И1). Аудит безопасности — после Г2 (приём файлов), Г3 (вход) и И1
(Caddy, контейнер). `api` и `album` — резать по эндпоинтам с первого раза (пределы — `scripts/limits.mjs`).

## Открытые вопросы

- Отступления от решения по стеку (001-1, 001-2, 001-4, 001-5, 001-8, 001-20) приняты владельцем 31.10.
- И1, гипотеза: unix-сокет из rootless-контейнера с `Network=none` доступен Caddy; не выйдет — TCP на
  loopback с `trusted_proxy` (design §1).
- Чья зона `tests/access/` — решает оркестратор.
