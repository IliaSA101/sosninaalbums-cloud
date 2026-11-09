#!/usr/bin/env bash
# actions_pins_cases (спека 004, design §12): сверка закреплённых действий в окне Б и проверка набора в image-verify. Сеть
# нет; git ls-remote и action.yml подменены функциями (в окне Б их отдаёт GitHub); runs.using разбирает PyYAML в настоящем
# образе контроллера без сети; запуск контроллера с workflow_guard.py — записью аргументов (сам guard.py — задача И6a; здесь проверяется только вызов и отказ при его коде ≠ 0).
#
#   pins_cases.sh <корень репозитория> <рабочий каталог> <образ контроллера с PyYAML>
# shellcheck source-path=SCRIPTDIR disable=SC2154,SC1091,SC2016  # переменные библиотек; источники — в копии дерева
set -uo pipefail

root=$1 w=$2 ctl_id=$3
fails=0
ok() { echo "ok   $1"; }
bad() {
	echo "FAIL $1"
	fails=$((fails + 1))
}
check() { # <имя> <команда…>
	local n=$1
	shift
	if "$@"; then ok "$n"; else bad "$n"; fi
}
want3() { # <имя> <команда…>
	local n=$1 rc
	shift
	"$@" >"$w/last.out" 2>&1
	rc=$?
	if [ "$rc" = 3 ]; then ok "$n"; else bad "$n: код $rc, ждали 3"; fi
}
gitc() { git -c user.name=t -c user.email=t@example.org "$@"; }

. "$root/deploy/ops/ctl-run.sh"
. "$root/deploy/ops/mirror.sh"
. "$root/deploy/ops/image.sh"
. "$root/deploy/ops/tools.sh"

# Рабочий каталог (iv_workdir) — в настоящем XDG_RUNTIME_DIR: подмена этой переменной ломает сам Podman. Создаётся в основной
# оболочке (в конвейере iv_tmp потерялся бы) и убирается по выходу.
trap '[ -z "$iv_tmp" ] || rm -rf -- "$iv_tmp"' EXIT
iv_workdir base || exit 1
guard_rc=0
prov_run() { # <роль> <флаги…> <образ> <команда…>: guard — запись аргументов; остальное — настоящий контейнер без записи в корень
	local role=$1
	shift
	if [ "$role" = guard ] && [ "${real_guard:-0}" != 1 ]; then
		printf '%s\n' "$*" >"$w/guard.args"
		return "$guard_rc"
	fi
	podman run --rm --read-only --cap-drop=ALL --security-opt=no-new-privileges --tmpfs /tmp:rw,mode=1777 "$@"
}

# --- pins_pick: имя ссылки ровно refs/tags/<тег>^{} или refs/tags/<тег> ---------------------------------------------------
A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb C=cccccccccccccccccccccccccccccccccccccccc
tab() { printf '%s\t%s\n' "$1" "$2"; }
pick() { pins_pick "$1"; }
check "метка аннотированная: берётся ^{} (коммит), а не объект метки" test "$( { tab "$A" refs/tags/v4; tab "$B" 'refs/tags/v4^{}'; } | pick v4)" = "$B"
check "метка лёгкая: берётся refs/tags/<тег>" test "$(tab "$A" refs/tags/v4 | pick v4)" = "$A"
check "только refs/heads/x/refs/tags/<тег> (хвост имени) — не принимается" test -z "$(tab "$A" refs/heads/x/refs/tags/v4 | pick v4)"
check "хвост refs/heads/x/refs/tags/<тег>^{} при верной лёгкой метке — берётся лёгкая" test "$( { tab "$C" 'refs/heads/x/refs/tags/v4^{}'; tab "$A" refs/tags/v4; } | pick v4)" = "$A"
check "чужой тег (v40 вместо v4) — не принимается" test -z "$(tab "$A" refs/tags/v40 | pick v4)"
check "пустой ответ ls-remote — пусто" test -z "$(: | pick v4)"

