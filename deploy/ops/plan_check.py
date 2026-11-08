"""Проверка сохранённого плана tofu перед apply (спека 005, design §4.1, 005-18, 005-30).

Запуск (provision.sh apply, в контроллере без сети): python3 -I plan_check.py <state.json> <plan.json>
  state.json — `tofu show -json` состояния до плана; plan.json — `tofu show -json <план>`.
Код 0 — план можно применять; код 3 — отказ с причиной в stdout (значений из JSON в причине нет).

Что защищаем. Удаление сервера передаёт его адреса в запрос удаления, и публичный адрес уходит вместе с ним; спасает
только привязка адреса (clo_network_ip_attach) в state — tofu снимает её раньше сервера. Пока привязки в state нет
(например, первый apply оборвался на ответе API), ЛЮБОЙ план с удалением сервера — `--replace-server`, смена поля
ForceNew, новая схема провайдера — унесёт адрес. Поэтому проверка падает закрыто: всё непонятное — отказ, а не пропуск
(проверка, которая пропускает при непонятном выводе, хуже её отсутствия: создаёт уверенность).

Ресурсы ищутся по ТИПУ, не по адресу: переименование ресурса в main.tf дало бы удаление под другим адресом.
"""

import json
import re
import sys

SERVER = "clo_compute_instance"
ATTACH = "clo_network_ip_attach"
ADDRESS = "clo_network_ip"


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
    """Типы ресурсов модуля и вложенных (values.root_module); неожиданная форма — отказ."""
    if depth > 16 or not isinstance(module, dict):
        refuse("значения состояния имеют неожиданную форму")
    res = module.get("resources", [])
    kids = module.get("child_modules", [])
    if not isinstance(res, list) or not isinstance(kids, list):
        refuse("значения состояния имеют неожиданную форму")
    for r in res:
        if not isinstance(r, dict) or not isinstance(r.get("type"), str):
            refuse("ресурс без типа в значениях состояния")
        out.add(r["type"])
    for k in kids:
        module_types(k, out, depth + 1)


def state_types(doc, key):
    """Множество типов ресурсов в doc[key]['values']; (пусто, если значений нет — пустое состояние)."""
    types = set()
    values = doc.get("values") if key is None else (doc.get(key) or {}).get("values")
    if values is None:
        return types
    if not isinstance(values, dict) or "root_module" not in values:
        refuse("значения состояния имеют неожиданную форму")
    module_types(values["root_module"], types)
    return types


def main():
    if len(sys.argv) != 3:
        refuse("вызов: plan_check.py <state.json> <plan.json>")
    state = load(sys.argv[1], "состояние")
    plan = load(sys.argv[2], "план")
    state_now = state_types(state, None)
    changes = plan.get("resource_changes")
    if not isinstance(changes, list):
        refuse("в плане нет списка resource_changes")
    # Пустое состояние (первый apply): prior_state в плане нет — это норма. Непустое состояние без prior_state — не норма.
    if state_now:
        if not isinstance(plan.get("prior_state"), dict):
            refuse("состояние не пусто, а в плане нет prior_state")
        prior = state_types(plan, "prior_state")
    else:
        prior = set()
    deletes = set()
    for ch in changes:
        if not isinstance(ch, dict) or not isinstance(ch.get("type"), str):
            refuse("запись resource_changes без типа ресурса")
        change = ch.get("change")
        acts = change.get("actions") if isinstance(change, dict) else None
        if not isinstance(acts, list) or not acts or not all(isinstance(a, str) for a in acts):
            refuse("запись resource_changes без списка actions")
        if "delete" in acts:
            deletes.add(ch["type"])
    if ADDRESS in deletes:
        refuse("план удаляет публичный адрес (clo_network_ip): адрес не удаляется никогда")
    if SERVER in deletes and SERVER in prior and ATTACH not in prior:
        refuse(
            "план удаляет сервер, а привязки адреса нет в state: вместе с сервером уйдёт публичный адрес "
            "(README, «Привязка адреса не в state»)"
        )
    print("plan_check: план проверен")


main()
