"""Состояния альбомов для матрицы (design §10): строятся один раз за прогон через /v1 по https."""

from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import timedelta

import sa
import sa_api
from sa_api import OWNER1, OWNER2, Album, make_book, replace_page, sign_in, upload

PW = "Пароль-стенда-4721"
PW2 = "Другой-пароль-8352"


@dataclass
class World:
    open: Album  # опубликован, без пароля
    locked: Album  # опубликован, пароль PW
    locked2: Album  # другой альбом с паролем: чужая cookie для locked
    locked_rev: Album  # у него потом revoke_sessions: прежняя cookie негодна
    locked_pw: Album  # у него потом смена пароля: прежняя cookie негодна
    unpublished: Album  # страницы загружены, publish не было
    draft: Album  # опубликован + загружена неопубликованная правка
    prev: Album  # публикация правки убрала файл прежней редакции
    expired: Album  # срок вышел, уборки (5 мин) ещё не было
    deleted: Album  # DELETE
    foreign: Album  # опубликован у второго владельца
    cookie_locked: str  # годная cookie locked
    cookie_locked2: str
    cookie_rev_old: str  # cookie locked_rev до revoke_sessions
    cookie_pw_old: str  # cookie locked_pw до смены пароля
    expired_cookie: str  # годная cookie альбома expired, выданная до истечения срока
    locked_exp: Album  # живой альбом (срок продлён PATCH), у которого прежняя cookie уже истекла
    cookie_exp_old: str  # его cookie, выданная при сроке 3 с (exp = старый срок)


def build() -> World:
    # Срок 3 с: API не принимает срок в прошлом (400), поэтому истекший альбом получается ожиданием.
    expired = upload(make_book("Истёкший"), password=PW, expires_in=timedelta(seconds=3))
    t_exp = time.monotonic() + 4.0  # срок 3 с и секундная точность cookie и срока — с запасом
    expired_cookie = sign_in(expired)
    # Настоящая истёкшая cookie: exp = min(30 дн., срок альбома) на момент выдачи; срок альбома потом продлевается.
    locked_exp = upload(make_book("Истёкшая cookie"), password=PW, expires_in=timedelta(seconds=3))
    cookie_exp_old = sign_in(locked_exp)
    OWNER1.patch(locked_exp.id, expires_at=sa_api.iso(timedelta(days=30)))

    opened = upload(make_book("Открытый"))
    locked = upload(make_book("Закрытый"), password=PW)
    locked2 = upload(make_book("Закрытый два"), password=PW2)
    locked_rev = upload(make_book("Сброс входов"), password=PW)
    locked_pw = upload(make_book("Смена пароля"), password=PW)
    cookie_locked, cookie_locked2 = sign_in(locked), sign_in(locked2)
    cookie_rev_old, cookie_pw_old = sign_in(locked_rev), sign_in(locked_pw)
    OWNER1.patch(locked_rev.id, revoke_sessions=True)
    OWNER1.patch(locked_pw.id, password=PW2)
    locked_pw.password = PW2

    unpublished = upload(make_book("Не опубликован"), publish=False)

    # Правка опубликованного: страница 3 заменена; у draft свой файл, опубликованная редакция прежняя.
    draft = upload(make_book("С черновиком"))
    b2 = replace_page(draft.book, 3)
    OWNER1.put_missing(draft.id, b2, OWNER1.update(draft.id, b2))
    draft.draft_file = b2.file(3)

    # Правка опубликована: файл страницы 3 прежней редакции уходит, новой — отдаётся.
    prev = upload(make_book("Прежняя редакция"))
    old3 = prev.book.file(3)
    b3 = replace_page(prev.book, 3)
    OWNER1.put_missing(prev.id, b3, OWNER1.update(prev.id, b3))
    OWNER1.publish(prev.id)
    prev.book, prev.prev_file = b3, old3

    deleted = upload(make_book("Удалённый"))
    OWNER1.delete(deleted.id)
    foreign = upload(make_book("Чужой"), owner=OWNER2)

    time.sleep(max(0.0, t_exp - time.monotonic()))
    sa.remember("expired_id", expired.id)
    return World(opened, locked, locked2, locked_rev, locked_pw, unpublished, draft, prev, expired, deleted, foreign,
                 cookie_locked, cookie_locked2, cookie_rev_old, cookie_pw_old, expired_cookie, locked_exp, cookie_exp_old)
