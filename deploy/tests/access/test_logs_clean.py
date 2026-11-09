"""Журналы без секретов (design §7): в журналах Caddy и сервиса нет id альбома, пароля, cookie, токена, строки
запроса, заголовков и точных адресов клиентов — и при остановленном сервисе (502).

Три стадии (stand.sh): main — трафик с «секретами» везде, где они могут оказаться (адрес, строка запроса,
Cookie, Authorization, Referer, тело формы); down — то же при остановленном сервисе, ответ 502 с единым
телом; logs — stand.sh собрал журналы в out/caddy.log и out/svc.log, и тесты ищут в них всё, что тест называл
секретом (out/secrets.txt) и что по виду похоже на id или токен.
"""

from __future__ import annotations

import json
import os
import re
from urllib.parse import quote

import pytest

import checks
import sa
import sa_api
from sa_api import form, make_book
from world import PW

ALLOWED_SVC_KEYS = {"time", "level", "msg", "route", "status", "code", "bytes", "ms", "owner", "album", "op", "err",
                    "reason", "count", "kind", "step"}


def _marks() -> dict[str, str]:
    m = {k: k + "-" + os.urandom(6).hex() for k in ("qmark", "cookie", "referer", "agent", "header")}
    for v in m.values():
        sa.note_secret(v)
    return m


def _traffic(a_open, a_locked, cookie: str, ip: str | None = None) -> list[sa.Snap]:
    """Запросы с секретами везде, где они могут оказаться. Возвращает ответы (для 502 — проверка ответов)."""
    m = _marks()
    tok = sa.TOKEN1
    h = {"Cookie": f"{cookie}; other={m['cookie']}", "Authorization": f"Bearer {tok}", "Referer": f"https://x.example/a/{a_open.id}/?{m['referer']}",
         "User-Agent": m["agent"], "X-Mark": m["header"]}
    q = f"?token={tok}&password={quote(PW)}&q={m['qmark']}"
    out = [
        sa.get(a_open.path() + q, headers=h),
        sa.get(a_locked.path("manifest.json") + q, headers=h),
        sa.get(a_locked.path(a_locked.file) + q, headers=h),
        sa.get(f"/v1/albums/{a_open.id}" + q, headers=h),
        sa.get("/v1/albums" + q, headers=h),
        sa.get(f"/viewer/00000000/app.js?{m['qmark']}", headers=h),
        sa.get(f"/no-such-route/{a_open.id}?{m['qmark']}", headers=h),
        sa.get(f"/{a_open.id}/x?{m['qmark']}", headers=h),
        sa.raw(f"//a/{a_open.id}/?{m['qmark']}", headers=h),
        sa.raw(f"/A/{a_open.id}/?{m['qmark']}", headers=h),
        sa.raw(f"/%61/{a_open.id}/?{m['qmark']}", headers=h),
        sa.raw(f"/a/{a_open.id}/../x?{m['qmark']}", headers=h),
    ]
    with sa.client(ip) as c:
        for pw in (PW, "неверный-" + m["qmark"]):
            sa.note_secret(pw)
            out.append(sa.snap_of(c.post(a_locked.path("login") + f"?pw={m['qmark']}", content=form(pw), headers={**sa.LOGIN_FORM, **h})))
    return out


def test_prepare_log_traffic(w):
    for r in _traffic(w.open, w.locked, w.cookie_locked):
        assert r.status in (200, 303, 401, 404, 429)
    sa.remember("log_ids", {"open": w.open.id, "locked": w.locked.id, "file": w.locked.file})


@pytest.mark.stage("down")
def test_502_when_service_stopped():
    ids = sa.recall("log_ids")
    open_, locked = sa_api.Album(ids["open"], make_book()), sa_api.Album(ids["locked"], make_book())
    ref = checks.not_found_ref()
    seen = _traffic(open_, locked, f"{sa.COOKIE_NAME}=1.0.4000000000.{'A' * 43}")
    # Прокси-маршруты (/a/, /v1/, /viewer/) при остановленном сервисе — 502 с телом notfound.html и общими заголовками;
    # вне маршрутов Caddy отвечает сам — 404, как и раньше.
    proxied = [s for s in seen if s.status == 502]
    assert len(proxied) >= 8, f"502 получено {len(proxied)} из {len(seen)}: {[s.status for s in seen]}"
    for s in seen:
        assert s.status in (502, 404, 413), s.status
        assert s.body == ref.body, "тело ответа не notfound.html"
        hs = {k: v for k, v in s.headers if k not in sa.VOLATILE}
        assert hs == {k: v for k, v in ref.headers if k not in sa.VOLATILE}, f"заголовки не как у единого 404: {checks.diff_headers(s, ref)}"


@pytest.mark.stage("down")
def test_502_ignores_range_and_conditional_headers():
    ids = sa.recall("log_ids")
    ref = checks.not_found_ref()
    for h in ({"Range": "bytes=0-9"}, {"If-None-Match": "*"}, {"If-Modified-Since": "Wed, 21 Oct 2037 07:28:00 GMT"}):
        s = sa.get(f"/a/{ids['open']}/", headers=h)
        assert s.status == 502 and s.body == ref.body, f"{h}: {s.status}"


