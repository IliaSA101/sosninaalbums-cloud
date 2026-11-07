"""Альбомы для просмотра листалки в браузере: `stand.sh seed` и `stand.sh edit` (запуск — внутри контейнера
стенда, как и матрица). Картинки — однотонные, сгенерированные; настоящих фото здесь нет.

seed: три опубликованных альбома по контракту /v1 от первого владельца —
  1) без пароля: обложка, белые страницы, название с разметкой `<b>x</b><script>`;
  2) с паролем (печатается): то же, другое название;
  3) «для правки»: открытый, 8 страниц; `stand.sh edit` публикует правку (страница 3 заменена белой) — листалка
     обязана перечитать манифест и убрать картинку.
edit: публикует правку альбома 3 из seed (id берётся из out/seed.json).
Ссылки печатаются с адресом для браузера (SA_BROWSER_BASE): id — ключ доступа, но стенд одноразовый.
"""

from __future__ import annotations

import json
import os
import sys

import sa
import sa_api
from sa_api import OWNER1, make_book, upload

PASSWORD = "Пароль-стенда-1234"
MARKUP = "Свадьба <b>x</b><script>alert(1)</script>"
STATE = os.path.join(sa.OUT, "seed.json")


def link(album_id: str) -> str:
    return f"{os.environ['SA_BROWSER_BASE']}/a/{album_id}/"


def seed() -> None:
    plain = upload(make_book(MARKUP, blanks=(2, 6)))
    locked = upload(make_book("С паролем: " + MARKUP, blanks=(2, 5)), password=PASSWORD)
    editable = upload(make_book("Для правки: " + MARKUP, blanks=(2,)))
    with open(STATE, "w", encoding="utf-8") as f:
        json.dump({"editable": editable.id, "manifest": editable.book.manifest}, f)
    print(f"1) без пароля:  {link(plain.id)}")
    print(f"2) с паролем:   {link(locked.id)}   пароль: {PASSWORD}")
    print(f"3) для правки:  {link(editable.id)}   (правка: bash deploy/tests/stand.sh edit, затем обновить страницу)")
    print(f"подпись студии владельца (в конфиге стенда, с разметкой): {sa.STUDIO1}")


def edit() -> None:
    with open(STATE, encoding="utf-8") as f:
        st = json.load(f)
    book = sa_api.Book(st["manifest"], {})
    # Правка: страница 3 (картинка) становится белой — публикация убирает её файл; остальные страницы те же.
    man = json.loads(json.dumps(book.manifest))
    man["pages"][2] = {"n": 3, "role": "page", "blank": True}
    new = sa_api.Book(man, {})
    missing = OWNER1.update(st["editable"], new)
    assert missing == [], f"правка без новых страниц не должна просить файлы: {missing}"
    OWNER1.publish(st["editable"])
    print(f"опубликована правка (страница 3 теперь белая): {link(st['editable'])}")


if __name__ == "__main__":
    {"seed": seed, "edit": edit}[sys.argv[1]]()
