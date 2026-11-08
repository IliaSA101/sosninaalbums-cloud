"""Проверка сохранённого плана tofu перед apply (спека 005, design §4.1, 005-18, 005-30, 005-40).

Запуск (provision.sh apply, в контроллере без сети):
  python3 -I plan_check.py <state.json> <plan.json> [--replace-server] [--traces]
  state.json — `tofu show -json` состояния до плана; plan.json — `tofu show -json <план>`;
  --replace-server — apply запущен с этим флагом (без него любое удаление сервера — отказ);
  --traces — в inventory есть следы прошлой раскатки (deployed-commit или hosts.yml).
Код 0 — план можно применять; код 3 — отказ с причиной в stdout (значений из JSON в причине нет).

Что защищаем. Удаление сервера передаёт его адреса в запрос удаления, и публичный адрес уходит вместе с ним; спасает
только привязка адреса (clo_network_ip_attach) в state — tofu снимает её раньше сервера. Пока привязки в state нет
(например, первый apply оборвался на ответе API), ЛЮБОЙ план с удалением сервера — `--replace-server`, смена поля
ForceNew, новая схема провайдера — унесёт адрес. Поэтому проверка падает закрыто: всё непонятное — отказ, а не пропуск
(проверка, которая пропускает при непонятном выводе, хуже её отсутствия: создаёт уверенность).

Правила (каждое — отказ):
  * план с `errored: true`; действия ресурсов clo_* вне белого списка (forget, пусто, неизвестные сочетания);
  * удаление clo_network_ip; удаление сервера без --replace-server;
  * удаление сервера, когда привязки нет в prior_state, или привязка (любая) не удаляется в том же плане
    (update/forget/no-op у привязки при удалении сервера: адрес ушёл бы с сервером);
  * непустое состояние без prior_state.values.root_module в плане;
  * следы прошлой раскатки, адреса нет в prior_state и план создаёт clo_network_ip (потерянный или недописанный
    state: был бы заведён второй, платный адрес вместо привязки существующего) — при любом остальном состоянии;
  * пустое состояние при непустом prior_state (выводы tofu расходятся).

Ресурсы ищутся по ТИПУ, не по адресу: переименование ресурса в main.tf дало бы удаление под другим адресом.
"""

import collections
import json
import re
import sys

SERVER = "clo_compute_instance"
ATTACH = "clo_network_ip_attach"
ADDRESS = "clo_network_ip"
# Белый список действий ресурсов clo_* (порядок важен: [delete, create] — замена, [create, delete] — create_before_destroy).
# forget (removed{destroy=false}), пустой список и любые иные сочетания — отказ: проверка их смысла не знает.
CLO_ACTIONS = {("no-op",), ("create",), ("read",), ("update",), ("delete",), ("delete", "create"), ("create", "delete")}
FLAGS = ("--replace-server", "--traces")


def refuse(msg):
    print("plan_check: отказ: " + msg)
    sys.exit(3)


def load(path, what):
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError:
        refuse("%s: файл не прочитан" % what)
    if not text.strip():
        refuse("%s: пустой вывод tofu show -json" % what)
    try:
        doc = json.loads(text)
    except ValueError:
        refuse("%s: вывод tofu show -json не разобран как JSON" % what)
    if not isinstance(doc, dict):
        refuse("%s: ожидался JSON-объект" % what)
    fv = doc.get("format_version")
    if not isinstance(fv, str) or not re.fullmatch(r"1\.[0-9]+", fv):
        refuse("%s: format_version не 1.x (формат вывода изменился, проверка не знает его)" % what)
    return doc


def module_types(module, out, depth=0):
    """Счётчик типов ресурсов модуля и вложенных child_modules (values.root_module); неожиданная форма — отказ."""
    if depth > 16 or not isinstance(module, dict):
        refuse("значения состояния имеют неожиданную форму")
    res = module.get("resources", [])
    kids = module.get("child_modules", [])
    if not isinstance(res, list) or not isinstance(kids, list):
        refuse("значения состояния имеют неожиданную форму")
    for r in res:
        if not isinstance(r, dict) or not isinstance(r.get("type"), str):
            refuse("ресурс без типа в значениях состояния")
        out[r["type"]] += 1
    for k in kids:
        module_types(k, out, depth + 1)