# --- runs.using: PyYAML в контроллере -----------------------------------------------------------------------------------
using_ok() { printf '%s\n' "$@" | pins_using_ok; }
using_bad() { ! using_ok "$@"; }
check "using: node20 принимается" using_ok 'name: x' 'runs:' '  using: node20' '  main: dist/index.js'
check "using: node24 в кавычках принимается" using_ok 'runs:' "  using: 'node24'" '  main: x.js'
check "using: поток runs: {using: node20} принимается (разбор YAML, а не строки)" using_ok 'runs: {using: node20, main: x.js}'
check "using: composite — отказ" using_bad 'runs:' '  using: composite' '  steps: []'
check "using: docker с образом docker://…:тег — отказ" using_bad 'runs:' '  using: docker' '  image: docker://alpine:3.20'
check "using: приманка «using: node20» в описании, настоящий ключ composite — отказ" using_bad 'description: |' '  using: node20' 'runs:' '  using: composite' '  steps: []'
check "using: ключ в кавычках \"using\": composite при приманке — отказ" using_bad 'description: |' '  using: node20' 'runs:' '  "using": composite'
check "using: тег !!str composite при приманке — отказ" using_bad 'description: |' '  using: node20' 'runs:' '  using: !!str composite'
check "using: node20 и docker:// в тексте — отказ" using_bad 'runs:' '  using: node20' '  main: x.js' 'description: docker://alpine:3'
check "using: повтор ключа (composite, затем node20) — отказ" using_bad 'runs:' '  using: composite' '  using: node20' '  main: x.js'
check "using: нет runs — отказ" using_bad 'name: x'
check "using: невалидный YAML — отказ" using_bad 'runs: [' '  using: node20'
check "using: node20 в списке, а не строка — отказ" using_bad 'runs:' '  using: [node20]'

eval "$(declare -f pins_action_yml | sed '1s/pins_action_yml/real_pins_action_yml/')" # настоящая — до подмены ниже

# --- pins_verify: lock против исходных репозиториев --------------------------------------------------------------------
SA_INVENTORY=$w/inv
mkdir -m 0700 "$SA_INVENTORY"
lock=$w/actions.lock
printf '# закреплённые действия\nactions/checkout v4.2.2 %s\nactions/attest v4.1.0 %s\n' "$A" "$B" >"$lock"
lsr_mode=ok
pins_lsremote() { # <имя> <тег>
	case $lsr_mode:$1 in
		ok:checkout) tab "$A" 'refs/tags/v4.2.2^{}'; tab "$C" refs/tags/v4.2.2 ;;
		ok:attest) tab "$B" refs/tags/v4.1.0 ;;
		other:checkout) tab "$C" 'refs/tags/v4.2.2^{}' ;;
		fork:checkout) tab "$A" refs/heads/x/refs/tags/v4.2.2 ;;
		fail:*) return 1 ;;
		*) tab "$B" refs/tags/v4.1.0 ;;
	esac
}
yml_mode=node
pins_action_yml() { # <имя> <sha>
	case $yml_mode in
		node) printf 'runs:\n  using: node20\n  main: index.js\n' ;;
		composite) printf 'runs:\n  using: composite\n  steps:\n    - uses: actions/x@v1\n' ;;
		docker) printf 'runs:\n  using: docker\n  image: docker://alpine:3.20\n' ;;
		none) return 1 ;;
	esac
}
sum=$(sha256sum <"$lock" | cut -d' ' -f1)
if pins_verify "$lock" "$SA_INVENTORY/actions-pins" >/dev/null 2>&1 && cmp -s "$lock" "$SA_INVENTORY/actions-pins/$sum.lock"; then ok "pins_verify: верный lock → 0, сверенная копия по sha256"; else bad "pins_verify: верный lock → 0"; fi
check "сверенный lock: права 0600" test "$(stat -c %a "$SA_INVENTORY/actions-pins/$sum.lock")" = 600
rm -rf -- "$SA_INVENTORY/actions-pins"
lsr_mode=other
want3 "ls-remote дал другой коммит → 3, lock не записан" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
check "после отказа сверенной копии нет" test ! -e "$SA_INVENTORY/actions-pins/$sum.lock"
lsr_mode=fork
want3 "в ответе только refs/heads/x/refs/tags/<тег> (хвост имени) → 3" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
lsr_mode=fail
want3 "ls-remote не выполнился → 3" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
lsr_mode=ok
yml_mode=composite
want3 "action.yml с using: composite → 3" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
yml_mode=docker
want3 "action.yml с docker://…:тег → 3" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
yml_mode=none
want3 "action.yml закреплённого коммита не получен → 3" pins_verify "$lock" "$SA_INVENTORY/actions-pins"
yml_mode=node
printf 'actions/checkout v4.2.2 %s\nthird-party/x v1 %s\n' "$A" "$B" >"$w/bad1.lock"
want3 "lock: владелец не actions → 3" pins_verify "$w/bad1.lock" "$SA_INVENTORY/actions-pins"
printf 'actions/checkout main %s\n' "$A" >"$w/bad2.lock"
want3 "lock: «тег» не вида vN → 3" pins_verify "$w/bad2.lock" "$SA_INVENTORY/actions-pins"
printf 'actions/checkout v4 %s\n' "${A:0:39}" >"$w/bad3.lock"
want3 "lock: SHA не 40 hex → 3" pins_verify "$w/bad3.lock" "$SA_INVENTORY/actions-pins"
printf '# только комментарий\n' >"$w/bad4.lock"
want3 "lock: пустой набор → 3" pins_verify "$w/bad4.lock" "$SA_INVENTORY/actions-pins"
check "после всех отказов в actions-pins ничего нет" test ! -e "$SA_INVENTORY/actions-pins"

