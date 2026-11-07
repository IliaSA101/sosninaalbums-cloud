#!/usr/bin/env python3
"""Поддельный gh для runner_check_cases: `gh api --paginate <путь>` печатает страницы из $SHIM_STATE/pages.json.

Страницы лежат файлом, как их отдаёт настоящий gh --paginate: объекты JSON подряд. SHIM_GH_FAIL=1 - ошибка gh.
Аргументы проверяются: нужен именно запрос заданий прогона с filter=all (иначе сверка не видела бы все попытки).
"""

import os
import re
import sys


def main(argv):
    if os.environ.get("SHIM_GH_FAIL"):
        print("gh: HTTP 500", file=sys.stderr)
        return 1
    if argv[:2] != ["api", "--paginate"] or len(argv) != 3:
        print(f"fake_gh: неожиданные аргументы {argv}", file=sys.stderr)
        return 2
    if not re.match(r"^repos/[^/]+/[^/]+/actions/runs/[0-9]+/jobs\?filter=all&per_page=100$", argv[2]):
        print(f"fake_gh: неожиданный запрос {argv[2]}", file=sys.stderr)
        return 2
    with open(os.path.join(os.environ["SHIM_STATE"], "pages.json"), encoding="utf-8") as f:
        sys.stdout.write(f.read())
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