# --- стадия logs ---------------------------------------------------------------------------------------------

def _read(name: str) -> str:
    with open(os.path.join(sa.OUT, name), encoding="utf-8", errors="replace") as f:
        return f.read()


def _forms(secret: str) -> set[str]:
    """Значение в тех видах, в каких оно могло попасть в строку журнала: как есть, в %-кодировке, в JSON-эскейпе."""
    return {secret, quote(secret), json.dumps(secret)[1:-1], quote(secret, safe="")}


def _secrets() -> list[str]:
    raw = {ln.strip() for ln in _read("secrets.txt").splitlines()} | {sa.TOKEN1, sa.TOKEN2}
    return sorted(s for s in raw if len(s) >= 6)


@pytest.fixture(scope="module")
def logs() -> dict[str, str]:
    return {"caddy": _read("caddy.log"), "svc": _read("svc.log")}


@pytest.mark.stage("logs")
@pytest.mark.parametrize("which", ["caddy", "svc"])
def test_logs_are_not_empty(logs, which):
    lines = [ln for ln in logs[which].splitlines() if ln.strip()]
    assert len(lines) > 100, f"журнал {which}: {len(lines)} строк — проверка была бы пустой"
    assert len(_secrets()) > 50


@pytest.mark.stage("logs")
@pytest.mark.parametrize("which", ["caddy", "svc"])
def test_no_secret_in_log(logs, which):
    text = logs[which]
    leaked = []
    for s in _secrets():
        if any(f in text for f in _forms(s)):
            leaked.append(s[:4] + "…")  # само значение в отчёт не печатаем
    assert not leaked, f"в журнале {which} есть секреты: {len(leaked)} (начала: {leaked[:5]})"


@pytest.mark.stage("logs")
@pytest.mark.parametrize("which", ["caddy", "svc"])
def test_no_id_or_token_shaped_strings(logs, which):
    text = logs[which]
    assert not re.search(r"[a-z2-7]{26}", text, re.I), "в журнале строка вида id альбома (26 знаков a-z2-7)"
    for word in ("Bearer", "Authorization", "Set-Cookie", "sa_session", "password", "X-Sa-Client"):
        assert word.lower() not in text.lower(), f"в журнале {which}: «{word}»"


@pytest.mark.stage("logs")
def test_caddy_log_masks_path_query_and_ip(logs):
    access, errors, proxy_errors = [], [], []
    for ln in logs["caddy"].splitlines():
        try:
            rec = json.loads(ln)
        except ValueError:
            pytest.fail(f"строка журнала Caddy не JSON: {ln[:80]!r}")
        if str(rec.get("logger", "")).startswith("http.log.access"):
            access.append(rec)
        if str(rec.get("logger", "")).startswith("http.log.error"):  # журнал ошибок обработки запросов (log сайта)
            errors.append(rec)
        elif "request" in rec and rec.get("level") == "error":  # ошибки прокси: 502 при остановленном сервисе
            proxy_errors.append(rec)
    assert len(access) > 100
    used_ips = {s for s in _secrets() if re.fullmatch(r"127\.\d+\.\d+\.\d+", s)}
    for rec in access + errors + proxy_errors:
        req = rec["request"]
        assert re.fullmatch(r"/[A-Za-z0-9]{0,8}/…", req["uri"]), f"uri в журнале: {req['uri'][:30]!r}"
        for k in ("remote_ip", "client_ip"):
            assert re.fullmatch(r"\d+\.\d+\.\d+\.0", req[k]), f"{k} без маски /24: {req[k]}"
            assert req[k] not in used_ips
        assert "headers" not in req and "remote_port" not in req, "заголовки или порт клиента в журнале"
        assert "resp_headers" not in rec
    assert any(rec.get("status") == 502 for rec in access), "в журнале нет 502 (стадия down не прошла?)"
    # При остановленном сервисе reverse_proxy пишет ошибку с адресом запроса целиком (design §7): она обязана быть
    # и обязана быть замаскирована. Записи logger http.log.error* (если log сайта их пишет) проверены циклом выше.
    assert proxy_errors, "ошибок прокси при остановленном сервисе в журнале нет — проверка 502 была бы пустой"


@pytest.mark.stage("logs")
def test_service_log_fields(logs):
    seen_album = False
    for ln in logs["svc"].splitlines():
        rec = json.loads(ln)
        assert set(rec) <= ALLOWED_SVC_KEYS, f"лишние поля в журнале сервиса: {sorted(set(rec) - ALLOWED_SVC_KEYS)}"
        if "album" in rec:
            assert re.fullmatch(r"[0-9a-f]{8}", rec["album"]), "метка альбома — 8 hex от HMAC, не id"
            seen_album = True
        for v in rec.values():
            assert "/data" not in str(v) and "owners/" not in str(v), "путь на диске в журнале сервиса"
    assert seen_album
    assert '"route":"POST /a/{id}/login"' in logs["svc"], "в журнале сервиса нет входов: проверка была бы пустой"
