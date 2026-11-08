"""Проба сети сервера до реестра образов (спека 004, design §11, решение 004-17).

Запускается provision.sh configure НА СЕРВЕРЕ по ssh, до любого изменения на нём (stdin: `python3 -I - <образ> ...`).
Скачивает образ целиком: манифест по дайджесту D, затем все блобы (конфигурация и слои), и сверяет размер и sha256 каждого
с тем, что записано в манифесте. Ничего не пишет на диск: байты хэшируются и отбрасываются. Нужна она потому, что из РФ
встречалась обрезка слоёв на 16 КиБ при ответе 200 (ВР-16): по коду ответа этого не поймать, поймают размер и хэш.

  ghcr_probe.py <реестр/имя@sha256:D> [--deadline СЕК] [--min-bps БАЙТ/С]

Коды: 0 - образ получен целиком; 1 - не получен (сеть, обрыв, хэш, предел времени или скорости); 2 - ошибка вызова.
Только stdlib; пользователь непривилегированный (под root provision.sh опускает права до nobody). Токен анонимный, на чтение
одного репозитория, и в вывод не попадает. Схема http допускается лишь для петлевого адреса (локальный реестр стенда).
"""

import hashlib
import http.client
import ipaddress
import json
import re
import ssl
import sys
import time
from urllib.parse import urlencode, urljoin, urlsplit

MAX_MANIFEST = 1 << 20  # манифест больше мегабайта - не манифест образа
MAX_TOKEN_BODY = 64 << 10
MAX_BLOB = 64 << 20  # слой, не влезающий в предел, - не наш образ (боевой около 5 МБ суммарно)
MAX_TOTAL = 128 << 20
MAX_LAYERS = 64
MAX_REDIRECTS = 5
CHUNK = 64 << 10
STALL = 30.0  # столько секунд без единого байта - обрыв
GRACE = 15.0  # до этого момента скорость не оценивается (разгон соединения, выдача токена)
MANIFEST_TYPES = ("application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json")

REF_RE = re.compile(r"^([a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:[0-9]{1,5})?)/([a-z0-9][a-z0-9._/-]{0,200})@sha256:([0-9a-f]{64})$")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
TOKEN_RE = re.compile(r"^[A-Za-z0-9._~+/=-]{1,8192}$")
PARAM_RE = re.compile(r'([A-Za-z_]+)="([^"]*)"')


class Fail(Exception):
    """Образ не получен целиком; текст - причина для владельца (без токенов и без адресов, которых нет в ссылке)."""


def loopback(host):
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


class Sink:
    """Приёмник тела ответа: считает байты и sha256; при keep сохраняет тело (манифест, ответ выдачи токена)."""

    def __init__(self, keep=False):
        self.keep = keep
        self.reset()

    def reset(self):
        self.h = hashlib.sha256()
        self.n = 0
        self.data = bytearray()

    def write(self, chunk):
        self.h.update(chunk)
        self.n += len(chunk)
        if self.keep:
            self.data += chunk


