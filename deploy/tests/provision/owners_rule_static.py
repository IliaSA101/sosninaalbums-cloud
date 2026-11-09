"""owners_rule_static (спека 005, design §1.1, §1.4, решения 005-4, 005-22, 005-32): правила полей владельца в роли
albums_svc совпадают с правилами сервиса (internal/config/config.go: checkOwners, checkSite).

Запуск (в образе проверок, из owners_static.sh):
    python3 -I owners_rule_static.py <каталог роли albums_svc> <site_cases.json сервиса> <vars.yml примера inventory>
        <vars.yml inventory стенда раскатки>

Зачем. Роль кладёт albums_owners в config.json как есть. Если роль мягче сервиса, сервис не стартует у всех
владельцев, и узнаёшь это после записи конфига на сервере. Поэтому:
  1. регэкспы роли (vars/main.yml) проверяются на общем файле случаев site_cases.json — тот же читает Go-тест
     TestConfigOwnerSite и тест листалки — и на таблице Unicode (класс «невидимых» знаков);
  2. задачи tasks/owners.yml гоняются настоящим Ansible на случаях владельцев: неизвестные ключи, записи токена
     (только name и sha256), name 1…64 знака без управляющих, studio без управляющих, site;
  3. albums_owners из примера inventory (в том числе с раскомментированным site) и из inventory стенда раскатки
     обязаны проходить те же задачи: пример, который роль отвергает, хуже отсутствия примера;
  4. самопроверка: на «испорченных» копиях роли тот же прогон обязан покраснеть — иначе зелёный результат
     ничего не доказывал бы.
Роль может быть строже сервиса (намеренно: site пустой, site: null и ключ Site в другом регистре сервис принимает —
encoding/json сопоставляет ключи без регистра, null даёт ""), но не мягче: всё, что отвергает сервис, роль
отвергает. Случаи «роль строже» в owner_cases подписаны так и ожидают отказа роли.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata

import jinja2
import yaml

HASH1 = "0" * 63 + "1"
HASH2 = "0" * 63 + "2"
HASH3 = "0" * 63 + "3"


def owner(**kw):
    """Годный владелец, поля заменяются kw; значение _DEL убирает поле."""
    o = {"slug": "p1", "studio": "Студия Пример", "quota_bytes": 10737418240, "max_albums": 500,
         "tokens": [{"name": "ноутбук", "sha256": HASH1}]}
    o.update(kw)
    return {k: v for k, v in o.items() if v is not _DEL}


_DEL = object()
BASE_SITE = "https://studio.example"
INT64_MAX = 2**63 - 1  # предел quota_bytes (int64) и max_albums (int на amd64/arm64) в config.go


def owner_cases():
    """(имя, список владельцев, принимает ли сервис). Для site-случаев — отдельно, из site_cases.json."""
    tok = lambda **kw: [dict({"name": "n", "sha256": HASH1}, **kw)]  # noqa: E731
    ok, bad = True, False
    c = [
        ("образец", [owner()], ok),
        ("с сайтом", [owner(site=BASE_SITE + "/albums")], ok),
        ("студия 100 знаков", [owner(studio="я" * 100)], ok),
        ("студия с пробелами, цифрами и эмодзи", [owner(studio="Студия № 1 «Свет» \U0001f4f7")], ok),
        ("имя токена 64 знака", [owner(tokens=tok(name="я" * 64))], ok),
        ("два токена и два владельца", [owner(tokens=[{"name": "a", "sha256": HASH1}, {"name": "b", "sha256": HASH2}]),
                                        owner(slug="p2", tokens=tok(sha256=HASH3))], ok),
        # Неизвестные ключи владельца (DisallowUnknownFields сервиса).
        ("неизвестный ключ", [owner(extra=1)], bad),
        ("опечатка в ключе site (кириллическая «е»)", [owner(**{"sit\u0435": BASE_SITE})], bad),
        ("ключ Site в другом регистре (роль строже)", [owner(Site=BASE_SITE)], bad),
        ("владелец — не словарь", ["p1"], bad),
        ("нет владельцев", [], bad),
        # Записи токена: только name и sha256.
        ("токен в открытом виде вместо sha256", [owner(tokens=[{"name": "n", "token": "abc"}])], bad),
        ("токен в открытом виде рядом с sha256", [owner(tokens=tok(token="abc"))], bad),
        ("лишний ключ токена", [owner(tokens=tok(note="x"))], bad),
        ("нет name у токена", [owner(tokens=[{"sha256": HASH1}])], bad),
        ("нет sha256 у токена", [owner(tokens=[{"name": "n"}])], bad),
        ("токен — строка", [owner(tokens=["abc"])], bad),
        ("токенов нет", [owner(tokens=[])], bad),
        ("tokens — строка", [owner(tokens="abc")], bad),
        ("sha256 заглавными", [owner(tokens=tok(sha256=HASH1.upper().replace("0", "A")))], bad),
        ("sha256 короткий", [owner(tokens=tok(sha256="ab"))], bad),
        ("sha256 с переводом строки в конце", [owner(tokens=tok(sha256=HASH1 + "\n"))], bad),
        # name: 1…64 знака без управляющих.
        ("name пустой", [owner(tokens=tok(name=""))], bad),
        ("name 65 знаков", [owner(tokens=tok(name="я" * 65))], bad),
        ("name — число", [owner(tokens=tok(name=5))], bad),
        ("name с NUL", [owner(tokens=tok(name="a\x00b"))], bad),
        ("name с переводом строки", [owner(tokens=tok(name="a\n"))], bad),
        ("name с RLO (U+202E)", [owner(tokens=tok(name="a\u202eb"))], bad),
        ("name с нулевой шириной (U+200B)", [owner(tokens=tok(name="a\u200bb"))], bad),
        ("name с управляющим C1 (U+0085)", [owner(tokens=tok(name="a\u0085b"))], bad),
        # studio: 1…100 знаков без управляющих.
        ("студия пустая", [owner(studio="")], bad),
        ("студия 101 знак", [owner(studio="я" * 101)], bad),
        ("студии нет", [owner(studio=_DEL)], bad),
        ("студия — число", [owner(studio=5)], bad),
        ("студия с переводом строки", [owner(studio="a\nb")], bad),
        ("студия с переводом строки в конце", [owner(studio="ab\n")], bad),
        ("студия с NUL", [owner(studio="a\x00b")], bad),
        ("студия с DEL", [owner(studio="a\x7fb")], bad),
        ("студия с RLO (U+202E)", [owner(studio="a\u202eb")], bad),
        ("студия с U+2028", [owner(studio="a\u2028b")], bad),
        ("студия с U+2029", [owner(studio="a\u2029b")], bad),
        ("студия с ZWJ (U+200D)", [owner(studio="a\u200db")], bad),
        ("студия с BOM (U+FEFF)", [owner(studio="a\ufeffb")], bad),
        ("студия с мягким переносом (U+00AD)", [owner(studio="a\xadb")], bad),
        ("студия с тег-знаком (U+E0020)", [owner(studio="a\U000e0020b")], bad),
        # site: пустой и null сервис принимает, роль нет (строже: «не нужен — уберите строку»); остальное — site_cases.json.
        ("site пустой (роль строже)", [owner(site="")], bad),
        ("site — число", [owner(site=5)], bad),
        ("site — null (роль строже)", [owner(site=None)], bad),
        ("site — список", [owner(site=[BASE_SITE])], bad),
        # Прочее: слаг, лимиты, уникальность.
        ("слаг с заглавной", [owner(slug="P1")], bad),
        ("слаг с переводом строки", [owner(slug="p1\n")], bad),
        ("слаг 33 знака", [owner(slug="a" * 33)], bad),
        ("квота 0", [owner(quota_bytes=0)], bad),
        ("квота ровно int64 max", [owner(quota_bytes=INT64_MAX)], ok),
        ("квота выше int64 max", [owner(quota_bytes=INT64_MAX + 1)], bad),
        ("квота — true", [owner(quota_bytes=True)], bad),
        ("квота — строка", [owner(quota_bytes="10")], bad),
        ("число альбомов 0", [owner(max_albums=0)], bad),
        ("число альбомов ровно int64 max", [owner(max_albums=INT64_MAX)], ok),
        ("число альбомов выше int64 max", [owner(max_albums=INT64_MAX + 1)], bad),
        ("число альбомов — true", [owner(max_albums=True)], bad),
        ("слаг повторяется", [owner(), owner(tokens=tok(sha256=HASH2))], bad),
        ("хэш повторяется между владельцами", [owner(), owner(slug="p2")], bad),
        ("хэш повторяется в одном владельце", [owner(tokens=[{"name": "a", "sha256": HASH1}, {"name": "b", "sha256": HASH1}])], bad),
    ]
    return c


def load_cases(path):
    with open(path, encoding="utf-8") as f:
        cases = json.load(f)
    assert isinstance(cases, list) and len(cases) >= 30, "site_cases.json: ожидали список из 30+ случаев"
    for c in cases:
        assert set(c) == {"site", "ok", "why"} and isinstance(c["ok"], bool), f"site_cases.json: форма случая: {c!r}"
    assert any(c["ok"] for c in cases) and any(not c["ok"] for c in cases), "site_cases.json: нужны и принятые, и отказные"
    return cases


def role_vars(role):
    with open(os.path.join(role, "vars", "main.yml"), encoding="utf-8") as f:
        v = yaml.safe_load(f)
    # albums_svc_visible_re собирается из двух частей через Jinja (длина строки в ansible-lint); здесь — тем же Jinja.
    v["albums_svc_visible_re"] = jinja2.Template(v["albums_svc_visible_re"]).render(**v)
    return v


def site_rule(v, s):
    """Условие задачи owners.yml для site (проверяется ещё и настоящим Ansible ниже)."""
    return (isinstance(s, str) and len(s) <= 200 and re.search(v["albums_svc_site_re"], s) is not None
            and re.search(v["albums_svc_site_dot_re"], s) is None)


def check_python(v, cases):
    """Регэкспы на общем файле случаев и на таблице Unicode (быстро, без Ansible)."""
    errs = []
    for c in cases:
        if site_rule(v, c["site"]) != c["ok"]:
            errs.append(f"site {c['site']!r} ({c['why']}): сервис {'принимает' if c['ok'] else 'отвергает'}, роль должна так же")
    # Класс «невидимых» знаков = Cc, Cf, Zl, Zp (config.go: hasInvisible) на всей таблице Unicode образа проверок.
    # Это сверка с unicodedata Python (Unicode 15), а не с Go. Набор сверен с таблицей Go 1.27 (Unicode 17) вручную
    # при написании; при обновлении Go (toolchain в go.mod) — сверить с Go заново, этот цикл Go не видит.
    rx = re.compile(v["albums_svc_visible_re"])
    for cp in range(0x110000):
        if 0xD800 <= cp <= 0xDFFF:
            continue
        inv = unicodedata.category(chr(cp)) in ("Cc", "Cf", "Zl", "Zp")
        if (rx.search("a" + chr(cp) + "b") is None) != inv:
            errs.append(f"U+{cp:04X} ({unicodedata.category(chr(cp))}): роль {'принимает' if inv else 'отвергает'} зря")
            if len(errs) > 20:
                break
    return errs


PLAY = """---
- name: Случаи правила владельцев (owners_rule_static)
  hosts: localhost
  connection: local
  gather_facts: false
  vars_files:
    - "{{ role_dir }}/vars/main.yml"
  tasks:
    - name: Случай
      ansible.builtin.include_tasks: one.yml
      loop: "{{ cases }}"
      loop_control:
        loop_var: case
        label: "{{ case.id }}"
