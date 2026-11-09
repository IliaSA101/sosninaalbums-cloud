#!/usr/bin/env python3
"""verify_nft_reload_only (design 002 §6): в ролях нет restarted/stopped для nftables.

Юнит nftables в Debian при stop и restart выполняет `nft flush ruleset`: хост остался бы без фильтра.
Смотрятся YAML задач и обработчиков каталога roles: модули systemd/service с состоянием restarted или stopped,
у которых имя службы (в том числе в loop) содержит nftables, и команды systemctl/service restart|stop nftables,
`nft flush ruleset|table` и `nft delete table`. Код выхода 1 — нашлось; 0 — чисто.

Использование: nft_reload_only.py <каталог deploy/ansible>
"""
import re
import sys
from pathlib import Path

import yaml

SERVICE_MODULES = {"systemd", "systemd_service", "service", "sysvinit"}
BAD_STATES = {"restarted", "stopped"}
# Остановка юнита и всё, что сносит правила мимо атомарной загрузки файла: flush ruleset/table, delete table.
BAD_CMD = re.compile(
    r"(?:systemctl|service)\s+(?:restart|stop|force-reload)\s+nftables"
    r"|nft\s+(?:flush\s+(?:ruleset|table)|delete\s+table)"
)


def walk(tasks):
    """Все задачи, включая вложенные block/rescue/always."""
    for t in tasks or []:
        if not isinstance(t, dict):
            continue
        yield t
        for key in ("block", "rescue", "always"):
            yield from walk(t.get(key))


def module_of(task):
    for key, value in task.items():
        short = key.rsplit(".", 1)[-1]
        if short in SERVICE_MODULES or short in {"command", "shell", "raw"}:
            return short, value
    return None, None


def command_line(args):
    """Командная строка из строки или из args: cmd / argv (argv склеивается через пробел)."""
    if isinstance(args, str):
        return args
    if isinstance(args, dict):
        if isinstance(args.get("argv"), list):
            return " ".join(str(x) for x in args["argv"])
        return str(args.get("cmd", ""))
    return str(args)


def violations(path):
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as e:
        return [f"{path}: не разобран YAML ({e})"]
    found = []
    if not isinstance(data, list):
        return found
    for t in walk(data):
        mod, args = module_of(t)
        if mod is None:
            continue
        if mod in SERVICE_MODULES and isinstance(args, dict):
            blob = " ".join(str(x) for x in (args.get("name"), t.get("loop"), t.get("with_items")))
            if "nftables" in blob and str(args.get("state")) in BAD_STATES:
                found.append(f"{path}: {t.get('name', '?')} — state={args.get('state')} для nftables")
        else:
            cmd = command_line(args)
            if BAD_CMD.search(cmd):
                found.append(f"{path}: {t.get('name', '?')} — команда сбрасывает правила nftables")
    return found


def main():
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    files = sorted(p for d in ("tasks", "handlers") for p in base.rglob(f"{d}/*.yml"))
    if not files:
        print(f"в {base} нет файлов tasks/ и handlers/ — проверять нечего", file=sys.stderr)
        return 2
    bad = [v for f in files for v in violations(f)]
    for v in bad:
        print(v)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
