"""tofu_static (спека 002, design §3, решения П2a-2, И2-4): проверки HCL без tofu и сети.

Запуск (в образе проверок, из tofu_static.sh): python3 -I tofu_static.py <каталог deploy/tofu/clo> <корень deploy>
Состав каталога — только *.tf, tofu-entry.sh, .terraform.lock.hcl и tests/*.tftest.hcl (дерево целиком, без
симлинков). Конфигурация: ephemeral у токена, validation у api_url, prevent_destroy у адреса, ignore_changes и
нет addresses/password у сервера, нет is_primary у привязки, ключ только импортом, нет провижинеров, terraform_data, null_resource,
external, http, чужих провайдеров и модулей с нелокальным source; lock-файл — только clo; дайджест образа
tofu — один в ops/images.env и в стенде. Затем самопроверка: каждое правило обязано сработать на мутации
настоящего кода (в том числе на обходах: *.tofu, *.tf.json, подкаталог, heredoc с /* */, метки без кавычек).
"""

import os
import re
import sys

BLOCK = re.compile(r'(?m)^[ \t]*([a-z_]+)((?:[ \t]+(?:"[^"]*"|[A-Za-z0-9_-]+))*)[ \t]*\{')
LABEL = re.compile(r'"([^"]*)"|([A-Za-z0-9_-]+)')
ALLOWED_TOP = ("tofu-entry.sh", ".terraform.lock.hcl")
# Запретные слова ищутся по сырому тексту, до вырезания комментариев: heredoc с /* */ прячет остаток файла.
RAW_BAD = re.compile(
    r"\bprovisioner\b|local-exec|remote-exec|\bdata\s+\"?(external|http)\b|\bresource\s+\"?(terraform_data|null_resource)\b"
    r"|\bkey_provider\b|\bmethod\s+\"?external\b"
)


def strip_comments(t):
    out, i, n, instr = [], 0, len(t), False
    while i < n:
        c = t[i]
        if instr:
            out.append(c)
            if c == "\\" and i + 1 < n:
                out.append(t[i + 1])
                i += 2
                continue
            instr = c != '"'
        elif c == '"':
            instr = True
            out.append(c)
        elif c == "#" or t.startswith("//", i):
            while i < n and t[i] != "\n":
                i += 1
            continue
        elif t.startswith("/*", i):
            j = t.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        else:
            out.append(c)
        i += 1
    return "".join(out)


def blocks(text):
    """Блоки верхнего уровня текста: (тип, [метки], тело)."""
    res, pos = [], 0
    while True:
        m = BLOCK.search(text, pos)
        if not m:
            return res
        depth, i, instr = 1, m.end(), False
        while i < len(text) and depth:
            c = text[i]
            if instr:
                if c == "\\":
                    i += 1
                elif c == '"':
                    instr = False
            elif c == '"':
                instr = True
            elif c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
            i += 1
        labels = [a or b for a, b in LABEL.findall(m.group(2))]
        res.append((m.group(1), labels, text[m.end() : i - 1]))
        pos = i


def all_blocks(text):
    """Все блоки на любой глубине (вложенные тоже): data внутри check {} или иного блока — такой же источник."""
    for kind, labels, body in blocks(text):
        yield kind, labels, body
        yield from all_blocks(body)


def attr(body, name):
    m = re.search(r"(?m)^[ \t]*%s[ \t]*=[ \t]*(.+)$" % re.escape(name), body)
    return m.group(1).strip() if m else None


FILE_FUNCS = re.compile(r"\b(file|templatefile|filebase64|filebase64sha256|filemd5|filesha1|filesha256|filesha512|fileset)\s*\(")


