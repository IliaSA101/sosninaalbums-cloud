#!/usr/bin/env bash
# build.sh <архив> — задание build-image и локальная сборка архива для стенда (design 004 §3).
# podman build по deploy/Containerfile → podman push в oci-archive → дайджест архива D → эталон состава.
# Окружение: SA_VERSION_REF — refs/tags/vX.Y.Z (выпуск) или ci (ветка проверок); GITHUB_SHA — ревизия
# (без неё — HEAD рабочей копии, для локального запуска); GITHUB_OUTPUT — туда пишется digest=<D>.
# Имя метки — недоверенный ввод: попадает сюда только через env и проходит регулярное выражение.
set -euo pipefail

root=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

die() {
  echo "build: $*" >&2
  exit 1
}

[ $# -eq 1 ] || die "использование: build.sh <архив>"
archive=$1
[ "$(basename -- "$archive")" = albums-svc.oci.tar ] || die "архив должен называться albums-svc.oci.tar"

num='(0|[1-9][0-9]*)'
ref=${SA_VERSION_REF:-ci}
if [[ $ref =~ ^refs/tags/v(${num}\.${num}\.${num})$ ]]; then
  version=${BASH_REMATCH[1]}
elif [ "$ref" = ci ]; then
  version=ci
else
  die "SA_VERSION_REF «${ref:0:40}» не refs/tags/vX.Y.Z и не ci"
fi
revision=${GITHUB_SHA:-$(git rev-parse HEAD)}
[[ $revision =~ ^[0-9a-f]{40}$ ]] || die "ревизия не sha1"

dir=$(dirname -- "$archive")
mkdir -p -- "$dir"
[ -z "$(find "$dir" -mindepth 1 -maxdepth 1)" ] || die "каталог архива $dir не пуст"

image="localhost/albums-svc:build-$$"
cleanup() {
  podman rmi -f "$image" >/dev/null 2>&1 || true
  [ -z "${work:-}" ] || rm -rf -- "$work"
}
trap cleanup EXIT

# Итоговая стадия: база по дайджесту — её слои и Env эталон сверяет с собранным образом.
base=$(awk '/^FROM /{ref=$2} END{print ref}' deploy/Containerfile)
[[ $base =~ @sha256:[0-9a-f]{64}$ ]] || die "база итоговой стадии не по дайджесту: $base"

podman build -q --ignorefile deploy/Containerfile.containerignore -f deploy/Containerfile \
  --build-arg "SA_VERSION=$version" --build-arg "SA_REVISION=$revision" -t "$image" . >/dev/null || die "podman build не удался"
podman push -q "$image" "oci-archive:$archive" || die "podman push в архив не удался"

digest=$(python3 -I deploy/release/oci_archive.py digest "$archive") || die "дайджест архива не получен"
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || die "D не вида sha256:<64 hex>"

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/build.XXXXXX")
# Имя с тегом и дайджестом podman может хранить только по дайджесту: пробуем оба написания.
podman image inspect "$base" >"$work/base.json" 2>/dev/null ||
  podman image inspect "${base%%[:@]*}@${base#*@}" >"$work/base.json" || die "база не найдена после сборки: $base"
python3 -I deploy/release/image_check.py "$archive" --containerfile deploy/Containerfile --base-inspect "$work/base.json" \
  --version "$version" --revision "$revision" || die "состав образа не по эталону"

echo "build: D=$digest версия=$version"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "digest=$digest" >>"$GITHUB_OUTPUT"
fi
