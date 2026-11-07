"""Содержательные правила workflow выпуска (design 004 §2.б, развилки 004-20, 004-25..004-34).

Подгружается из workflow_guard.py по пути (под `python3 -I` каталог скрипта не в sys.path). Работает по дереву,
которое уже сверено двумя разборами; ошибки копятся в списке errs, исключений наружу не бросает
(неожиданная форма узла ловится в workflow_guard.check_tree и тоже становится отказом).

Критичные задания (release-gate, release-publish, release-attest, build-image, runner-check) закреплены ЦЕЛИКОМ:
список шагов, их ключи, env и with — ровно по SPECS (только `name` свободен). Всё, что исполняет код или
меняет поведение шага (env, BASH_ENV, SHELLOPTS, PS4, NODE_OPTIONS, GH_HOST, LD_PRELOAD, PATH …), поэтому не
пройдёт ни в одном из них; остальные шаги — только из короткого перечня env.
"""

import hashlib
import re

REPO_IMAGE = "ghcr.io/iliasa101/albums-svc"
ALLOWED_FILES = ("checks.yml", "ci.yml", "release.yml")
PINNED_ACTIONS = ("checkout", "setup-go", "setup-node", "upload-artifact", "download-artifact", "attest")
ARTIFACT_MIN = (4, 1, 3)  # CVE-2024-42471: выход за каталог при распаковке (download-artifact)
TAG_PATTERN = "v[0-9]+.[0-9]+.[0-9]+"
# sha256 текста run шага сверки раннеров (design §6.1): правка проверки видна дважды - в workflow и здесь.
# Менять вместе с шагом (например, после уточнения группы раннеров на ci/*).
RUNNER_STEP_SHA = "6ab99e5f2e7ccf657b204e9a0cff6a174a562fb566f0770384ec9854ebdbb69d"
# env некритичных шагов (check, stand-full, probe): только это. У шагов uses: env нет вовсе.
ENV_FREE = ("GITLEAKS_VERSION", "GITLEAKS_SHA256", "SA_IMAGE_ARCHIVE", "SA_IMAGE_DIGEST")

WF_KEYS = {"name", "on", "permissions", "concurrency", "jobs"}
ON_KEYS = {"push", "workflow_dispatch", "workflow_call"}
JOB_KEYS = {"name", "needs", "permissions", "timeout-minutes", "runs-on", "environment", "outputs", "steps", "uses", "with"}
STEP_KEYS = {"name", "id", "uses", "with", "env", "run", "if"}
PERM_KEYS = {"contents", "packages", "id-token", "attestations", "actions"}
WITH_KEYS = {
    "checkout": {"persist-credentials", "fetch-depth", "sparse-checkout", "path"},
    "setup-go": {"go-version-file", "cache"},
    "setup-node": {"node-version", "package-manager-cache"},
    "upload-artifact": {"name", "path", "if-no-files-found", "retention-days"},
    "download-artifact": {"name", "path"},
    "attest": {"subject-name", "subject-digest", "push-to-registry"},
}
# Права и needs заданий release.yml - ровно (design §1, §2.б.3).
RELEASE_JOBS = {
    "release-gate": ({"contents": "read"}, []),
    "checks": ({"contents": "read"}, []),
    "release-publish": ({"contents": "read", "packages": "write"}, ["release-gate", "checks"]),
    "release-attest": (
        {"contents": "read", "actions": "read", "id-token": "write", "attestations": "write"},
        ["release-gate", "checks", "release-publish"],
    ),
}
USES_STEP = re.compile(r"^actions/([a-z][a-z-]*)@([0-9a-f]{40})$")
CO, UP, DL, AT = "actions/checkout", "actions/upload-artifact", "actions/download-artifact", "actions/attest"
TMP = "${{ runner.temp }}"
DIGEST = "${{ needs.checks.outputs.digest }}"


def runner_step(required):
    return {"env": {"GH_TOKEN": "${{ github.token }}", "SA_REQUIRED_JOBS": required}, "run": "sha256:" + RUNNER_STEP_SHA}


