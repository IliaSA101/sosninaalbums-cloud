#!/usr/bin/env bash
# Отказы provision.sh до любых действий (design 002 §2): код 3, сообщение без значений. Только source.
# Граница доступа — отдельная машина раскатки (ops-ВМ без агентов, design §16): секреты, inventory и копии живут
# только в доме учётной записи раскатки на ней. Против агента рабочей ВМ рубеж один — полный diff в ops/review.sh
# до checkout; метка машины, сессия и страж Claude Code — защита от ошибки владельца (раскатка не в той машине,
# не в той сессии), а не вторая граница. Здесь проверяется то, что можно проверить изнутри.
# shellcheck disable=SC2154,SC2015  # refuse завершает скрипт (A && B || refuse); repo, SA_*: задаёт основной скрипт

refuse() {
	echo "provision: отказ: $*" >&2
	exit 3
}

# Ближайший каталог с .git (включая сам путь или его существующего предка); пусто — путь вне git-дерева.
git_root_of() {
	local d
	d=$(realpath -m -- "$1")
	while [ ! -e "$d" ] && [ "$d" != / ]; do d=$(dirname -- "$d"); done
	[ -d "$d" ] || d=$(dirname -- "$d")
	while :; do
		if [ -e "$d/.git" ]; then
			echo "$d"
			return 0
		fi
		[ "$d" = / ] && return 0
		d=$(dirname -- "$d")
	done
}

# Путь и все его существующие предки принадлежат учётной записи раскатки или root и закрыты на запись группе и
# прочим (иначе чужой процесс подменил бы inventory, копии или файл секретов — решение И2-5).
owned_safe() { # <метка для сообщения> <путь>
	local d m u
	d=$(realpath -m -- "$2")
	while [ ! -e "$d" ] && [ "$d" != / ]; do d=$(dirname -- "$d"); done
	while :; do
		u=$(stat -c %u -- "$d")
		m=$(stat -c %a -- "$d")
		{ [ "$u" = "$(id -u)" ] || [ "$u" = 0 ]; } || refuse "$1: владелец $d — не учётная запись раскатки и не root"
		if [ $((8#$m & 8#022)) -ne 0 ]; then
			# Подсказка — только для своего каталога; чужой системный (root) править нельзя и не нужно.
			if [ "$u" = "$(id -u)" ]; then refuse "$1: на $d есть запись группе или прочим (исправить: chmod go-w $(printf '%q' "$d"))"; fi
			refuse "$1: на $d есть запись группе или прочим (системный каталог: права не менять, выбрать путь в доме учётной записи раскатки)"
		fi
		[ "$d" = / ] && return 0
		d=$(dirname -- "$d")
	done
}

# Метка машины раскатки (design §16.4, ВР-5, ВР-9): файл root, без записи группе и прочим, содержимое побайтово
# «sa-ops-machine <machine-id>» и один перевод строки. Путь постоянный, без переменной окружения: иначе метку
# можно было бы обойти по ошибке (тесты подставляют файлы монтированием). Симлинк отдельно не проверяется:
# stat -L и чтение смотрят на цель, а в /etc пишет только root. Клон ops-ВМ со сменой machine-id метку теряет.
# Отказ и при живом процессе claude, claude-* (например claude-desktop), node или cowork* (признак рабочей ВМ; ВР-11);
# ps или grep не выполнились — тоже отказ. Назначение — ошибка владельца: в рабочей ВМ агент переписал бы и саму
# проверку. Копия функции — в ops/review.sh (скрипт самодостаточен), совпадение проверяет шаг ctl_shared: правка
# здесь без правки там красит стенд.
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

guard_user() {
	[ "$(id -un)" = "$SA_OPS_USER" ] || refuse "не та учётная запись (нужна учётная запись раскатки)"
	[ -O "$HOME" ] && [ "$(stat -c %a -- "$HOME")" = 700 ] || refuse "дом учётной записи раскатки должен быть 0700"
}

# Сессия (design §16.4, ВР-6, ВР-13): правильное окружение для файла токена, сокета агента и rootless Podman —
# XDG_RUNTIME_DIR живёт в сессии. Графика разрешена (Type — tty, x11 или wayland): в ops-ВМ нет агентов, которым
# есть что перехватывать. Удалённая сессия — отказ: SSH-сервера нет, значит, это не то окружение.
# XDG_SESSION_ID в терминале GNOME не задан (gnome-terminal-server — потомок systemd --user, не scope сессии), поэтому
# без него сессия берётся из Display учётной записи (графическая сессия); проверки дальше те же. State — active или
# online (сессия без фокуса после переключения консоли — то же окружение); closing и прочее — отказ: runtime-каталог
# вот-вот исчезнет.
guard_session() {
	local out line t='' u='' r='' s='' uid sid=${XDG_SESSION_ID:-}
	uid=$(id -u)
	if [ -z "$sid" ]; then
		sid=$(loginctl show-user "$uid" -p Display --value 2>/dev/null) || sid=''
		[ -n "$sid" ] || refuse "нет сессии: ни XDG_SESSION_ID, ни графической сессии учётной записи"
	fi
	out=$(loginctl show-session "$sid" -p Type -p User -p Remote -p State 2>/dev/null) || refuse "сессия не определена"
	while IFS= read -r line; do
		case $line in
			Type=*) t=${line#Type=} ;;
			User=*) u=${line#User=} ;;
			Remote=*) r=${line#Remote=} ;;
			State=*) s=${line#State=} ;;
		esac
	done <<<"$out"
	case $t in
		tty | x11 | wayland) ;;
		*) refuse "тип сессии не tty, x11 или wayland" ;;
	esac
	[ "$u" = "$uid" ] || refuse "сессия принадлежит другой учётной записи"
	[ "$r" = no ] || refuse "сессия удалённая"
	case $s in
		active | online) ;;
		*) refuse "сессия не активна (State не active и не online)" ;;
	esac
	[ "${XDG_RUNTIME_DIR:-}" = "/run/user/$uid" ] || refuse "XDG_RUNTIME_DIR не /run/user/<uid>"
	[ -d "$XDG_RUNTIME_DIR" ] && [ -O "$XDG_RUNTIME_DIR" ] && [ "$(stat -c %a -- "$XDG_RUNTIME_DIR")" = 700 ] ||
		refuse "XDG_RUNTIME_DIR чужой или не 0700"
}

