"""Альбомы через /v1 для матрицы доступа: однотонные JPEG (генерируются, не фото), книга, выгрузка, правка.

JPEG — четыре однотонных шаблона 120x160, выданных `image/jpeg` Go (качество 75); отличаются цветом.
Каждая картинка получает свою соль в сегменте COM сразу за SOI (как у Conformance контракта): байты и хэш
у каждого вызова свои, а декодер и проверка сервиса (COM разрешён) картинке не мешают.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode

import httpx

import sa

TEMPLATES_B64 = [
    "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAKAAeAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AMWiiivKPvgooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooA/9k=",
    "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAKAAeAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AIKKKK8s+ZCiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigD/9k=",
    "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAKAAeAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AOdooor7U8oKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKAP//Z",
    "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAKAAeAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AOxooor8/PcCiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigD//Z",
]
W, H = 120, 160


def jpeg(color: int, salt: bytes | None = None) -> bytes:
    raw = base64.b64decode(TEMPLATES_B64[color % len(TEMPLATES_B64)])
    salt = salt or os.urandom(8)
    return raw[:2] + b"\xff\xfe" + (2 + len(salt)).to_bytes(2, "big") + salt + raw[2:]


@dataclass
class Book:
    """Манифест (как отправляет программа: file пуст) и картинки по номерам страниц."""

    manifest: dict
    images: dict[int, bytes]

    def file(self, n: int) -> str:
        return hashlib.sha256(self.images[n]).hexdigest() + ".jpg"

    @property
    def files(self) -> list[str]:
        return [self.file(n) for n in sorted(self.images)]


def make_book(title: str = "Альбом стенда", blanks: tuple[int, ...] = (2, 6), pages: int = 8, colors: tuple[int, ...] = (0, 1, 2, 3)) -> Book:
    """Книга с обложкой: страница 1 — front, последняя — back, белые — из blanks, остальные — картинки."""
    pg, imgs = [], {}
    for n in range(1, pages + 1):
        role = "front" if n == 1 else "back" if n == pages else "page"
        if n in blanks:
            pg.append({"n": n, "role": role, "blank": True})
            continue
        data = jpeg(colors[n % len(colors)])
        imgs[n] = data
        pg.append({"n": n, "role": role, "type": "image/jpeg", "sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data), "w": W, "h": H})
    man = {"schema": 1, "generator": "access-matrix", "title": title, "book": {"cover": True, "page_mm": {"w": 210, "h": 297}}, "pages": pg}
    return Book(man, imgs)


def replace_page(book: Book, n: int, color: int = 3) -> Book:
    """Копия книги, где страница n — другая картинка (новая редакция: прежний файл уйдёт после publish)."""
    man = json.loads(json.dumps(book.manifest))
    data = jpeg(color)
    man["pages"][n - 1].update({"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)})
    return Book(man, {**book.images, n: data})


def iso(delta: timedelta) -> str:
    return (datetime.now(timezone.utc) + delta).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")


class Api:
    """Клиент /v1 с токеном владельца, по https через Caddy. Каждый вызов — отдельное соединение с новым адресом."""

    def __init__(self, token: str):
        self.token = token

    def call(self, method: str, path: str, *, json_body=None, content: bytes | None = None, headers: dict | None = None, ip: str | None = None) -> sa.Snap:
        h = {"Authorization": f"Bearer {self.token}", **(headers or {})}
        with sa.client(ip) as c:
            return sa.snap_of(c.request(method, path, json=json_body, content=content, headers=h))

    def create(self, book: Book, *, password: str | None = None, expires_in: timedelta = timedelta(days=30)) -> dict:
        body = {"manifest": book.manifest, "expires_at": iso(expires_in)}
        if password:
            body["password"] = password
        r = self.call("POST", "/v1/albums", json_body=body)
        assert r.status in (200, 201), f"создание альбома: {r.status}"
        out = json.loads(r.body)
        sa.note_secret(out["id"])
        return out

    def update(self, album_id: str, book: Book) -> list[int]:
        r = self.call("PUT", f"/v1/albums/{album_id}", json_body={"manifest": book.manifest})
        assert r.status == 200, f"правка альбома: {r.status}"
        return json.loads(r.body)["missing"]

    def put_page(self, album_id: str, book: Book, n: int) -> sa.Snap:
        return self.call("PUT", f"/v1/albums/{album_id}/pages/{n}", content=book.images[n], headers={"Content-Type": "image/jpeg"})

    def put_missing(self, album_id: str, book: Book, missing: list[int]) -> None:
        for n in missing:
            r = self.put_page(album_id, book, n)
            assert r.status == 204, f"страница {n}: {r.status}"

    def publish(self, album_id: str) -> sa.Snap:
        r = self.call("POST", f"/v1/albums/{album_id}/publish")
        assert r.status == 200, f"publish: {r.status}"
        return r

    def patch(self, album_id: str, **fields) -> sa.Snap:
        r = self.call("PATCH", f"/v1/albums/{album_id}", json_body=fields)
        assert r.status == 200, f"PATCH: {r.status}"
        return r

    def delete(self, album_id: str) -> None:
        r = self.call("DELETE", f"/v1/albums/{album_id}")
        assert r.status in (200, 204), f"DELETE: {r.status}"


OWNER1 = Api(sa.TOKEN1)
OWNER2 = Api(sa.TOKEN2)


@dataclass
class Album:
    id: str
    book: Book
    password: str | None = None
    owner: Api = field(default=OWNER1)
    draft_file: str = ""  # файл страницы неопубликованной правки (есть у альбомов со своим draft)
    prev_file: str = ""  # файл страницы прежней редакции, убранный publish

    def path(self, tail: str = "") -> str:
        return f"/a/{self.id}/{tail}"

    @property
    def file(self) -> str:
        """Файл первой картинки (страница 1 — обложка)."""
        return self.book.file(1)

    def unknown_file(self) -> str:
        """Имя по грамматике, которого в альбоме нет (хэш не от этих байт)."""
        return os.urandom(32).hex() + ".jpg"


def upload(book: Book | None = None, *, title: str = "Альбом стенда", password: str | None = None, owner: Api = OWNER1, expires_in: timedelta = timedelta(days=30), publish: bool = True) -> Album:
    """Полная выгрузка по контракту: POST → PUT недостающих страниц → publish (publish=False — оставить черновиком)."""
    book = book or make_book(title)
    if password:
        sa.note_secret(password)
    cr = owner.create(book, password=password, expires_in=expires_in)
    owner.put_missing(cr["id"], book, cr["missing"])
    if publish:
        owner.publish(cr["id"])
    return Album(cr["id"], book, password, owner)


def form(password: str) -> bytes:
    return urlencode({"password": password}).encode()


def login(album: Album, password: str, ip: str | None = None, headers: dict | None = None) -> sa.Snap:
    """POST /a/<id>/login формой с полем password."""
    sa.note_secret(password)
    with sa.client(ip) as c:
        return sa.snap_of(c.post(album.path("login"), content=form(password), headers={**sa.LOGIN_FORM, **(headers or {})}))


def sign_in(album: Album, ip: str | None = None) -> str:
    """Верный вход: возвращает пару «имя=значение» для заголовка Cookie."""
    assert album.password
    r = login(album, album.password, ip)
    assert r.status == 303, f"вход: {r.status}"
    c = sa.session_cookie(r)
    assert c, "вход без Set-Cookie"
    sa.note_secret(c.value)
    return c.pair