# Шаги критичных заданий ровно (без name). uses - имя действия без sha (sha проверяет lock), run сверки - по sha256.
SPECS = {
    ("release.yml", "release-gate"): [
        {"uses": CO, "with": {"fetch-depth": "0", "persist-credentials": "false"}},
        {"run": "bash deploy/release/gate.sh"},
    ],
    ("release.yml", "release-publish"): [
        {"uses": DL, "with": {"name": "albums-svc-oci", "path": f"{TMP}/release-oci"}},
        {"uses": CO, "with": {"sparse-checkout": "deploy/release", "path": "src", "persist-credentials": "false"}},
        {"env": {"TOKEN": "${{ secrets.GITHUB_TOKEN }}"}, "run": "bash src/deploy/release/registry.sh login"},
        {
            "env": {
                "SA_AUTHFILE": f"{TMP}/auth.json",
                "SA_ARCHIVE": f"{TMP}/release-oci/albums-svc.oci.tar",
                "SA_DIGEST": DIGEST,
                "SA_OUT_DIR": f"{TMP}/release-manifest",
                "SA_REPO": REPO_IMAGE,
            },
            "run": 'bash src/deploy/release/publish.sh "$SA_ARCHIVE" "$SA_DIGEST" "$SA_REPO" "$GITHUB_REF_NAME"',
        },
        {"if": "always()", "run": "bash src/deploy/release/registry.sh logout"},
        {"uses": UP, "with": {"name": "release-manifest", "path": f"{TMP}/release-manifest/release.manifest.json", "if-no-files-found": "error"}},
    ],
    ("release.yml", "release-attest"): [
        runner_step("checks / build-image;checks / stand-full;release-publish"),
        {"uses": AT, "id": "attest", "with": {"subject-name": REPO_IMAGE, "subject-digest": DIGEST, "push-to-registry": "false"}},
        {"uses": UP, "with": {"name": "release-attestation", "path": "${{ steps.attest.outputs.bundle-path }}", "if-no-files-found": "error"}},
    ],
    ("checks.yml", "build-image"): [
        {"uses": CO, "with": {"persist-credentials": "false"}},
        {
            "id": "build",
            "env": {"SA_VERSION_REF": "${{ inputs.version }}", "SA_ARCHIVE": f"{TMP}/oci/albums-svc.oci.tar"},
            "run": 'bash deploy/release/build.sh "$SA_ARCHIVE"',
        },
        {"uses": UP, "with": {"name": "albums-svc-oci", "path": f"{TMP}/oci/albums-svc.oci.tar", "if-no-files-found": "error", "retention-days": "7"}},
    ],
    ("ci.yml", "runner-check"): [runner_step("checks / build-image;checks / stand-full")],
}
# Ключи уровня задания у критичных заданий - ровно эти (без name и environment: до запасного пути токена, 004-34;
# name задания меняет имена в ответе API, по которым сверка раннеров ищет обязательные). timeout-minutes - точное значение.
_BASE = {"runs-on", "timeout-minutes", "permissions", "needs", "steps"}
JOB_SPECS = {
    ("release.yml", "release-gate"): (_BASE, "10"),
    ("release.yml", "checks"): ({"permissions", "uses", "with"}, None),
    ("release.yml", "release-publish"): (_BASE, "20"),
    ("release.yml", "release-attest"): (_BASE, "10"),
    ("checks.yml", "build-image"): (_BASE | {"outputs"}, "30"),
    ("ci.yml", "runner-check"): (_BASE, "10"),
}
CRITICAL_RUN_ASCII = {"release-gate", "build-image", "release-publish", "release-attest", "runner-check"}


def plain(v):
    """Scalar → str (комментарии не участвуют в сверке)."""
    if isinstance(v, dict):
        return {k: plain(x) for k, x in v.items()}
    if isinstance(v, list):
        return [plain(x) for x in v]
    return None if v is None else str(v)


def as_list(v):
    return [] if v is None else [str(x) for x in v] if isinstance(v, list) else [str(v)]


def dict_of(v, where, errs):
    """Узел, который обязан быть словарём; иначе отказ (а не исключение)."""
    if v is None:
        return {}
    if not isinstance(v, dict):
        errs.append(f"{where}: ожидался словарь, а там скаляр или список")
        return {}
    return v


def list_of(v, where, errs):
    if v is None:
        return []
    if not isinstance(v, list):
        errs.append(f"{where}: ожидался список")
        return []
    return v


def unknown(d, allowed, where, errs):
    for k in d:
        if k not in allowed:
            errs.append(f"{where}: ключ «{k}» вне перечня")


def ascii_walk(node, where, errs, skip_run=False):
    """Всё, кроме значений name (и run вне критичных заданий), - только ASCII (design §2.б.5, 004-23)."""
    if isinstance(node, dict):
        for k, v in node.items():
            if k == "name" or (k == "run" and skip_run):
                continue
            ascii_walk(v, f"{where}.{k}", errs, skip_run)
    elif isinstance(node, list):
        for i, v in enumerate(node):
            ascii_walk(v, f"{where}[{i}]", errs, skip_run)
    elif isinstance(node, str) and not node.isascii():
        errs.append(f"{where}: не-ASCII символ вне имени")


