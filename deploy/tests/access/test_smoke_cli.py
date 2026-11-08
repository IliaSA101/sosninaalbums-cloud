"""Дымовая проверка deploy/ops/smoke.py (спека 005, design §5.3): отказы на каждом шаге, уборка, вывод без секретов.

Счастливый путь против настоящих сервиса и Caddy — `stand.sh smoke` (там же проверка `podman inspect` контейнера
и журнала Caddy для `ls`). Здесь — то, чего настоящий стенд не покажет: 500, обрыв, переадресация и чужой
сертификат на любом шаге (fake_api.py). smoke.py грузится как модуль и вызывается как CLI (main(argv)): порт и путь
токена подменяются константами модуля, домен и корень TLS — окружением, как в бою.
"""

from __future__ import annotations

import ast
import contextlib
import importlib.util
import io
import os
import re
import sys
import tempfile

import pytest

import fake_api

SMOKE = os.environ.get("SA_SMOKE_PY", "/src/deploy/ops/smoke.py")
TOKEN = "tok-" + "Zq9" * 14  # выдуманный токен теста
KEYS = ["list", "create", "put", "publish", "view", "manifest", "image", "patch", "manifest_locked", "delete", "after"]
# Шаги, до которых альбом уже создан и не удалён: при сбое уборка обязана удалить его (и только его).
LEFTOVER = {"put", "publish", "view", "manifest", "image", "patch", "manifest_locked", "delete"}

class _IdLike:
    """Поиск длинных строк base64url (id, токен, ссылка). Имена классов исключений («…Error») длинны, но безопасны."""

    _exc = re.compile(r"[A-Z][A-Za-z]*(Error|Exception|Exit|Interrupt)")
    _long = re.compile(r"[A-Za-z0-9_-]{22,}")

    def search(self, s: str):
        return self._long.search(self._exc.sub("", s))


ID_LIKE = _IdLike()


@pytest.fixture(scope="module")
def smoke():
    spec = importlib.util.spec_from_file_location("smoke_under_test", SMOKE)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


@pytest.fixture(scope="module")
def pki():
    with tempfile.TemporaryDirectory(prefix="sa-pki-") as d:
        ca = fake_api.make_ca(d, "good-ca")
        other = fake_api.make_ca(d, "other-ca")
        yield {
            "dir": d, "ca": ca[1],
            "good": fake_api.make_leaf(d, ca, "good", fake_api.HOST),
            "alien": fake_api.make_leaf(d, other, "alien", fake_api.HOST),  # цепочка не от доверенного корня
            "wrongname": fake_api.make_leaf(d, ca, "wrongname", "other.example"),  # корень тот, имя не то
        }


@pytest.fixture()
def env(smoke, pki, monkeypatch, tmp_path):
    tok = tmp_path / "token"
    tok.write_text(TOKEN + "\n")
    monkeypatch.setattr(smoke, "TOKEN_PATH", str(tok))
    monkeypatch.setenv("SA_DOMAIN", fake_api.HOST)
    monkeypatch.setenv("SSL_CERT_FILE", pki["ca"])
    monkeypatch.delenv("SSL_CERT_DIR", raising=False)
    monkeypatch.setattr(smoke.time, "sleep", lambda s: sleeps.append(s))
    sleeps.clear()
    started = []

    def start(cert=None):
        f = fake_api.Fake(TOKEN, cert or pki["good"])
        f.start()
        started.append(f)
        monkeypatch.setattr(smoke, "PORT", f.port)
        return f

    yield start
    for f in started:
        f.stop()


sleeps: list[float] = []


def run(smoke, cmd: str) -> tuple[int, str]:
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        rc = smoke.main(["smoke.py", cmd])
    return rc, out.getvalue()


def no_secrets(out: str, fake: fake_api.Fake) -> None:
    assert TOKEN not in out and "Bearer" not in out
    assert not ID_LIKE.search(out), "в выводе длинная строка (токен, id или ссылка?)"
    for a in fake.albums.values():
        assert a.id not in out
    if fake.password_seen:
        assert fake.password_seen not in out
    assert "other.example" not in out, "адрес переадресации в выводе"


