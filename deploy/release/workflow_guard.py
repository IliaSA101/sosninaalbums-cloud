#!/usr/bin/env python3
"""Инварианты workflow выпуска (design 004 §2, развилки 004-20, 004-25..004-34).

Один скрипт для CI (тесты workflow_static и шаг check) и для ops-ВМ (image-verify, в контейнере контроллера
`--network=none`, без .git). Вызов:

    python3 -I deploy/release/workflow_guard.py --root <дерево> --paths <файл> [--pins <каталог>]

--root - рабочее дерево коммита (HEAD): читаются `.github/workflows/*`, `deploy/release/actions.lock`,
`deploy/release/requirements.txt` и `deploy/ansible/requirements.txt` (обязателен: и в paths, и в --root; строка
PyYAML с хэшами у них одна, версию установленного PyYAML сверяет сам скрипт). --paths (обязателен) - вывод `git ls-tree -r -z --name-only
HEAD`, имена через NUL: правда о дереве коммита. Из него берутся проверка «пути только ASCII» и состав `.github/`;
файл есть в paths, но нет в --root или не обычный файл - отказ. --pins - каталог сверенных lock (ops-ВМ): sha256
lock обязан лежать там как `<sha256>.lock` (сверка с исходными репозиториями - в окне Б, §7).
Коды выхода: 0 - инварианты выполнены, 3 - отказ (причины на stderr), 2 - неверный вызов или нет PyYAML.

Разбор двумя путями со сверкой (004-25): (а) wfparse - белый список строк; (б) PyYAML safe_load. Из обоих -
одно и то же представление; расхождение - отказ. Содержательные правила (design §2.б) - в wfrules.py.
"""

import hashlib
import importlib.util
import os
import re
import stat
import sys

_here = os.path.dirname(os.path.abspath(__file__))


