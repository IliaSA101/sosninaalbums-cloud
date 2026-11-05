"""Заголовки ответов (design §5.6): страница, форма, cookie входа, 429, view.json, manifest.json, листалка в
/viewer/<версия>/, единые 404 без листинга."""

from __future__ import annotations

import json
import re

import pytest

import checks
import sa
import sa_api
from sa_api import OWNER1, login, make_book, sign_in, upload
from world import PW

HTML = "text/html; charset=utf-8"


def _viewer_version(w) -> str:
    m = re.search(r"/viewer/([0-9a-f]{8})/", sa.get(w.open.path()).text())
    assert m, "в странице нет адреса /viewer/<версия>/"
    return m.group(1)


def test_page_and_form_headers(w):
    page = sa.get(w.open.path())
    assert page.status == 200
    checks.check_common(page, "страница")
    assert page.one("content-type") == HTML and page.one("content-security-policy") == checks.HTML_CSP
    assert page.one("cache-control") == checks.CACHE_PAGE
    assert b"og:image" not in page.body and w.open.book.manifest["title"].encode() not in page.body
    form_page = sa.get(w.locked.path())
    assert form_page.status == 401
    checks.check_common(form_page, "форма")
    assert form_page.one("content-security-policy") == checks.HTML_CSP and form_page.one("cache-control") == checks.CACHE_NONE
    t = form_page.text()
    assert 'autocomplete="current-password"' in t and 'autocapitalize="none"' in t
    assert 'spellcheck="false"' in t and 'autocorrect="off"' in t


def test_studio_is_text_not_markup(w):
    """Подпись студии с разметкой (<b>x</b><script>) выводится текстом: в HTML и в форме её нет «сырой»."""
    for s in (sa.get(w.open.path()), sa.get(w.locked.path())):
        t = s.text()
        assert "<b>x</b>" not in t and "<script>" not in t
        assert "&lt;b&gt;x&lt;/b&gt;" in t, "подпись студии не показана экранированной"


def test_login_success_cookie_attrs(w):
    a = upload(make_book("Для cookie"), password=PW)
    r = login(a, PW)
    assert r.status == 303 and r.one("location") == a.path()
    assert r.one("cache-control") == checks.CACHE_NONE
    checks.check_common(r, "вход")
    cookies = r.all("set-cookie")
    assert len(cookies) == 1, f"Set-Cookie: {len(cookies)}"
    c = sa.parse_set_cookie(cookies[0])
    sa.note_secret(c.value)
    assert c.name == "__Secure-sa_session"
    assert re.fullmatch(r"1\.\d+\.\d+\.[A-Za-z0-9_-]{43}", c.value), "формат значения 1.<epoch>.<exp>.<mac>"
    assert "secure" in c.attrs and "httponly" in c.attrs
    assert c.attrs.get("samesite") == "Lax"
    assert c.attrs.get("path") == a.path()
    assert "domain" not in c.attrs
    assert 0 < int(c.attrs["max-age"]) <= 30 * 86400


def test_cookie_max_age_not_beyond_album_expiry():
    from datetime import timedelta

    a = upload(make_book("Короткий срок"), password=PW, expires_in=timedelta(hours=2))
    c = sa.session_cookie(login(a, PW))
    assert c is not None
    sa.note_secret(c.value)
    assert 0 < int(c.attrs["max-age"]) <= 2 * 3600


def test_wrong_password_form_without_cookie(w):
    r = login(w.locked, "совсем-не-тот-пароль")
    assert r.status == 401 and not r.all("set-cookie")
    assert r.one("cache-control") == checks.CACHE_NONE and r.one("content-security-policy") == checks.HTML_CSP
    checks.check_common(r, "неверный пароль")
    assert "Неверный пароль" in r.text()


def test_429_headers():
    a = upload(make_book("Для 429"), password=PW)
    ip = sa.new_ip()
    codes = [login(a, f"неверный-{i}-xx", ip).status for i in range(6)]
    assert codes == [401] * 5 + [429], codes
    r = login(a, PW, ip)  # даже верный пароль: попытка засчитывается до сверки
    assert r.status == 429 and not r.all("set-cookie")
    ra = r.one("retry-after")
    assert ra and re.fullmatch(r"\d+", ra) and 1 <= int(ra) <= 15 * 60, ra
    assert r.one("cache-control") == checks.CACHE_NONE and r.one("content-security-policy") == checks.HTML_CSP
    checks.check_common(r, "429")
    assert "Слишком много попыток" in r.text()


def test_view_json_and_manifest_json(w):
    v = sa.get(w.open.path("view.json"))
    assert v.status == 200 and v.one("content-type") == "application/json"
    assert json.loads(v.body) == {"studio": sa.STUDIO1}
    m = sa.get(w.open.path("manifest.json"))
    assert m.status == 200 and m.one("content-type") == "application/json"
    assert m.one("content-security-policy") == checks.SANDBOX_CSP and m.one("cache-control") == checks.CACHE_PAGE
    man = json.loads(m.body)
    assert man["title"] == "Открытый" and "password" not in man and "expires_at" not in man
    assert all(p.get("file", "").endswith(".jpg") for p in man["pages"] if not p.get("blank"))
    assert "?" not in m.text(), "в manifest.json нет строки запроса"
    other = sa.get(sa_api.upload(make_book("Второго владельца"), owner=sa_api.OWNER2).path("view.json"))
    assert json.loads(other.body) == {"studio": "Студия Два"}