def check(tree, lock, images_env, lib_sh, example_tfvars):
    """Возвращает список ошибок (пустой — чисто). tree: {относительный путь: текст, None — симлинк}."""
    err = []
    sources = {}
    for rel, body in tree.items():
        top_level = "/" not in rel
        if body is None:
            err.append("%s: симлинк" % rel)
        elif top_level and (rel.endswith(".tf") or rel in ALLOWED_TOP):
            if rel.endswith(".tf"):
                sources[rel] = body
        elif rel.startswith("tests/") and rel.count("/") == 1 and rel.endswith(".tftest.hcl"):
            pass
        else:
            err.append("%s: лишний файл (разрешены *.tf, tofu-entry.sh, .terraform.lock.hcl, tests/*.tftest.hcl)" % rel)
    for rel, body in tree.items():
        if body and RAW_BAD.search(body) and rel.endswith((".tf", ".tofu", ".json", ".hcl")):
            err.append("%s: provisioner, terraform_data, null_resource, external, http, key_provider или method external (выполнились бы с эфемерным токеном)" % rel)
        if body and FILE_FUNCS.search(body) and rel.endswith((".tf", ".tftest.hcl")):
            err.append("%s: функции file/templatefile/filebase64/fileset запрещены (читали бы файлы раскатки в план)" % rel)
    text = "\n".join(strip_comments(t) for t in sources.values())
    seen = {"ip": 0, "srv": 0}
    for kind, labels, body in all_blocks(text):
        # Шифрование состояния средствами tofu: key_provider/method "external" запускает команду в контейнере apply
        # (там токен и сеть). Шифрование state у нас — age поверх файла, поэтому блок запрещён на любой глубине.
        if kind == "encryption":
            err.append("encryption: блок шифрования состояния запрещён (key_provider/method external запускает команду с токеном)")
        if kind in ("resource", "data", "ephemeral") and labels:
            t = labels[0]
            if not t.startswith("clo_"):
                err.append("%s %s: допустимы только ресурсы, источники и ephemeral-ресурсы провайдера clo (terraform_data, null_resource, external, http, terraform_remote_state — нет)" % (kind, t))
            if kind == "resource" and t == "clo_network_ip":
                seen["ip"] += 1
                lc = [b for k, _, b in blocks(body) if k == "lifecycle"]
                if not any(attr(b, "prevent_destroy") == "true" for b in lc):
                    err.append("clo_network_ip без lifecycle prevent_destroy = true")
            if kind == "resource" and t == "clo_compute_instance":
                seen["srv"] += 1
                lc = " ".join(b for k, _, b in blocks(body) if k == "lifecycle")
                ign = attr(lc, "ignore_changes") or ""
                if "image_id" not in ign or "block_device" not in ign or "keypairs" not in ign:
                    err.append("clo_compute_instance без ignore_changes = [image_id, block_device, keypairs]")
                for bad in ("addresses", "password"):
                    if re.search(r"(?m)^[ \t]*%s[ \t]*=" % bad, body):
                        err.append("clo_compute_instance: поле %s запрещено (адрес удалился бы с сервером / пароль лёг бы в state)" % bad)
            # Спека 005, 005-12: is_primary при 422 оставляет адрес привязанным в API без записи в state — защита плана
            # apply (ops/plan.sh) от этого не спасла бы первую привязку.
            if kind == "resource" and t == "clo_network_ip_attach" and re.search(r"(?m)^[ \t]*is_primary[ \t]*=", body):
                err.append("clo_network_ip_attach: is_primary запрещён (422 оставляет привязку в API без записи в state, 005-12)")
            if kind == "resource" and t == "clo_compute_keypair" and attr(body, "public_key") is None:
                err.append("clo_compute_keypair без public_key: закрытый ключ ляжет в state")
        if kind == "provider" and labels != ["clo"]:
            err.append("provider %s: допустим только clo" % (labels or "?"))
        if kind == "provider" and labels == ["clo"] and attr(body, "auth_url") != "var.api_url":
            err.append("provider clo: auth_url должен быть var.api_url (адрес с проверкой validation)")
        if kind == "module":
            src = (attr(body, "source") or "").strip('"')
            if not src.startswith(("./", "../")):
                err.append("module: source должен быть локальным (./ или ../)")
        if kind == "variable" and labels == ["api_url"]:
            val = [b for k, _, b in blocks(body) if k == "validation"]
            if not val or "https://" not in " ".join(val):
                err.append("api_url: нужен validation с https:// (токен уходит на этот адрес)")
        if kind == "variable" and labels == ["clo_api_token"]:
            if attr(body, "ephemeral") != "true" or attr(body, "sensitive") != "true":
                err.append("clo_api_token: нужны sensitive = true и ephemeral = true")
            if attr(body, "default") is not None:
                err.append("clo_api_token: значения по умолчанию быть не должно")
        if kind == "terraform":
            for k2, l2, b2 in blocks(body):
                # Тип backend: state — только локальный файл в inventory (-backend-config=path=…); s3, http, remote
                # и блок cloud отправили бы state (а в нём всё, что видит провайдер) во внешнее хранилище.
                if k2 == "backend" and l2 != ["local"]:
                    err.append('terraform: допустим только backend "local" или его отсутствие, а не backend %s' % (l2 or "?"))
                if k2 == "cloud":
                    err.append('terraform: блок cloud запрещён (допустим только backend "local")')
                if k2 == "required_providers":
                    names = re.findall(r"(?m)^[ \t]*([A-Za-z0-9_-]+)[ \t]*=[ \t]*\{", b2)
                    if names != ["clo"]:
                        err.append("required_providers: допустим только clo")
    if seen["ip"] == 0 or seen["srv"] == 0:
        err.append("нет clo_network_ip или clo_compute_instance")
    provs = re.findall(r'(?m)^provider\s+"([^"]+)"', lock)
    if provs != ["registry.opentofu.org/clo-ru/clo"]:
        err.append(".terraform.lock.hcl: в lock-файле должен быть только registry.opentofu.org/clo-ru/clo")
    if "zh:" not in lock and "h1:" not in lock:
        err.append(".terraform.lock.hcl: нет хэшей")
    # Шапка lock говорит «сверено» для конкретной версии: после обновления провайдера устаревшая шапка не должна
    # продолжать уверять, что новая версия сверена владельцем.
    hv = re.findall(r"(?m)^#.*terraform-provider-clo v(\d+\.\d+\.\d+)", lock)
    bv = re.findall(r'(?m)^\s*version\s*=\s*"(\d+\.\d+\.\d+)"', lock)
    if len(hv) != 1 or len(bv) != 1 or hv[0] != bv[0]:
        err.append(".terraform.lock.hcl: версия в шапке (%s) не совпадает с version в блоке provider (%s): "
                   "шапка «сверено» относится к другой версии" % (hv, bv))
    a = re.search(r"(?m)^SA_TOFU_IMAGE=(\S+)$", images_env)
    b = re.search(r"TOFU_IMAGE:-([^}]+)\}", lib_sh)
    if not a or not b or a.group(1) != b.group(1):
        err.append("образ tofu в deploy/ops/images.env и в deploy/tests/provision/lib.sh должен совпадать (по дайджесту)")
    if example_tfvars and re.search(r"(?i)token", example_tfvars):
        err.append("в примере *.tfvars упомянут token: токен только в файле секретов")
    return err


