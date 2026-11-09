#!/usr/bin/env bash
# Просмотр нового кода раскатки ДО checkout (решение И2-1; единственный рубеж против агента рабочей ВМ, design
# §16, ВР-2): метка машины раскатки и учётная запись → bundle из каталога входящих (в ops-ВМ он смонтирован
# только на чтение) → копия в доме (dd с nofollow, пределом и таймаутом, в скрытом временном каталоге) → проверка во временном пустом
# репозитории (verify, fetch, fsck --strict) → fetch в клон → fsck → полный diff в less → «yes» → только потом
# checkout. Иначе уже подменённый provision.sh нарисовал бы пустой diff и спросил фразу. Первый раз — те же
# команды руками из runbook (deploy/README.md); после успешного configure копия этого скрипта лежит в
# ~/.local/libexec/sa/.
#
#   review.sh <ветка>
#
# Окружение: SA_OPS_USER (sa-ops), SA_INBOX (/srv/sa-inbox), SA_SRC_BUNDLE (<SA_INBOX>/code.bundle),
# SA_CLONE (~/src/sosninaalbums-cloud), SA_INVENTORY (~/inventory). Коды: 0 — клон переведён на просмотренный
# коммит; 1 — отменено или ошибка; 3 — отказ до просмотра (сообщение без значений).
set -uo pipefail

refuse() {
	echo "review: отказ: $*" >&2
	exit 3
}
fail() {
	echo "review: $*" >&2
	exit 1
}

# Метка машины раскатки и отказ при процессе claude или node — копия guard_machine из ops/guard.sh (этот скрипт
# самодостаточен: ставится в libexec и работает до checkout, подключить guard.sh он не может). Тело функции
# совпадает побайтово, это проверяет шаг ctl_shared; правка — в обоих файлах.
guard_machine() {
	local LC_ALL=C id st procs
	id=$(cat -- /etc/machine-id 2>/dev/null)
	if ! { [[ $id =~ ^[0-9a-f]{32}$ ]] && printf '%s\n' "$id" | cmp -s - /etc/machine-id; }; then
		refuse "не машина раскатки: /etc/machine-id не 32 строчных hex и перевод строки"
	fi
	[ -f /etc/sa-ops-machine ] || refuse "не машина раскатки: нет файла метки /etc/sa-ops-machine"
	st=$(stat -L -c '%u %a' -- /etc/sa-ops-machine 2>/dev/null) || refuse "не машина раскатки: метка не читается"
	if [ "${st% *}" != 0 ] || [ $((8#${st#* } & 8#022)) -ne 0 ]; then
		refuse "не машина раскатки: метка не root или с записью группе и прочим"
	fi
	printf 'sa-ops-machine %s\n' "$id" | cmp -s - /etc/sa-ops-machine || refuse "не машина раскатки: метка другой машины или не одна строка"
	procs=$(ps -e -o comm= 2>/dev/null) || refuse "не машина раскатки: ps не выполнился"
	[ -n "$procs" ] || refuse "не машина раскатки: ps ничего не вернул"
	grep -qxE '[[:space:]]*(claude(-.*)?|node|cowork.*)[[:space:]]*' <<<"$procs"
	case $? in
		0) refuse "не машина раскатки: идёт процесс claude, node или cowork" ;;
		1) ;;
		*) refuse "не машина раскатки: проверка процессов не выполнилась" ;;
	esac
}