"""

ONE = """---
- block:
    - name: Задачи владельцев роли
      ansible.builtin.include_tasks: "{{ role_dir }}/tasks/owners.yml"
      vars:
        albums_owners: "{{ case.owners }}"
    - name: Принято
      ansible.builtin.debug:
        msg: "CASE {{ case.id }} ok"
  rescue:
    - name: Отвергнуто
      ansible.builtin.debug:
        msg: "CASE {{ case.id }} fail"
"""


def run_ansible(role, cases):
    """Гонит tasks/owners.yml роли на каждом случае; возвращает {id: True/False} (принято/нет) или строку-ошибку."""
    ansible = shutil.which("ansible-playbook")
    if not ansible:
        return "ansible-playbook не найден (шаг идёт в образе проверок)"
    with tempfile.TemporaryDirectory(prefix="sa-owners-") as d:
        for name, text in (("play.yml", PLAY), ("one.yml", ONE)):
            with open(os.path.join(d, name), "w", encoding="utf-8") as f:
                f.write(text)
        with open(os.path.join(d, "vars.json"), "w", encoding="utf-8") as f:
            json.dump({"role_dir": os.path.abspath(role),
                       "cases": [{"id": i, "owners": c[1]} for i, c in enumerate(cases)]}, f, ensure_ascii=False)
        env = dict(os.environ, ANSIBLE_NOCOLOR="1", ANSIBLE_LOCALHOST_WARNING="false", ANSIBLE_FORCE_COLOR="0")
        r = subprocess.run([ansible, "-i", "localhost,", "play.yml", "-e", "@vars.json"], cwd=d, env=env, text=True,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900, check=False)
    got = {int(m.group(1)): m.group(2) == "ok" for m in re.finditer(r'"msg": "CASE (\d+) (ok|fail)"', r.stdout)}
    if r.returncode != 0 or len(got) != len(cases):
        return f"ansible-playbook: код {r.returncode}, получено {len(got)} из {len(cases)} ответов\n{r.stdout[-1500:]}"
    return got


def check_ansible(role, cases):
    """Список расхождений настоящей роли с ожиданием по каждому случаю."""
    got = run_ansible(role, cases)
    if isinstance(got, str):
        return [got]
    return [f"{c[0]}: ожидалось, что роль {'принимает' if c[2] else 'отвергает'}, а она делает наоборот"
            for i, c in enumerate(cases) if got[i] != c[2]]


def inventory_cases(example, stand):
    """Владельцы из примера inventory (как есть и с раскомментированным site) и из inventory стенда раскатки."""
    def owners(text):
        return yaml.safe_load(text)["albums_owners"]
    with open(example, encoding="utf-8") as f:
        text = f.read()
    uncommented = re.sub(r"(?m)^(\s*)# (site: )", r"\1\2", text)
    if uncommented == text:
        raise SystemExit("пример inventory: нет закомментированной строки «# site: …» у владельца-образца")
    with open(stand, encoding="utf-8") as f:
        stand_text = f.read()
    got = owners(stand_text)
    if not any("site" in o for o in got):
        raise SystemExit("inventory стенда раскатки: у владельцев нет site (спека 005: стенд с site)")
    return [("пример inventory", owners(text), True), ("пример inventory с site", owners(uncommented), True),
            ("inventory стенда раскатки", got, True)]


def all_cases(site_cases, extra):
    cases = owner_cases() + extra
    for i, c in enumerate(site_cases):
        cases.append((f"site #{i}: {c['site'][:60]!r} ({c['why']})", [owner(site=c["site"])], c["ok"]))
    return cases


def mutate(role, name, edit):
    """Копия роли с испорченным правилом: (файл, функция правки текста)."""
    tmp = tempfile.mkdtemp(prefix="sa-owners-mut-")
    dst = os.path.join(tmp, "albums_svc")
    shutil.copytree(role, dst)
    p = os.path.join(dst, *name.split("/"))
    with open(p, encoding="utf-8") as f:
        text = f.read()
    new = edit(text)
    if new == text:
        raise SystemExit(f"самопроверка: правка {name} ничего не изменила — якорь ушёл из роли")
    with open(p, "w", encoding="utf-8") as f:
        f.write(new)
    return tmp, dst


# (описание, файл, правка, подстроки имён случаев): роль с дефектом обязана ошибиться хотя бы в одном из случаев,
# чьё имя содержит подстроку (весь набор на каждой копии — минуты: Ansible медленный, 0,15 с на случай).
MUTANTS = [
    ("«\\Z» заменён на «$» в правиле site (хвостовой перевод строки)", "vars/main.yml",
     lambda t: re.sub(r"(albums_svc_site_re: '.*?)\\Z'", r"\1$'", t, count=1), ("перевод строки",)),
    ("не отвергаются точечные сегменты пути", "tasks/owners.yml",
     lambda t: t.replace("and item.site is not regex(albums_svc_site_dot_re)", ""), ("сегмент",)),
    ("не отвергаются неизвестные ключи владельца", "tasks/owners.yml",
     lambda t: t.replace("      - item.keys() | list | difference(albums_svc_owner_keys) | length == 0\n", ""), ("ключ",)),
    ("токен с лишним ключом (token: …) проходит", "tasks/owners.yml",
     lambda t: t.replace("      - item.tokens | map('list') | list | flatten | unique | list | difference(albums_svc_token_keys) | length == 0\n", ""),
     ("токен в открытом виде", "лишний ключ токена")),
    ("name токена без предела 64", "tasks/owners.yml",
     lambda t: t.replace("      - item.tokens | map(attribute='name', default='') | map('length') | select('le', 64) | list | length == item.tokens | length\n", ""),
     ("name 65 знаков",)),
    ("name токена без запрета управляющих", "tasks/owners.yml",
     lambda t: t.replace("item.tokens | map(attribute='name', default='') | select('string') | select('regex', albums_svc_visible_re) | list",
                         "item.tokens | map(attribute='name', default='') | select('string') | list"), ("name с",)),
    ("квота без верхнего предела", "tasks/owners.yml",
     lambda t: t.replace("      - item.quota_bytes <= albums_svc_int64_max\n", ""), ("квота выше",)),
    ("число альбомов без верхнего предела", "tasks/owners.yml",
     lambda t: t.replace("      - item.max_albums <= albums_svc_int64_max\n", ""), ("число альбомов выше",)),
    ("студия без запрета управляющих", "tasks/owners.yml",
     lambda t: t.replace("      - item.studio is regex(albums_svc_visible_re)\n", ""), ("студия с",)),
]


def main():
    if len(sys.argv) != 5:
        print(__doc__)
        return 2
    role, cases_path, example, stand = sys.argv[1:]
    site_cases = load_cases(cases_path)
    v = role_vars(role)
    errs = check_python(v, site_cases)
    cases = all_cases(site_cases, inventory_cases(example, stand))
    errs += check_ansible(role, cases)
    for e in errs:
        print("FAIL", e)
    if errs:
        return 1
    print(f"owners_rule: роль согласна с сервисом на {len(site_cases)} случаях site и {len(cases) - len(site_cases)} случаях владельцев")
    # Самопроверка: испорченная роль обязана краснеть.
    for what, name, edit, picks in MUTANTS:
        subset = [c for c in cases if any(p in c[0] for p in picks)]
        if not subset:
            print(f"FAIL самопроверка: для дефекта «{what}» нет случаев с именем {picks}")
            return 1
        tmp, dst = mutate(role, name, edit)
        try:
            if not check_ansible(dst, subset):
                print(f"FAIL самопроверка: роль с дефектом «{what}» прошла все случаи")
                return 1
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
    print(f"owners_rule: самопроверка — {len(MUTANTS)} испорченных копий роли обнаружены")
    return 0


if __name__ == "__main__":
    sys.exit(main())
