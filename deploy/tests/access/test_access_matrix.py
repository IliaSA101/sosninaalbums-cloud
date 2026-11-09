"""Матрица доступа (design §10): состояние альбома x адрес x вход. Ожидание — статус и заголовки §5.6; все 404
(и сервиса, и Caddy) — одни и те же байты и заголовки."""

from __future__ import annotations

import base64
import dataclasses
import json
import os
from urllib.parse import quote

import pytest

import checks
import sa
import sa_api
from sa_api import Album, form, make_book, upload
from world import PW

ADDRS = ["index", "slashless", "manifest", "view", "file", "file3", "draft_file", "prev_file", "dotdot_raw", "dotdot_enc",
         "dotdot_enc2", "dslash", "dslash2", "login_get", "head_index", "login_post"]
RAW = ("dotdot", "dslash")  # адреса, которые httpx переписал бы по пути, — уходят «как есть»
# POST входа с неверным паролем засчитывается в лимит попыток: у альбомов с паролем его шлют только невидимые
# (404 до лимита), у видимых — альбомы без пароля (303).
INVISIBLE_LOCKED_ENTRIES = {"valid-cookie-after-album-expired"}
STUDIO2 = "Студия Два"
BAD_MAC = "A" * 43
HTML = "text/html; charset=utf-8"


def fire(addr: str, a: Album, headers: dict | None = None, query: str = "") -> sa.Snap:
    i = a.id
    target = {
        "index": f"/a/{i}/", "slashless": f"/a/{i}", "manifest": f"/a/{i}/manifest.json", "view": f"/a/{i}/view.json",
        "file": f"/a/{i}/{a.file}", "file3": f"/a/{i}/{a.book.file(3)}", "head_index": f"/a/{i}/", "login_post": f"/a/{i}/login",
        "draft_file": f"/a/{i}/{a.draft_file or a.unknown_file()}", "prev_file": f"/a/{i}/{a.prev_file or a.unknown_file()}",
        "login_get": f"/a/{i}/login",
        "dotdot_raw": f"/a/{i}/../meta.json", "dotdot_enc": f"/a/{i}/%2e%2e/meta.json", "dotdot_enc2": f"/a/{i}/..%2fmeta.json",
        "dslash": f"//a/{i}/", "dslash2": f"/a//{i}/",
    }[addr] + query
    if addr.startswith(RAW):
        return sa.raw(target, headers=headers)
    if addr == "head_index":
        return sa.raw(target, method="HEAD", headers=headers)
    if addr == "login_post":
        return sa.raw(target, method="POST", headers={**sa.LOGIN_FORM, **(headers or {})}, body=form("неверный-пароль-матрицы"))
    return sa.get(target, headers=headers)


def expected(addr: str, visible: bool, locked: bool, authorized: bool) -> str:
    if not visible or addr.startswith(RAW):
        return "404"
    if addr == "slashless":
        return "308"
    if addr == "login_get":
        return "303"
    if addr == "login_post":  # у альбома без пароля — перенаправление, как у GET; с паролем — неверный пароль
        return "401" if locked else "303"
    if locked and not authorized:
        return "401"
    return {"index": "200", "head_index": "200", "manifest": "200", "view": "200", "file": "200", "file3": "200"}.get(addr, "404")