def _src() -> str:
    with open(SMOKE, encoding="utf-8") as f:
        return f.read()


def _tree() -> ast.AST:
    return ast.parse(_src())


def test_only_standard_library():
    mods = set()
    for n in ast.walk(_tree()):
        if isinstance(n, ast.Import):
            mods |= {a.name.split(".")[0] for a in n.names}
        elif isinstance(n, ast.ImportFrom):
            assert n.level == 0
            mods.add((n.module or "").split(".")[0])
    assert mods and mods <= set(sys.stdlib_module_names), f"не из стандартной библиотеки: {mods - set(sys.stdlib_module_names)}"
    assert not mods & {"logging", "urllib", "subprocess", "socket", "shutil", "tempfile", "pathlib"}, mods


def test_no_unsafe_calls():
    """Проверка TLS не отключается, файлы не пишутся, журнал и редиректы не включаются — по разбору кода, не по тексту."""
    names = set()
    opens = []
    for n in ast.walk(_tree()):
        if isinstance(n, ast.Name):
            names.add(n.id)
        elif isinstance(n, ast.Attribute):
            names.add(n.attr)
        elif isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id == "open":
            opens.append(n)
    assert not names & {"verify_mode", "CERT_NONE", "check_hostname", "_create_unverified_context", "load_verify_locations",
                        "urlopen", "HTTPRedirectHandler", "basicConfig", "getLogger"}, names
    assert "create_default_context" in names
    (o,) = opens  # единственный open — чтение файла токена, режим "rb"
    assert [a.value for a in o.args[1:]] == ["rb"] and isinstance(o.args[0], ast.Name) and o.args[0].id == "TOKEN_PATH"


def test_token_only_from_file():
    envs = [n for n in ast.walk(_tree()) if isinstance(n, ast.Attribute) and n.attr == "environ"]
    assert len(envs) == 1, "окружение читается в одном месте (SA_DOMAIN)"
    src = _src()
    assert "SA_TOKEN" not in src
    assert src.count("sys.argv") == 1, "argv читает только точка входа (разбор команды), токен в нём не ждём"
    assert re.findall(r"os\.environ[^\n]*", src) == ['os.environ.get("SA_DOMAIN", "").strip().lower()']


def test_usage_and_domain(smoke, env, monkeypatch):
    env()
    for argv in (["smoke.py"], ["smoke.py", "rm"], ["smoke.py", "ls", "extra"]):
        with contextlib.redirect_stderr(io.StringIO()):
            assert smoke.main(argv) == 2
    for bad in ("", "a/b", "a@b", "a:443", "-x.example", "a b"):
        monkeypatch.setenv("SA_DOMAIN", bad)
        rc, out = run(smoke, "ls")
        assert rc == 1 and "SA_DOMAIN" in out, bad


def test_token_file_problems(smoke, env, monkeypatch, tmp_path):
    env()
    monkeypatch.setattr(smoke, "TOKEN_PATH", str(tmp_path / "нет"))
    rc, out = run(smoke, "ls")
    assert rc == 1 and "FileNotFoundError" in out and str(tmp_path) not in out
    bad = tmp_path / "bad"
    bad.write_bytes(b"a b\n")
    monkeypatch.setattr(smoke, "TOKEN_PATH", str(bad))
    assert run(smoke, "ls")[0] == 1


def test_upload_happy_path(smoke, env):
    f = env()
    rc, out = run(smoke, "upload")
    assert rc == 0, out
    assert "итог: дымовая проверка прошла" in out
    no_secrets(out, f)
    (a,) = f.albums.values()
    assert a.deleted and a.password and len(a.pages) == 4
    # Срок — около часа вперёд, а не дольше.
    exp = smoke.parse_time(a.expires)
    delta = (exp - smoke.datetime.now(smoke.timezone.utc)).total_seconds()
    assert 3500 < delta <= 3601
    # Токен уходит только на /v1; на /a/ — ни заголовка Authorization.
    for s in f.seen:
        assert s.has_auth == s.path.startswith("/v1/"), s.path[:8]
    # Редиректов нет и подключений к другим именам тоже (сервер один), удалён один альбом, свой.
    assert [s.path for s in f.seen if s.method == "DELETE"] == [f"/v1/albums/{a.id}"]


