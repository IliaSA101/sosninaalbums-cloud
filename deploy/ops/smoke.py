#!/usr/bin/env python3
"""Дымовая проверка боевого сервера и список альбомов (спека 005, design §5; решения 005-9, 005-29).

Запуск: `python3 -I smoke.py upload|clean|ls` в контейнере контроллера на ops-ВМ (provision.sh smoke, albums) и в
образе стенда (stand.sh smoke). Только стандартная библиотека Python: pip в образе контроллера нет.

  upload  полная выгрузка по контракту с токеном владельца `smoke`: список пуст -> POST (срок: сейчас + 1 ч) -> PUT
          страниц -> publish -> страница, manifest.json и картинка открываются -> PATCH пароля -> manifest.json 401 ->
          DELETE -> страница 404. DELETE в `finally` касается только id этого прогона.
  clean   остаток прошлого сбойного прогона: удаляются ВСЕ альбомы списка, если они все с названием `smoke`, их не
          больше двух и срок каждого не дальше «сейчас + 2 ч»; иначе отказ без единого DELETE (токен не тот).
  ls      список альбомов (токеном фотографа `ops-ls`): только GET.

Что делает код нарочно:
  - токен читается только из файла /run/sa/token (том ro): не из окружения и не из argv, чтобы его не показал
    `podman inspect`; в вывод и ошибки он не попадает;
  - база — https://$SA_DOMAIN, аргументом не задаётся: опечатка не отправит токен чужому серверу;
  - TLS — системное хранилище (на стенде корень `tls internal` подкладывает stand.sh через SSL_CERT_FILE, который
    читает OpenSSL), проверка имени и цепочки не отключается; редиректы не выполняются (3xx — провал шага);
  - главный обработчик ловит BaseException и печатает номер шага и ИМЯ КЛАССА исключения, не его текст: в тексте
    ошибки могут оказаться адрес, id или заголовок; из тела отказа берётся только короткий код контракта;
  - ничего не пишется на диск, журнал (logging) не включается; id, ссылка, пароль PATCH и токен не печатаются;
  - размеры ответов ограничены, у каждого соединения таймаут, у прогона — общий срок.
Код выхода: 0 — успех, 1 — провал проверки, 2 — неверный вызов. Подсказку про ufw при отказе соединения печатает
обёртка раскатки.
"""

import base64
import hashlib
import http.client
import json
import os
import re
import secrets
import ssl
import sys
import time
from datetime import datetime, timedelta, timezone

sys.dont_write_bytecode = True

TOKEN_PATH = "/run/sa/token"  # том ro, 0600; тесты подменяют путь
PORT = 443  # тесты подменяют порт, чтобы поднять свой сервер
TIMEOUT = 15.0  # сокет: соединение и каждое чтение, секунды
DEADLINE = 300.0  # весь прогон, секунды
JSON_LIMIT = 4 << 20  # ответ API и manifest.json: список из ~10 тыс. альбомов
HTML_LIMIT = 256 << 10  # страница просмотра
IMG_LIMIT = 1 << 20  # картинка стенда — килобайты; больше — это не то, что мы загрузили
ERR_LIMIT = 16 << 10
TITLE = "smoke"
TTL = timedelta(hours=1)  # срок альбома: остаток при сбое уходит сам
CLEAN_MAX_AHEAD = timedelta(hours=2)  # clean трогает только альбомы со сроком не дальше этого
CLEAN_MAX_ALBUMS = 2
PAUSE_MAX = 60  # потолок паузы по Retry-After, секунды
PAGES = 4  # front, две страницы, back
W, H = 120, 160

ID_RE = re.compile(r"^[a-z2-7]{26}\Z")
FILE_RE = re.compile(r"^[0-9a-f]{64}\.jpg\Z")
CODE_RE = re.compile(r"^[a-z0-9_.-]{1,40}\Z")
DOMAIN_RE = re.compile(r"^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?\Z")
TIME_RE = re.compile(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)\Z")

