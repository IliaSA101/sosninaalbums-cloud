#!/usr/bin/env bash
# publish.sh <архив> <D> <репозиторий> <тег> — публикация образа из архива build-image (design 004 §5, 004-21).
# Один скрипт для release-publish (ghcr.io) и для стенда (реестр на 127.0.0.1, SA_DEST_TLS_VERIFY=false).
# Окружение: SA_AUTHFILE — файл входа skopeo; SA_OUT_DIR — куда положить release.manifest.json
# (по умолчанию $RUNNER_TEMP/release-manifest); GITHUB_SHA — для итоговой строки.
# `set -x` запрещён: в окружении шага есть токен.
set -euo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

die() {
  echo "publish: $*" >&2
  exit 1
}

case $- in *x*) die "set -x запрещён: токен попал бы в журнал" ;; esac
[ $# -eq 4 ] || die "использование: publish.sh <архив> <D> <репозиторий> <тег>"
archive=$1 digest=$2 repo=$3 tag=$4

# 1. Аргументы — регулярными выражениями; репозиторий и тег выпуска — константы, стенд — свой реестр.
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || die "D не вида sha256:<64 hex>"
tls=() desttls=()
if [ -z "${SA_DEST_TLS_VERIFY:-}" ]; then
  num='(0|[1-9][0-9]*)'
  [ "$repo" = ghcr.io/iliasa101/albums-svc ] || die "репозиторий не ghcr.io/iliasa101/albums-svc"
  [[ $tag =~ ^v${num}\.${num}\.${num}$ ]] || die "тег не вида vX.Y.Z"
elif [ "$SA_DEST_TLS_VERIFY" = false ]; then
  # Только стенд: одноразовый реестр без TLS на адресе хоста; боевой путь этого режима не имеет.
  [[ $repo =~ ^127\.0\.0\.1:[0-9]{1,5}/albums-svc$ ]] || die "стенд: репозиторий не 127.0.0.1:<порт>/albums-svc"
  [ "$tag" = stand ] || die "стенд: тег только stand"
  tls=(--tls-verify=false) desttls=(--dest-tls-verify=false)
else
  die "SA_DEST_TLS_VERIFY: только false или пусто"
fi
auth=()
[ -z "${SA_AUTHFILE:-}" ] || auth=(--authfile "$SA_AUTHFILE")

# Артефакт — ровно один обычный файл albums-svc.oci.tar в своём каталоге: лишнее рядом — признак подмены.
[ "$(basename -- "$archive")" = albums-svc.oci.tar ] || die "имя архива не albums-svc.oci.tar"
if [ ! -f "$archive" ] || [ -L "$archive" ]; then die "архив — не обычный файл"; fi
n=$(find "$(dirname -- "$archive")" -mindepth 1 -maxdepth 1 | wc -l)
[ "$n" -eq 1 ] || die "в каталоге артефакта $n объектов, нужен ровно один файл"

# 2. Дайджест архива (sha256 блоба манифеста из index.json с одним манифестом) обязан быть D из build-image.
got=$(python3 -I "$here/oci_archive.py" digest "$archive") || die "архив не разобран"
[ "$got" = "$digest" ] || die "дайджест архива $got ≠ D из build-image"

# Сырой манифест по ссылке → sha256 его байтов. Ошибка чтения (нет пакета, denied, сеть) — код ≠ 0.
raw_sha() { # <ссылка без docker://> [--out файл]
  local ref=$1
  shift
  skopeo inspect --raw "${tls[@]}" "${auth[@]}" "docker://$ref" | python3 -I "$here/oci_archive.py" rawsha "$@"
}

# 3. Тег. «Чужой» — только по успешно прочитанному манифесту; текст ошибки не разбирается: до создания пакета
# GHCR может ответить не «нет такого» (design §10), а метка дорогая. Перевес тега принят (004-5), решает шаг 6.
write=1
if cur=$(raw_sha "$repo:$tag" 2>/dev/null); then
  if [ "$cur" = "$digest" ]; then
    echo "publish: тег $tag уже указывает на D — запись пропущена"
    write=0
  else
    die "тег $tag занят образом $cur, а D = $digest — новая версия"
  fi
else
  echo "publish: тег $tag не прочитан (нет пакета, denied или сеть) — пишем, решает сверка после записи"
fi

# 4. Запись: дайджесты слоёв и манифеста сохраняются (--preserve-digests), digestfile обязан дать D.
if [ "$write" -eq 1 ]; then
  tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/publish.XXXXXX")
  trap 'rm -rf -- "$tmp"' EXIT
  skopeo copy --preserve-digests --digestfile "$tmp/digest" "${desttls[@]}" "${auth[@]}" \
    "oci-archive:$archive" "docker://$repo:$tag" >/dev/null || die "skopeo copy не удался"
  [ "$(cat "$tmp/digest")" = "$digest" ] || die "digestfile skopeo ≠ D"
fi

# 5. Сырой манифест по D → release.manifest.json: его байты — субъект аттестации, sha256 = D.
out=${SA_OUT_DIR:-${RUNNER_TEMP:-}/release-manifest}
if [ -z "$out" ] || [ "$out" = /release-manifest ]; then die "нет каталога для release.manifest.json (RUNNER_TEMP или SA_OUT_DIR)"; fi
mkdir -p -- "$out"
by_digest=$(raw_sha "$repo@$digest" --out "$out/release.manifest.json") || die "манифест по D не прочитан"
[ "$by_digest" = "$digest" ] || {
  rm -f -- "$out/release.manifest.json"
  die "манифест по D даёт $by_digest"
}

# 6. Обязательно и решает: тег → sha256 сырого манифеста = D, иначе провал (и release-attest не идёт).
after=$(raw_sha "$repo:$tag") || die "тег $tag не читается после записи"
[ "$after" = "$digest" ] || die "тег $tag указывает на $after, а не на D"

echo "publish: D=$digest тег=$tag коммит=${GITHUB_SHA:-?}"
