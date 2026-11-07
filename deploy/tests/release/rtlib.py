"""Общее для тестов выпуска (design 004 §12, задача И6a): пути, копия дерева, запуск, синтетические OCI-архивы.

Только stdlib. Тестовые образы - синтетические однотонные данные (не настоящие фото и не боевые сборки).
Модули deploy/release грузятся по пути: под `python3 -I` каталог скрипта в sys.path не входит.
"""

import gzip
import hashlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

# `python3 -I` игнорирует PYTHONDONTWRITEBYTECODE, а байт-код рядом с загруженными модулями (deploy/ops, deploy/release) попал бы
# в копию дерева стенда и в просмотр кода: не пишем его.
sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
REL = os.path.join(ROOT, "deploy", "release")
SHIM = os.path.join(HERE, "skopeo_shim.py")
FAKE_GH = os.path.join(HERE, "fake_gh.py")
FAKE_PODMAN = os.path.join(HERE, "podman_shim.py")


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def copy_tree(dst):
    """Копия того, что читает workflow_guard: .github, deploy/release, requirements контроллера."""
    shutil.copytree(os.path.join(ROOT, ".github"), os.path.join(dst, ".github"))
    shutil.copytree(REL, os.path.join(dst, "deploy", "release"), ignore=shutil.ignore_patterns("__pycache__"))
    os.makedirs(os.path.join(dst, "deploy", "ansible"))
    shutil.copy(os.path.join(ROOT, "deploy", "ansible", "requirements.txt"), os.path.join(dst, "deploy", "ansible"))


def tree_paths(root, top=(".github", "deploy")):
    """Пути файлов под root так, как их дал бы ls-tree дерева коммита (для тестов; inventory и кэш байт-кода не берём)."""
    out = []
    for t in top:
        for dp, dirs, files in os.walk(os.path.join(root, t)):
            dirs[:] = [d for d in dirs if d not in ("inventory", "__pycache__")]
            out += [os.path.relpath(os.path.join(dp, n), root).replace(os.sep, "/") for n in files]
    return sorted(out)


def check(guard, root, pins=None, extra=(), drop=()):
    """guard.check_tree по дереву root с его же списком путей (extra - добавить, drop - убрать)."""
    paths = [p for p in tree_paths(root) if p not in drop] + list(extra)
    return guard.check_tree(root, pins, paths)


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def sub(text, old, new, count=1):
    """Подмена с проверкой: мутация обязана куда-то попасть, иначе тест ничего бы не проверял."""
    assert text.count(old) >= 1, f"мутация не нашла место: {old!r}"
    return text.replace(old, new, count)


def sh(args, env=None, cwd=None, stdin=None):
    """Запуск без оболочки; возвращает (код, stdout+stderr)."""
    e = dict(os.environ)
    e.update(env or {})
    p = subprocess.run(args, env=e, cwd=cwd, input=stdin, capture_output=True, text=True, timeout=120)
    return p.returncode, p.stdout + p.stderr


class Case(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="sa-rel-")
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def path(self, *parts):
        return os.path.join(self.tmp, *parts)


# ---- синтетические OCI-архивы (как после `podman push … oci-archive:`) ----

OCI_MANIFEST = "application/vnd.oci.image.manifest.v1+json"
OWN = {
    "org.opencontainers.image.source": "https://github.com/IliaSA101/sosninaalbums-cloud",
    "org.opencontainers.image.licenses": "AGPL-3.0-only",
    "org.opencontainers.image.title": "albums-svc",
    "org.opencontainers.image.version": "1.2.3",
    "org.opencontainers.image.revision": "0123456789abcdef0123456789abcdef01234567",
}
BASE_ENV = ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"]


def sha(b):
    return hashlib.sha256(b).hexdigest()


def layer(files):
    """[(имя, байты, режим, uid, gid)] -> (gz-байты, diff_id)."""
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w") as tf:
        for name, data, mode, uid, gid in files:
            ti = tarfile.TarInfo(name)
            ti.size, ti.mode, ti.uid, ti.gid, ti.mtime = len(data), mode, uid, gid, 0
            tf.addfile(ti, io.BytesIO(data))
    tar = raw.getvalue()
    return gzip.compress(tar, mtime=0), "sha256:" + sha(tar)