def _load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(_here, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


wfparse = _load("wfparse")
wfrules = _load("wfrules")
ALLOWED_FILES = wfrules.ALLOWED_FILES
# Формат lock общий с окном Б (tools.sh): имя, тег, 40 hex; комментарии - только строкой целиком.
LOCK_LINE = re.compile(r"^actions/([a-z0-9][a-z0-9._-]*) (v[0-9][A-Za-z0-9._-]*) ([0-9a-f]{40})$")
LOCK_ROOT = "deploy/release/actions.lock"
ANSIBLE_REQ = "deploy/ansible/requirements.txt"


def slurp(path, binary=False):
    """Файл целиком (с закрытием: ResourceWarning в тестах - это предупреждение)."""
    with open(path, "rb" if binary else "r", **({} if binary else {"encoding": "utf-8"})) as f:
        return f.read()


def norm(v):
    """Значение PyYAML → вид wfparse (строки; True в ключе `on`)."""
    if isinstance(v, dict):
        return {("on" if k is True else "off" if k is False else str(k)): norm(x) for k, x in v.items()}
    if isinstance(v, list):
        return [norm(x) for x in v]
    if isinstance(v, bool):
        return "true" if v else "false"
    return None if v is None else str(v)


def two_ways(text, name, errs):
    """Путь (а) и путь (б) → дерево (а) или None; расхождение и ошибки - в errs."""
    import yaml

    try:
        tree = wfparse.parse(text)
    except wfparse.Refusal as e:
        errs.append(f"{name}: {e}")
        return None
    try:
        docs = list(yaml.safe_load_all(text))
    except yaml.YAMLError as e:
        errs.append(f"{name}: PyYAML не разобрал файл: {str(e).splitlines()[0]}")
        return None
    if len(docs) != 1 or norm(docs[0]) != wfrules.plain(tree):
        errs.append(f"{name}: расхождение белого списка и PyYAML - файл читается двумя способами по-разному")
        return None
    return tree


def load_lock(root, errs):
    try:
        raw = slurp(os.path.join(root, LOCK_ROOT), True)
    except OSError:
        errs.append(f"{LOCK_ROOT} не читается")
        return {}, ""
    lock = {}
    for n, line in enumerate(raw.decode("utf-8", "replace").split("\n"), 1):
        if line == "" or line.startswith("#"):
            continue
        m = LOCK_LINE.match(line)
        if not m:
            errs.append(f"actions.lock:{n}: строка не вида «actions/<имя> <тег> <40 hex>» (комментарии - строкой целиком)")
        elif re.search(r"0{8}", m.group(3)):
            errs.append(f"actions.lock:{n}: SHA похож на заготовку (хвост из нулей)")
        else:
            lock[(m.group(1), m.group(3))] = m.group(2)
    if not lock:
        errs.append("actions.lock: ни одной строки")
    return lock, hashlib.sha256(raw).hexdigest()


def pyyaml_block(path):
    try:
        lines = slurp(path).split("\n")
    except OSError:
        return None
    out, on = [], False
    for ln in lines:
        if re.match(r"^pyyaml==", ln, re.I):
            on = True
        elif on and not ln.startswith((" ", "\t")):
            break
        if on and not ln.strip().startswith("#"):
            out.append(ln.rstrip())
    return out or None


def check_requirements(root, errs):
    a = pyyaml_block(os.path.join(root, "deploy/release/requirements.txt"))
    if a is None:
        errs.append("в deploy/release/requirements.txt нет строки PyYAML с хэшами")
        return
    ansible = pyyaml_block(os.path.join(root, ANSIBLE_REQ))
    if ansible is None:
        errs.append(f"{ANSIBLE_REQ} не читается или без строки PyYAML (он обязателен: сверка строки с контроллером)")
        return
    if ansible != a:
        errs.append("строка PyYAML с хэшами в deploy/release/requirements.txt ≠ deploy/ansible/requirements.txt")
        return
    import yaml

    want = re.match(r"^pyyaml==([0-9.]+)", a[0], re.I).group(1)
    if yaml.__version__ != want:
        errs.append(f"PyYAML {yaml.__version__} ≠ закреплённый {want}")


def check_paths(paths, errs):
    """Пути дерева коммита (git ls-tree) - только печатный ASCII; состав .github/ - ровно три workflow."""
    if not paths:
        errs.append("список путей пуст")
    for p in paths:
        if not re.match(r"^[\x20-\x7e]+$", p):
            errs.append(f"путь дерева не печатный ASCII: {ascii(p)}")  # ascii(): ESC и прочие управляющие - escape-последовательностью
    want = {f".github/workflows/{n}" for n in ALLOWED_FILES}
    gh = {p for p in paths if p == ".github" or p.startswith(".github/")}
    for p in sorted(gh - want):
        errs.append(f"{p}: незнакомый файл в .github (допустимы только {', '.join(ALLOWED_FILES)})")
    for p in sorted(want - gh):
        errs.append(f"{p}: нет в дереве коммита")
    if ANSIBLE_REQ not in paths:
        errs.append(f"{ANSIBLE_REQ}: нет в дереве коммита (обязателен)")


def decode_paths(raw):
    """NUL-разделённые имена; байты не UTF-8 становятся U+FFFD - не ASCII, значит отказ в check_paths."""
    return [chunk.decode("utf-8", "replace") for chunk in raw.split(b"\0") if chunk]


def read_workflows(root, errs):
    trees = {}
    for n in ALLOWED_FILES:
        p = os.path.join(root, ".github", "workflows", n)
        try:
            st = os.lstat(p)
        except OSError:
            errs.append(f".github/workflows/{n}: нет файла в --root")
            continue
        if not stat.S_ISREG(st.st_mode):
            errs.append(f".github/workflows/{n}: не обычный файл (симлинк или иное)")
            continue
        try:
            text = slurp(p, True).decode("utf-8")
        except UnicodeDecodeError:
            errs.append(f"{n}: не UTF-8")
            continue
        t = two_ways(text, n, errs)
        if t is not None:
            trees[n] = t
        if re.search(r"^[^#]*\blatest\b", text, re.M):
            errs.append(f"{n}: строка «latest» вне комментария")
    return trees


def check_tree(root, pins=None, paths=None):
    """Возвращает список отказов (пустой - инварианты выполнены)."""
    errs = []
    if paths is None:
        return ["нет списка путей дерева (--paths)"]
    check_paths(paths, errs)
    lock, lock_sha = load_lock(root, errs)
    check_requirements(root, errs)
    if pins is not None and not os.path.isfile(os.path.join(pins, f"{lock_sha}.lock")):
        errs.append(f"actions.lock ({lock_sha[:12]}) не сверен: нет в {pins} (окно Б: сверка действий)")
    trees = read_workflows(root, errs)
    if len(trees) < len(ALLOWED_FILES):
        if not errs:
            errs.append("разобраны не все workflow")  # молчаливого успеха при неполном наборе не бывает
        return errs
    try:
        wfrules.check_all(trees, lock, errs)
    except (AttributeError, TypeError, KeyError, ValueError, IndexError) as e:
        errs.append(f"структура файла вне ожидаемой формы ({type(e).__name__}: {e})")
    return errs


def usage():
    print("использование: workflow_guard.py --root <дерево> --paths <файл> [--pins <каталог>]", file=sys.stderr)
    return 2


def main(argv):
    opts, i = {}, 0
    while i < len(argv):
        if argv[i] in ("--root", "--paths", "--pins") and argv[i] not in opts and i + 1 < len(argv):
            opts[argv[i]] = argv[i + 1]
            i += 2
        else:
            return usage()
    if "--root" not in opts or "--paths" not in opts:
        return usage()
    try:
        import yaml  # noqa: F401
    except ImportError:
        print("workflow_guard: нет PyYAML (образ контроллера или venv из deploy/release/requirements.txt)", file=sys.stderr)
        return 2
    try:
        raw = slurp(opts["--paths"], True)
    except OSError as e:
        print(f"workflow_guard: файл путей не читается: {e}", file=sys.stderr)
        return 2
    errs = check_tree(opts["--root"], opts.get("--pins"), decode_paths(raw))
    for e in errs:
        print(f"workflow_guard: {e}", file=sys.stderr)
    if errs:
        return 3
    print("workflow_guard: инварианты workflow выполнены")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
