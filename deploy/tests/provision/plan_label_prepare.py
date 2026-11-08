"""Помощник шага tofu_plan_label (спека 005, 005-30): конфигурации для плана с токеном-меткой и разбор файла плана.

    python3 -I plan_label_prepare.py <variables.tf> <каталог>      пишет <каталог>/ok/main.tf и <каталог>/mut/main.tf
    python3 -I plan_label_prepare.py --check <каталог out> <метка>  метки нет в ok.plan и есть в mut.plan

ok — блок переменной clo_api_token из настоящего variables.tf (ephemeral = true) и ресурс встроенного провайдера; mut — то же
с `ephemeral = false`: на нём проверка обязана увидеть метку (самопроверка — иначе «метки нет» ничего не значило бы).
Файл плана — zip: метка ищется и в самом файле, и в каждом распакованном члене.
"""

import re
import sys
import zipfile


def prepare(variables_tf, out):
    src = open(variables_tf, encoding="utf-8").read()
    m = re.search(r"(?ms)^variable \"clo_api_token\" \{.*?^\}\n", src)
    if not m or "ephemeral = true" not in m.group(0):
        sys.exit("plan_label: в variables.tf нет блока clo_api_token с ephemeral = true")
    res = 'resource "terraform_data" "x" {\n  input = "static"\n}\n'
    with open(out + "/ok/main.tf", "w", encoding="utf-8") as f:
        f.write(m.group(0) + "\n" + res)
    with open(out + "/mut/main.tf", "w", encoding="utf-8") as f:
        f.write(m.group(0).replace("ephemeral = true", "ephemeral = false") + "\n" + res)


def has(path, marker):
    raw = open(path, "rb").read()
    z = zipfile.ZipFile(path)
    return marker.encode() in raw or any(marker.encode() in z.read(n) for n in z.namelist())


def check(d, marker):
    bad = 0
    if has(d + "/ok.plan", marker):
        print("tofu_plan_label: токен-метка попала в файл плана при ephemeral = true")
        bad = 1
    if not has(d + "/mut.plan", marker):
        print("tofu_plan_label: при ephemeral = false метки в плане нет — проверка бессмысленна (формат плана изменился?)")
        bad = 1
    return bad


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "--check":
        sys.exit(check(sys.argv[2], sys.argv[3]))
    if len(sys.argv) == 3:
        prepare(sys.argv[1], sys.argv[2])
        return
    sys.exit("plan_label: вызов: variables.tf каталог | --check каталог метка")


main()