def tag_tuple(tag):
    m = re.match(r"^v(\d+)\.(\d+)\.(\d+)", tag)
    return tuple(int(x) for x in m.groups()) if m else None


def check_uses(uses, where, lock, errs, job_level):
    u = str(uses)
    if job_level:
        if u != "./.github/workflows/checks.yml":
            errs.append(f"{where}: uses задания - только ./.github/workflows/checks.yml")
        return None
    m = USES_STEP.match(u)
    if not m or m.group(1) not in PINNED_ACTIONS:
        errs.append(f"{where}: uses «{u}» - не actions/<имя>@<40 hex> из набора")
        return None
    name, sha = m.groups()
    if re.search(r"0{8}", sha):
        errs.append(f"{where}: SHA действия похож на заготовку (хвост из нулей) - нужен настоящий коммит метки")
    tag = lock.get((name, sha))
    comment = getattr(uses, "comment", "").strip()
    if tag is None:
        errs.append(f"{where}: actions/{name}@{sha[:12]}… нет в deploy/release/actions.lock")
    elif comment != tag:
        errs.append(f"{where}: тег в комментарии «{comment}» ≠ «{tag}» из lock")
    elif name == "download-artifact" and (tag_tuple(tag) is None or tag_tuple(tag) < ARTIFACT_MIN):
        errs.append(f"{where}: download-artifact {tag} старше v4.1.3 (CVE-2024-42471) или тег не разобран")
    return name


def shape(step):
    """Вид шага для сверки со SPECS: без name, uses - без sha, run сверки раннеров - по sha256."""
    out = {}
    for k, v in step.items():
        if k == "name":
            continue
        if k == "uses":
            v = str(v).split("@")[0]
        elif k == "run" and "gh api" in str(v):
            v = "sha256:" + hashlib.sha256(str(v).encode()).hexdigest()
        out[k] = plain(v)
    return out


def check_step(step, where, lock, errs, fname, jid):
    if not isinstance(step, dict):
        errs.append(f"{where}: шаг - не словарь")
        return
    unknown(step, STEP_KEYS, where, errs)
    name = check_uses(step["uses"], where, lock, errs, False) if "uses" in step else None
    w = dict_of(step.get("with"), f"{where}.with", errs)
    env = dict_of(step.get("env"), f"{where}.env", errs)
    if name:
        unknown(w, WITH_KEYS[name], f"{where}.with", errs)
        if env:
            errs.append(f"{where}: у шага uses: переменных окружения нет")
        if name == "setup-go" and w.get("cache") != "false":
            errs.append(f"{where}: у setup-go нужен cache: false")
        if name == "setup-node" and w.get("package-manager-cache") != "false":
            errs.append(f"{where}: у setup-node нужен package-manager-cache: false")
        if name == "checkout" and w.get("persist-credentials") != "false":
            errs.append(f"{where}: checkout без persist-credentials: false")
        if name == "download-artifact" and not str(w.get("path", "")).startswith(TMP + "/"):
            errs.append(f"{where}: download-artifact - path только под ${{{{ runner.temp }}}}/")
        if name == "upload-artifact":
            if w.get("if-no-files-found") != "error":
                errs.append(f"{where}: upload-artifact без if-no-files-found: error")
            p = re.sub(r"\$\{\{ [^{}]* \}\}", "X", str(w.get("path", "")))
            if not p or any(c in p for c in "*?[\n "):
                errs.append(f"{where}: upload-artifact - path ровно один файл, без шаблонов")
    if (fname, jid) not in SPECS:
        # Некритичные шаги: env из короткого перечня, без if (в критичных решает SPECS).
        for k in env:
            if k not in ENV_FREE:
                errs.append(f"{where}: переменная окружения «{k}» вне перечня {list(ENV_FREE)}")
        if "if" in step:
            errs.append(f"{where}: if: допустим один раз - выход из реестра в release-publish")