# --- настоящий pins_action_yml: action.yaml — только при 404 на action.yml -----------------------------------------------------
yml_get=ok http_code=404
mirror_get() { # <url> <файл> <предел>
	printf '%s\n' "$1" >>"$w/urls"
	case $1 in
		*/action.yml) [ "$yml_get" = ok ] || return 22 ;;
	esac
	printf 'runs:\n  using: node20\n' >"$2"
}
pins_http_code() { printf '%s' "$http_code"; }
: >"$w/urls"
yml_get=ok http_code=404
yaml_fetch() { # <yml_get> <http_code>: настоящий pins_action_yml; urls — журнал запросов
	: >"$w/urls"
	yml_get=$1 http_code=$2
	real_pins_action_yml checkout aaaa >/dev/null
}
yaml_fetch ok 404
check "action.yml есть: берётся он, action.yaml не запрашивается" bash -c '! grep -q action.yaml "$1"' _ "$w/urls"
yaml_fetch missing 404
check "action.yml 404: берётся action.yaml" grep -q action.yaml "$w/urls"
if yaml_fetch missing 500; then bad "action.yml: ошибка не 404 (500) — отказ"; else ok "action.yml: ошибка не 404 (500) — отказ"; fi
check "action.yml: при ошибке не 404 action.yaml не запрашивался" bash -c '! grep -q action.yaml "$1"' _ "$w/urls"
yml_get=ok

# --- image-verify: набор действий коммита должен быть сверен ------------------------------------------------------------
repo=$w/repo
here=$root/deploy
mkdir -p "$repo/.github/workflows" "$repo/deploy/release" "$repo/deploy/ansible"
gitc -C "$repo" init -q -b main
printf 'name: x\n' >"$repo/.github/workflows/release.yml"
printf 'actions/checkout v4.2.2 %s\n' "$A" >"$repo/deploy/release/actions.lock"
printf '# заглушка workflow_guard.py (до слияния с И6a)\n' >"$repo/deploy/release/workflow_guard.py"
printf 'pyyaml==0.0.0 --hash=sha256:%064d\n' 0 >"$repo/deploy/ansible/requirements.txt"
gitc -C "$repo" add -A
gitc -C "$repo" commit -q -m c1
mkdir -m 0700 "$w/d1" "$w/d2" "$w/d3"
want3 "image-verify: lock коммита не сверен (нет в actions-pins) → 3 с подсказкой про окно Б" iv_workflow_guard "$w/d1"
check "подсказка называет окно Б и сверку действий" grep -q 'окно Б: сверка действий' "$w/last.out"
check "контроллер при этом не запускался" test ! -e "$w/guard.args"
lsr_mode=ok
pins_verify "$repo/deploy/release/actions.lock" "$SA_INVENTORY/actions-pins" >/dev/null 2>&1
lsr_mode=ok
# lock в репозитории — только строка checkout; ответ ls-remote подменённой функции для него верен.
check "lock сверен в окне Б → набор принят" iv_workflow_guard "$w/d2"
check "вызов guard: python3 -I, без сети, по HEAD-дереву, наборы и список путей" bash -c 'a=$(cat "$1"); case $a in *"--network=none"*"python3 -I /src/deploy/release/workflow_guard.py --root /src --pins /pins --paths /in/paths") exit 0 ;; esac; exit 1' _ "$w/guard.args"
guard_rc=1
want3 "workflow_guard.py вернул код ≠ 0 → 3" iv_workflow_guard "$w/d3"
guard_rc=0
printf 'actions/checkout v4.2.2 %s\n' "$B" >"$repo/deploy/release/actions.lock"
gitc -C "$repo" commit -q -a -m c2
mkdir -m 0700 "$w/d4"
want3 "uses: вне сверенного набора (lock изменён после окна Б) → 3" iv_workflow_guard "$w/d4"