# Однотонный JPEG 120x160 (image/jpeg Go, качество 75, не фото); соль в сегменте COM делает каждую страницу своей.
_JPEG_B64 = (
    "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJ"
    "DAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAKAAeAMBIgAC"
    "EQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQy"
    "gZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeI"
    "iYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEB"
    "AQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEK"
    "FiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Sl"
    "pqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AMWiiivKPvgooooA"
    "KKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKK"
    "ACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACii"
    "igAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAo"
    "oooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooA"
    "KKKKACiiigAooooA/9k="
)


class Fail(Exception):
    """Провал проверки: текст — только из этого файла (константы), без данных сервера."""


class Run:
    """Состояние прогона: номер и имя шага для сообщений, домен, токен, общий срок."""

    def __init__(self, domain, token):
        self.domain = domain
        self.token = token
        self.n = 0
        self.name = "начало"
        self.t_end = time.monotonic() + DEADLINE
        self.ctx = ssl.create_default_context()  # системные корни; проверка имени и цепочки включена

    def step(self, name):
        self.n += 1
        self.name = name

    def ok(self, detail=""):
        print(f"[{self.n}] {self.name}: ок{(' ' + detail) if detail else ''}", flush=True)

    def fail(self, why):
        raise Fail(why)


class Resp:
    def __init__(self, status, headers, body):
        self.status = status
        self.headers = headers
        self.body = body

    def json(self):
        try:
            return json.loads(self.body)
        except ValueError:
            raise Fail("ответ не JSON") from None


def call(run, method, path, *, limit, body=None, ctype=None, auth=False, methods=None):
    """Один запрос по https на новом соединении. Редиректы не выполняются (http.client их не делает: 3xx вернётся
    как есть и проверка шага его отвергнет), так что Authorization не уйдёт на другой хост. Тело ответа читается
    не больше limit байт: больше — провал."""
    if methods is not None and method not in methods:
        raise Fail("метод не из разрешённых этой командой")
    left = run.t_end - time.monotonic()
    if left <= 0:
        raise Fail("вышел общий срок прогона")
    headers = {"Accept": "*/*", "Connection": "close", "User-Agent": "sa-smoke"}
    if auth:
        headers["Authorization"] = "Bearer " + run.token
    if ctype:
        headers["Content-Type"] = ctype
    conn = http.client.HTTPSConnection(run.domain, PORT, timeout=min(TIMEOUT, left), context=run.ctx)
    try:
        conn.request(method, path, body=body, headers=headers)
        r = conn.getresponse()
        data = r.read(limit + 1)
        if len(data) > limit:
            raise Fail("ответ больше предела")
        return Resp(r.status, {k.lower(): v for k, v in r.getheaders()}, data)
    finally:
        conn.close()


def api_code(resp):
    """Короткий код отказа из тела {"error": {"code": …}} — только если он похож на код контракта."""
    try:
        e = json.loads(resp.body[:ERR_LIMIT]).get("error")
        code = e.get("code") if isinstance(e, dict) else e
    except (ValueError, AttributeError):
        return "?"
    return code if isinstance(code, str) and CODE_RE.match(code) else "?"


def expect(run, resp, status, extra=None):
    """Код ответа шага — ровно ожидаемый; иначе провал с кодом ответа и кодом отказа контракта (но не телом)."""
    if resp.status != status:
        tail = f", код отказа {api_code(resp)}" if resp.status >= 400 and resp.headers.get("content-type", "").startswith("application/json") else ""
        run.fail(f"ответ {resp.status}, ожидали {status}{tail}")
    run.ok(f"{resp.status}" + (f" {extra}" if extra else ""))


def jpeg_page(salt):
    raw = base64.b64decode(_JPEG_B64)
    return raw[:2] + b"\xff\xfe" + (2 + len(salt)).to_bytes(2, "big") + salt + raw[2:]