def check_job(jid, job, fname, lock, errs):
    """Общие правила задания; возвращает список шагов."""
    where = f"{fname}:{jid}"
    if not isinstance(job, dict):
        errs.append(f"{where}: задание - не словарь")
        return []
    unknown(job, JOB_KEYS, where, errs)
    js = JOB_SPECS.get((fname, jid))
    if js is not None:
        for k in job:
            if k in JOB_KEYS and k not in js[0]:
                errs.append(f"{where}: ключ задания «{k}» вне эталона guard (критичное задание закреплено целиком)")
        if js[1] is not None and str(job.get("timeout-minutes")) != js[1]:
            errs.append(f"{where}: timeout-minutes должен быть {js[1]} (эталон guard)")
    for k, v in dict_of(job.get("permissions"), f"{where}.permissions", errs).items():
        if k not in PERM_KEYS or v not in ("read", "write"):
            errs.append(f"{where}: право {k}: {v} вне перечня")
    if "uses" in job:
        check_uses(job["uses"], where, lock, errs, True)
        return []
    if job.get("runs-on") != "ubuntu-24.04":
        errs.append(f"{where}: runs-on - ровно ubuntu-24.04")
    if "timeout-minutes" not in job:
        errs.append(f"{where}: нет timeout-minutes")
    if "environment" in job:
        errs.append(f"{where}: environment не допускается (запасной путь токена - отдельным выпуском с правкой guard, 004-34)")
    steps = list_of(job.get("steps"), f"{where}.steps", errs)
    for i, st in enumerate(steps):
        check_step(st, f"{where}.steps[{i}]", lock, errs, fname, jid)
    spec = SPECS.get((fname, jid))
    if spec is not None:
        if len(steps) != len(spec):
            errs.append(f"{where}: шагов {len(steps)}, ожидалось ровно {len(spec)} (задание закреплено целиком)")
        for i, (st, want) in enumerate(zip(steps, spec)):
            got = shape(st) if isinstance(st, dict) else None
            if got != want:
                keys = sorted(set(got or {}) ^ set(want) | {k for k in want if got and got.get(k) != want[k]})
                errs.append(f"{where}.steps[{i}]: шаг не равен эталону guard (расходятся: {', '.join(keys) or '?'})")
    ascii_walk({k: v for k, v in job.items() if k != "steps"}, where, errs)
    for i, st in enumerate(steps):
        ascii_walk(st, f"{where}.steps[{i}]", errs, skip_run=jid not in CRITICAL_RUN_ASCII)
    return steps


def check_release(wf, lock, errs):
    f = "release.yml"
    if wf.get("permissions") != {}:
        errs.append(f"{f}: наверху нужен permissions: {{}}")
    if plain(wf.get("on")) != {"push": {"tags": [TAG_PATTERN]}}:
        errs.append(f"{f}: on - только push.tags: [{TAG_PATTERN}]")
    if plain(wf.get("concurrency")) != {"group": "release-${{ github.ref }}", "cancel-in-progress": "false"}:
        errs.append(f"{f}: concurrency - group release-${{{{ github.ref }}}}, cancel-in-progress: false")
    jobs = dict_of(wf.get("jobs"), f"{f}: jobs", errs)
    if sorted(jobs) != sorted(RELEASE_JOBS):
        errs.append(f"{f}: задания {sorted(jobs)} ≠ {sorted(RELEASE_JOBS)}")
    for jid, (perms, needs) in RELEASE_JOBS.items():
        job = jobs.get(jid)
        if job is None:
            continue
        where = f"{f}:{jid}"
        check_job(jid, job, f, lock, errs)
        if isinstance(job, dict):
            if plain(job.get("permissions")) != perms:
                errs.append(f"{where}: права должны быть ровно {perms}")
            if sorted(as_list(job.get("needs"))) != sorted(needs):
                errs.append(f"{where}: needs должен быть {needs}")
            if "if" in job:
                errs.append(f"{where}: if: у задания выпуска нет (design §2.б.4)")
            if jid == "checks" and plain(job.get("with")) != {"version": "${{ github.ref }}"}:
                errs.append(f"{where}: with - только version: ${{{{ github.ref }}}}")


def check_checks(wf, lock, errs):
    f = "checks.yml"
    on = dict_of(wf.get("on"), f"{f}: on", errs)
    if list(on) != ["workflow_call"]:
        errs.append(f"{f}: on - только workflow_call")
    wc = dict_of(on.get("workflow_call"), f"{f}: workflow_call", errs)
    out = plain(dict_of(dict_of(wc.get("outputs"), f"{f}: outputs", errs).get("digest"), f"{f}: outputs.digest", errs)).get("value")
    if out != "${{ jobs.build-image.outputs.digest }}":
        errs.append(f"{f}: outputs.digest.value - ровно ${{{{ jobs.build-image.outputs.digest }}}}")
    jobs = dict_of(wf.get("jobs"), f"{f}: jobs", errs)
    if sorted(jobs) != ["build-image", "check", "stand-full"]:
        errs.append(f"{f}: задания {sorted(jobs)} ≠ build-image, check, stand-full")
    for jid, job in jobs.items():
        steps = check_job(jid, job, f, lock, errs)
        if not isinstance(job, dict):
            continue
        if plain(job.get("permissions")) not in ({"contents": "read"}, None):
            errs.append(f"{f}:{jid}: права - только contents: read")
        if jid == "build-image" and plain(job.get("outputs")) != {"digest": "${{ steps.build.outputs.digest }}"}:
            errs.append(f"{f}:{jid}: outputs.digest - ровно из шага build")
        if as_list(job.get("needs")) != (["build-image"] if jid == "stand-full" else []):
            errs.append(f"{f}:{jid}: needs не по design §1")
        for i, st in enumerate(steps):
            if isinstance(st, dict) and str(st.get("uses", "")).startswith(UP + "@") and jid != "build-image":
                errs.append(f"{f}:{jid}.steps[{i}]: загрузка артефактов - только в build-image (имена release-* заняты выпуском)")


