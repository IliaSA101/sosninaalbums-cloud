"""Общее для матрицы доступа: окружение стенда, клиенты, «сырой» запрос, снимок ответа.

Тесты ходят к Caddy стенда из контейнера в его сетевом пространстве (stand.sh): https://<домен стенда>,
сертификат — корень `tls internal`, а адрес источника выбирает сам тест (127.x.y.z): так лимит попыток по IP
проверяется на настоящих разных клиентах, а не на одном адресе из-за трансляции порта.
"""

from __future__ import annotations

import json
import os
import random
import re
import socket
import ssl
from dataclasses import dataclass

import httpx

BASE = os.environ["SA_BASE"]
HOST = os.environ["SA_HOST"]
CA = os.environ["SA_CA"]
PUBLIC_BASE = os.environ["SA_PUBLIC_BASE"]
STUDIO1 = os.environ["SA_STUDIO1"]
TOKEN1 = os.environ["SA_TOKEN1"]
TOKEN2 = os.environ["SA_TOKEN2"]
STAGE = os.environ.get("SA_STAGE", "main")
OUT = os.environ.get("SA_OUT", "/out")

# Cookie входа (design §5.4) и пределы формы входа (design §5.2).
COOKIE_NAME = "__Secure-sa_session"
LOGIN_FORM = {"Content-Type": "application/x-www-form-urlencoded"}

_ctx = ssl.create_default_context(cafile=CA)
_used_ips: set[str] = set()


def new_ip() -> str:
    """Свежий адрес клиента 127.x.y.z (последний октет не 0: маска журнала /24 не должна совпасть с адресом).

    Случайный и не повторяющийся в прогоне: лимит попыток по IP живёт в памяти сервиса, чужой прогон или
    соседний тест не должен делить с этим тестом ключ.
    """
    while True:
        ip = f"127.{random.randint(16, 239)}.{random.randint(0, 255)}.{random.randint(1, 254)}"
        if ip not in _used_ips:
            _used_ips.add(ip)
            note_secret(ip)
            return ip


class Client(httpx.Client):
    """httpx без собственной памяти: cookie, редиректы и повторы — только то, что тест задал руками."""

    def send(self, request: httpx.Request, **kw):  # type: ignore[override]
        try:
            return super().send(request, **kw)
        finally:
            self.cookies.clear()


def client(ip: str | None = None, timeout: float = 30.0) -> Client:
    """Клиент к стенду; ip — адрес источника (по умолчанию — свежий). Только HTTP/1.1 по https."""
    transport = httpx.HTTPTransport(verify=_ctx, local_address=ip or new_ip(), retries=0, http2=False)
    return Client(base_url=BASE, transport=transport, follow_redirects=False, timeout=timeout)


# --- секреты прогона для стадии «журналы»: всё, чего в журналах быть не должно -----------------------------

def note_secret(value: str) -> None:
    """Дописывает значение (id, пароль, cookie, адрес…) в список для проверки журналов."""
    if value:
        with open(os.path.join(OUT, "secrets.txt"), "a", encoding="utf-8") as f:
            f.write(value.replace("\n", " ") + "\n")


def remember(key: str, value) -> None:
    """Состояние между стадиями прогона (stand.sh запускает pytest по стадиям): out/state.json."""
    path = os.path.join(OUT, "state.json")
    data = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    data[key] = value
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f)


def recall(key: str):
    with open(os.path.join(OUT, "state.json"), encoding="utf-8") as f:
        return json.load(f)[key]


# --- снимок ответа ------------------------------------------------------------------------------------------

VOLATILE = ("date", "connection")


@dataclass
class Snap:
    status: int
    headers: list[tuple[str, str]]  # имя в нижнем регистре, порядок как пришёл
    body: bytes
    reason: str = ""

    def all(self, name: str) -> list[str]:
        return [v for k, v in self.headers if k == name.lower()]

    def get(self, name: str) -> str | None:
        vals = self.all(name)
        return vals[0] if vals else None

    def one(self, name: str) -> str | None:
        """Значение заголовка, если он ровно один; иначе — пометка о повторе (тест это увидит)."""
        vals = self.all(name)
        if len(vals) == 1:
            return vals[0]
        return None if not vals else "<повтор: " + " | ".join(vals) + ">"

    def comparable(self) -> tuple:
        """Для сравнения «одинаковые байты и заголовки»: без Date и Connection (сам запрос raw просит close);
        остальное — как есть, порядок не важен."""
        return (self.status, sorted((k, v) for k, v in self.headers if k not in VOLATILE), self.body)

    def text(self) -> str:
        return self.body.decode("utf-8", "replace")