# --- дерево для guard строится без .gitattributes (export-ignore, export-subst) -----------------------------------------------
printf 'actions/checkout v4.2.2 %s\n' "$A" >"$repo/deploy/release/actions.lock"
printf 'name: $Format:%%H$\n' >"$repo/.github/workflows/release.yml"
printf 'name: hidden\n' >"$repo/.github/workflows/hidden.yml"
printf '%s\n' '.github/workflows/release.yml export-subst' '.github/workflows/hidden.yml export-ignore' >"$repo/.gitattributes"
gitc -C "$repo" add -A
gitc -C "$repo" commit -q -m c3
mkdir -m 0700 "$w/d5"
check "export-ignore/export-subst в .gitattributes: guard вызван" iv_workflow_guard "$w/d5"
check "export-ignore не прячет файл workflow от guard" test -f "$w/d5/tree/.github/workflows/hidden.yml"
check "export-subst не меняет текст workflow (Format остаётся буквально)" grep -qF 'name: $Format:%H$' "$w/d5/tree/.github/workflows/release.yml"
check "в дереве guard есть deploy/ansible/requirements.txt (сверка строки PyYAML)" test -f "$w/d5/tree/deploy/ansible/requirements.txt"
check "в дереве guard нет .git и лишних каталогов (только .github, deploy/release, deploy/ansible/requirements.txt)" test "$(cd "$w/d5/tree" && find . -type f ! -path './.github/*' ! -path './deploy/release/*' ! -path './deploy/ansible/requirements.txt' | wc -l)" = 0
git -C "$repo" rm -q -f deploy/ansible/requirements.txt
gitc -C "$repo" commit -q -m c4
mkdir -m 0700 "$w/d6"
want3 "в HEAD нет deploy/ansible/requirements.txt → 3" iv_workflow_guard "$w/d6"
gitc -C "$repo" revert --no-edit HEAD >/dev/null
ln -s release.yml "$repo/.github/workflows/link.yml"
gitc -C "$repo" add -A
gitc -C "$repo" commit -q -m c6
mkdir -m 0700 "$w/d7"
want3 "симлинк (120000) в .github/workflows → 3" iv_workflow_guard "$w/d7"
check "отказ по симлинку называет файл" grep -q 'link.yml' "$w/last.out"

# --- настоящий workflow_guard.py (И6a) на настоящем дереве: контейнер контроллера, без сети ----------------------------------
repo=$w/real
mkdir -p "$repo/deploy/ansible"
cp -r "$root/.github" "$repo/.github"
cp -r "$root/deploy/release" "$repo/deploy/release"
cp "$root/deploy/ansible/requirements.txt" "$repo/deploy/ansible/requirements.txt"
find "$repo" -name __pycache__ -prune -exec rm -rf {} +
gitc -C "$repo" init -q -b main
gitc -C "$repo" add -A
gitc -C "$repo" commit -q -m real1
mkdir -p "$SA_INVENTORY/actions-pins"
cp "$repo/deploy/release/actions.lock" "$SA_INVENTORY/actions-pins/$(sha256sum <"$repo/deploy/release/actions.lock" | cut -d' ' -f1).lock"
real_guard=1
mkdir -m 0700 "$w/d8"
check "настоящий workflow_guard.py на настоящих .github и deploy/release проходит (контроллер, без сети)" iv_workflow_guard "$w/d8"
sed -i 's/runs-on: ubuntu-24.04/runs-on: self-hosted/' "$repo/.github/workflows/checks.yml"
gitc -C "$repo" commit -q -a -m real2
mkdir -m 0700 "$w/d9"
want3 "настоящий guard: чужой runs-on в workflow → 3" iv_workflow_guard "$w/d9"
real_guard=0

echo "actions_pins_cases: провалов $fails"
[ "$fails" -eq 0 ]
