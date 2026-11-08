"""Фикстуры для provision_cli (спека 005, design §4.1, §6): `tofu show -json` состояния и плана, как их отдаёт поддельный tofu.

    python3 -I plan_fixtures.py state <файл> [--types ТИП,ТИП…]
    python3 -I plan_fixtures.py plan  <файл> [--prior ТИП,ТИП…] [--change АДРЕС=ТИП:ДЕЙСТВИЕ[+ДЕЙСТВИЕ]]… [--format 1.2] [--no-changes]

state без --types — пустое состояние (`{"format_version":"1.0"}`, как у tofu show -json без ресурсов).
plan: без --prior в плане нет prior_state (первый apply); --change добавляет запись resource_changes; --no-changes убирает
ключ resource_changes. Форма — как у OpenTofu 1.x (values.root_module.resources[].type, resource_changes[].change.actions).
"""

import argparse
import json
import sys


def resources(types):
    return [{"address": "%s.r%d" % (t, i), "type": t, "name": "r%d" % i, "values": {"name": "example"}} for i, t in enumerate(types)]


def split(v):
    return [x for x in v.split(",") if x]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("kind", choices=("state", "plan"))
    ap.add_argument("out")
    ap.add_argument("--types", default="")
    ap.add_argument("--prior")
    ap.add_argument("--change", action="append", default=[])
    ap.add_argument("--format", default="1.2")
    ap.add_argument("--no-changes", action="store_true")
    a = ap.parse_args()
    if a.kind == "state":
        doc = {"format_version": "1.0"}
        if a.types:
            doc["values"] = {"root_module": {"resources": resources(split(a.types))}}
    else:
        doc = {"format_version": a.format, "terraform_version": "1.13.5"}
        if a.prior is not None:
            doc["prior_state"] = {"format_version": "1.0", "values": {"root_module": {"resources": resources(split(a.prior))}}}
        if not a.no_changes:
            changes = []
            for c in a.change:
                addr, _, rest = c.partition("=")
                typ, _, acts = rest.partition(":")
                changes.append({"address": addr, "type": typ, "change": {"actions": acts.split("+")}})
            doc["resource_changes"] = changes
    with open(a.out, "w", encoding="utf-8") as f:
        json.dump(doc, f)
        f.write("\n")


main()
sys.exit(0)