class Probe:
    def __init__(self, ref, deadline, min_bps):
        m = REF_RE.match(ref)
        if not m:
            raise ValueError("образ: ожидается реестр/имя@sha256:<64 hex>")
        self.host, self.name, self.digest = m.group(1), m.group(4), "sha256:" + m.group(5)
        self.scheme = "http" if loopback(self.host.rsplit(":", 1)[0] if ":" in self.host else self.host) else "https"
        self.start = time.monotonic()
        self.deadline = self.start + deadline
        self.min_bps = min_bps
        self.bytes = 0
        self.token = None
        self.ctx = ssl.create_default_context()

    def left(self):
        return self.deadline - time.monotonic()

    def tick(self):
        """Предел общего времени и минимальной средней скорости (после разгона)."""
        now = time.monotonic()
        if now > self.deadline:
            raise Fail("предел времени исчерпан")
        el = now - self.start
        if el > GRACE and self.bytes / el < self.min_bps:
            raise Fail(f"скорость ниже {self.min_bps} байт/с")

    def get(self, url, auth, limit, sink):
        """GET с переходами по Location. Authorization - только на исходный адрес (подписанный адрес хранилища слоёв его не
        должен получать). Возвращает (код, заголовки); тело при 200 уходит в sink, при другом коде читается до 64 КиБ и
        отбрасывается. Тело длиннее limit, обрыв и расхождение с Content-Length - отказ."""
        try:
            origin = urlsplit(url).netloc
        except ValueError:
            raise Fail("адрес реестра не разбирается") from None
        for _ in range(MAX_REDIRECTS + 1):
            conn = None
            try:
                u = urlsplit(url)
                port = u.port  # ValueError при порте вроде :abc или :99999
                if u.scheme != self.scheme or not u.hostname or u.username is not None:
                    raise Fail("переход на недопустимый адрес (схема или учётные данные)")
                # Инвариант «http только на петлю» - для каждого перехода, не только для адреса реестра.
                if u.scheme == "http" and not loopback(u.hostname):
                    raise Fail("переход по http на непетлевой узел")
                self.tick()
                cls = http.client.HTTPSConnection if u.scheme == "https" else http.client.HTTPConnection
                kw = {"context": self.ctx} if u.scheme == "https" else {}
                conn = cls(u.hostname, port, timeout=max(0.2, min(STALL, self.left())), **kw)
                hdr = {"Accept-Encoding": "identity", "User-Agent": "sa-ghcr-probe", "Accept": ", ".join(MANIFEST_TYPES)}
                if auth and u.netloc == origin and self.token:
                    hdr["Authorization"] = "Bearer " + self.token
                conn.request("GET", (u.path or "/") + ("?" + u.query if u.query else ""), headers=hdr)
                r = conn.getresponse()
                if r.status in (301, 302, 303, 307, 308):
                    loc = r.getheader("Location")
                    if not loc:
                        raise Fail(f"ответ {r.status} без Location")
                    url = urljoin(url, loc)
                    continue
                cl = r.getheader("Content-Length")
                if r.status != 200:
                    self.read_body(conn, r, 64 << 10, Sink(), strict=False)
                    return r.status, r.headers
                # isascii: у верхних индексов и других цифр вне ASCII isdigit() истинно, а int() падает.
                if cl is not None and not (cl.isascii() and cl.isdigit()):
                    raise Fail("Content-Length не число")
                if cl is not None and int(cl) > limit:
                    raise Fail("Content-Length больше предела")
                self.read_body(conn, r, limit, sink, strict=True, cl=None if cl is None else int(cl))
                return 200, r.headers
            except (OSError, http.client.HTTPException) as e:
                raise Fail(f"нет ответа или обрыв ({type(e).__name__})") from None
            except (ValueError, RecursionError):
                raise Fail("ответ или адрес перехода не разбираются") from None
            finally:
                if conn is not None:
                    conn.close()
        raise Fail("слишком много переходов")

    def read_body(self, conn, r, limit, sink, strict, cl=None):
        total = 0
        while True:
            self.tick()
            if conn.sock is not None:
                conn.sock.settimeout(max(0.2, min(STALL, self.left())))
            # read1: один recv на вызов. read(n) крутил бы recv до n байт, и сервер, отдающий байт в полминуты, держал бы
            # пробу мимо tick() сколько угодно.
            chunk = r.read1(min(CHUNK, limit + 1 - total))
            if not chunk:
                break
            total += len(chunk)
            self.bytes += len(chunk)
            if total > limit:
                if strict:
                    raise Fail("тело длиннее заявленного")
                break
            sink.write(chunk)
        if strict and cl is not None and total != cl:
            raise Fail(f"получено {total} байт из {cl} (Content-Length): обрыв")

    def fetch_token(self, challenge):
        """Анонимный токен по заголовку WWW-Authenticate: realm обязан быть на том же узле, что реестр."""
        if not challenge or not challenge.startswith("Bearer "):
            raise Fail("реестр требует не анонимный токен Bearer")
        p = dict(PARAM_RE.findall(challenge))
        realm = p.get("realm", "")
        ru = urlsplit(realm)
        if ru.scheme != self.scheme or ru.netloc != self.host or ru.query or ru.fragment:
            raise Fail("адрес выдачи токена не на узле реестра")
        q = {"scope": p.get("scope") or f"repository:{self.name}:pull"}
        if p.get("service"):
            q["service"] = p["service"]
        sink = Sink(keep=True)
        code, _ = self.get(realm + "?" + urlencode(q), False, MAX_TOKEN_BODY, sink)
        if code != 200:
            raise Fail(f"выдача токена: ответ {code}")
        try:
            doc = json.loads(bytes(sink.data))
            tok = doc.get("token") or doc.get("access_token")
        except (ValueError, AttributeError, RecursionError):
            raise Fail("выдача токена: не JSON") from None
        if not isinstance(tok, str) or not TOKEN_RE.match(tok):
            raise Fail("выдача токена: нет токена или недопустимые знаки")
        self.token = tok

    def authed(self, path, limit, sink):
        """GET к реестру; на 401 - один раз взять анонимный токен и повторить (токен живёт недолго, на слоях - снова)."""
        url = f"{self.scheme}://{self.host}{path}"
        for attempt in range(2):
            sink.reset()
            code, hdr = self.get(url, True, limit, sink)
            if code == 200:
                return
            if code != 401 or attempt:
                raise Fail(f"{path.split('/')[-2]}: ответ {code}")
            self.fetch_token(hdr.get("WWW-Authenticate"))

    def run(self):
        sink = Sink(keep=True)
        self.authed(f"/v2/{self.name}/manifests/{self.digest}", MAX_MANIFEST, sink)
        if "sha256:" + sink.h.hexdigest() != self.digest:
            raise Fail("манифест: sha256 не равен дайджесту образа")
        blobs = self.blobs(bytes(sink.data))
        for digest, size in blobs.items():
            sink = Sink()
            self.authed(f"/v2/{self.name}/blobs/{digest}", size, sink)
            if sink.n != size:
                raise Fail(f"блоб {digest[7:19]}: получено {sink.n} байт из {size}")
            if "sha256:" + sink.h.hexdigest() != digest:
                raise Fail(f"блоб {digest[7:19]}: sha256 не сошёлся")
        return len(blobs)

    def blobs(self, raw):
        """{дайджест: размер} конфигурации и слоёв из манифеста образа; индекс и странные значения - отказ."""
        try:
            doc = json.loads(raw)
            if doc.get("schemaVersion") != 2 or "manifests" in doc:
                raise ValueError
            if doc.get("mediaType", MANIFEST_TYPES[0]) not in MANIFEST_TYPES:
                raise ValueError
            descs = [doc["config"]] + list(doc["layers"])
            out = {}
            for d in descs:
                size = d["size"]
                if not DIGEST_RE.match(d["digest"]) or type(size) is not int or not 0 < size <= MAX_BLOB:
                    raise ValueError
                if out.setdefault(d["digest"], size) != size:
                    raise ValueError
        except (ValueError, KeyError, TypeError, AttributeError, RecursionError):
            raise Fail("манифест не похож на манифест образа (нужен один образ, не индекс)") from None
        if len(descs) > MAX_LAYERS + 1 or sum(out.values()) > MAX_TOTAL:
            raise Fail("манифест: слишком много слоёв или суммарный размер выше предела")
        return out


def parse(argv):
    if len(argv) < 2:
        raise ValueError("нужен образ")
    ref, deadline, min_bps, i = argv[1], 600.0, 20480, 2
    while i < len(argv):
        if argv[i] not in ("--deadline", "--min-bps") or i + 1 >= len(argv) or not argv[i + 1].isdigit():
            raise ValueError("ожидаются --deadline СЕК и --min-bps БАЙТ/С")
        if argv[i] == "--deadline":
            deadline = float(argv[i + 1])
        else:
            min_bps = int(argv[i + 1])
        i += 2
    if not 0 < deadline <= 3600:
        raise ValueError("--deadline: от 1 до 3600 секунд")
    return ref, deadline, min_bps


def main(argv):
    try:
        ref, deadline, min_bps = parse(argv)
        p = Probe(ref, deadline, min_bps)
    except ValueError as e:
        print(f"ghcr_probe: {e}", file=sys.stderr)
        return 2
    try:
        n = p.run()
    except Fail as e:
        print(f"ghcr_probe: образ не получен целиком: {e}", file=sys.stderr)
        return 1
    print(f"ghcr_probe: ok, блобов {n}, байт {p.bytes}, {time.monotonic() - p.start:.1f} с")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
