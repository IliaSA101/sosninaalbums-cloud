"""Свой https-сервер для test_smoke_cli: минимальный двойник /v1 и /a/ с впрыском сбоев по шагам.

Нужен ровно для того, что настоящий Caddy стенда не умеет: отвечать 500, обрывать соединение, переадресовывать и
показывать чужой сертификат на нужном шаге. Сам двойник — не проверяемое, а стенд проверки: «счастливый путь» против
настоящего сервиса и Caddy идёт отдельно (stand.sh smoke). Слушает 127.0.0.1 на случайном порту; сертификаты
выдаёт openssl из образа (на хост ничего не ставится).
"""

from __future__ import annotations

import base64
import json
import os
import re
import socketserver
import ssl
import subprocess
import threading
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone

HOST = "albums.test"  # имя в /etc/hosts контейнера стенда (127.0.0.1)
OPENSSL = "openssl"


def _run(*args: str, cwd: str) -> None:
    subprocess.run([OPENSSL, *args], cwd=cwd, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def make_ca(d: str, name: str) -> tuple[str, str]:
    """Корень (ключ, сертификат) в каталоге d; возвращает пути."""
    key, crt = os.path.join(d, f"{name}.key"), os.path.join(d, f"{name}.crt")
    _run("req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", key, "-out", crt,
         "-subj", f"/CN={name}", "-days", "2", "-addext", "basicConstraints=critical,CA:TRUE",
         "-addext", "keyUsage=critical,keyCertSign,cRLSign", cwd=d)
    return key, crt


def make_leaf(d: str, ca: tuple[str, str], name: str, dns: str) -> tuple[str, str]:
    """Сертификат сервера для имени dns, подписанный корнем ca."""
    key, csr, crt, ext = (os.path.join(d, f"{name}.{e}") for e in ("key", "csr", "crt", "ext"))
    with open(ext, "w", encoding="utf-8") as f:
        f.write(f"subjectAltName=DNS:{dns}\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\n"
                "extendedKeyUsage=serverAuth\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid\n")
    _run("req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", key, "-out", csr,
         "-subj", f"/CN={dns}", cwd=d)
    _run("x509", "-req", "-in", csr, "-CA", ca[1], "-CAkey", ca[0], "-CAcreateserial", "-out", crt, "-days", "2",
         "-extfile", ext, cwd=d)
    return key, crt


def new_id() -> str:
    return base64.b32encode(os.urandom(16)).decode().lower().rstrip("=")[:26]


@dataclass
class Album:
    id: str
    title: str = "smoke"
    manifest: dict = field(default_factory=dict)
    pages: dict[int, bytes] = field(default_factory=dict)
    published: bool = False
    password: str | None = None
    deleted: bool = False
    expires: str = ""


@dataclass
class Seen:
    method: str
    path: str
    has_auth: bool
    body: bytes


class Fake:
    """Состояние двойника и журнал запросов. fault = (ключ шага, режим): 500 | drop | redirect | slow для запросов этого шага."""

    def __init__(self, token: str, cert: tuple[str, str]):
        self.token = token
        self.cert = cert
        self.albums: dict[str, Album] = {}
        self.listing: list[dict] | None = None  # подмена ответа GET /v1/albums
        self.fault: tuple[str, str] | None = None
        self.manifest_status: int | None = None  # подмена статуса manifest.json в ls (например 429)
        self.seen: list[Seen] = []
        self.password_seen: str | None = None
        self.srv = _Server(("127.0.0.1", 0), _Handler)
        self.srv.fake = self
        self.port = self.srv.server_address[1]
        self._t = threading.Thread(target=self.srv.serve_forever, daemon=True)

    def start(self) -> None:
        self._t.start()

    def stop(self) -> None:
        self.srv.shutdown()
        self.srv.server_close()
        self._t.join(timeout=5)

    # --- маршруты ---------------------------------------------------------------------------------------------
    def key_of(self, method: str, path: str) -> str:
        if path == "/v1/albums":
            return "list" if method == "GET" else "create"
        if re.fullmatch(r"/v1/albums/[a-z2-7]{26}/pages/\d+", path):
            return "put"
        if path.endswith("/publish"):
            return "publish"
        if path.startswith("/v1/albums/"):
            return {"PATCH": "patch", "DELETE": "delete"}.get(method, "?")
        m = re.fullmatch(r"/a/([a-z2-7]{26})/(manifest\.json|[0-9a-f]{64}\.jpg)?", path)
        if m and method == "GET":
            a = self.albums.get(m.group(1))
            if a and a.deleted:
                return "after"
            if not m.group(2):
                return "view"
            if m.group(2) == "manifest.json":
                return "manifest_locked" if a and a.password else "manifest"
            return "image"
        return "?"

    def respond(self, method: str, path: str, auth: bool, body: bytes) -> tuple[int, dict, bytes] | str:
        """(статус, заголовки, тело) или 'drop'."""
        key = self.key_of(method, path)
        if self.fault and self.fault[0] == key:
            mode = self.fault[1]
            if mode in ("drop", "slow"):
                return mode  # slow: заголовки с большим Content-Length и тело по байту (обработчик)
            if mode == "redirect":
                # Адрес переадресации с «секретами»: токен и id не должны оказаться ни в выводе, ни в запросах.
                return 302, {"Location": f"https://other.example/{new_id()}?t={self.token}"}, b""
            leak = json.dumps({"error": {"code": "boom", "message": f"{self.token} {path}"}}).encode()
            return 500, {"Content-Type": "application/json"}, leak
        if key in ("list", "create", "put", "publish", "patch", "delete") and not auth:
            return 401, {}, b""
        if key == "list":
            items = self.listing if self.listing is not None else [
                {"id": a.id, "url": "x", "title": a.title, "expires_at": a.expires, "has_password": a.password is not None,
                 "published": a.published, "pages": len(a.pages)} for a in self.albums.values() if not a.deleted]
            return 200, _J, json.dumps({"albums": items}).encode()
        if key == "create":
            req = json.loads(body)
            a = Album(new_id(), manifest=req["manifest"], title=req["manifest"]["title"], expires=req["expires_at"])
            self.albums[a.id] = a
            return 201, _J, json.dumps({"id": a.id, "url": "x", "expires_at": a.expires,
                                        "missing": [p["n"] for p in a.manifest["pages"]]}).encode()
        a = self.albums.get(path.split("/")[3] if path.startswith("/v1/") else path.split("/")[2])
        if key == "put" and a:
            a.pages[int(path.rsplit("/", 1)[1])] = body
            return 204, {}, b""
        if key == "publish" and a:
            a.published = True
            return 200, _J, b"{}"
        if key == "patch" and a:
            a.password = json.loads(body)["password"]
            self.password_seen = a.password
            return 200, _J, b"{}"
        if key == "delete":  # id из подменённого списка (listing) в albums может не быть
            if a:
                a.deleted = True
            return 204, {}, b""
        if key == "view" and a and a.published:
            return 200, {"Content-Type": "text/html"}, b"<html></html>"
        if key in ("manifest", "manifest_locked") and a and a.published:
            if a.password:
                return 401, {}, b""
            if self.manifest_status:
                return self.manifest_status, {"Retry-After": "3"}, b""
            man = json.loads(json.dumps(a.manifest))
            for p in man["pages"]:
                if not p.get("blank"):
                    p["file"] = p["sha256"] + ".jpg"
            return 200, {"Content-Type": "application/json"}, json.dumps(man).encode()
        if key == "image" and a:
            for n, p in enumerate(a.manifest["pages"], 1):
                if path.endswith(p["sha256"] + ".jpg") and n in a.pages:
                    return 200, {"Content-Type": "image/jpeg"}, a.pages[n]
        return 404, {}, b""

    def album_json(self, aid: str, title: str = "smoke", hours: float = 1.0, **kw) -> dict:
        exp = (datetime.now(timezone.utc) + timedelta(hours=hours)).strftime("%Y-%m-%dT%H:%M:%S.123456789Z")
        return {"id": aid, "url": "x", "title": title, "expires_at": exp, "has_password": False, "published": True,
                "pages": 4, **kw}


_J = {"Content-Type": "application/json"}


class _Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True
    fake: Fake

    def handle_error(self, request, client_address) -> None:  # обрыв клиентом при проверке сертификата — норма
        pass


class _Handler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        srv: _Server = self.server  # type: ignore[assignment]
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(srv.fake.cert[1], srv.fake.cert[0])
        try:
            tls = ctx.wrap_socket(self.request, server_side=True)
        except (ssl.SSLError, OSError):
            return
        try:
            data = b""
            while b"\r\n\r\n" not in data:
                chunk = tls.recv(65536)
                if not chunk:
                    return
                data += chunk
            head, _, rest = data.partition(b"\r\n\r\n")
            lines = head.decode("latin-1").split("\r\n")
            method, path, _ = lines[0].split(" ", 2)
            hdrs = {k.strip().lower(): v.strip() for k, _, v in (ln.partition(":") for ln in lines[1:])}
            need = int(hdrs.get("content-length", "0"))
            while len(rest) < need:
                chunk = tls.recv(65536)
                if not chunk:
                    return
                rest += chunk
            auth = hdrs.get("authorization") == "Bearer " + srv.fake.token
            srv.fake.seen.append(Seen(method, path, "authorization" in hdrs, rest[:need]))
            out = srv.fake.respond(method, path, auth, rest[:need])
            if out == "drop":
                return
            if out == "slow":  # тело по байту каждые 0,1 с, пока клиент не оборвёт соединение (его срок короче)
                tls.sendall(b"HTTP/1.1 200 X\r\nContent-Length: 1000000\r\nConnection: close\r\n\r\n")
                pause = threading.Event()  # не time.sleep: тест подменяет time.sleep у модуля smoke (он общий)
                for _ in range(300):
                    tls.sendall(b"x")
                    pause.wait(0.1)
                return
            status, headers, body = out  # type: ignore[misc]
            head_out = f"HTTP/1.1 {status} X\r\nContent-Length: {len(body)}\r\nConnection: close\r\n" + "".join(
                f"{k}: {v}\r\n" for k, v in headers.items()) + "\r\n"
            tls.sendall(head_out.encode("latin-1") + body)
        except (ssl.SSLError, OSError, ValueError):
            return
        finally:
            try:
                tls.close()
            except OSError:
                pass
