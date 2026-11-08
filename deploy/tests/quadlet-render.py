"""Рендер шаблона Quadlet вне Ansible: python3 -I quadlet-render.py <шаблон> <образ>.

Тест `regex` в Ansible есть, в чистом Jinja2 — нет, поэтому определён здесь так же (re.search). Неопределённая
переменная — ошибка (StrictUndefined), как в Ansible: так образ без дайджеста валит шаблон, а не молча
рендерится в пустую строку. Код возврата 1 — шаблон не отрендерился."""

import re
import sys

import jinja2


def main() -> int:
    if len(sys.argv) != 3:
        print("использование: quadlet-render.py <шаблон> <образ>", file=sys.stderr)
        return 2
    env = jinja2.Environment(undefined=jinja2.StrictUndefined, keep_trailing_newline=True)
    env.tests["regex"] = lambda value, pattern: re.search(pattern, str(value)) is not None
    with open(sys.argv[1], encoding="utf-8") as f:
        tpl = env.from_string(f.read())
    try:
        sys.stdout.write(tpl.render(albums_svc_image=sys.argv[2]))
    except jinja2.UndefinedError as e:
        print(f"шаблон отвергнут: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
