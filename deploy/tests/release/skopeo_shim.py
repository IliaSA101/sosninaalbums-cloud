#!/usr/bin/env python3
"""Поддельный skopeo для тестов publish.sh (в сборке и на стенде настоящий skopeo; здесь - без сети и реестра).

Состояние - JSON в $SHIM_STATE/state.json: {"tags": {"репо:тег": D}, "manifests": {D: base64}}.
Команды: copy [--preserve-digests] [--digestfile F] [--dest-tls-verify=false] [--authfile A] oci-archive:П docker://Р:Т;
inspect --raw [--tls-verify=false] [--authfile A] docker://Р:Т|Р@D; login --password-stdin --authfile A -u U хост; logout --authfile A.
Сбои для случаев - через переменные окружения:
  SHIM_INSPECT_DENIED=1  чтение по тегу всегда denied (как до создания пакета)
  SHIM_SWAP_AFTER_COPY=1 после copy тег указывает на чужой манифест
  SHIM_NO_LF=1           inspect --raw без завершающего LF (по умолчанию - с LF, как у настоящего skopeo, гипотеза)
"""

import base64
import hashlib
import importlib.util
import json
import os
import sys

# Скрипт копируют в каталог PATH теста, поэтому путь к deploy/release приходит из окружения.
spec = importlib.util.spec_from_file_location("oci_archive", os.path.join(os.environ["SA_REL_DIR"], "oci_archive.py"))
oci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(oci)

STATE = os.path.join(os.environ["SHIM_STATE"], "state.json")
FOREIGN = b'{"schemaVersion":2,"note":"foreign"}'


def load():
    if os.path.exists(STATE):
        with open(STATE, encoding="utf-8") as f:
            return json.load(f)
    return {"tags": {}, "manifests": {}}


def save(st):
    with open(STATE, "w", encoding="utf-8") as f:
        json.dump(st, f)


def opts(argv):
    """-> (флаги-значения, позиционные)."""
    val, pos, i = {}, [], 0
    while i < len(argv):
        a = argv[i]
        if a in ("--digestfile", "--authfile", "-u") and i + 1 < len(argv):
            val[a] = argv[i + 1]
            i += 2
        elif a.startswith("--"):
            val[a] = True
            i += 1
        else:
            pos.append(a)
            i += 1
    return val, pos


def main(argv):
    cmd, rest = argv[0], argv[1:]
    val, pos = opts(rest)
    st = load()
    if cmd == "copy":
        src, dst = pos
        try:
            files = oci.read_archive(src.removeprefix("oci-archive:"))
            d = oci.manifest_digest(files)
            raw = oci.blob(files, d)
            # Настоящий skopeo проверяет дайджест каждого переданного блоба: порча слоя - отказ копирования.
            man = json.loads(raw)
            for desc in [man["config"]] + man["layers"]:
                oci.blob(files, desc["digest"])
        except (oci.ArchiveError, KeyError, ValueError) as e:
            print(f"skopeo_shim: {e}", file=sys.stderr)
            return 1
        ref = dst.removeprefix("docker://")
        st["manifests"][d] = base64.b64encode(raw).decode()
        st["tags"][ref] = d
        if os.environ.get("SHIM_SWAP_AFTER_COPY"):
            fd = "sha256:" + hashlib.sha256(FOREIGN).hexdigest()
            st["manifests"][fd] = base64.b64encode(FOREIGN).decode()
            st["tags"][ref] = fd
        save(st)
        if "--digestfile" in val:
            with open(val["--digestfile"], "w", encoding="utf-8") as f:
                f.write(d)
        return 0
    if cmd == "inspect":
        ref = pos[0].removeprefix("docker://")
        if "@" in ref:
            d = ref.split("@", 1)[1]
        else:
            if os.environ.get("SHIM_INSPECT_DENIED"):
                print("denied: requested access to the resource is denied", file=sys.stderr)
                return 1
            d = st["tags"].get(ref)
        if d is None or d not in st["manifests"]:
            print("manifest unknown", file=sys.stderr)
            return 1
        sys.stdout.buffer.write(base64.b64decode(st["manifests"][d]) + (b"" if os.environ.get("SHIM_NO_LF") else b"\n"))
        return 0
    if cmd == "login":
        with open(val["--authfile"], "w", encoding="utf-8") as f:
            json.dump({"auths": {pos[0]: {"user": val.get("-u"), "secret": sys.stdin.read()}}}, f)
        return 0
    if cmd == "logout":
        # Настоящий skopeo logout не удаляет файл, а убирает запись: удаление - дело registry.sh (rm -f).
        if os.path.exists(val["--authfile"]):
            with open(val["--authfile"], "w", encoding="utf-8") as f:
                json.dump({"auths": {}}, f)
        return 0
    print(f"skopeo_shim: неизвестная команда {cmd}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