def make_book():
    """Манифест (file пуст, как отправляет программа) и картинки по номерам страниц."""
    pages, images = [], {}
    for n in range(1, PAGES + 1):
        data = jpeg_page(secrets.token_bytes(8))
        images[n] = data
        role = "front" if n == 1 else "back" if n == PAGES else "page"
        pages.append({"n": n, "role": role, "type": "image/jpeg", "sha256": hashlib.sha256(data).hexdigest(),
                      "bytes": len(data), "w": W, "h": H})
    manifest = {"schema": 1, "generator": "smoke", "title": TITLE, "book": {"cover": True, "page_mm": {"w": 210, "h": 297}},
                "pages": pages}
    return manifest, images


def iso(t):
    return t.astimezone(timezone.utc).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_time(s):
    """RFC 3339 из ответа сервера -> aware datetime; ничего другого не принимаем."""
    m = TIME_RE.match(s) if isinstance(s, str) else None
    if not m:
        raise Fail("срок альбома в непонятном виде")
    tz = "+00:00" if m.group(3) == "Z" else m.group(3)
    try:
        return datetime.fromisoformat(m.group(1) + tz)
    except ValueError:
        raise Fail("срок альбома в непонятном виде") from None


def list_albums(run):
    """GET /v1/albums -> список словарей; форма проверена, поля — только нужные и по типам."""
    r = call(run, "GET", "/v1/albums", limit=JSON_LIMIT, auth=True, methods=None)
    expect(run, r, 200)
    items = r.json().get("albums") if isinstance(r.json(), dict) else None
    if not isinstance(items, list) or not all(isinstance(a, dict) for a in items):
        run.fail("список альбомов в непонятном виде")
    return items


def album_id(a):
    i = a.get("id")
    if not isinstance(i, str) or not ID_RE.match(i):
        raise Fail("идентификатор альбома в непонятном виде")
    return i


# --- upload ---------------------------------------------------------------------------------------------------

def cmd_upload(run):
    run.step("список альбомов владельца smoke (должен быть пуст)")
    if list_albums(run):
        run.fail("у владельца smoke уже есть альбомы: сначала `smoke clean`")
    manifest, images = make_book()
    state = {"id": None}
    try:
        run.step("создание альбома, срок 1 ч")
        body = json.dumps({"manifest": manifest, "expires_at": iso(datetime.now(timezone.utc) + TTL)}).encode()
        r = call(run, "POST", "/v1/albums", limit=JSON_LIMIT, body=body, ctype="application/json", auth=True)
        if r.status == 201:
            state["id"] = album_id(r.json())  # сразу: от этого зависит уборка в finally
        expect(run, r, 201)
        aid = state["id"]
        missing = r.json().get("missing")
        if sorted(missing if isinstance(missing, list) else []) != list(range(1, PAGES + 1)):
            run.fail("список недостающих страниц не тот")
        run.step("загрузка страниц")
        for n in range(1, PAGES + 1):
            r = call(run, "PUT", f"/v1/albums/{aid}/pages/{n}", limit=ERR_LIMIT, body=images[n], ctype="image/jpeg", auth=True)
            if r.status != 204:
                expect(run, r, 204)
        run.ok(f"204 x{PAGES}")
        run.step("публикация")
        expect(run, call(run, "POST", f"/v1/albums/{aid}/publish", limit=JSON_LIMIT, auth=True), 200)
        run.step("страница просмотра")
        expect(run, call(run, "GET", f"/a/{aid}/", limit=HTML_LIMIT), 200)
        run.step("manifest.json")
        r = call(run, "GET", f"/a/{aid}/manifest.json", limit=JSON_LIMIT)
        expect(run, r, 200)
        man = r.json()
        files = [p.get("file") for p in man.get("pages", []) if isinstance(p, dict)] if isinstance(man, dict) else []
        want = [hashlib.sha256(images[n]).hexdigest() + ".jpg" for n in range(1, PAGES + 1)]
        if files != want or man.get("title") != TITLE:
            run.fail("манифест опубликованного альбома не совпал с загруженным")
        run.step("картинка")
        if not FILE_RE.match(files[0]):
            run.fail("имя файла страницы в непонятном виде")
        r = call(run, "GET", f"/a/{aid}/{files[0]}", limit=IMG_LIMIT)
        expect(run, r, 200)
        if not r.headers.get("content-type", "").startswith("image/jpeg") or r.body != images[1]:
            run.fail("картинка не та или не image/jpeg")
        run.step("пароль (PATCH)")
        pw = secrets.token_urlsafe(16)  # не печатается
        body = json.dumps({"password": pw}).encode()
        expect(run, call(run, "PATCH", f"/v1/albums/{aid}", limit=JSON_LIMIT, body=body, ctype="application/json", auth=True), 200)
        run.step("manifest.json с паролем закрыт")
        expect(run, call(run, "GET", f"/a/{aid}/manifest.json", limit=ERR_LIMIT), 401)
        run.step("удаление")
        expect(run, call(run, "DELETE", f"/v1/albums/{aid}", limit=ERR_LIMIT, auth=True), 204)
        state["id"] = None
        run.step("страница после удаления")
        expect(run, call(run, "GET", f"/a/{aid}/", limit=HTML_LIMIT), 404)
    finally:
        if state["id"]:
            cleanup(run, state["id"])
    print("итог: дымовая проверка прошла", flush=True)
    return 0


