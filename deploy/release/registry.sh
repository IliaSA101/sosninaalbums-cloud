#!/usr/bin/env bash
# Вход в реестр и выход из него для release-publish (design 004 §5). Токен — только из окружения шага (TOKEN),
# файл входа — в $RUNNER_TEMP под umask 077, на stdout/stderr токен не попадает.
#   registry.sh login     skopeo login ghcr.io -u $GITHUB_ACTOR --password-stdin --authfile $RUNNER_TEMP/auth.json
#   registry.sh logout    skopeo logout и удаление файла входа (шаг идёт до загрузки публичного артефакта)
set -euo pipefail

die() {
  echo "registry: $*" >&2
  exit 1
}

case $- in *x*) die "set -x запрещён: токен попал бы в журнал" ;; esac

tmp=${RUNNER_TEMP:-}
if [ -z "$tmp" ] || [ ! -d "$tmp" ]; then die "нет RUNNER_TEMP"; fi
auth="$tmp/auth.json"

case "${1:-}" in
  login)
    [ -n "${TOKEN:-}" ] || die "нет TOKEN"
    [[ ${GITHUB_ACTOR:-} =~ ^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$ ]] || die "GITHUB_ACTOR не по формату"
    (
      umask 077
      printf '%s' "$TOKEN" | skopeo login ghcr.io -u "$GITHUB_ACTOR" --password-stdin --authfile "$auth" >/dev/null
    ) || die "вход не удался"
    echo "registry: вход выполнен"
    ;;
  logout)
    # Выход не должен падать из-за отсутствия файла (вход мог не состояться): цель — чтобы файла не осталось.
    skopeo logout --authfile "$auth" >/dev/null 2>&1 || true
    rm -f -- "$auth"
    [ ! -e "$auth" ] || die "файл входа не удалён"
    echo "registry: выход выполнен, файла входа нет"
    ;;
  *) die "registry.sh login | logout" ;;
esac