guard_env() {
	local n
	[ -z "${CLAUDECODE+x}" ] || refuse "запуск из сессии помощника (CLAUDECODE)"
	[ -z "${TF_VAR_clo_api_token+x}" ] && [ -z "${CLO_API_AUTH_TOKEN+x}" ] || refuse "токен провайдера уже в окружении"
	while IFS= read -r n; do
		case $n in
			TF_LOG*) refuse "задан TF_LOG*: журнал tofu мог бы записать токен" ;;
			ANSIBLE_KEEP_REMOTE_FILES | ANSIBLE_*WORLD_READABLE*) refuse "задана опасная настройка ANSIBLE_*" ;;
		esac
	done < <(compgen -e)
}

# Клон: чистое дерево, отсоединённый HEAD, без alternates, fsck объектов включён.
guard_repo() {
	local st
	[ -d "$repo/.git" ] || refuse "код раскатки — не обычный git-клон"
	owned_safe "клон" "$repo"
	# alternates первым: с битым путём git сам падает на любой команде.
	[ ! -e "$repo/.git/objects/info/alternates" ] || refuse "в клоне есть objects/info/alternates"
	st=$(git -C "$repo" status --porcelain 2>/dev/null) || refuse "git status в клоне не выполнился"
	[ -z "$st" ] || refuse "дерево клона грязное"
	if git -C "$repo" symbolic-ref -q HEAD >/dev/null 2>&1; then refuse "HEAD не отсоединён"; fi
	[ "$(git -C "$repo" config --get --type=bool transfer.fsckObjects 2>/dev/null)" = true ] &&
		[ "$(git -C "$repo" config --get --type=bool fetch.fsckObjects 2>/dev/null)" = true ] ||
		refuse "в клоне не включён fsck объектов (transfer.fsckObjects, fetch.fsckObjects)"
}

# Белый список символов в раскатываемых файлах HEAD (шаблон — побайтово, UTF-8; находит строки, где есть хоть один
# символ вне списка): \t, печатный ASCII, кириллица U+0400-04FF, § © « » ° № → ≠ ≤ ≥ — …. Всё остальное —
# управляющие, невидимые (нулевой ширины, теги U+E0000, селекторы вариантов, разделители строк U+2028/2029,
# заполнители вроде U+3164) и символы смены направления: ими прячут строки от глаз; перечнем запретов их не
# исчерпать, поэтому список разрешённого. Двойники ASCII (en dash, типографские кавычки, знак умножения) не
# входят: в less они читаются как - ' " x и молча выключили бы проверку в скрипте. Кириллица остаётся
# (комментарии по-русски); кириллический двойник в коде - остаток, рубеж тот же просмотр диффа. Тот же шаблон -
# в ops/review.sh (там он работает до checkout) и в runbook (deploy/README.md); совпадение трёх копий
# проверяет шаг ctl_shared.
# shellcheck disable=SC2016  # шаблон PCRE, не раскрытие
ctl_pattern='^(?!(?:[\t\x20-\x7e]|[\xd0-\xd3][\x80-\xbf]|\xc2[\xa7\xa9\xab\xb0\xbb]|\xe2\x80[\x94\xa6]|\xe2\x84\x96|\xe2\x86\x92|\xe2\x89[\xa0\xa4\xa5])*+$)'

