#!/usr/bin/env python3
"""Проверка по выводу `caddy adapt` (JSON на stdin): каждый reverse_proxy идёт в цепочке обработчиков ПОСЛЕ
headers.request.delete для «X-Sa-*» и «X-Forwarded-*» (design §1: сервис доверяет только X-Sa-Client-IP,
который ставит сам Caddy). Одной `header_up X-Sa-Client-IP` для этого мало: она затирает только это поле, а
прочие X-Sa-* и X-Forwarded-* дошли бы до сервиса. Поведением срез не наблюдается (сервис эти заголовки не читает),
поэтому проверяется конфигурация. Запуск: caddy adapt … | python3 caddy-adapt-check.py
"""

import json
import sys

NEEDED = {"X-Sa-*", "X-Forwarded-*"}
MIN_PROXIES = 4  # /v1, форма входа, /a/*, /viewer/*


def chains(node):
    """Все списки «handle» (цепочки обработчиков) в конфигурации, вглубь."""
    if isinstance(node, dict):
        if isinstance(node.get("handle"), list):
            yield node["handle"]
        for v in node.values():
            yield from chains(v)
    elif isinstance(node, list):
        for v in node:
            yield from chains(v)


def main() -> int:
    cfg = json.load(sys.stdin)
    proxies, bad = 0, 0
    for chain in chains(cfg):
        seen: set[str] = set()
        for h in chain:
            seen |= set((h.get("request") or {}).get("delete") or []) if h.get("handler") == "headers" else set()
            if h.get("handler") == "reverse_proxy":
                proxies += 1
                if not NEEDED <= seen:
                    bad += 1
                    print(f"FAIL: reverse_proxy без среза до себя: нет {sorted(NEEDED - seen)}", file=sys.stderr)
    if proxies < MIN_PROXIES:
        print(f"FAIL: reverse_proxy найдено {proxies}, ожидалось не меньше {MIN_PROXIES}", file=sys.stderr)
        return 1
    if bad:
        return 1
    print(f"OK: все {proxies} reverse_proxy идут после среза X-Sa-* и X-Forwarded-*")
    return 0


if __name__ == "__main__":
    sys.exit(main())
