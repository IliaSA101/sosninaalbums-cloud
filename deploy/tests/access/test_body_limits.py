"""Пределы тела у Caddy (design §1): форма входа — 4 КиБ, прочее на /a/ и /viewer/ — 1 КиБ, /v1 — 16 МиБ.

Отдельно — пункт аудита Г3: `handle @login` стоит раньше `handle /a/*`, поэтому кириллический пароль в форме
1…4 КиБ доходит до сервиса (неверный пароль — 401 с формой), а не режется общим пределом 1 КиБ (413). Форма
больше 4 КиБ — 413 от Caddy, и такая попытка в лимит не засчитывается (пароль не читался).
"""

from __future__ import annotations

import pytest

import checks
import sa
import sa_api
from sa_api import login, make_book, upload
from world import PW

KIB = 1024


def _form_of(size: int) -> bytes:
    """Форма password=<кириллица в %-кодировке> ровно в size байт."""
    head = b"password="
    body = head + b"%D0%B0%D0%B1" * ((size - len(head)) // 12)
    body += b"x" * (size - len(body))
    assert len(body) == size
    return body


def _post_login(a, body: bytes, ip: str | None = None) -> sa.Snap:
    return sa.raw(a.path("login"), method="POST", headers=sa.LOGIN_FORM, body=body, ip=ip)


@pytest.mark.parametrize("size", [1 * KIB - 1, 1 * KIB + 1, 2 * KIB, 3 * KIB + 500, 4 * KIB])
def test_login_form_up_to_4kib_reaches_the_service(size):
    a = upload(make_book(f"Форма {size}"), password=PW)
    r = _post_login(a, _form_of(size))
    assert r.status == 401, f"форма {size} Б: {r.status} (413 — Caddy режет раньше предела формы входа)"
    assert "Неверный пароль" in r.text() and not r.all("set-cookie")


@pytest.mark.parametrize("size", [4 * KIB + 1, 5 * KIB, 64 * KIB, 1024 * KIB])
def test_login_form_over_4kib_is_413_from_caddy(size):
    a = upload(make_book(f"Форма {size}"), password=PW)
    r = _post_login(a, _form_of(size))
    assert r.status == 413, f"форма {size} Б: {r.status}"
    ref = checks.not_found_ref()
    assert r.body == ref.body, "413 Caddy — страница notfound.html (handle_errors)"
    assert not r.all("set-cookie")


def test_413_is_not_an_attempt():
    a = upload(make_book("413 не попытка"), password=PW)
    ip = sa.new_ip()
    assert [_post_login(a, _form_of(4 * KIB + 1), ip).status for _ in range(8)] == [413] * 8
    codes = [login(a, f"неверный-{i}-пароль", ip).status for i in range(6)]
    assert codes == [401] * 5 + [429], codes


def test_correct_password_in_a_form_near_the_limit_still_works():
    """Предел формы не ломает вход с верным паролем среди лишних полей (в пределах 4 КиБ)."""
    a = upload(make_book("Верный пароль в большой форме"), password=PW)
    sa.note_secret(PW)
    from urllib.parse import urlencode

    body = urlencode({"junk": "я" * 450, "password": PW}).encode()
    assert 2 * KIB < len(body) < 4 * KIB
    r = _post_login(a, body)
    assert r.status == 303 and sa.session_cookie(r) is not None


@pytest.mark.parametrize("path_tail", ["", "manifest.json", "view.json"])
def test_a_other_posts_limit_1kib(w, path_tail):
    small = sa.raw(w.open.path(path_tail), method="POST", body=b"x" * 1000)
    checks.assert_not_found(small, "POST до 1 КиБ: сервис отвечает единым 404")
    big = sa.raw(w.open.path(path_tail), method="POST", body=b"x" * (KIB + 1))
    assert big.status == 413, f"POST 1 КиБ + 1 на /a/…: {big.status}"


def test_viewer_post_limit_1kib():
    assert sa.raw("/viewer/00000000/app.js", method="POST", body=b"x" * (KIB + 1)).status == 413


def test_v1_body_limit_16mib():
    r = sa_api.OWNER1.call("POST", "/v1/albums", content=b"x" * (16 * 1024 * KIB + 1), headers={"Content-Type": "application/json"})
    assert r.status == 413, f"/v1: тело 16 МиБ + 1: {r.status}"