# Раскатываемое — только обычные файлы: симлинк или gitlink вывели бы чтение и монтирование за пределы клона.
# Режимы смотрятся по ВСЕМУ дереву коммита, а не по pathspec deploy/: симлинк на месте самого deploy, web или
# web/viewer pathspec не видит (ls-tree -r по нему пуст, grep и diff тоже). Плюс явные типы: deploy — дерево,
# страница 404 — файл. Тот же разбор — в ops/review.sh (до checkout).
guard_modes() { # <коммит>
	local out bad
	out=$(git -C "$repo" ls-tree -r "$1") || refuse "git ls-tree не выполнился"
	bad=$(awk '$1 != "100644" && $1 != "100755"' <<<"$out")
	[ -z "$bad" ] || refuse "в дереве коммита есть симлинк, gitlink или нестандартный режим файла"
	[ "$(git -C "$repo" cat-file -t "$1:deploy" 2>/dev/null)" = tree ] || refuse "deploy в коммите — не каталог"
	[ "$(git -C "$repo" cat-file -t "$1:web/viewer/notfound.html" 2>/dev/null)" = blob ] || refuse "страница 404 в коммите — не файл"
}

# Что читает владелец до «yes», не должно прятать строки: .gitattributes (меняет показ и выгрузку) и
# символы вне белого списка. Первый раз .gitattributes в diff от пустого дерева — читать, не отказ.
guard_content() {
	local base=$1 rc
	guard_modes HEAD
	if [ -n "$base" ] && [ -n "$(git -C "$repo" diff --name-only "$base" HEAD -- ':(glob)**/.gitattributes' 2>/dev/null)" ]; then
		refuse "изменён .gitattributes после прошлого развёртывания"
	fi
	# -a: бинарные файлы тоже как текст (NUL не должен прятать файл). Код 0 — нашли, 1 — чисто, иное — ошибка.
	LC_ALL=C git -C "$repo" grep -a -l -P "$ctl_pattern" HEAD -- deploy/ web/viewer/notfound.html .github go.mod go.sum >/dev/null 2>&1
	rc=$?
	case $rc in
		1) ;;
		0) refuse "в файлах deploy/, .github, go.mod, go.sum или в странице 404 есть символы вне белого списка (управляющие, невидимые)" ;;
		*) refuse "проверка управляющих символов не выполнилась" ;;
	esac
}

# Пути секретов, inventory, копий и bundle: вне чужих git-деревьев; inventory может быть своим git-деревом.
# Владелец и права — у inventory, каталога копий и каталога файла секретов вместе со всеми родителями.
guard_paths() {
	local p r
	r=$(git_root_of "$SA_INVENTORY")
	{ [ -z "$r" ] || [ "$r" = "$(realpath -m -- "$SA_INVENTORY")" ]; } || refuse "SA_INVENTORY внутри чужого git-дерева"
	for p in "$SA_BACKUP_DIR" "$SA_SECRETS"; do
		[ -z "$(git_root_of "$p")" ] || refuse "SA_BACKUP_DIR или SA_SECRETS внутри git-дерева"
	done
	owned_safe SA_INVENTORY "$SA_INVENTORY"
	owned_safe SA_BACKUP_DIR "$SA_BACKUP_DIR"
	owned_safe "каталог SA_SECRETS" "$(dirname -- "$SA_SECRETS")"
}

guard_files() { # [nosecrets]: prepare идёт без секретов, файла может ещё не быть; проверка token в *.tfvars остаётся
	local f
	if [ "${1:-}" != nosecrets ]; then
		[ -f "$SA_SECRETS" ] || refuse "нет файла секретов"
		{ [ -O "$SA_SECRETS" ] && [ "$(stat -c %a -- "$SA_SECRETS")" = 600 ]; } || refuse "файл секретов чужой или не 0600"
	fi
	for f in "$SA_INVENTORY"/tofu/*.tfvars; do
		[ -f "$f" ] || continue
		if grep -qi token "$f"; then refuse "в *.tfvars упомянут token: токен только в файле секретов"; fi
	done
}

# Состав deploy/tofu/clo: только *.tf, tofu-entry.sh, .terraform.lock.hcl и tests/*.tftest.hcl; ни симлинков, ни
# подкаталогов, ни *.tofu и *.tf.json (их tofu тоже читает — решение И2-4). Тот же список проверяет tofu_static.
guard_tofu_files() {
	local d="$repo/deploy/tofu/clo" e n
	for e in "$d"/* "$d"/.[!.]*; do
		[ -e "$e" ] || [ -L "$e" ] || continue
		n=${e##*/}
		[ ! -L "$e" ] || refuse "в deploy/tofu/clo симлинк"
		case $n in
			tests) [ -d "$e" ] || refuse "deploy/tofu/clo/tests не каталог" ;;
			tofu-entry.sh | .terraform.lock.hcl) [ -f "$e" ] || refuse "в deploy/tofu/clo неожиданный каталог" ;;
			*.tf) [ -f "$e" ] || refuse "в deploy/tofu/clo неожиданный каталог" ;;
			*) refuse "в deploy/tofu/clo лишний файл (разрешены *.tf, tofu-entry.sh, .terraform.lock.hcl, tests/)" ;;
		esac
	done
	if [ -n "$(find "$d/tests" -mindepth 1 \( -type d -o -type l -o ! -name '*.tftest.hcl' \) -print -quit 2>/dev/null)" ]; then
		refuse "в deploy/tofu/clo/tests допустимы только файлы *.tftest.hcl"
	fi
}
