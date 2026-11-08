"""Тестовые архивы для verify_restore_rejects (design 002 §11, §12): один хороший и плохие, каждый — с одним
пороком. Запуск: python3 -I - <каталог> <предел_байт>, скрипт — через stdin. Файлы пишутся в <каталог>:
<имя>.tar.zst (без age: расшифровку делает контроллер до sa-restore). Содержимое выдуманное, однотонное;
реальных данных нет.
"""
import io
import subprocess
import sys
import tarfile


def tar_bytes(members, fmt=tarfile.GNU_FORMAT):
    """members: список (имя, тип, содержимое_или_цель[, pax-заголовки]). Тип: file, dir, symlink, hardlink."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=fmt) as tf:
        for member in members:
            name, kind, payload = member[:3]
            info = tarfile.TarInfo(name)
            info.mtime = 1700000000
            if len(member) > 3:
                info.pax_headers = member[3]
            info.uid = info.gid = 0
            if kind == "dir":
                info.type = tarfile.DIRTYPE
                info.mode = 0o700
                tf.addfile(info)
            elif kind == "file":
                info.mode = 0o600
                info.size = len(payload)
                tf.addfile(info, io.BytesIO(payload))
            elif kind == "fifo":
                info.type = tarfile.FIFOTYPE
                info.mode = 0o600
                tf.addfile(info)
            elif kind == "symlink":
                info.type = tarfile.SYMTYPE
                info.linkname = payload
                tf.addfile(info)
            elif kind == "hardlink":
                info.type = tarfile.LNKTYPE
                info.linkname = payload
                tf.addfile(info)
    return buf.getvalue()


def marker():
    """Второй архив с одним файлом SA-END, как делает sa-backup-stream."""
    return tar_bytes([("SA-END", "file", b"date=2026-01-01T00:00:00Z\nfiles=3\ntar=0\n")])


def good_body():
    return [
        ("data", "dir", None),
        ("data/LAYOUT", "file", b'{"layout": 1}\n'),
        ("data/owners", "dir", None),
        ("data/owners/p1", "dir", None),
        ("conf", "dir", None),
        ("conf/config.json", "file", b"{}\n"),
    ]


def write(directory, name, raw):
    out = subprocess.run(["zstd", "-q", "-c"], input=raw, stdout=subprocess.PIPE, check=True).stdout
    with open("%s/%s.tar.zst" % (directory, name), "wb") as f:
        f.write(out)


def main():
    directory, limit = sys.argv[1], int(sys.argv[2])
    base = good_body()
    write(directory, "good", tar_bytes(base) + marker())
    write(directory, "symlink", tar_bytes(base + [("data/owners/p1/link", "symlink", "/etc/shadow")]) + marker())
    write(directory, "hardlink", tar_bytes(base + [("data/owners/p1/hard", "hardlink", "conf/config.json")]) + marker())
    write(directory, "dotdot", tar_bytes(base + [("data/../../evil", "file", b"x")]) + marker())
    write(directory, "absolute", tar_bytes(base + [("/etc/evil", "file", b"x")]) + marker())
    write(directory, "foreign", tar_bytes(base + [("etc/evil", "file", b"x")]) + marker())
    write(directory, "nomarker", tar_bytes(base))
    # Длиннее предела: однотонное содержимое сжимается в крохи, поэтому предел проверяется по распакованному.
    # Огромный mtime в pax-заголовке: tar печатает дату одним числом, первое слово имени уходит в «дату».
    hidden = ("zz data/x", "file", b"evil", {"mtime": "100000000000000000"})
    write(directory, "mtime", tar_bytes(base + [hidden], tarfile.PAX_FORMAT) + marker())
    # Знак вне белого списка в имени (без пробела): единственное, что его останавливает, — правило charset.
    write(directory, "charset", tar_bytes(base + [("data/a;b", "file", b"x")]) + marker())
    # Тысячи пустых каталогов: размеры 0, а inode и блоки заняты — сумму считает 4096 на член.
    dirs = [("data/d%05d" % i, "dir", None) for i in range(1500)]
    write(directory, "dirs", tar_bytes(base + dirs) + marker())
    # Немного пустых каталогов: останавливает только предел числа членов (его в тесте задаёт окружение).
    many = [("data/m%02d" % i, "dir", None) for i in range(50)]
    write(directory, "many", tar_bytes(base + many) + marker())
    # Устройство/канал: строка листинга из шести полей, держит только правило type.
    write(directory, "fifo", tar_bytes(base + [("data/owners/p1/pipe", "fifo", None)]) + marker())
    # Глубина: один член, а каталогов 30 (имя короткое, сумма неявных каталогов под предел не выходит).
    deep = "data/" + "/".join("a" * 1 for _ in range(30)) + "/f"
    write(directory, "deep", tar_bytes(base + [(deep, "file", b"x")]) + marker())
    # Длина имени: два компонента по 200 знаков (каждый короче предела файловой системы, 255), весь путь — больше 255.
    # Один компонент длиннее 255 отверг бы сам tar (ENAMETOOLONG), а не правило разбора: мутация namelen осталась бы зелёной.
    write(directory, "namelen", tar_bytes(base + [("data/" + "n" * 200 + "/" + "m" * 200, "file", b"x")]) + marker())
    # Неявные каталоги: сотня файлов в разных ветках глубиной семь, размеры членов под предел укладываются, а
    # промежуточные каталоги (по пять на ветку) — нет.
    fan = [("data/p%03d/a/a/a/a/f" % i, "file", b"x") for i in range(100)]
    write(directory, "fanout", tar_bytes(base + fan) + marker())
    big = base + [("data/owners/p1/big", "file", b"\0" * (limit + 1024 * 1024))]
    write(directory, "toolong", tar_bytes(big) + marker())


main()