def mutations():
    """(имя, файл, (что, чем) или None, добавляемые файлы, подстрока ожидаемой ошибки)."""

    def ins(s):
        return ("flavor_ram   = 4", "flavor_ram   = 4\n  " + s)

    def ap(s):
        return ('provider "clo"', s + '\nprovider "clo"')

    return [
        ("ephemeral снят", "variables.tf", ("ephemeral = true", "ephemeral = false"), {}, "ephemeral"),
        ("prevent_destroy снят", "main.tf", ("prevent_destroy = true", "prevent_destroy = false"), {}, "prevent_destroy"),
        ("ignore_changes без block_device", "main.tf", ("[image_id, block_device, keypairs]", "[image_id, keypairs]"), {}, "ignore_changes"),
        ("ignore_changes без keypairs", "main.tf", ("[image_id, block_device, keypairs]", "[image_id, block_device]"), {}, "ignore_changes"),
        ("auth_url не из var.api_url", "main.tf", ("auth_url = var.api_url", 'auth_url = "https://x.example.org"'), {}, "auth_url"),
        ("функция file()", "main.tf", ap('locals {\n  x = file("/etc/hostname")\n}'), {}, "file/templatefile"),
        ("функция templatefile()", "main.tf", ap('locals {\n  x = templatefile("a", {})\n}'), {}, "file/templatefile"),
        ("функция fileset()", "main.tf", ap('locals {\n  x = fileset(".", "*")\n}'), {}, "file/templatefile"),
        ("addresses у сервера", "main.tf", ins("addresses = []"), {}, "addresses"),
        ("password у сервера", "main.tf", ins('password = "x"'), {}, "password"),
        ("keypair без public_key", "main.tf", ("  public_key = var.ssh_public_key\n", ""), {}, "public_key"),
        (
            "провижинер",
            "main.tf",
            ("entity_id   = clo_compute_instance.srv.id", 'entity_id   = clo_compute_instance.srv.id\n  provisioner "local-exec" {\n  }'),
            {},
            "provisioner",
        ),
        (
            "is_primary в привязке",
            "main.tf",
            ("entity_id   = clo_compute_instance.srv.id", "entity_id   = clo_compute_instance.srv.id\n  is_primary  = true"),
            {},
            "is_primary",
        ),
        ("terraform_data", "main.tf", ap('resource "terraform_data" "x" {\n}'), {}, "terraform_data"),
        ("terraform_data, метки без кавычек", "main.tf", ap("resource terraform_data x {\n}"), {}, "terraform_data"),
        ("data external", "main.tf", ap('data "external" "x" {\n}'), {}, "external"),
        ("data http", "main.tf", ap('data "http" "x" {\n}'), {}, "http"),
        ("чужой провайдер", "main.tf", ap('provider "null" {\n}'), {}, "provider"),
        ("модуль не локальный", "main.tf", ap('module "m" {\n  source = "git::https://example.org/m.git"\n}'), {}, "source"),
        ("значение по умолчанию у токена", "variables.tf", ("ephemeral = true", 'ephemeral = true\n  default   = "x"'), {}, "по умолчанию"),
        ("api_url без validation", "variables.tf", ("validation {", "xvalidation {"), {}, "api_url"),
        # Аудит итоговой версии: вложенные блоки, тип backend, ephemeral-блоки.
        (
            "data terraform_remote_state внутри check",
            "main.tf",
            ap('check "x" {\n  data "terraform_remote_state" "r" {\n  }\n}'),
            {},
            "terraform_remote_state",
        ),
        (
            "encryption с key_provider external",
            "versions.tf",
            ('backend "local" {}', 'backend "local" {}\n  encryption {\n    key_provider "external" "x" {\n    }\n  }'),
            {},
            "encryption",
        ),
        ("method external вне encryption", "main.tf", ap('method "external" "y" {\n}'), {}, "key_provider или method external"),
        ("backend s3", "versions.tf", ('backend "local" {}', 'backend "s3" {}'), {}, "backend"),
        ("backend http", "versions.tf", ('backend "local" {}', 'backend "http" {}'), {}, "backend"),
        ("блок cloud вместо backend", "versions.tf", ('backend "local" {}', "cloud {}"), {}, "cloud"),
        ("ephemeral не clo", "main.tf", ap('ephemeral "random_password" "x" {\n}'), {}, "ephemeral"),
        # Обходы из аудита И2-4: файлы, которые tofu тоже читает, подкаталоги и heredoc, прячущий остаток файла.
        ("файл *.tofu", None, None, {"x.tofu": 'provider "clo" {}\n'}, "лишний файл"),
        ("файл *.tf.json", None, None, {"x.tf.json": "{}"}, "лишний файл"),
        ("модуль в подкаталоге", None, None, {"sub/m.tf": 'resource "clo_network_ip" "y" {}\n'}, "лишний файл"),
        (
            "heredoc с /* */ прячет ресурс",
            "main.tf",
            ap("locals {\n  x = <<EOT\n/*\nEOT\n}\nresource terraform_data y {\n}\n/* */"),
            {},
            "terraform_data",
        ),
    ]