def check(s: sa.Snap, kind: str, a: Album, addr: str) -> None:
    if kind == "404" and addr == "head_index":  # HEAD: те же заголовки (в том числе длина), тела нет
        ref = checks.not_found_ref()
        assert s.status == 404 and s.body == b"" and s.comparable()[:2] == ref.comparable()[:2], checks.diff_headers(s, ref)
        return
    if kind == "404":
        checks.assert_not_found(s, addr)
        assert a.id.encode() not in s.body
        return
    assert str(s.status) == kind, f"{addr}: статус {s.status}, нужно {kind}"
    checks.check_common(s, addr)
    if kind in ("308", "303"):
        assert s.one("location") == f"/a/{a.id}/" and s.body == b"" and s.one("cache-control") == checks.CACHE_NONE
        assert not s.all("set-cookie")
    elif addr == "head_index":
        assert s.body == b"" and s.one("content-security-policy") == checks.HTML_CSP and s.one("content-type") == HTML
        assert s.one("cache-control") == (checks.CACHE_NONE if kind == "401" else checks.CACHE_PAGE)
    elif kind == "401":
        assert not s.all("set-cookie") and s.one("cache-control") == checks.CACHE_NONE
        if addr in ("index", "login_post"):  # форма входа: подпись студии, без названия альбома
            assert s.one("content-type") == HTML and s.one("content-security-policy") == checks.HTML_CSP
            assert b'name="password"' in s.body and a.book.manifest["title"].encode() not in s.body
            assert sa.SITE1.encode() not in s.body, "сайт студии — только в view.json после входа, не в форме (005-2)"
        else:
            assert s.body == b"" and s.one("content-type") == "text/plain; charset=utf-8"
            assert s.one("content-security-policy") == checks.SANDBOX_CSP
    elif addr == "index":
        text = s.text()
        assert s.one("content-type") == HTML and s.one("content-security-policy") == checks.HTML_CSP
        assert s.one("cache-control") == checks.CACHE_PAGE
        assert a.book.manifest["title"] not in text, "название альбома в HTML до скрипта"
        assert "og:image" not in text and "<b>x</b>" not in text
        assert sa.SITE1 not in text, "сайт студии — только в view.json, не в HTML страницы (005-2)"
    elif addr == "manifest":
        assert s.one("content-type") == "application/json" and s.one("content-security-policy") == checks.SANDBOX_CSP
        assert s.one("cache-control") == checks.CACHE_PAGE
        man = json.loads(s.body)
        assert man["title"] == a.book.manifest["title"] and not {"password", "expires_at", "token"} & set(man)
        assert [p["file"] for p in man["pages"] if not p.get("blank")] == a.book.files, "файлы манифеста не те, что загружены"
    elif addr == "view":
        # Владелец с сайтом (стенд-a) отдаёт {studio, site}, без сайта (стенд-b) — прежние байты {studio} (спека 005).
        want = {"studio": STUDIO2} if a.owner is sa_api.OWNER2 else {"studio": sa.STUDIO1, "site": sa.SITE1}
        assert json.loads(s.body) == want and s.one("cache-control") == checks.CACHE_PAGE
        assert (b'"site"' in s.body) == ("site" in want)
    elif addr in ("file", "file3"):
        n = 1 if addr == "file" else 3
        assert s.body == a.book.images[n] and s.one("content-type") == "image/jpeg"
        assert s.one("content-security-policy") == checks.SANDBOX_CSP and s.one("cache-control") == checks.CACHE_IMAGE
        assert s.one("etag") == '"' + a.book.file(n).removesuffix(".jpg") + '"'


# --- состояния без пароля: (имя, альбом, виден) ---------------------------------------------------------------

def _stub(w, a_id: str) -> Album:
    return dataclasses.replace(w.open, id=a_id)


def _unknown() -> str:
    return "".join("abcdefghijklmnopqrstuvwxyz234567"[b % 32] for b in os.urandom(26))


PLAIN = {
    "open": lambda w: (w.open, True),
    "draft": lambda w: (w.draft, True),
    "prev": lambda w: (w.prev, True),
    "foreign": lambda w: (w.foreign, True),
    "unpublished": lambda w: (w.unpublished, False),
    "expired-no-cookie": lambda w: (w.expired, False),
    "deleted": lambda w: (w.deleted, False),
    "unknown-id": lambda w: (_stub(w, _unknown()), False),
    "id-25-chars": lambda w: (_stub(w, w.open.id[:25]), False),
    "id-upper": lambda w: (_stub(w, w.open.id.upper()), False),
}


@pytest.mark.parametrize("addr", ADDRS)
@pytest.mark.parametrize("state", list(PLAIN))
def test_state_by_address(w, state, addr):
    a, visible = PLAIN[state](w)
    sa.note_secret(a.id)
    check(fire(addr, a), expected(addr, visible, False, True), a, addr)


# --- альбом с паролем: вход (cookie и прочее) ---------------------------------------------------------------

def _flip(cookie: str) -> str:
    return cookie[:-1] + ("B" if cookie[-1] != "B" else "C")


def _name_value(pair: str) -> str:
    return pair.split("=", 1)[1]


def _bad(n: int) -> list[str]:
    return [f"{sa.COOKIE_NAME}=1.0.{4_000_000_000 + i}.{BAD_MAC}" for i in range(n)]


