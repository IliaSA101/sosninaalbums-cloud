"""Адрес /32 на lo тестового контейнера provision_cli (спека 005, design §4.2, 005-33).

    python3 -I lo_addr.py <IPv4>

Поддельный tofu отдаёт адрес сервера, а hosts_check_ip в боевом коде отказывает для loopback и служебных диапазонов;
обхода по переменной окружения в provision.sh нет, поэтому тестовый адрес — настоящий, вне служебных диапазонов,
и живёт на lo контейнера без сети (--network=none): наружу он не выходит. Нужна возможность NET_ADMIN (её даёт только
запуск основного прогона, cli_run … --cap-add=NET_ADMIN; inner_start.sh сбрасывает её сразу после этого скрипта).
Алиас lo:1, а не замена адреса lo: 127.0.0.1 остаётся.
"""

import fcntl
import socket
import struct
import sys

SIOCSIFADDR = 0x8916
SIOCSIFNETMASK = 0x891C


def ifreq(name, addr):
    return struct.pack("16sH2s4s8s", name, socket.AF_INET, b"\0\0", socket.inet_aton(addr), b"\0" * 8)


def main():
    if len(sys.argv) != 2:
        sys.exit("lo_addr: нужен один аргумент — адрес")
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    fcntl.ioctl(s, SIOCSIFADDR, ifreq(b"lo:1", sys.argv[1]))
    fcntl.ioctl(s, SIOCSIFNETMASK, ifreq(b"lo:1", "255.255.255.255"))


main()
