"""owners_first_static (спека 005, решение 005-35): проверка владельцев идёт первой в site.yml и в bootstrap.yml, до всех ролей.

Запуск (в образе проверок, из owners_static.sh):
    python3 -I owners_first_static.py <каталог deploy/ansible>

Зачем. Раньше tasks/owners.yml вызывался из assert.yml роли albums_svc, а bootstrap, nftables и остальные роли до
неё успевали примениться: опечатка во владельцах оставляла сервер частично обновлённым. Теперь это pre_tasks плея.
  1. Разбор site.yml: в первом плее есть pre_tasks, первая задача — include_role albums_svc / tasks_from: owners.yml
     с тегом always (проверка идёт и при --tags), роли идут после, bootstrap — первая.
  2. Настоящий Ansible: site.yml против localhost (соединение local, --check, без become) с владельцем «P1» (слаг с
     заглавной) обязан упасть на задаче владельцев, а в выводе не должно быть ни одной задачи роли bootstrap.
     Парный прогон с годным владельцем обязан дойти до первой задачи bootstrap: без него отсутствие bootstrap в
     выводе ничего бы не доказывало (плей мог упасть раньше по другой причине).
  3. Самопроверка: копия site.yml без pre_tasks с тем же плохим владельцем обязана дойти до bootstrap.
То же для bootstrap.yml (первый запуск и --replace-server идут через него до site.yml): первый плей, те же пункты 1-3.
Первая задача bootstrap — assert без изменений; --check страхует остальное. Значения владельцев в вывод не попадают
(no_log), в выводе ищутся только заголовки задач.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

import yaml

HASH1 = "0" * 63 + "1"
GOOD = {"slug": "p1", "studio": "Студия Пример", "quota_bytes": 1, "max_albums": 1,
        "tokens": [{"name": "n", "sha256": HASH1}]}
BAD = dict(GOOD, slug="P1")  # слаг с заглавной: сервис и роль отвергают
OWNERS_TASK = "Владельцы — ключи, слаг, лимиты"
FIRST_ROLE_RE = re.compile(r"^TASK \[bootstrap : ", re.M)
PLAYS = ("site.yml", "bootstrap.yml")


def static_check(play_dir, name):
    """Разбор плейбука: pre_tasks с проверкой владельцев — первое в первом плее; роли после, bootstrap первая."""
    errs = []
    with open(os.path.join(play_dir, name), encoding="utf-8") as f:
        plays = yaml.safe_load(f)
    play = plays[0]
    pre = play.get("pre_tasks") or []
    if not pre:
        return [f"{name}: у первого плея нет pre_tasks с проверкой владельцев"]
    first = pre[0]
    inc = first.get("ansible.builtin.include_role") or {}
    if inc.get("name") != "albums_svc" or inc.get("tasks_from") != "owners.yml":
        errs.append(f"{name}: первая задача pre_tasks — не include_role albums_svc / tasks_from: owners.yml")
    if "always" not in (first.get("tags") or []):
        errs.append(f"{name}: у проверки владельцев нет тега always (--tags пропустил бы её)")
    roles = [r.get("role") if isinstance(r, dict) else r for r in play.get("roles") or []]
    # В site.yml роль albums_svc нужна (проверка внутри неё остаётся вторым рубежом); в bootstrap.yml её нет.
    if not roles or roles[0] != "bootstrap" or (name == "site.yml" and "albums_svc" not in roles):
        errs.append(f"{name}: роли плея неожиданные: {roles}")
    if play.get("tasks"):
        errs.append(f"{name}: у первого плея есть tasks — порядок «pre_tasks, роли, tasks» нарушать нельзя")
    return errs


def run_site(play_dir, owner, workdir, name):
    """Плейбук name на localhost: (код выхода, вывод). Без become, --check; ssh и ключей не нужно."""
    vars_path = os.path.join(workdir, "vars.json")
    with open(vars_path, "w", encoding="utf-8") as f:
        json.dump({"admin_user": "adm", "bootstrap_user": "img", "sa_ssh_args": ["-o", "X=y"], "vault_admin_password": "x", "ansible_become": False,
                   "ansible_python_interpreter": "auto_silent", "albums_domain": "example.org", "albums_owners": [owner]}, f)
    env = dict(os.environ, ANSIBLE_NOCOLOR="1", ANSIBLE_FORCE_COLOR="0", ANSIBLE_LOCALHOST_WARNING="false",
               ANSIBLE_CONFIG=os.path.join(play_dir, "ansible.cfg"))
    r = subprocess.run(["ansible-playbook", "--check", "-i", "localhost,", "-c", "local", name, "-e", "@" + vars_path],
                       cwd=play_dir, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300,
                       check=False)
    return r.returncode, r.stdout


def case(play_dir, workdir, name):
    """(упала ли проверка владельцев на плохом, дошёл ли плей до bootstrap с плохим и с годным)."""
    rc_bad, out_bad = run_site(play_dir, BAD, workdir, name)
    rc_good, out_good = run_site(play_dir, GOOD, workdir, name)
    return {
        "bad_rc": rc_bad,
        "bad_owners_failed": f"TASK [albums_svc : {OWNERS_TASK}]" in out_bad,
        "bad_reached_bootstrap": FIRST_ROLE_RE.search(out_bad) is not None,
        "good_reached_bootstrap": FIRST_ROLE_RE.search(out_good) is not None,
        "good_owners_ran": f"TASK [albums_svc : {OWNERS_TASK}]" in out_good,
        "tail": out_bad[-600:],
    }


def check_play(play_dir, name, tmp):
    """Все проверки одного плейбука и самопроверка на его копии без pre_tasks; 0 — годно."""
    errs = static_check(play_dir, name)
    c = case(play_dir, tmp, name)
    if c["bad_rc"] == 0:
        errs.append(f"{name}: плохой владелец: плейбук не упал")
    if not c["bad_owners_failed"]:
        errs.append(f"{name}: плохой владелец: нет задачи владельцев в выводе\n" + c["tail"])
    if c["bad_reached_bootstrap"]:
        errs.append(f"{name}: плохой владелец: плей дошёл до роли bootstrap — проверка стоит не первой")
    if not (c["good_owners_ran"] and c["good_reached_bootstrap"]):
        errs.append(f"{name}: годный владелец: плей не дошёл до bootstrap после проверки (прогон не годится как доказательство)")
    for e in errs:
        print("FAIL", e)
    if errs:
        return 1
    print(f"owners_first: {name}: проверка владельцев — первая; плохой владелец останавливает плей до bootstrap")
    # Самопроверка: без pre_tasks плохой владелец обязан дойти до bootstrap, иначе проверка выше ничего не доказывала.
    mut = os.path.join(tmp, "ansible-" + name)
    shutil.copytree(play_dir, mut)
    path = os.path.join(mut, name)
    with open(path, encoding="utf-8") as f:
        text = f.read()
    new = re.sub(r"(?ms)^  pre_tasks:\n.*?^  roles:\n", "  roles:\n", text, count=1)
    if new == text:
        print(f"FAIL самопроверка: правка {name} ничего не изменила — якорь ушёл")
        return 1
    with open(path, "w", encoding="utf-8") as f:
        f.write(new)
    if static_check(mut, name) == []:
        print(f"FAIL самопроверка: разбор {name} без pre_tasks прошёл")
        return 1
    if not case(mut, tmp, name)["bad_reached_bootstrap"]:
        print(f"FAIL самопроверка: {name} без pre_tasks не дошёл до bootstrap с плохим владельцем")
        return 1
    print(f"owners_first: самопроверка — {name} без pre_tasks обнаружен")
    return 0


def main():
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    play_dir = os.path.abspath(sys.argv[1])
    rc = 0
    with tempfile.TemporaryDirectory(prefix="sa-owners-first-") as tmp:
        for name in PLAYS:
            rc |= check_play(play_dir, name, tmp)
    return rc


if __name__ == "__main__":
    sys.exit(main())
