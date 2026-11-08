#!/usr/bin/env python3
"""Эталон состава образа albums-svc (design 004 §3). Только stdlib.

    image_check.py <архив> --containerfile <файл> --base-inspect <json> --version <V> --revision <R>

<json> — вывод `podman image inspect <база итоговой стадии по дайджесту>` (слои и Env базы). Проверка стоит
между сборкой и публикацией: в образ попадает ровно база плюс один слой с одним файлом. Отказ — код 1 и все
расхождения списком на stderr. Эталон сверяет и то, что мог подложить стенд или раннер: чужой слой, лишний
файл, чужие переменные и метки, пути машины сборки в истории.
"""

import gzip
import importlib.util
import io
import json
import os
import re
import sys
import tarfile

# Под -I каталог скрипта не в sys.path: соседний модуль грузим по пути.
_spec = importlib.util.spec_from_file_location("oci_archive", os.path.join(os.path.dirname(os.path.abspath(__file__)), "oci_archive.py"))
oci = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(oci)

SOURCE = "https://github.com/IliaSA101/sosninaalbums-cloud"
LICENSE = "AGPL-3.0-only"
TITLE = "albums-svc"
OWN_LABELS = ("org.opencontainers.image.source", "org.opencontainers.image.licenses", "org.opencontainers.image.title",
              "org.opencontainers.image.version", "org.opencontainers.image.revision")
EXTRA_LABELS = ("io.buildah.version",)
# Пути и имена машины сборки, которым нет места в истории слоёв.
LEAKS = ("/home/", "/runner", "RUNNER_", "/tmp/", "sa-tgt", ".gotmp", "/work/")
MAX_LAYER = 128 * 1024 * 1024


def final_stage(path):
    """USER / ENTRYPOINT / CMD итоговой стадии Containerfile."""
    out = {}
    for line in open(path, encoding="utf-8"):
        s = line.strip()
        if s.startswith("FROM "):
            out = {}
        for key in ("USER", "ENTRYPOINT", "CMD"):
            if s.startswith(key + " "):
                v = s[len(key) + 1:]
                out[key] = json.loads(v) if v.startswith("[") else v
    return out


def check_layer(data, errs):
    """Верхний слой: один обычный файл albums-svc, 0555, 0:0."""
    try:
        raw = gzip.GzipFile(fileobj=io.BytesIO(data)).read(MAX_LAYER + 1)
        if len(raw) > MAX_LAYER:
            errs.append("верхний слой больше предела")
            return
        members = tarfile.open(fileobj=io.BytesIO(raw), mode="r:").getmembers()
    except (OSError, tarfile.TarError, EOFError) as e:
        errs.append(f"верхний слой не читается: {e}")
        return
    names = [m.name.removeprefix("./") for m in members]
    if names != ["albums-svc"]:
        errs.append(f"верхний слой: ожидался один файл albums-svc, а там {names}")
        return
    m = members[0]
    if not m.isreg() or m.islnk() or m.issym():
        errs.append("albums-svc в слое — не обычный файл")
    if (m.mode & 0o7777) != 0o555:
        errs.append(f"albums-svc: права {m.mode & 0o7777:04o}, нужны 0555")
    if (m.uid, m.gid) != (0, 0):
        errs.append(f"albums-svc: владелец {m.uid}:{m.gid}, нужен 0:0")


def check(archive, containerfile, base_inspect, version, revision):
    errs = []
    try:
        files = oci.read_archive(archive)
        mdig = oci.manifest_digest(files)
        manifest = json.loads(oci.blob(files, mdig))
        config = json.loads(oci.blob(files, manifest["config"]["digest"]))
        base = json.load(open(base_inspect, encoding="utf-8"))[0]
        base_layers = base["RootFS"]["Layers"]
        base_env = base["Config"].get("Env") or []
        base_labels = base["Config"].get("Labels") or {}
        want = final_stage(containerfile)
    except (oci.ArchiveError, KeyError, ValueError, TypeError, IndexError, OSError) as e:
        return [f"не удалось прочитать образ или эталон: {e}"]
    layers = manifest.get("layers") or []
    diff_ids = (config.get("rootfs") or {}).get("diff_ids") or []
    if len(layers) != len(diff_ids) or len(layers) != len(base_layers) + 1:
        errs.append(f"слоёв {len(layers)} (diff_id {len(diff_ids)}), ожидалось {len(base_layers)} базы + 1")
    elif diff_ids[: len(base_layers)] != base_layers:
        errs.append("первые слои не равны слоям базы (diff_id)")
    else:
        try:
            check_layer(oci.blob(files, layers[-1]["digest"]), errs)
        except (oci.ArchiveError, KeyError, TypeError) as e:
            errs.append(f"верхний слой: {e}")
    cfg = config.get("config") or {}
    if cfg.get("User") != "65532:65532" or want.get("USER") != "65532:65532":
        errs.append(f"User {cfg.get('User')!r}, нужен 65532:65532 (и в Containerfile)")
    for key, name in (("Entrypoint", "ENTRYPOINT"), ("Cmd", "CMD")):
        if cfg.get(key) != want.get(name):
            errs.append(f"{key} {cfg.get(key)!r} ≠ Containerfile {want.get(name)!r}")
    if (cfg.get("Env") or []) != base_env:
        errs.append("Env не равен Env базы")
    expect = dict(base_labels)
    expect.update(zip(OWN_LABELS, (SOURCE, LICENSE, TITLE, version, revision)))
    got = dict(cfg.get("Labels") or {})
    for k in EXTRA_LABELS:
        got.pop(k, None)
    if got != expect:
        errs.append(f"метки не по эталону: лишние {sorted(set(got) - set(expect))}, не те {sorted(k for k in expect if got.get(k) != expect[k])}")
    if config.get("os") != "linux" or config.get("architecture") != "amd64":
        errs.append(f"платформа {config.get('os')}/{config.get('architecture')}, нужна linux/amd64")
    for h in config.get("history") or []:
        text = str(h.get("created_by", ""))
        for leak in LEAKS:
            if leak in text:
                errs.append(f"в истории слоя путь машины сборки «{leak}»: {text[:80]}")
    return errs


def main(argv):
    a = {}
    it = iter(argv)
    pos = next(it, None)
    for k in it:
        if k in ("--containerfile", "--base-inspect", "--version", "--revision"):
            a[k] = next(it, None)
        else:
            pos = None
    if pos is None or len(a) != 4 or None in a.values():
        print("использование: image_check.py <архив> --containerfile F --base-inspect F --version V --revision R", file=sys.stderr)
        return 2
    if not re.match(r"^[0-9a-f]{40}$", a["--revision"]) or not re.match(r"^(\d+\.\d+\.\d+|ci)$", a["--version"]):
        print("image_check: версия или ревизия не по формату", file=sys.stderr)
        return 2
    errs = check(pos, a["--containerfile"], a["--base-inspect"], a["--version"], a["--revision"])
    for e in errs:
        print(f"image_check: {e}", file=sys.stderr)
    if not errs:
        print("image_check: состав образа по эталону")
    return 1 if errs else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