def test_upload_refuses_nonempty_list(smoke, env):
    f = env()
    f.listing = [f.album_json(fake_api.new_id())]
    rc, out = run(smoke, "upload")
    assert rc == 1 and "сначала `smoke clean`" in out
    assert [s.method for s in f.seen] == ["GET"], "POST после непустого списка"
    no_secrets(out, f)


@pytest.mark.parametrize("mode", ["500", "drop", "redirect"])
@pytest.mark.parametrize("key", KEYS)
def test_upload_fault_each_step(smoke, env, key, mode):
    f = env()
    f.fault = (key, mode)
    rc, out = run(smoke, "upload")
    assert rc == 1, out
    assert "итог: дымовая проверка прошла" not in out
    no_secrets(out, f)
    if mode == "500":
        assert "ответ 500" in out
    elif mode == "drop":
        assert "исключение" in out
    else:
        assert "ответ 302" in out
    deletes = [s.path for s in f.seen if s.method == "DELETE"]
    if key in ("list", "create"):
        assert not deletes
    elif key in LEFTOVER:
        (a,) = f.albums.values()
        assert deletes and set(deletes) == {f"/v1/albums/{a.id}"}, "уборка трогает не свой id"
        if key != "delete":
            assert a.deleted, "остаток не убран"
    # Ни одного обращения к чужому имени/пути: переадресация не выполнена.
    assert all(s.path.startswith(("/v1/", "/a/")) for s in f.seen)


@pytest.mark.parametrize("which", ["alien", "wrongname"])
def test_upload_bad_certificate(smoke, env, pki, which):
    f = env(cert=pki[which])
    rc, out = run(smoke, "upload")
    assert rc == 1 and "SSLCertVerificationError" in out and "ответ" not in out
    assert not f.seen, "запрос ушёл по непроверенному сертификату"
    no_secrets(out, f)


def test_clean_deletes_only_smoke(smoke, env):
    f = env()
    ids = [fake_api.new_id(), fake_api.new_id()]
    f.listing = [f.album_json(i, hours=1.5) for i in ids]
    rc, out = run(smoke, "clean")
    assert rc == 0 and "остаток убран" in out
    assert sorted(s.path for s in f.seen if s.method == "DELETE") == sorted(f"/v1/albums/{i}" for i in ids)
    assert not ID_LIKE.search(out) and not any(i in out for i in ids)


def test_clean_empty(smoke, env):
    f = env()
    f.listing = []
    rc, out = run(smoke, "clean")
    assert rc == 0 and "убирать нечего" in out and [s.method for s in f.seen] == ["GET"]


@pytest.mark.parametrize("name,listing", [
    ("чужое название", lambda f: [f.album_json(fake_api.new_id(), title="Свадьба"), f.album_json(fake_api.new_id())]),
    ("одно из двух чужое", lambda f: [f.album_json(fake_api.new_id()), f.album_json(fake_api.new_id(), title="smoke2")]),
    ("срок дальше 2 ч", lambda f: [f.album_json(fake_api.new_id(), hours=2.5)]),
    ("срок через сутки", lambda f: [f.album_json(fake_api.new_id(), hours=24)]),
    ("три альбома", lambda f: [f.album_json(fake_api.new_id()) for _ in range(3)]),
    ("срок в непонятном виде", lambda f: [{**f.album_json(fake_api.new_id()), "expires_at": "завтра"}]),
    ("id в непонятном виде", lambda f: [f.album_json("../v1")]),
    ("название не строка", lambda f: [f.album_json(fake_api.new_id(), title=5)]),
])
def test_clean_refuses_without_delete(smoke, env, name, listing):
    f = env()
    f.listing = listing(f)
    rc, out = run(smoke, "clean")
    assert rc == 1, name
    assert not [s for s in f.seen if s.method != "GET"], f"{name}: был запрос не GET"
    assert not ID_LIKE.search(out) and "Свадьба" not in out