def read_tree(d):
    """Все файлы каталога; симлинк — None."""
    tree = {}
    for dirpath, _dirs, files in os.walk(d):
        for n in files:
            full = os.path.join(dirpath, n)
            rel = os.path.relpath(full, d)
            if os.path.islink(full):
                tree[rel] = None
            else:
                with open(full, encoding="utf-8", errors="replace") as f:
                    tree[rel] = f.read()
    return tree


def main():
    d, deploy = sys.argv[1:3]
    tree = read_tree(d)
    lock = tree.get(".terraform.lock.hcl") or ""
    images = open(os.path.join(deploy, "ops", "images.env"), encoding="utf-8").read()
    lib = open(os.path.join(deploy, "tests", "provision", "lib.sh"), encoding="utf-8").read()
    ex = os.path.join(deploy, "inventory.example", "tofu", "clo.tfvars.example")
    example = open(ex, encoding="utf-8").read() if os.path.exists(ex) else ""

    errors = check(tree, lock, images, lib, example)
    for e in errors:
        print("tofu_static: " + e)
    # Самопроверка: правило, которое не срабатывает на мутации, ничего не проверяет.
    for name, fname, rep, extra, want in mutations():
        mutated = dict(tree)
        if fname:
            old, new = rep
            if old not in mutated[fname]:
                print("tofu_static: самопроверка «%s»: мутация не применилась (код изменился?)" % name)
                errors.append(name)
                continue
            mutated[fname] = mutated[fname].replace(old, new, 1)
        mutated.update(extra)
        got = check(mutated, lock, images, lib, example)
        if not any(want in g for g in got):
            print("tofu_static: самопроверка «%s»: правило не сработало" % name)
            errors.append(name)
    # Самопроверка шапки lock: другая версия в блоке или в шапке, и шапка без версии, обязаны краснеть.
    # Версия берётся из самого lock: при обновлении провайдера мутации не должны устареть вместе с ним.
    m = re.search(r'(?m)^\s*version\s*=\s*"(\d+\.\d+\.\d+)"', lock)
    if m:
        v = m.group(1)
        for name, old, new in (
            ("lock: версия в блоке изменена", 'version     = "%s"' % v, 'version     = "0.0.1"'),
            ("lock: версия в шапке изменена", "provider-clo v" + v, "provider-clo v0.0.1"),
            ("lock: шапка без версии", "provider-clo v" + v, "provider-clo"),
        ):
            if old not in lock:
                print("tofu_static: самопроверка «%s»: мутация не применилась (код изменился?)" % name)
                errors.append(name)
                continue
            got = check(tree, lock.replace(old, new, 1), images, lib, example)
            if not any("версия в шапке" in g for g in got):
                print("tofu_static: самопроверка «%s»: правило не сработало" % name)
                errors.append(name)
    sys.exit(1 if errors else 0)


main()
