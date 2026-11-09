"""Драйвер псевдотерминала для provision_cli (design 002 §12): запускает команду под настоящим pty и
отвечает по сценарию. Использование:

    python3 -I pty_drive.py <сценарий> <values.env> -- <команда…>

Сценарий — строки `expect <regex>` (ждать в выводе) и `send <текст>`; в тексте `$ИМЯ` подставляется из
values.env (фразы и метки — только в файлах fixtures, не в argv). Вывод команды (транскрипт) — в stdout,
код выхода — код команды; 99 — не дождались ожидаемого текста за 120 с.
"""

import os
import pty
import re
import select
import signal
import sys
import time

TIMEOUT = 120


def load_values(path):
    values = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            if "=" in line:
                k, v = line.rstrip("\n").split("=", 1)
                values[k] = v
    return values


def load_steps(path, values):
    steps = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            kind, _, arg = line.partition(" ")
            if kind == "send":
                arg = re.sub(r"\$([A-Z_]+)", lambda m: values[m.group(1)], arg)
            steps.append((kind, arg))
    return steps


def main():
    if "--" not in sys.argv:
        sys.exit("pty_drive: нужен `--` перед командой")
    cut = sys.argv.index("--")
    script, values_path = sys.argv[1:3]
    cmd = sys.argv[cut + 1 :]
    steps = load_steps(script, load_values(values_path))

    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)
    out = []
    pending = ""
    i = 0
    started = time.monotonic()
    code = None
    while True:
        ready, _, _ = select.select([fd], [], [], 1)
        if ready:
            try:
                data = os.read(fd, 4096)
            except OSError:
                break
            if not data:
                break
            out.append(data)
            pending += data.decode("utf-8", "replace")
        while i < len(steps):
            kind, arg = steps[i]
            if kind == "expect":
                m = re.search(arg, pending)
                if not m:
                    break
                pending = pending[m.end() :]
                started = time.monotonic()
            else:
                # Пауза: ответ до того, как программа выключила эхо, попал бы в вывод.
                time.sleep(0.3)
                os.write(fd, (arg + "\n").encode())
            i += 1
        if i < len(steps) and time.monotonic() - started > TIMEOUT:
            os.kill(pid, signal.SIGKILL)
            code = 99
            break
    _, status = os.waitpid(pid, 0)
    sys.stdout.write(b"".join(out).decode("utf-8", "replace"))
    if code is None:
        code = os.waitstatus_to_exitcode(status)
        # Команда завершилась с ошибкой раньше сценария (например, отказ до вопросов) — её код и нужен тесту.
        if i < len(steps) and code == 0:
            sys.stdout.write("\npty_drive: сценарий не дошёл до конца\n")
            code = 99
    sys.exit(code)


main()