def build_archive(path, **kw):
    """Пишет архив; kw - отклонения от эталона. Возвращает (дайджест манифеста, путь base-inspect.json)."""
    base_n = kw.get("base_layers", 2)
    base = [layer([(f"etc/base-{i}", b"base", 0o644, 0, 0)]) for i in range(base_n)]
    top = layer(kw.get("top_files", [("albums-svc", b"\x7fELF-synthetic", 0o555, 0, 0)]))
    base_ids = [d for _, d in base]
    ids = list(base_ids)
    if "first_diff" in kw:
        ids[0] = kw["first_diff"]
    ids.append(top[1])
    labels = dict(OWN)
    labels.update(kw.get("labels", {}))
    for k in kw.get("drop_labels", ()):
        labels.pop(k, None)
    cfg = {
        "architecture": "amd64",
        "os": "linux",
        "config": {
            "User": kw.get("user", "65532:65532"),
            "Env": kw.get("env", BASE_ENV),
            "Entrypoint": kw.get("entrypoint", ["/albums-svc"]),
            "Cmd": kw.get("cmd", ["-config", "/etc/albums-svc/config.json"]),
            "Labels": labels,
        },
        "rootfs": {"type": "layers", "diff_ids": ids},
        "history": [{"created_by": "base"}] * base_n + [{"created_by": kw.get("history", "COPY /out/albums-svc /albums-svc")}],
    }
    cfg_b = json.dumps(cfg).encode()
    blobs = {sha(cfg_b): cfg_b}
    layers = []
    for gz, _ in base + [top]:
        blobs[sha(gz)] = gz
        layers.append({"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + sha(gz), "size": len(gz)})
    man = {
        "schemaVersion": 2,
        "mediaType": OCI_MANIFEST,
        "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + sha(cfg_b), "size": len(cfg_b)},
        "layers": layers,
    }
    man_b = json.dumps(man).encode()
    blobs[sha(man_b)] = man_b
    entries = [{"mediaType": OCI_MANIFEST, "digest": "sha256:" + sha(man_b), "size": len(man_b)}]
    if kw.get("two_manifests"):
        entries.append(dict(entries[0]))
    with tarfile.open(path, "w") as tf:
        for name, data in [("oci-layout", b'{"imageLayoutVersion":"1.0.0"}'), ("index.json", json.dumps({"schemaVersion": 2, "manifests": entries}).encode())]:
            ti = tarfile.TarInfo(name)
            ti.size = len(data)
            tf.addfile(ti, io.BytesIO(data))
        for h, data in blobs.items():
            if kw.get("corrupt_top") and h == sha(top[0]):
                data = data + b"x"
            ti = tarfile.TarInfo(f"blobs/sha256/{h}")
            ti.size = len(data)
            tf.addfile(ti, io.BytesIO(data))
    insp = kw.get("inspect")  # вывод podman image inspect базы - только если просят (не в каталог артефакта)
    if insp:
        with open(insp, "w", encoding="utf-8") as f:
            json.dump([{"RootFS": {"Layers": base_ids}, "Config": {"Env": BASE_ENV, "Labels": {}}}], f)
    return "sha256:" + sha(man_b), insp


def shim_env(state, **extra):
    """Окружение с поддельным skopeo в PATH (каталог shim создаётся рядом с состоянием)."""
    bindir = os.path.join(state, "bin")
    os.makedirs(bindir, exist_ok=True)
    for name, src in (("skopeo", SHIM), ("gh", FAKE_GH), ("podman", FAKE_PODMAN)):
        dst = os.path.join(bindir, name)
        if not os.path.exists(dst):
            shutil.copy(src, dst)
            os.chmod(dst, 0o755)
    env = {"PATH": bindir + os.pathsep + os.environ["PATH"], "SHIM_STATE": state, "SA_REL_DIR": REL, "PYTHONDONTWRITEBYTECODE": "1"}
    env.update(extra)
    return env


def main():
    unittest.main(argv=[sys.argv[0], "-v"] if "-v" not in sys.argv else sys.argv)