def cleanup(run, aid):
    """Уборка в finally: DELETE только id этого прогона. Свои сбои не маскируют исходный."""
    print("уборка: удаление альбома этого прогона", flush=True)
    try:
        r = call(run, "DELETE", f"/v1/albums/{aid}", limit=ERR_LIMIT, auth=True)
        print(f"уборка: ответ {r.status}" + ("" if r.status in (204, 404) else "; остаток уйдёт по сроку (1 ч) или `smoke clean`"),
              flush=True)
    except BaseException as e:  # noqa: BLE001 — уборка не должна заменить исходную ошибку
        print(f"уборка: исключение {type(e).__name__}; остаток уйдёт по сроку (1 ч) или `smoke clean`", flush=True)


# --- clean ----------------------------------------------------------------------------------------------------

def cmd_clean(run):
    run.step("список альбомов владельца smoke")
    items = list_albums(run)
    if not items:
        print("итог: убирать нечего", flush=True)
        return 0
    limit = datetime.now(timezone.utc) + CLEAN_MAX_AHEAD
    ids = []
    for a in items:
        if a.get("title") != TITLE or parse_time(a.get("expires_at")) > limit:
            run.fail("в списке есть альбом не от smoke или со сроком дальше 2 ч: удалений не было (токен не тот?)")
        ids.append(album_id(a))
    if len(ids) > CLEAN_MAX_ALBUMS:
        run.fail(f"альбомов больше {CLEAN_MAX_ALBUMS}: удалений не было (токен не тот?)")
    run.step("удаление остатка")
    for i in ids:
        r = call(run, "DELETE", f"/v1/albums/{i}", limit=ERR_LIMIT, auth=True)
        if r.status != 204:
            expect(run, r, 204)
    run.ok(f"204 x{len(ids)}")
    print("итог: остаток убран", flush=True)
    return 0


# --- ls -------------------------------------------------------------------------------------------------------

GET_ONLY = frozenset({"GET"})
NO_BYTES = "—"


def clean_text(s, width):
    """Название — ПД фотографа и данные сервера: только печатные знаки, длина ограничена, пробелы схлопнуты."""
    s = s if isinstance(s, str) else ""
    s = "".join(c if c.isprintable() else "?" for c in s)
    return (s[: width - 1] + "…") if len(s) > width else s


