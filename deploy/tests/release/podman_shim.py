#!/usr/bin/env python3
"""Поддельный podman для проверки build.sh без сборки (настоящую сборку делает задание build-image на ci/*).

build - запоминает аргументы в $SHIM_STATE/build.args; push - кладёт заранее заготовленный архив $SHIM_ARCHIVE_SRC
по адресу oci-archive:<путь>; image inspect - печатает $SHIM_INSPECT; rmi - ничего. Остальное - ошибка.
"""

import os
import shutil
import sys


def main(argv):
    state = os.environ["SHIM_STATE"]
    if argv[:1] == ["build"]:
        with open(os.path.join(state, "build.args"), "w", encoding="utf-8") as f:
            f.write("\n".join(argv))
        return 0
    if argv[:1] == ["push"]:
        dst = argv[-1]
        if not dst.startswith("oci-archive:"):
            return 2
        shutil.copy(os.environ["SHIM_ARCHIVE_SRC"], dst.removeprefix("oci-archive:"))
        return 0
    if argv[:2] == ["image", "inspect"]:
        with open(os.environ["SHIM_INSPECT"], encoding="utf-8") as f:
            sys.stdout.write(f.read())
        return 0
    if argv[:1] == ["rmi"]:
        return 0
    print(f"podman_shim: неожиданные аргументы {argv}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