def state_types(doc, key):
    """Счётчик типов ресурсов в doc[key]['values'] (key None — сам doc); пусто, если значений нет — пустое состояние."""
    types = collections.Counter()
    sub = doc if key is None else doc.get(key)
    values = sub.get("values") if isinstance(sub, dict) else None
    if values is None:
        return types
    if not isinstance(values, dict) or "root_module" not in values:
        refuse("значения состояния имеют неожиданную форму")
    module_types(values["root_module"], types)
    return types


def read_changes(plan):
    """resource_changes → список (тип, действия); белый список действий clo_*; неожиданная форма — отказ."""
    changes = plan.get("resource_changes")
    if not isinstance(changes, list):
        refuse("в плане нет списка resource_changes")
    out = []
    for ch in changes:
        if not isinstance(ch, dict) or not isinstance(ch.get("type"), str):
            refuse("запись resource_changes без типа ресурса")
        change = ch.get("change")
        acts = change.get("actions") if isinstance(change, dict) else None
        if not isinstance(acts, list) or not acts or not all(isinstance(a, str) for a in acts):
            refuse("запись resource_changes без списка actions")
        if ch["type"].startswith("clo_") and tuple(acts) not in CLO_ACTIONS:
            refuse("у ресурса clo_* действия вне белого списка (forget, пустой список или неизвестное сочетание)")
        out.append((ch["type"], acts))
    return out


def main():
    args = sys.argv[1:]
    flags = [a for a in args[2:] if a in FLAGS]
    if len(args) < 2 or len(flags) != len(args) - 2 or len(set(flags)) != len(flags):
        refuse("вызов: plan_check.py <state.json> <plan.json> [--replace-server] [--traces]")
    replace, traces = "--replace-server" in flags, "--traces" in flags
    state = load(args[0], "состояние")
    plan = load(args[1], "план")
    if plan.get("errored") not in (None, False):
        refuse("план помечен errored: tofu не смог его посчитать")
    state_now = state_types(state, None)
    changes = read_changes(plan)
    # prior_state читается всегда, когда он есть. Непустое состояние без prior_state.values.root_module — не норма
    # (по нему принимается решение о привязке), пустое (первый apply) — норма.
    prior_doc = plan.get("prior_state")
    if prior_doc is not None and not isinstance(prior_doc, dict):
        refuse("prior_state в плане неожиданной формы")
    if state_now:
        pv = prior_doc.get("values") if prior_doc else None
        if not isinstance(pv, dict) or "root_module" not in pv:
            refuse("состояние не пусто, а в плане нет prior_state.values.root_module")
    prior = state_types(plan, "prior_state")
    # Пустое состояние при непустом prior_state — расхождение двух выводов tofu: закрыто, а не пропуск.
    if not state_now and prior:
        refuse("состояние пусто, а prior_state в плане содержит ресурсы: выводы tofu расходятся")
    deletes = {t for t, acts in changes if "delete" in acts}
    if ADDRESS in deletes:
        refuse("план удаляет публичный адрес (clo_network_ip): адрес не удаляется никогда")
    # Следы прошлой раскатки + создание адреса при отсутствии адреса в prior_state — отказ при любом остальном состоянии:
    # оборванный первый apply оставляет в state ключ и сервер без адреса (ответ API потерян), и второй запуск завёл бы
    # второй, платный адрес, а hosts.yml и DNS остались бы на первом.
    if traces and prior[ADDRESS] == 0 and any(t == ADDRESS and "create" in acts for t, acts in changes):
        refuse(
            "в state нет публичного адреса, но в inventory следы прошлой раскатки (deployed-commit или hosts.yml), "
            "а план создаёт новый адрес: state потерян или прошлый apply оборвался? Сначала восстановить state "
            "(копия inventory) или привязать существующий адрес (README, «state потерян»); если раскатка действительно "
            "первая — убрать эти файлы из inventory"
        )
    if SERVER in deletes:
        if not replace:
            refuse("план удаляет сервер, а apply запущен без --replace-server: вместе с сервером уйдёт публичный адрес")
        if prior[ATTACH] == 0:
            refuse(
                "план удаляет сервер, а привязки адреса нет в state: вместе с сервером уйдёт публичный адрес "
                "(README, «Привязка адреса не в state»)"
            )
        attach = [acts for t, acts in changes if t == ATTACH]
        # Привязка, не удаляемая вместе с сервером (update, forget, no-op), оставила бы адрес в запросе удаления сервера.
        if any("delete" not in acts for acts in attach) or len(attach) < prior[ATTACH]:
            refuse("план удаляет сервер, а привязка адреса в том же плане не удаляется: адрес уйдёт вместе с сервером")
    print("plan_check: план проверен")


main()