def manifest_bytes(run, aid):
    """Сумма bytes страниц из manifest.json; None, если открыть нельзя (429 — пауза по Retry-After)."""
    r = call(run, "GET", f"/a/{aid}/manifest.json", limit=JSON_LIMIT, methods=GET_ONLY)
    if r.status == 429:
        try:
            pause = int(r.headers.get("retry-after", "1"))
        except ValueError:
            pause = 1
        time.sleep(max(0, min(pause, PAUSE_MAX, int(run.t_end - time.monotonic()))))
        return None
    if r.status != 200:
        return None
    try:
        pages = json.loads(r.body).get("pages")
        sizes = [p.get("bytes", 0) for p in pages if isinstance(p, dict) and not p.get("blank")]
    except (ValueError, AttributeError, TypeError):
        return None
    if not all(isinstance(b, int) and not isinstance(b, bool) and 0 <= b <= 1 << 40 for b in sizes):
        return None
    return sum(sizes)


def cmd_ls(run):
    run.step("список альбомов")
    r = call(run, "GET", "/v1/albums", limit=JSON_LIMIT, auth=True, methods=GET_ONLY)
    expect(run, r, 200)
    items = r.json().get("albums") if isinstance(r.json(), dict) else None
    if not isinstance(items, list):
        run.fail("список альбомов в непонятном виде")
    total, known = 0, 0
    print(f"{'название':40} {'стр.':>4} {'срок':20} {'пароль':6} {'опубл.':6} {'байт':>12}")
    for a in items:
        if not isinstance(a, dict):
            run.fail("элемент списка в непонятном виде")
        locked, published = a.get("has_password") is True, a.get("published") is True
        size = None
        if published and not locked and ID_RE.match(a.get("id") if isinstance(a.get("id"), str) else ""):
            size = manifest_bytes(run, a["id"])
        if size is not None:
            total, known = total + size, known + 1
        pages = a.get("pages")
        pages = pages if isinstance(pages, int) and not isinstance(pages, bool) and 0 <= pages <= 100000 else "?"
        try:  # цифры и строки из ответа не доверены: срок печатаем только после разбора, своим форматом
            exp = parse_time(a.get("expires_at")).astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
        except Fail:
            exp = "?"
        print(f"{clean_text(a.get('title'), 40):40} {pages:>4} {exp:20} {'да' if locked else 'нет':6} "
              f"{'да' if published else 'нет':6} {size if size is not None else NO_BYTES:>12}")
    print(f"итог: альбомов {len(items)}, байт известно по {known}: {total}", flush=True)
    return 0


# --- вход -----------------------------------------------------------------------------------------------------

COMMANDS = {"upload": cmd_upload, "clean": cmd_clean, "ls": cmd_ls}


def read_token():
    with open(TOKEN_PATH, "rb") as f:
        raw = f.read(257)
    tok = raw.decode("ascii").rstrip("\r\n")
    if not (1 <= len(tok) <= 200) or not tok.isprintable() or " " in tok:
        raise Fail("файл токена в непонятном виде")
    return tok


def main(argv):
    if len(argv) != 2 or argv[1] not in COMMANDS:
        print("использование: smoke.py upload|clean|ls (база — SA_DOMAIN, токен — /run/sa/token)", file=sys.stderr)
        return 2
    run = Run("", "")
    try:
        domain = os.environ.get("SA_DOMAIN", "").strip().lower()
        if not DOMAIN_RE.match(domain):
            raise Fail("SA_DOMAIN не задан или не имя сервера")
        run.domain = domain
        run.step("токен из файла")
        run.token = read_token()
        run.ok()
        return COMMANDS[argv[1]](run)
    except Fail as e:
        print(f"шаг {run.n} ({run.name}): провал: {e}", flush=True)
        return 1
    except BaseException as e:  # noqa: BLE001 — любой сбой: только шаг и класс, текст исключения может содержать id или адрес
        print(f"шаг {run.n} ({run.name}): исключение {type(e).__name__}", flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