# Путь и все существующие предки — учётной записи раскатки или root, без записи группе и прочим (как в ops/guard.sh).
owned_safe() { # <метка> <путь>
	local d m u
	d=$(realpath -m -- "$2")
	while [ ! -e "$d" ] && [ "$d" != / ]; do d=$(dirname -- "$d"); done
	while :; do
		u=$(stat -c %u -- "$d")
		m=$(stat -c %a -- "$d")
		if [ "$u" != "$(id -u)" ] && [ "$u" != 0 ]; then refuse "$1: владелец $d — не учётная запись раскатки и не root"; fi
		if [ $((8#$m & 8#022)) -ne 0 ]; then
			# Подсказка — только для своего каталога; чужой системный (root) править нельзя и не нужно.
			if [ "$u" = "$(id -u)" ]; then refuse "$1: на $d есть запись группе или прочим (исправить: chmod go-w $(printf '%q' "$d"))"; fi
			refuse "$1: на $d есть запись группе или прочим (системный каталог: права не менять, выбрать путь в доме учётной записи раскатки)"
		fi
		[ "$d" = / ] && return 0
		d=$(dirname -- "$d")
	done
}

guard_machine
[ "$(id -un)" = "${SA_OPS_USER:-sa-ops}" ] || refuse "не та учётная запись (нужна учётная запись раскатки)"
branch=${1:-}
# Без ведущего «-»: имя ветки не должно стать опцией git.
[[ $branch =~ ^[A-Za-z0-9._][A-Za-z0-9._/-]*$ ]] || refuse "нужна ветка (review.sh <ветка>)"
inbox=${SA_INBOX:-/srv/sa-inbox}
src=${SA_SRC_BUNDLE:-$inbox/code.bundle}
clone=${SA_CLONE:-$HOME/src/sosninaalbums-cloud}
inv=${SA_INVENTORY:-$HOME/inventory}
tmp=''
copy=''
max_bytes=209715200 # 200 МиБ: код раскатки столько не весит; больше — подозрительно
empty_tree=4b825dc642cb6eb9a060e54bf8d69288fbee4904
# Тот же шаблон, что в ops/guard.sh: белый список (находит строки с символом вне списка), побайтово, UTF-8.
# shellcheck disable=SC2016  # шаблон PCRE, не раскрытие
ctl_pattern='^(?!(?:[\t\x20-\x7e]|[\xd0-\xd3][\x80-\xbf]|\xc2[\xa7\xa9\xab\xb0\xbb]|\xe2\x80[\x94\xa6]|\xe2\x84\x96|\xe2\x86\x92|\xe2\x89[\xa0\xa4\xa5])*+$)'
# trap — до первой записи: прерванный просмотр не оставляет ни копию, ни временный репозиторий (оба — в $tmp).
trap '[ -z "$tmp" ] || rm -rf -- "$tmp"' EXIT

if [ ! -f "$src" ] || [ -L "$src" ]; then refuse "bundle не обычный файл или симлинк"; fi
case "$(realpath -- "$src")/" in
	"$(realpath -m -- "$inbox")"/*) ;;
	*) refuse "bundle вне каталога входящих" ;;
esac
owned_safe SA_INVENTORY "$inv"
owned_safe "клон" "$clone"

if [ ! -e "$clone" ]; then
	# umask 077: при umask 002 (Ubuntu, USERGROUPS_ENAB) каталоги вышли бы 775, и следующий запуск отказал бы в owned_safe
	(umask 077 && mkdir -p "$(dirname -- "$clone")") || fail "каталог клона"
	(umask 077 && git init -q "$clone") || fail "git init"
fi
[ -d "$clone/.git" ] || refuse "клон — не обычный git-репозиторий"
[ ! -e "$clone/.git/objects/info/alternates" ] || refuse "в клоне есть objects/info/alternates"
git -C "$clone" config transfer.fsckObjects true
git -C "$clone" config fetch.fsckObjects true

# Временный каталог — до копии и скрытый (имя с точкой): индексатор файлов графической сессии разбирает видимые
# файлы в доме, а копия — чужие данные. В нём и копия, и временный репозиторий; уборка — в trap.
tmp=$(umask 077 && mktemp -d "$HOME/.review.XXXXXX") || fail "временный каталог"
copy=$tmp/in.bundle
# Копия с пределом: читаем не больше max_bytes+1 и отказываем, если вышли за предел (design §16.2, ВР-4, ВР-10).
# nofollow — симлинк, подложенный между проверкой -L и открытием (агент меняет файл в любой момент), даёт ELOOP, а не
# чтение файла ops-ВМ; nonblock — FIFO, подложенный после проверки -f, не повесит открытие (копия выйдет пустой, и
# verify откажет); fullblock — без него GNU dd при коротких чтениях (возможно у FUSE общей папки) считает блоки по
# числу read, а не байты, и молча режет копию; timeout — страховка от вечного ожидания.
(umask 077 && timeout 120 dd if="$src" of="$copy" iflag=nofollow,nonblock,count_bytes,fullblock count=$((max_bytes + 1)) bs=1M status=none) ||
	fail "копия bundle не удалась или обрезана"
[ "$(stat -c %s -- "$copy")" -le "$max_bytes" ] || refuse "bundle больше предела размера"

# Проверка во временном пустом репозитории: битые объекты не попадут в клон. На git 2.43 fsckObjects при fetch
# из bundle битый объект пропускает (проверено), поэтому fsck --strict — отдельно.
v=$tmp/v
git init -q "$v" || fail "git init (временный)"
git -C "$v" bundle verify "$copy" >/dev/null 2>&1 || fail "git bundle verify не прошёл"
git -C "$v" fetch -q "$copy" "refs/heads/$branch" 2>/dev/null || fail "в bundle нет ветки или fetch не прошёл"
git -C "$v" fsck --strict --no-dangling >/dev/null 2>&1 || fail "git fsck --strict нашёл повреждённые объекты"
git -C "$clone" fetch -q "$copy" "refs/heads/$branch" 2>/dev/null || fail "fetch в клон не прошёл"
git -C "$clone" fsck --strict --no-dangling >/dev/null 2>&1 || fail "git fsck --strict клона нашёл повреждённые объекты"
new=$(git -C "$clone" rev-parse "FETCH_HEAD^{commit}") || fail "FETCH_HEAD"

base=''
if [ -s "$inv/deployed-commit" ]; then
	base=$(head -n 1 "$inv/deployed-commit")
	git -C "$clone" cat-file -e "$base^{commit}" 2>/dev/null || fail "deployed-commit неизвестен клону"
fi
if [ -n "$base" ] && [ -n "$(git -C "$clone" diff --name-only "$base" "$new" -- ':(glob)**/.gitattributes')" ]; then
	refuse "изменён .gitattributes после прошлого развёртывания"
fi
# Только обычные файлы — по всему дереву коммита, а не по pathspec deploy/: симлинк на месте самого deploy, web или
# web/viewer pathspec не видит (ls-tree, grep и diff по нему пусты). Плюс явные типы (как в ops/guard.sh).
modes=$(git -C "$clone" ls-tree -r "$new") || fail "git ls-tree"
if [ -n "$(awk '$1 != "100644" && $1 != "100755"' <<<"$modes")" ]; then refuse "в дереве коммита есть симлинк, gitlink или нестандартный режим"; fi
[ "$(git -C "$clone" cat-file -t "$new:deploy" 2>/dev/null)" = tree ] || refuse "deploy в коммите — не каталог"
[ "$(git -C "$clone" cat-file -t "$new:web/viewer/notfound.html" 2>/dev/null)" = blob ] || refuse "страница 404 в коммите — не файл"
LC_ALL=C git -C "$clone" grep -a -l -P "$ctl_pattern" "$new" -- deploy/ web/viewer/notfound.html .github go.mod go.sum >/dev/null 2>&1
case $? in
	1) ;;
	0) refuse "в раскатываемых файлах (deploy/, .github, go.mod, go.sum, страница 404) есть символы вне белого списка (управляющие, невидимые)" ;;
	*) refuse "проверка управляющих символов не выполнилась" ;;
