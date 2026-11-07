#!/usr/bin/env python3
"""Чтение OCI-архива образа и дайджест манифеста (design 004 §3, §5). Только stdlib.

    oci_archive.py digest <архив>          печатает sha256:<hex> блоба манифеста; иначе код 1
    oci_archive.py rawsha [--out <файл>]   stdin → sha256 сырого манифеста (один завершающий LF срезается);
                                           --out пишет те же байты в файл; пустой ввод — код 1

Дайджест архива везде (build-image, стенд, publish.sh) — sha256 байтов блоба манифеста, на который указывает
index.json с ровно одним манифестом; значение `digest` в index.json само по себе не доверяется: блоб читается и
хэшируется. Файл архива — недоверенный ввод: только обычные файлы с известными именами, предел размера.
"""

import hashlib
import json
import re
import sys
import tarfile

MAX_ARCHIVE = 256 * 1024 * 1024  # образ около 5 МБ; предел — защита памяти от архива-бомбы
NAME_OK = re.compile(r"^(index\.json|oci-layout|blobs/sha256/[0-9a-f]{64})$")
DIR_OK = ("blobs", "blobs/sha256", ".")
DIGEST = re.compile(r"^sha256:([0-9a-f]{64})$")


class ArchiveError(Exception):
    pass


def read_archive(path):
    """Архив → {имя: байты}; любое нестандартное — ArchiveError."""
    files, total = {}, 0
    try:
        tf = tarfile.open(path, "r:")
    except (tarfile.TarError, OSError) as e:
        raise ArchiveError(f"не tar: {e}") from None
    with tf:
        for m in tf:
            name = m.name[2:] if m.name.startswith("./") else m.name
            if m.isdir():
                if name.rstrip("/") not in DIR_OK:
                    raise ArchiveError(f"лишний каталог в архиве: {name}")
                continue
            if not m.isfile():
                raise ArchiveError(f"в архиве не обычный файл: {name}")
            if not NAME_OK.match(name):
                raise ArchiveError(f"лишнее имя в архиве: {name}")
            if name in files:
                raise ArchiveError(f"повтор имени в архиве: {name}")
            total += m.size
            if total > MAX_ARCHIVE:
                raise ArchiveError("архив больше предела")
            files[name] = tf.extractfile(m).read()
    return files


def blob(files, digest):
    """Байты блоба по дайджесту; хэш содержимого обязан совпасть с именем."""
    m = DIGEST.match(digest if isinstance(digest, str) else "")
    if not m or f"blobs/sha256/{m.group(1)}" not in files:
        raise ArchiveError(f"нет блоба {digest}")
    data = files[f"blobs/sha256/{m.group(1)}"]
    if hashlib.sha256(data).hexdigest() != m.group(1):
        raise ArchiveError(f"блоб {digest} не совпадает с собственным хэшем")
    return data


def manifest_digest(files):
    try:
        ms = json.loads(files["index.json"])["manifests"]
    except (KeyError, ValueError, TypeError):
        raise ArchiveError("index.json не читается") from None
    if not isinstance(ms, list) or len(ms) != 1:
        raise ArchiveError("в index.json манифестов не один: нужен ровно один")
    d = ms[0].get("digest") if isinstance(ms[0], dict) else None
    blob(files, d)
    return d


def main(argv):
    if len(argv) == 2 and argv[0] == "digest":
        try:
            print(manifest_digest(read_archive(argv[1])))
        except (ArchiveError, OSError) as e:
            print(f"oci_archive: {e}", file=sys.stderr)
            return 1
        return 0
    if argv and argv[0] == "rawsha" and (len(argv) == 1 or (len(argv) == 3 and argv[1] == "--out")):
        data = sys.stdin.buffer.read()
        data = data[:-1] if data.endswith(b"\n") else data
        if not data:
            print("oci_archive: пустой манифест", file=sys.stderr)
            return 1
        if len(argv) == 3:
            with open(argv[2], "wb") as f:
                f.write(data)
        print("sha256:" + hashlib.sha256(data).hexdigest())
        return 0
    print("использование: oci_archive.py digest <архив> | rawsha [--out <файл>]", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