def test_image_etag_range_head(w):
    a = w.open
    path = a.path(a.file)
    ok = sa.get(path)
    etag = ok.one("etag")
    assert etag == '"' + a.file.removesuffix(".jpg") + '"'
    nm = sa.get(path, headers={"If-None-Match": etag})
    assert nm.status == 304 and nm.body == b""
    rg = sa.get(path, headers={"Range": "bytes=0-9"})
    assert rg.status == 206 and rg.body == a.book.images[1][:10] and rg.one("content-range") == f"bytes 0-9/{len(a.book.images[1])}"
    with sa.client() as c:
        h = sa.snap_of(c.head(a.path()))
    assert h.status == 200 and h.body == b"" and h.one("content-security-policy") == checks.HTML_CSP


def test_viewer_assets(w):
    ver = _viewer_version(w)
    js = sa.get(f"/viewer/{ver}/app.js")
    assert js.status == 200 and js.one("content-type") == "text/javascript; charset=utf-8"
    assert js.one("cache-control") == "public, max-age=31536000, immutable"
    checks.check_common(js, "app.js")
    assert js.one("content-security-policy") == checks.HTML_CSP, "CSP на /viewer/ ставит Caddy (сервис её не ставит)"
    css = sa.get(f"/viewer/{ver}/viewer.css")
    assert css.status == 200 and css.one("content-type") == "text/css; charset=utf-8"
    for name in ("SOURCE.txt", "LICENSE-page-flip.txt"):
        txt = sa.get(f"/viewer/{ver}/vendor/{name}")
        assert txt.status == 200 and txt.one("content-type") == "text/plain; charset=utf-8", name
        assert txt.one("x-content-type-options") == "nosniff"
    checks.assert_not_found(sa.get("/viewer/00000000/app.js"), "чужая версия листалки")
    checks.assert_not_found(sa.get(f"/viewer/{ver}/index.html"), "html листалки не отдаётся как файл")
    checks.assert_not_found(sa.get(f"/viewer/{ver}/nope.js"), "нет файла")


@pytest.mark.parametrize("path", ["/", "/favicon.ico", "/meta/", "/meta/x.json", "/data/", "/a/", "/a", "/viewer", "/viewer/",
                                  "/viewer/{ver}/", "/viewer/{ver}/vendor/", "/viewer/{ver}/vendor", "/robots.txt", "/v1", "/v1/x"])
def test_no_listing_and_no_stray_routes(w, path):
    """Листинга нет, meta/ и прочее не отдаются: вне маршрутов — единый 404."""
    p = path.replace("{ver}", _viewer_version(w))
    s = sa.get(p)
    if p == "/v1/x":  # /v1/* идёт в сервис без токена: ответ контракта (401), но не данные
        assert s.status in (401, 404)
        return
    checks.assert_not_found(s, p)


@pytest.mark.parametrize("method", ["POST", "PUT", "PATCH", "DELETE", "OPTIONS"])
@pytest.mark.parametrize("tail", ["", "manifest.json", "view.json"])
def test_other_methods_are_404(w, method, tail):
    r = sa.raw(w.open.path(tail), method=method, body=b"x=1" if method in ("POST", "PUT", "PATCH") else b"")
    checks.assert_not_found(r, f"{method} /a/<id>/{tail}")


def test_v1_headers_from_caddy():
    r = OWNER1.call("GET", "/v1/health")
    assert r.status == 200 and r.one("content-type").startswith("application/json")
    for k, v in checks.COMMON.items():
        assert r.one(k) == v, k
    assert r.one("content-security-policy") == checks.HTML_CSP
    assert r.get("via") is None and r.get("server") is None and r.get("alt-svc") is None
    assert r.one("strict-transport-security") == checks.HSTS


def test_cookie_after_login_opens_album_and_form_not_shown_again(w):
    a = upload(make_book("Повторный заход"), password=PW)
    cookie = sign_in(a)
    again = sa.get(a.path(), headers={"Cookie": cookie})
    assert again.status == 200 and b'name="password"' not in again.body


CONDITIONAL = {"Range": "bytes=0-9", "If-Range": '"x"', "If-None-Match": "*", "If-Match": '"nope"',
               "If-Modified-Since": "Wed, 21 Oct 2037 07:28:00 GMT", "If-Unmodified-Since": "Wed, 21 Oct 1970 07:28:00 GMT"}


@pytest.mark.parametrize("header", list(CONDITIONAL))
@pytest.mark.parametrize("where", ["caddy-own", "service"])
def test_404_ignores_range_and_conditional_headers(w, where, header):
    """Свой 404 Caddy (file_server) выполнил бы Range и условные запросы — 206, 304, 412 вместо тех же байт."""
    path = "/no-such-route" if where == "caddy-own" else "/a/" + "a" * 26 + "/"
    checks.assert_not_found(sa.get(path, headers={header: CONDITIONAL[header]}), f"{where} + {header}")


@pytest.mark.parametrize("header", list(CONDITIONAL))
def test_413_ignores_range_and_conditional_headers(header):
    r = sa.raw("/a/" + "a" * 26 + "/", method="POST", body=b"x" * 2048, headers={header: CONDITIONAL[header]})
    ref = checks.not_found_ref()
    assert r.status == 413 and r.body == ref.body, f"{header}: {r.status}"
    assert {k: v for k, v in r.headers if k not in sa.VOLATILE} == {k: v for k, v in ref.headers if k not in sa.VOLATILE}
