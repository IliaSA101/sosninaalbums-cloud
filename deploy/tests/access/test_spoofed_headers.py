"""Присланные клиентом X-Sa-Client-IP, X-Forwarded-For и родственные заголовки не действуют (design §1, §5.2).

Что видно поведением: адрес клиента для сервиса — только то, что ставит Caddy (`header_up X-Sa-Client-IP
{remote_host}` затирает присланное, в том числе дубли и мусор). Наблюдать подмену можно через лимит попыток: если бы
сервис верил присланному, перебор менял бы ключ «альбом + IP» на каждой попытке (обход лимита), а чужой адрес в
заголовке позволял бы закрыть вход чужому клиенту.

Чего поведением не видно: срез остальных X-Sa-* и X-Forwarded-* до проксирования — сервис их не читает. Он проверяется
по конфигурации: deploy/tests/caddy-validate.sh (`caddy adapt` + caddy-adapt-check.py) — каждый reverse_proxy идёт
после headers.request.delete для этих полей.
"""

from __future__ import annotations

import random
import re

import checks
import sa
import sa_api
from sa_api import form, login, make_book, upload
from world import PW


def _fake_ip() -> str:
    return f"203.0.113.{random.randint(1, 254)}"


def _spoof(ip: str) -> dict:
    return {"X-Sa-Client-IP": ip, "X-Forwarded-For": ip, "X-Forwarded-Host": "evil.example", "X-Real-IP": ip,
            "Forwarded": f"for={ip}", "X-Sa-Other": "1", "X-Sa-Client-Ip": ip}


def test_spoofed_ip_does_not_give_a_fresh_key():
    a = upload(make_book("Подмена: обход лимита"), password=PW)
    ip = sa.new_ip()
    codes = [login(a, f"неверный-{i}-yy", ip, _spoof(_fake_ip())).status for i in range(7)]
    assert codes == [401] * 5 + [429, 429], codes


def test_spoofed_ip_cannot_lock_out_another_client():
    a = upload(make_book("Подмена: чужой адрес"), password=PW)
    attacker, victim = sa.new_ip(), sa.new_ip()
    codes = [login(a, f"неверный-{i}-zz", attacker, _spoof(victim)).status for i in range(7)]
    assert codes[-1] == 429, codes
    ok = login(a, PW, victim)
    assert ok.status == 303, f"клиент, чей адрес подделан, не должен страдать: {ok.status}"


def test_duplicate_and_odd_client_ip_headers_do_not_reach_the_service():
    """Несколько X-Sa-Client-IP, мусор в значении: если бы сервис получил чужой заголовок, он ответил бы 503
    («адрес не определён»); header_up Caddy заменяет их одним настоящим."""
    a = upload(make_book("Подмена: дубли"), password=PW)
    for extra in ([("X-Sa-Client-IP", "198.51.100.1"), ("X-Sa-Client-IP", "198.51.100.2")],
                  [("X-Sa-Client-IP", "not-an-ip")], [("x-sa-client-ip", "")], [("X-Forwarded-For", "192.0.2.1, 192.0.2.2")]):
        r = sa.raw(a.path("login"), method="POST", headers=[*sa.LOGIN_FORM.items(), *extra], body=form("неверный-пароль-ab"))
        assert r.status == 401, f"{extra}: {r.status}"
        checks.check_common(r, "вход с подменой")


def test_spoofed_headers_on_v1_and_viewer_do_not_break_or_open_anything(w):
    r = sa_api.OWNER1.call("GET", "/v1/health", headers=_spoof("198.51.100.9"))
    assert r.status == 200
    plain = sa.get("/v1/albums", headers=_spoof("198.51.100.9"))  # без токена — по-прежнему отказ, подмена не пускает
    assert plain.status == 401
    ver = re.search(r"/viewer/([0-9a-f]{8})/", sa.get(w.open.path()).text()).group(1)
    js = sa.get(f"/viewer/{ver}/app.js", headers=_spoof("198.51.100.9"))
    assert js.status == 200 and js.one("content-type") == "text/javascript; charset=utf-8"
    checks.check_common(js, "листалка с подменой")
