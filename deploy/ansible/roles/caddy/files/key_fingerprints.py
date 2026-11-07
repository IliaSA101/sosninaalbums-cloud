"""Отпечатки основных ключей файла OpenPGP (ASCII armor) — для проверки ключа apt-репозитория Caddy.

Запуск: python3 -I - <файл>, скрипт — через stdin (design 002 §4: чужой код под root не берётся из каталога).
Печатает JSON {"primary": [отпечатки], "subkeys": число}. Разбор строгий: неизвестный пакет, ключ не версии 4,
частичная длина, обрыв пакета — ошибка (код 1). Основных ключей может оказаться больше одного: apt доверил бы
каждому, поэтому вызывающий сверяет весь список, а не первый отпечаток.
"""
import base64
import hashlib
import json
import re
import sys

# Допустимые теги: подпись, основной ключ, имя, подключ, атрибут пользователя.
ALLOWED = {2, 6, 13, 14, 17}


def crc24(data):
    """Контрольная сумма OpenPGP (RFC 4880 §6.1)."""
    crc = 0xB704CE
    for byte in data:
        crc ^= byte << 16
        for _ in range(8):
            crc <<= 1
            if crc & 0x1000000:
                crc ^= 0x1864CFB
    return crc & 0xFFFFFF


def dearmor(text):
    """Ровно один armor-блок: второй BEGIN или любой текст после END — ошибка (apt доверил бы и ему)."""
    if text.count("-----BEGIN PGP") != 1 or text.count("-----END PGP") != 1:
        raise ValueError("в файле не ровно один armor-блок")
    # split по «\n», а не splitlines: тот режет и по другим знакам конца строки, которыми прячут данные.
    lines = [line.rstrip("\r") for line in text.split("\n")]
    if not lines or lines[0] != "-----BEGIN PGP PUBLIC KEY BLOCK-----":
        raise ValueError("нет заголовка PGP PUBLIC KEY BLOCK")
    body, checksum = [], None
    i = 1
    # Заголовки armor — только «Имя: значение»; строка иного вида (например, данные ключа до пустой строки, которые
    # разбор иначе пропустил бы как заголовок, а apt прочёл бы) — ошибка. Пустая строка после заголовков обязательна.
    while i < len(lines) and lines[i] != "":
        if not re.match(r"^[A-Za-z][A-Za-z0-9-]*: ", lines[i]):
            raise ValueError("строка заголовка armor недопустимого вида")
        i += 1
    if i >= len(lines):
        raise ValueError("нет пустой строки после заголовков armor")
    ended = False
    for line in lines[i + 1:]:
        if ended:
            if line:
                raise ValueError("текст после конца armor-блока")
            continue
        if line == "-----END PGP PUBLIC KEY BLOCK-----":
            ended = True
        elif line.startswith("="):  # контрольная сумма CRC24
            if checksum is not None or not re.match(r"^=[A-Za-z0-9+/]{4}$", line):
                raise ValueError("контрольная сумма armor не одна или не того вида")
            checksum = line[1:]
        elif checksum is not None:
            raise ValueError("данные после контрольной суммы")
        elif re.match(r"^[A-Za-z0-9+/=]+$", line):
            body.append(line)
        else:
            raise ValueError("строка тела armor недопустимого вида")
    if not ended or checksum is None:
        raise ValueError("нет окончания блока или контрольной суммы")
    data = base64.b64decode("".join(body), validate=True)
    if base64.b64decode(checksum, validate=True) != crc24(data).to_bytes(3, "big"):
        raise ValueError("контрольная сумма CRC24 не сходится")
    return data


def packets(data):
    pos = 0
    while pos < len(data):
        first = data[pos]
        pos += 1
        if not first & 0x80:
            raise ValueError("не пакет OpenPGP")
        if first & 0x40:  # новый формат
            tag = first & 0x3F
            b0 = data[pos]
            pos += 1
            if b0 < 192:
                size = b0
            elif b0 < 224:
                size = ((b0 - 192) << 8) + data[pos] + 192
                pos += 1
            elif b0 == 255:
                size = int.from_bytes(data[pos:pos + 4], "big")
                pos += 4
            else:
                raise ValueError("частичная длина пакета")
        else:  # старый формат
            tag = (first >> 2) & 0x0F
            kind = first & 3
            if kind == 3:
                raise ValueError("неопределённая длина пакета")
            width = (1, 2, 4)[kind]
            size = int.from_bytes(data[pos:pos + width], "big")
            pos += width
        if pos + size > len(data):
            raise ValueError("пакет длиннее файла")
        yield tag, data[pos:pos + size]
        pos += size


def main():
    with open(sys.argv[1], encoding="ascii") as f:
        data = dearmor(f.read())
    primary, subkeys = [], 0
    for tag, body in packets(data):
        if tag not in ALLOWED:
            raise ValueError("недопустимый пакет, тег %d" % tag)
        if tag in (6, 14):
            if body[0] != 4:
                raise ValueError("ключ не версии 4")
            digest = hashlib.sha1(b"\x99" + len(body).to_bytes(2, "big") + body).hexdigest().upper()
            if tag == 6:
                primary.append(digest)
            else:
                subkeys += 1
    print(json.dumps({"primary": primary, "subkeys": subkeys}))


try:
    main()
except (ValueError, IndexError, OSError) as exc:
    print("key_fingerprints: %s" % exc, file=sys.stderr)
    sys.exit(1)