def test_clean_delete_fault(smoke, env):
    f = env()
    f.listing = [f.album_json(fake_api.new_id())]
    f.fault = ("delete", "500")
    rc, out = run(smoke, "clean")
    assert rc == 1 and "ответ 500" in out and TOKEN not in out and not ID_LIKE.search(out)


def test_ls_get_only_and_sizes(smoke, env):
    f = env()
    open_id, locked_id, draft_id = (fake_api.new_id() for _ in range(3))
    man_id = fake_api.new_id()
    f.albums[man_id] = fake_api.Album(man_id, manifest={"pages": [{"n": 1, "bytes": 100, "sha256": "a" * 64}, {"n": 2, "blank": True},
                                                                  {"n": 3, "bytes": 23, "sha256": "b" * 64}]}, published=True)
    f.listing = [
        f.album_json(man_id, title="Открытый"),
        f.album_json(locked_id, title="Закрытый", has_password=True),
        f.album_json(draft_id, title="Черновик", published=False),
    ]
    f.albums[man_id].expires = f.listing[0]["expires_at"]
    rc, out = run(smoke, "ls")
    assert rc == 0, out
    assert {s.method for s in f.seen} == {"GET"}
    assert [s.path for s in f.seen if s.path.startswith("/a/")] == [f"/a/{man_id}/manifest.json"], "манифест — только открытого"
    assert all(s.has_auth == s.path.startswith("/v1/") for s in f.seen)
    assert "Открытый" in out and "123" in out and "итог: альбомов 3, байт известно по 1: 123" in out
    assert out.count("—") == 2, "у запароленного и неопубликованного размер — «—»"
    assert TOKEN not in out and not any(i in out for i in (man_id, locked_id, draft_id))


def test_ls_429_pauses(smoke, env):
    f = env()
    aid = fake_api.new_id()
    f.listing = [f.album_json(aid)]
    f.albums[aid] = fake_api.Album(aid, manifest={"pages": []}, published=True)
    f.manifest_status = 429
    rc, out = run(smoke, "ls")
    assert rc == 0 and sleeps == [3] and "байт известно по 0: 0" in out and "—" in out


def test_ls_hostile_response(smoke, env):
    """Данные ответа не доверены: управляющие знаки в названии, чужие типы, огромные числа — в вывод только безопасное."""
    f = env()
    f.listing = [f.album_json(fake_api.new_id(), title="A\x1b[31m\u202eB" + "я" * 100, pages=10**30, published=False),
                 {"id": 5, "title": None, "expires_at": "x" * 500}]
    rc, out = run(smoke, "ls")
    assert rc == 0, out
    assert "\x1b" not in out and "\u202e" not in out and "я" * 41 not in out and "10" * 15 not in out and "x" * 50 not in out


def test_ls_cannot_send_non_get(smoke, env):
    env()
    r = smoke.Run("albums.test", TOKEN)
    with pytest.raises(smoke.Fail):
        smoke.call(r, "DELETE", "/v1/albums/x", limit=10, auth=True, methods=smoke.GET_ONLY)


def test_response_size_limit(smoke, env, monkeypatch):
    f = env()
    f.listing = [f.album_json(fake_api.new_id(), title="я" * 50) for _ in range(5)]
    monkeypatch.setattr(smoke, "JSON_LIMIT", 300)
    rc, out = run(smoke, "ls")
    assert rc == 1 and "больше предела" in out


def test_redirect_is_not_followed(smoke, env):
    f = env()
    f.fault = ("list", "redirect")
    rc, out = run(smoke, "ls")
    assert rc == 1 and "ответ 302" in out
    assert [s.path for s in f.seen] == ["/v1/albums"], "переадресация выполнена"