esac

git -C "$clone" diff --text --no-color --no-ext-diff --no-textconv "${base:-$empty_tree}" "$new" -- \
	deploy/ web/viewer/notfound.html .github go.mod go.sum ':(glob)**/.gitattributes' |
	LESS=-X LESSSECURE=1 LESSOPEN='' LESSCLOSE='' LESSHISTFILE=- less
echo "коммит: $new"
git -C "$clone" diff --stat --no-color --no-ext-diff "${base:-$empty_tree}" "$new" -- . ':(exclude)deploy' ':(exclude)web/viewer/notfound.html' \
	':(exclude).github' ':(exclude)go.mod' ':(exclude)go.sum'
printf '%s [yes/no]: ' "Прочитан весь diff выше, перейти на этот коммит?" >/dev/tty
IFS= read -r ans </dev/tty || fail "нет терминала"
[ "$ans" = yes ] || fail "отменено"

# checkout без атрибутов из дерева (clean/smudge-фильтры и eol по .gitattributes) — git ≥ 2.40 (--attr-source);
# на старше защита одна: отказ при изменённом .gitattributes (выше) и просмотр его в diff.
gv=$(git --version | sed -n 's/^git version \([0-9]*\)\.\([0-9]*\).*/\1 \2/p')
if [ -n "$gv" ] && { [ "${gv% *}" -gt 2 ] || [ "${gv#* }" -ge 40 ]; }; then
	git -C "$clone" hash-object -t tree -w /dev/null >/dev/null
	git -C "$clone" --attr-source="$empty_tree" checkout -q --detach "$new" || fail "checkout"
else
	git -C "$clone" checkout -q --detach "$new" || fail "checkout"
fi
echo "клон переведён на просмотренный коммит"