def snap_of(r: httpx.Response) -> Snap:
    return Snap(r.status_code, [(k.decode().lower(), v.decode("latin-1")) for k, v in r.headers.raw], r.content, r.reason_phrase)


def get(path: str, ip: str | None = None, headers: dict | None = None, c: Client | None = None) -> Snap:
    cl = c or client(ip)
    try:
        return snap_of(cl.get(path, headers=headers))
    finally:
        if c is None:
            cl.close()


# --- «сырой» запрос: путь уходит в сеть как написан (httpx сворачивает «..» и «.») ---------------------------

def raw(target: str, method: str = "GET", headers: dict | list | None = None, body: bytes = b"", ip: str | None = None, timeout: float = 20.0) -> Snap:
    """Один запрос по TLS с целью запроса «как есть»; Connection: close, ответ читается до конца."""
    hdrs: list[tuple[str, str]] = [("Host", HOST), ("Connection", "close")]
    items = headers.items() if isinstance(headers, dict) else (headers or [])
    hdrs += list(items)
    if body or method in ("POST", "PUT", "PATCH"):
        hdrs.append(("Content-Length", str(len(body))))
    head = f"{method} {target} HTTP/1.1\r\n" + "".join(f"{k}: {v}\r\n" for k, v in hdrs) + "\r\n"
    sock = socket.create_connection(("127.0.0.1", 443), timeout=timeout, source_address=(ip or new_ip(), 0))
    try:
        with _ctx.wrap_socket(sock, server_hostname=HOST) as tls:
            tls.sendall(head.encode("latin-1") + body)
            data = b""
            while True:
                try:
                    chunk = tls.recv(65536)
                except (ssl.SSLError, ConnectionResetError):
                    break
                if not chunk:
                    break
                data += chunk
    finally:
        sock.close()
    return _parse(data)


def _parse(data: bytes) -> Snap:
    head, _, rest = data.partition(b"\r\n\r\n")
    lines = head.decode("latin-1").split("\r\n")
    m = re.match(r"HTTP/1\.[01] (\d{3}) ?(.*)", lines[0])
    if not m:
        raise AssertionError(f"ответ не HTTP: {lines[0][:60]!r}")
    headers = [(k.strip().lower(), v.strip()) for k, _, v in (ln.partition(":") for ln in lines[1:])]
    body = rest
    te = [v for k, v in headers if k == "transfer-encoding"]
    if te and "chunked" in te[0].lower():
        body, out = rest, b""
        while body:
            size_line, _, body = body.partition(b"\r\n")
            size = int(size_line.split(b";")[0] or b"0", 16)
            if size == 0:
                break
            out, body = out + body[:size], body[size + 2 :]
        body = out
    return Snap(int(m.group(1)), headers, body, m.group(2))


# --- Set-Cookie -------------------------------------------------------------------------------------------

@dataclass
class SetCookie:
    name: str
    value: str
    attrs: dict[str, str]  # имя атрибута в нижнем регистре; флаги — пустая строка

    @property
    def pair(self) -> str:
        return f"{self.name}={self.value}"


def parse_set_cookie(line: str) -> SetCookie:
    first, *rest = [p.strip() for p in line.split(";")]
    name, _, value = first.partition("=")
    attrs = {}
    for p in rest:
        k, _, v = p.partition("=")
        attrs[k.strip().lower()] = v.strip()
    return SetCookie(name, value, attrs)


def session_cookie(s: Snap) -> SetCookie | None:
    for line in s.all("set-cookie"):
        c = parse_set_cookie(line)
        if c.name == COOKIE_NAME:
            return c
    return None