# имя → (альбом, заголовки, query, виден, годный вход)
LOGINS = {
    "no-cookie": lambda w: (w.locked, {}, "", True, False),
    "password-without-cookie": lambda w: (w.locked, {"Authorization": "Bearer " + quote(PW), "X-Password": quote(PW)},
                                          "?password=" + quote(PW), True, False),
    "valid-cookie": lambda w: (w.locked, {"Cookie": w.cookie_locked}, "", True, True),
    "forged-cookie": lambda w: (w.locked, {"Cookie": _flip(w.cookie_locked)}, "", True, False),
    "other-albums-cookie": lambda w: (w.locked, {"Cookie": w.cookie_locked2}, "", True, False),
    "after-revoke-sessions": lambda w: (w.locked_rev, {"Cookie": w.cookie_rev_old}, "", True, False),
    "after-password-change": lambda w: (w.locked_pw, {"Cookie": w.cookie_pw_old}, "", True, False),
    "forged-cookie-past-exp": lambda w: (w.locked, {"Cookie": _bad(1)[0]}, "", True, False),
    # настоящая истёкшая: альбом жил 3 с, вход, срок альбома продлён PATCH, прежняя cookie (exp = старый срок) вышла
    "expired-cookie": lambda w: (w.locked_exp, {"Cookie": w.cookie_exp_old}, "", True, False),
    "five-same-name-valid-last": lambda w: (w.locked, {"Cookie": "; ".join(_bad(4) + [w.cookie_locked])}, "", True, False),
    "five-same-name-valid-first": lambda w: (w.locked, {"Cookie": "; ".join([w.cookie_locked] + _bad(4))}, "", True, True),
    "five-same-name-valid-fourth": lambda w: (w.locked, {"Cookie": "; ".join(_bad(3) + [w.cookie_locked, _bad(1)[0]])}, "", True, True),
    "valid-cookie-after-album-expired": lambda w: (w.expired, {"Cookie": w.expired_cookie}, "", False, True),
}


LOGIN_CASES = [(e, a) for e in LOGINS for a in ADDRS if a != "login_post" or e in INVISIBLE_LOCKED_ENTRIES]


@pytest.mark.parametrize(("entry", "addr"), LOGIN_CASES)
def test_login_by_address(w, entry, addr):
    a, headers, query, visible, ok = LOGINS[entry](w)
    check(fire(addr, a, headers, query), expected(addr, visible, True, ok), a, addr)


def test_expired_cookie_is_really_expired(w):
    """Контроль к «expired-cookie»: альбом жив (продлён), его прежняя cookie негодна, свежий вход даёт новую."""
    assert sa.get(w.locked_exp.path("manifest.json"), headers={"Cookie": w.cookie_exp_old}).status == 401
    fresh = sa_api.sign_in(w.locked_exp)
    assert sa.get(w.locked_exp.path("manifest.json"), headers={"Cookie": fresh}).status == 200


def test_cookie_values_differ_between_albums(w):
    """Подпись cookie зависит от id и соли: чужая cookie не годна, а значения у альбомов разные."""
    assert _name_value(w.cookie_locked) != _name_value(w.cookie_locked2)
    assert w.cookie_locked.startswith(sa.COOKIE_NAME + "=1.")


# --- неполный альбом: файл исчез с диска (стадия damage; stand.sh удаляет файл между стадиями) --------------

def test_prepare_incomplete():
    a = upload(make_book("Неполный"))
    assert sa.get(a.path(a.book.file(3))).status == 200
    sa.remember("incomplete", {"id": a.id, "manifest": a.book.manifest,
                               "images": {str(n): base64.b64encode(d).decode() for n, d in a.book.images.items()}})
    with open(os.path.join(sa.OUT, "damage.txt"), "w", encoding="utf-8") as f:
        f.write(f"owners/stand-a/albums/{a.id}/{a.book.file(3)}\n")


def _incomplete() -> Album:
    st = sa.recall("incomplete")
    book = sa_api.Book(st["manifest"], {int(n): base64.b64decode(d) for n, d in st["images"].items()})
    return Album(st["id"], book)


@pytest.mark.stage("damage")
def test_incomplete_is_404_everywhere():
    a = _incomplete()
    # Первое же обращение к пропавшему файлу — 404 и пометка «неполный»; дальше 404 на всех адресах альбома.
    checks.assert_not_found(sa.get(a.path(a.book.file(3))), "пропавший файл")
    for addr in ADDRS:
        check(fire(addr, a), "404", a, addr)
    checks.assert_not_found(sa.get(a.path(a.book.file(1))), "уцелевший файл неполного альбома")


@pytest.mark.stage("damage")
def test_incomplete_republish_via_v1():
    a = _incomplete()
    checks.assert_not_found(sa.get(a.path()), "до повторной выгрузки")
    missing = sa_api.OWNER1.update(a.id, a.book)
    assert missing == [3], f"сервис не заметил пропавший файл: missing = {missing}"
    sa_api.OWNER1.put_missing(a.id, a.book, missing)
    sa_api.OWNER1.publish(a.id)
    page3 = sa.get(a.path(a.book.file(3)))
    assert page3.status == 200 and page3.body == a.book.images[3]
    assert sa.get(a.path()).status == 200
