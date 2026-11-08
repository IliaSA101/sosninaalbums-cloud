"""Перебор пароля: 429 по трём ключам (design §5.5) — альбом+IP (5 неудач), IP (20), альбом (20) — и новая корзина
после смены пароля и после сброса входов (решение пачки Г3: ключ альбома — id + соль пароля + epoch).

Источников несколько: у каждого теста свои адреса 127.x.y.z (sa.new_ip), поэтому ключи «IP» и «альбом» не
пересекаются между тестами и повторными прогонами.
"""

from __future__ import annotations

import sa
from sa_api import OWNER1, login, make_book, upload
from world import PW


def _wrong(a, ip: str, n: int, tag: str = "x") -> list[int]:
    return [login(a, f"неверный-{tag}-{i}-пароль", ip).status for i in range(n)]


def _album(title: str):
    return upload(make_book(title), password=PW)


def test_key_album_plus_ip():
    a, b = _album("Ключ альбом+IP"), _album("Ключ альбом+IP, соседний")
    ip, other = sa.new_ip(), sa.new_ip()
    assert _wrong(a, ip, 6) == [401] * 5 + [429]
    assert login(a, PW, ip).status == 429, "верный пароль с исчерпанным ключом — всё равно 429 (попытка считается до сверки)"
    assert login(a, PW, other).status == 303, "другой адрес к тому же альбому не заблокирован"
    assert login(b, "неверный-сосед", ip).status == 401, "тот же адрес к другому альбому — другой ключ"


def test_key_ip_across_albums():
    albums = [_album(f"Ключ IP {i}") for i in range(6)]
    ip, other = sa.new_ip(), sa.new_ip()
    for a in albums[:5]:  # 5 x 4 = 20 неудач с одного адреса; на каждом альбоме меньше предела альбом+IP (5)
        assert _wrong(a, ip, 4) == [401] * 4
    r = login(albums[5], "неверный-новый-альбом", ip)
    assert r.status == 429 and r.one("retry-after"), "21-я неудача с адреса: 429 на альбоме, где этот адрес ещё не пробовал"
    assert login(albums[0], PW, ip).status == 429, "и на альбоме, где ключ альбом+IP ещё не исчерпан"
    assert login(albums[5], "неверный-другой-адрес", other).status == 401, "другой адрес не затронут"


def _exhaust_album_key(a, tag: str) -> None:
    for k in range(4):  # 4 адреса x 5 попыток = 20 неудач на альбоме; на каждом адресе — предел альбом+IP, не больше
        assert _wrong(a, sa.new_ip(), 5, f"{tag}{k}") == [401] * 5


def test_key_album_over_many_ips():
    a, b = _album("Ключ альбом"), _album("Ключ альбом, соседний")
    _exhaust_album_key(a, "a")
    fresh = sa.new_ip()
    r = login(a, "неверный-свежий-адрес", fresh)
    assert r.status == 429 and r.one("retry-after"), "21-я неудача на альбоме с нового адреса: 429 по ключу «альбом»"
    assert login(a, PW, fresh).status == 429
    assert login(b, "неверный-соседний", fresh).status == 401, "тот же адрес на другом альбоме — свободен"


def test_new_bucket_after_password_change():
    a = _album("Новая корзина: смена пароля")
    _exhaust_album_key(a, "p")
    assert login(a, "неверный-перед-сменой", sa.new_ip()).status == 429
    new_pw = "Новый-пароль-6093"
    sa.note_secret(new_pw)
    OWNER1.patch(a.id, password=new_pw)
    a.password = new_pw
    ip = sa.new_ip()
    assert login(a, "неверный-после-смены", ip).status == 401, "смена пароля — новая корзина ключа «альбом»"
    assert login(a, new_pw, ip).status == 303


def test_new_bucket_after_revoke_sessions():
    a = _album("Новая корзина: сброс входов")
    _exhaust_album_key(a, "r")
    assert login(a, "неверный-перед-сбросом", sa.new_ip()).status == 429
    OWNER1.patch(a.id, revoke_sessions=True)
    ip = sa.new_ip()
    assert login(a, "неверный-после-сброса", ip).status == 401, "сброс входов (epoch) — новая корзина"
    assert login(a, PW, ip).status == 303


def test_success_resets_album_ip_key():
    """Верный вход возвращает попытку и сбрасывает ключ «альбом+IP»: 4 неудачи, вход, снова 5 неудач подряд."""
    a = _album("Успех сбрасывает ключ")
    ip = sa.new_ip()
    assert _wrong(a, ip, 4) == [401] * 4
    assert login(a, PW, ip).status == 303
    assert _wrong(a, ip, 5, "после") == [401] * 5
    assert login(a, "неверный-шестой", ip).status == 429
