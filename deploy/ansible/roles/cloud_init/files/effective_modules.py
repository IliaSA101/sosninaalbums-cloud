"""Итоговые списки модулей cloud-init (стадии init, config, final) в JSON:
{"lists": {...}, "kept": {...}, "banned_present": {...}, "banned_sources": [...]}.

Имена модулей cloud-init нормализует (`cc_set_passwords`, `set-passwords`, `set_passwords.py` и имя с пробелами —
один и тот же модуль), поэтому запрещённые модули ищутся не по строке, а по каноническому имени из самого
cloud-init (form_module_name): lists — списки как есть; kept — те же списки без запрещённых модулей (из них
роль пишет свой файл); banned_present — запрещённые записи, которые остались в lists, по стадиям.

Режим `base` — исходные списки образа БЕЗ нашего zz-sa.cfg: cloud.cfg плюс cloud.cfg.d/*.cfg по алфавиту
(cloudinit.util.read_conf_with_confd, как в cloud-init). Из них роль строит свой файл.

Режим `full` — как соберёт конфигурацию сам cloud-init при загрузке: helpers.ConfigMerger с базовой
конфигурацией stages.fetch_base_config (встроенная, cloud.cfg и cloud.cfg.d, /run/cloud-init/cloud.cfg, параметры
ядра) и cloud-config экземпляра (cloud-config, vendor-cloud-config, vendor2-cloud-config из
/var/lib/cloud/instance). Кроме того, каждый из этих файлов проверяется отдельно: запрещённый модуль в
списках стадий любого из них попадает в banned_sources с именем файла (понятная причина провала, а не просто
«списки не равны»).

Чего это НЕ покрывает: datasource без кэша на диске (данные, которые cloud-init получит от провайдера только при
следующей загрузке) и динамические vendor-данные, которых ещё нет в /var/lib/cloud/instance. Они проверяются
только на пробной аренде (design §13).

Запуск: python3 -I - <base|full> < этот файл, из каталога /: изолированный режим (-I) и chdir не дают
python взять модули (json.py, shutil.py) из дома администратора, под которым идёт прогон. Скрипт приходит через
stdin: файла на хосте нет.
"""
import json
import os
import sys

from cloudinit import helpers, stages, util
from cloudinit.config import modules as ci_modules

STAGES = ("cloud_init_modules", "cloud_config_modules", "cloud_final_modules")
OURS = "zz-sa.cfg"
RUN_DIR = "/run/cloud-init"
# Пользователи, пароли, ключи (включая импорт ключей с GitHub/Launchpad) — в любом написании: имена приводятся
# к каноническому виду функцией cloud-init.
BANNED_RAW = ("users_groups", "set_passwords", "ssh", "ssh_import_id")
EXPLICIT = (
    "/var/lib/cloud/instance/cloud-config.txt",
    "/var/lib/cloud/instance/vendor-cloud-config.txt",
    "/var/lib/cloud/instance/vendor2-cloud-config.txt",
    "/run/cloud-init/cloud.cfg",
)


def canon(item):
    """Каноническое имя модуля — как у cloud-init: имя без пробелов по краям, затем form_module_name.

    Запись — строка, список [имя, частота, ...] или словарь {name: имя, frequency: ...}. Пустые записи (пустая строка,
    [], {}, None, словарь без name) cloud-init пропускает, поэтому имя у них пустое и они не запрещены. Любой другой
    вид записи — отказ: прогон падает, а не пропускает то, чего эта проверка не понимает."""
    if item is None:
        return ""
    if isinstance(item, str):
        raw = item
    elif isinstance(item, (list, tuple)):
        raw = item[0] if item else ""
    elif isinstance(item, dict):
        raw = item.get("name", "")
    else:
        sys.exit(f"неизвестная запись в списке модулей cloud-init: {item!r}")
    if raw is None:
        return ""
    if not isinstance(raw, str):
        sys.exit(f"неизвестное имя модуля в списке cloud-init: {item!r}")
    raw = raw.strip()
    if not raw:
        return ""
    form = getattr(ci_modules, "form_module_name", None)
    if form is None:  # в старых cloud-init это метод класса Modules
        form = ci_modules.Modules._form_module_name
    return form(raw) or ""


BANNED = {canon(n) for n in BANNED_RAW}


def banned_sources():
    """Файлы с запрещёнными модулями в списках стадий: [«файл: стадия: модули»]."""
    found = []
    for path in EXPLICIT:
        if not os.path.isfile(path):
            continue
        cfg = util.read_conf(path)
        for stage in STAGES:
            bad = [str(i) for i in (cfg.get(stage) or []) if canon(i) in BANNED]
            if bad:
                found.append(f"{path}: {stage}: {', '.join(bad)}")
    return found


if len(sys.argv) != 2 or sys.argv[1] not in ("base", "full"):
    sys.exit("аргумент: base или full")

if sys.argv[1] == "base":
    _listdir = os.listdir
    os.listdir = lambda p: [f for f in _listdir(p) if f != OURS]
    cfg = util.read_conf_with_confd("/etc/cloud/cloud.cfg")
    bad = []
else:
    paths = helpers.Paths({"cloud_dir": "/var/lib/cloud", "run_dir": RUN_DIR})
    merger = helpers.ConfigMerger(
        paths=paths, datasource=None, additional_fns=[], base_cfg=stages.fetch_base_config(RUN_DIR)
    )
    cfg = merger.cfg
    bad = banned_sources()

lists = {s: cfg.get(s) for s in STAGES}
kept = {s: [i for i in (lists[s] or []) if canon(i) not in BANNED] for s in STAGES}
present = {s: [str(i) for i in (lists[s] or []) if canon(i) in BANNED] for s in STAGES}
print(json.dumps({"lists": lists, "kept": kept, "banned_present": present, "banned_sources": bad}))
