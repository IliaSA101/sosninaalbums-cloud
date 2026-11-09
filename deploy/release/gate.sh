#!/usr/bin/env bash
# release-gate (design 004 §1, развилка 004-1): метка выпуска — vX.Y.Z, событие — push, коммит метки — из main.
# Вызывается из release.yml после checkout с fetch-depth: 0; всё берётся из переменных окружения прогона, а не из
# выражений workflow (имя метки — недоверенный ввод: в run: ему не место, развилка В1).
# Тестовых крючков нет: ссылка main - константа (тесты создают refs/remotes/origin/main в своём репозитории).
# Код 0 — выпуск можно продолжать; 1 — отказ (release-publish и release-attest не стартуют).
set -euo pipefail

die() {
  echo "release-gate: $*" >&2
  exit 1
}

case $- in *x*) die "set -x запрещён" ;; esac

ref=${GITHUB_REF:-}
event=${GITHUB_EVENT_NAME:-}
sha=${GITHUB_SHA:-}
main_ref=refs/remotes/origin/main

# Без ведущих нулей: v01.2.3 и v1.2.3-rc1 — не наши метки (версии Go-модуля тоже не двигаются).
num='(0|[1-9][0-9]*)'
[[ $ref =~ ^refs/tags/v${num}\.${num}\.${num}$ ]] || die "метка «${ref:0:60}» не вида refs/tags/vX.Y.Z"
[ "$event" = push ] || die "событие «${event:0:20}», выпуск идёт только по push метки"
[[ $sha =~ ^[0-9a-f]{40}$ ]] || die "GITHUB_SHA не sha1"

# Метка лёгкая (004-24): у аннотированной GITHUB_SHA - коммит, но объект метки другой, и version Go-модуля не совпал бы.
[ "$(git cat-file -t "$ref" 2>/dev/null)" = commit ] || die "метка $ref не найдена локально или не лёгкая (аннотированная)"
[ "$(git rev-parse "$ref^{commit}")" = "$sha" ] || die "метка указывает не на GITHUB_SHA"

# Коммит метки обязан входить в main: иначе ops-ВМ, раскатывающая только коммиты main, его не увидит.
git rev-parse --verify --quiet "$main_ref^{commit}" >/dev/null || die "нет $main_ref (checkout без fetch-depth: 0?)"
git merge-base --is-ancestor "$sha" "$main_ref" || die "коммит метки не входит в main"

# Не отказ: main мог сдвинуться за секунды до метки. Но метка на старом коммите main - образ старого кода под новым номером.
if [ "$(git rev-parse "$main_ref")" = "$sha" ]; then
  echo "release-gate: верхний main = метка: да"
else
  echo "release-gate: верхний main = метка: НЕТ (метка не на верхнем коммите main)"
fi
echo "release-gate: метка ${ref#refs/tags/} на коммите из main"