def check_ci(wf, lock, errs):
    f = "ci.yml"
    on = dict_of(wf.get("on"), f"{f}: on", errs)
    if sorted(on) != ["push", "workflow_dispatch"] or plain(on.get("push")) != {"branches": ["ci/**"]}:
        errs.append(f"{f}: on - push в ci/** и workflow_dispatch")
    jobs = dict_of(wf.get("jobs"), f"{f}: jobs", errs)
    if sorted(jobs) != ["checks", "probe", "runner-check"]:
        errs.append(f"{f}: задания {sorted(jobs)} ≠ checks, probe, runner-check")
    for jid, job in jobs.items():
        steps = check_job(jid, job, f, lock, errs)
        if not isinstance(job, dict):
            continue
        if jid == "checks" and plain(job.get("with")) != {"version": "ci"}:
            errs.append(f"{f}:{jid}: with - только version: ci")
        if jid == "runner-check":
            if plain(job.get("permissions")) != {"contents": "read", "actions": "read"}:
                errs.append(f"{f}:{jid}: права - contents: read, actions: read")
        elif job.get("permissions") is not None:
            errs.append(f"{f}:{jid}: права задания - только у runner-check")
        for i, st in enumerate(steps):
            if isinstance(st, dict) and str(st.get("uses", "")).startswith(UP + "@"):
                errs.append(f"{f}:{jid}.steps[{i}]: загрузка артефактов вне checks.yml")


def secrets_scan(trees, errs):
    """secrets.* - только в задании release-publish файла release.yml (там шаг закреплён SPECS)."""

    def walk(node, fname, jid, path):
        if isinstance(node, dict):
            for k, v in node.items():
                walk(v, fname, jid, f"{path}.{k}")
        elif isinstance(node, list):
            for i, v in enumerate(node):
                walk(v, fname, jid, f"{path}[{i}]")
        elif isinstance(node, str) and "secrets." in node and not (fname == "release.yml" and jid == "release-publish"):
            errs.append(f"{fname}: {path}: secrets.* допустимы только в release-publish (release.yml)")

    for fname, wf in trees.items():
        for k, v in wf.items():
            if k == "jobs" and isinstance(v, dict):
                for jid, job in v.items():
                    walk(job, fname, jid, f"jobs.{jid}")
            else:
                walk(v, fname, None, k)


def check_permissions_everywhere(trees, errs):
    """Вне release.yml нет ни одного права write; permissions - всегда словарь."""
    for fname, wf in trees.items():
        blocks = [wf.get("permissions")] + [(j or {}).get("permissions") if isinstance(j, dict) else None for j in dict_of(wf.get("jobs"), fname, []).values()]
        for b in blocks:
            if b is not None and not isinstance(b, dict):
                errs.append(f"{fname}: permissions - не словарь (write-all и подобное недопустимы)")
            elif b and fname != "release.yml" and "write" in b.values():
                errs.append(f"{fname}: право write вне release.yml")


def check_all(trees, lock, errs):
    for n, t in trees.items():
        unknown(t, WF_KEYS, n, errs)
        unknown(dict_of(t.get("on"), f"{n}: on", errs), ON_KEYS, f"{n}: on", errs)
        perms = t.get("permissions")
        if isinstance(perms, dict):
            for k, v in perms.items():
                if k not in PERM_KEYS or v not in ("read", "write"):
                    errs.append(f"{n}: право {k}: {v}")
            if "contents" not in perms and n != "release.yml":
                errs.append(f"{n}: наверху нужен permissions: contents: read")
        elif n != "release.yml" or perms is not None:
            errs.append(f"{n}: наверху нужен словарь permissions")
    check_permissions_everywhere(trees, errs)
    secrets_scan(trees, errs)
    check_release(trees["release.yml"], lock, errs)
    check_checks(trees["checks.yml"], lock, errs)
    check_ci(trees["ci.yml"], lock, errs)
