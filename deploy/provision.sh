#!/usr/bin/env bash
# Точка входа владельца: подъём и настройка сервера, забор копии (спека 002, design §2, §16). Запускается ТОЛЬКО
# учётной записью раскатки в ВМ раскатки (метка машины) по полному пути из клона (runbook — deploy/README.md); tofu,
# ansible и age идут в контейнерах sa-prov-* (без журнала), в систему ничего не ставится. Новый код сначала
# просматривается ops/review.sh (до checkout, решение И2-1); просмотр здесь — второй взгляд.
#
#   ~/src/sosninaalbums-cloud/deploy/provision.sh up                      apply + configure
#   …/provision.sh prepare                     окно подготовки: провайдер архивом с GitHub, сборка контроллера (без секретов)
#   …/provision.sh apply [--replace-server]    tofu apply ровно по сохранённому плану: показ, проверка кодом, «yes» (ops/plan.sh)
#   …/provision.sh hosts [<IPv4>]              записать inventory/ansible/hosts.yml (apply оборвался после создания сервера)
#   …/provision.sh smoke [clean] | albums      дымовая проверка боевого сервера и список альбомов (токены — из файла секретов)
#   …/provision.sh configure                   bootstrap.yml (если входа администратора нет) и site.yml (образ — ровно проверенный)
#   …/provision.sh image-verify vX.Y.Z [--allow-downgrade]   проверка выпущенного образа без сети (спека 004, ops/image.sh)
#   …/provision.sh backup                      забрать копию, проверить метку конца, почистить старые
#   …/provision.sh forget-host                 забыть ключ хоста после пересоздания сервера
#
# Исходящая сеть ops-ВМ в раскатке закрыта (README §1, design §16.10): prepare — единственная команда, которой нужна
# сеть (GitHub — архив провайдера; apt Debian и PyPI — сборка контроллера), и единственная без секретов; остальные идут без сети и сами ничего не ставят.
# prepare берёт ещё gh, корень Sigstore и сверяет закреплённые действия (ops/tools.sh).
# Коды: 0 — готово; 1 — ошибка; 2 — не реализовано (способы B/C, провайдеры кроме clo); 3 — отказ до действий
# (guard.sh); 4 — нет сети до сервера. Окружение: SA_OPS_USER (sa-ops), SA_INVENTORY (~/inventory),
# SA_SECRETS (~/secrets.age), SA_BACKUP_DIR (~/backups), SA_INBOX (/srv/sa-inbox: входящие; bundle читает ops/review.sh, улики выпуска — image-verify).
# shellcheck source-path=SCRIPTDIR
set -uo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo=$(dirname -- "$here")
# shellcheck source=ops/guard.sh
. "$here/ops/guard.sh"
# shellcheck source=ops/secrets.sh
. "$here/ops/secrets.sh"
# shellcheck source=ops/backup-lib.sh
. "$here/ops/backup-lib.sh"
# shellcheck source=ops/ctl-run.sh
. "$here/ops/ctl-run.sh"
# shellcheck source=ops/mirror.sh
. "$here/ops/mirror.sh"
# shellcheck source=ops/image.sh
. "$here/ops/image.sh"
# shellcheck source=ops/tools.sh
. "$here/ops/tools.sh"
# shellcheck source=ops/probe.sh
. "$here/ops/probe.sh"
# shellcheck source=ops/hosts.sh
. "$here/ops/hosts.sh"
# shellcheck source=ops/plan.sh
. "$here/ops/plan.sh"
# shellcheck source=ops/api.sh
. "$here/ops/api.sh"

die() {
	echo "provision: $*" >&2
	exit 1
}

# load_kv <файл> <ключ…>: только перечисленные KEY=VALUE, без исполнения кода; уже заданное в окружении не затирается.
load_kv() {
	local f=$1 k v want
	shift
	[ -f "$f" ] || return 0
	while IFS='=' read -r k v; do
		for want in "$@"; do
			if [ "$k" = "$want" ] && [ -z "${!k:-}" ]; then printf -v "$k" '%s' "$v"; fi
		done
	done <"$f"
}

confirm() {
	local ans
	printf '%s [yes/no]: ' "$1" >/dev/tty
	IFS= read -r ans </dev/tty || return 1
	[ "$ans" = yes ]
}

# --- просмотр кода (второй взгляд; первый — ops/review.sh до checkout) --------------------------------------

# Полный diff deploy/ и страницы 404 от прошлого развёртывания в less: без R (управляющие последовательности
# показываются текстом, а не исполняются терминалом) и с LESSSECURE (без выхода в оболочку). Первый раз — от
# пустого дерева, весь текст.
review_code() {
	local base=${deployed:-$empty_tree}
	git -C "$repo" diff --text --no-color --no-ext-diff --no-textconv "$base" HEAD -- \
		deploy/ web/viewer/notfound.html .github go.mod go.sum ':(glob)**/.gitattributes' |
		LESS=-X LESSSECURE=1 LESSOPEN='' LESSCLOSE='' LESSHISTFILE=- less
	echo "HEAD: $(git -C "$repo" rev-parse HEAD)"
	git -C "$repo" diff --stat --no-color --no-ext-diff "$base" HEAD -- . ':(exclude)deploy' ':(exclude)web/viewer/notfound.html' \
		':(exclude).github' ':(exclude)go.mod' ':(exclude)go.sum'
	confirm "Прочитан весь diff выше, раскатывать этот HEAD?" || die "отменено"
}

# --- контроллер, ключи, inventory ------------------------------------------------------------------------

ctl_id=''
cmd_hint=': окно подготовки, provision.sh prepare' # в самом prepare подсказка была бы кругом — main её сбрасывает
ctl_prepare() {
	# Раскатка без сети: слои — из кэша; кэш стёрт или изменились Containerfile/requirements — сборка падает (так и надо).
	ctl_id=$(nolock podman build -q -f "$repo/deploy/ansible/Containerfile" "$repo/deploy/ansible") ||
		die "не собрался контроллер${cmd_hint}"
	[ -n "$ctl_id" ] || die "контроллер: пустой ID образа"
}

# Обычный файл, не симлинк, и по каноническому пути (то, что ставится в libexec): realpath равен самому пути, значит
# и каталоги по дороге — не симлинки (путь строится от here/repo, уже канонических).
plain_file() {
	if [ ! -f "$1" ] || [ -L "$1" ] || [ "$(realpath -- "$1")" != "$1" ]; then die "ожидается обычный файл (не симлинк): ${1#"$repo"/}"; fi
}

tmp_rm=() # файлы, которые cleanup убирает при любом выходе (ops/plan.sh, ops/api.sh)
# shellcheck disable=SC2317  # вызывается из trap
cleanup() {
	[ -z "$tok_file" ] || rm -f -- "$tok_file"
	[ "${#tmp_rm[@]}" -eq 0 ] || rm -f -- "${tmp_rm[@]}" # сохранённый план, его JSON, файл токена smoke
	[ -z "$iv_tmp" ] || rm -rf -- "$iv_tmp"
	[ -z "$agent_pid" ] || kill "$agent_pid" 2>/dev/null
	[ -z "$agent_sock" ] || rm -f -- "$agent_sock"
}

# Ключ грузится в агент только тот, что нужен команде: сервера — для apply/configure, забора — для backup.
load_keys() { # <server|backup>
	local k=$1 phrase
	[ -f "$HOME/.ssh/id_$k" ] || die "нет ключа ~/.ssh/id_$k"
	phrase=$s_ssh_phrase
	[ "$k" = backup ] && phrase=$s_backup_phrase
	agent_start || die "ssh-agent не запустился"
	agent_add "$HOME/.ssh/id_$k" "$phrase" || die "ключ $k не загружен в агент (неверная фраза?)"
}

known_hosts() { echo "$SA_INVENTORY/known_hosts"; }

host_ip() {
	local f="$SA_INVENTORY/ansible/hosts.yml" ip
	[ -f "$f" ] || die "нет $f: сначала apply"
	ip=$(sed -n 's/^ *ansible_host: *//p' "$f" | head -n 1)
	hosts_check_ip "$ip" 2>/dev/null || die "в hosts.yml нет допустимого адреса IPv4 (provision.sh hosts)"
	echo "$ip"
}

# shellcheck disable=SC2016  # $0 раскрывает внутренний bash
tcp22() { timeout 5 bash -c ': </dev/tcp/$0/22' "$1" 2>/dev/null; }

# Одни SSH-аргументы для пробных входов; ключ — открытая часть, закрытый — в агенте. -F /dev/null: ни
# настроек, ни прокси-команд из окружения.
ssh_host_opts() { # <имя ключа: server|backup>
	printf '%s\n' -F /dev/null -o IdentitiesOnly=yes -o "IdentityFile=$HOME/.ssh/id_$1.pub" -o "IdentityAgent=$agent_sock" \
		-o "UserKnownHostsFile=$(known_hosts)" -o StrictHostKeyChecking=yes -o ConnectTimeout=10
}

# Запуск плея в контроллере — общий с локальным стендом код (ops/ctl-run.sh): argv podman run, тома, окружение,
# контракт -e и канал с паролем vault собираются там, здесь только значения раскатки. Переменные ctl_* — local
# (не export): пароль vault не должен попасть в окружение podman.
ansible_run() { # <плей>
	local ctl_repo=$repo ctl_image=$ctl_id ctl_name="sa-prov-ansible-$$" ctl_inv="$SA_INVENTORY/ansible"
	local ctl_agent_sock=$agent_sock ctl_known_hosts ctl_pubfile="$HOME/.ssh/id_server.pub" ctl_vault=$s_vault_pass
	local ctl_domain=$SA_DOMAIN ctl_admin=$SA_ADMIN_USER ctl_backup=$SA_BACKUP_USER ctl_bootstrap=$SA_BOOTSTRAP_USER
	local ctl_imguser=$SA_IMAGE_USER
	ctl_known_hosts=$(known_hosts)
	ctl_playbook "$@"
}

# Копии скриптов, которые выполняются без консоли (таймер чистки) или до checkout (просмотр), ставит только
# успешный configure — из коммита, который владелец уже прочитал (решения И2-1, И2-2).
install_libexec() {
	local d="$HOME/.local/libexec/sa" u="$HOME/.config/systemd/user" f
	mkdir -p "$d" "$u" || return 1
	chmod 700 "$d" || return 1
	for f in review.sh sa-backup-prune.sh backup-lib.sh; do
		plain_file "$here/ops/$f"
		install -m 0700 "$here/ops/$f" "$d/.$f.new" && mv -f "$d/.$f.new" "$d/$f" || return 1
	done
	# Юниты таймера — копией при каждом configure (включить один раз: systemctl --user enable --now …timer).
	for f in sa-backup-prune.service sa-backup-prune.timer; do
		plain_file "$here/ops/$f"
		install -m 0600 "$here/ops/$f" "$u/.$f.new" && mv -f "$u/.$f.new" "$u/$f" || return 1
	done
	systemctl --user daemon-reload 2>/dev/null || true
}

# --- apply -----------------------------------------------------------------------------------------------

# Зеркало провайдера в state (ставит prepare, читают prepare и apply: init -plugin-dir, без реестра). Хвост пути — один,
# из него и путь в контейнере, и путь на хосте (они не могут разойтись).
mirror_tail=.terraform-clo/mirror
mirror_dir=/state/$mirror_tail

# tf <флаги podman…> -- <аргументы tofu…>; токен — только томом (apply), не -e. tf_entry=tofu tf …: без обёртки токена
# (prepare проверяет провайдер по lock командой, которой токен не нужен, а обёртка без файла токена её не пустит).
tf() {
	local pf=()
	while [ "$1" != -- ]; do
		pf+=("$1")
		shift
	done
	shift
	mkdir -p "$SA_INVENTORY/tofu"
	prov_run tofu "${pf[@]}" -e HOME=/tmp -e CHECKPOINT_DISABLE=1 -e TF_DATA_DIR=/state/.terraform-clo \
		-e "TF_VAR_ptr=$SA_DOMAIN" -v "$repo/deploy/tofu/clo:/work:ro" -v "$SA_INVENTORY/tofu:/state" -w /work \
		--entrypoint "${tf_entry:-/work/tofu-entry.sh}" "$SA_TOFU_IMAGE" "$@"
}

# Окно подготовки: единственное место, где ставятся и обновляются провайдер tofu (по lock) и образ контроллера. Секретов
# и сервера не трогает: ни файла секретов, ни агента, ни токена. Просмотр кода — отдельно, ops/review.sh до checkout.
# Провайдер — не из реестра (из России он закрыт, ВР-15): архив с GitHub, оба хэша сверены (ops/mirror.sh), init из
# зеркала без сети; init принимает пакет по любому хэшу lock (верный zh пропускает неверный h1), поэтому validate (тоже
# без сети) запускает провайдер и сверяет h1 установленного пакета с lock.
cmd_prepare() {
	guard_tofu_files
	mkdir -p "$SA_INVENTORY/tofu"
	mirror_fetch "$repo/deploy/tofu/clo/.terraform.lock.hcl" "$SA_INVENTORY/tofu/$mirror_tail"
	tf --network=none -- init -lockfile=readonly -input=false -plugin-dir="$mirror_dir" \
		-backend-config=path=/state/clo.tfstate || die "tofu init не прошёл (провайдер из зеркала)"
	tf_entry=tofu tf --network=none -- validate || die "провайдер не совпал с lock (h1) или validate не прошёл"
	ctl_prepare
	tools_prepare || die "окно Б: gh, корень Sigstore или закреплённые действия не готовы"
	echo "подготовка готова: провайдер tofu по lock, образ контроллера, gh, корень Sigstore, пины действий (README §4; в раскатку — §1)"
}

cmd_apply() {
	local replace=${1:-} extra=() out ip
	[ -f "$SA_INVENTORY/tofu/clo.tfvars" ] || die "нет inventory/tofu/clo.tfvars (образец — deploy/inventory.example/tofu/)"
	guard_tofu_files
	[ "$replace" = --replace-server ] && extra=(-replace=clo_compute_instance.srv)
	token_file_make
	# init без сети (раскатка): lock-файл строго из репозитория, провайдер — из зеркала, которое ставит prepare; без
	# -plugin-dir init опрашивал бы реестр и падал бы без сети. Токена init не получает. Подмену архива в зеркале
	# заметит сам apply: h1 установленного провайдера сверяется с lock при каждом запуске провайдера.
	tf --network=none -- init -lockfile=readonly -input=false -plugin-dir="$mirror_dir" \
		-backend-config=path=/state/clo.tfstate || die "провайдер не установлен или не совпал с lock: окно подготовки, provision.sh prepare"
	# Единственная дорога (ops/plan.sh): план сохранён, показан, проверен кодом и подтверждён; применяется именно он.
	plan_prepare "${extra[@]}"
	tf -it -v "$tok_file:/run/sa/token:ro" -- apply -input=false "$plan_ct" ||
		die "tofu apply не завершён; если сервер уже создан — provision.sh hosts запишет его адрес из state"
	rm -f -- "$tok_file" "$SA_INVENTORY/tofu/$plan_tail"
	tok_file=''
	# В state не должно быть закрытого ключа, пароля и секретов: иначе — перевыпуск (design §3). Без сети: провайдер
	# в show и output не ходит к API.
	out=$(tf --network=none -- show -json) || die "tofu show не прошёл"
	if grep -Eiq '"[a-z_]*(password|secret|token|private_key)[a-z_]*": *"[^"]' <<<"$out"; then
		die "в state попало значение password, secret, token или private_key: перевыпустить ключи и пароли, state не коммитить"
	fi
	ip=$(tf --network=none -- output -raw ipv4) || die "tofu output не прошёл"
	hosts_check_ip "$ip" || die "адрес из вывода tofu не прошёл проверку: сверить с панелью, затем provision.sh hosts <IPv4>"
	mkdir -p "$SA_INVENTORY/ansible"
	ctl_write_hosts "$SA_INVENTORY/ansible/hosts.yml" "$ip" || die "hosts.yml не записан"
	echo "сервер: адрес записан в inventory/ansible/hosts.yml; A-запись домена — владелец"
	hosts_report "$ip"
	wait_ssh "$ip"
}

wait_ssh() {
	local i=0 limit=${SA_WAIT_SSH_SEC:-600}
	echo "жду tcp/22 сервера (до $limit с)…"
	while ! tcp22 "$1"; do
		i=$((i + 5))
		if [ "$i" -gt "$limit" ]; then
			echo "provision: сервер не открыл tcp/22 за $limit с" >&2
			return 4
		fi
		sleep 5
	done
}

# --- configure -------------------------------------------------------------------------------------------

# Ключ ed25519 хоста: «тип ключ» первой строки (комментарии # отбрасываются).
hostkey_fields() { grep -v '^#' | head -n 1 | awk '{print $2" "$3}'; }

cmd_configure() {
	local ip kh out fp opts rc scanned known
	ip=$(host_ip) || exit 1
	kh=$(known_hosts)
	tcp22 "$ip" || {
		echo "provision: нет сети до сервера (tcp/22)" >&2
		return 4
	}
	if ! grep -q "^$ip " "$kh" 2>/dev/null; then
		# Отпечаток — на экран: владелец сверяет его в веб-консоли провайдера (ssh-keygen -lf на сервере).
		out=$(ssh-keyscan -t ed25519 "$ip" 2>/dev/null | grep -v '^#') || true
		[ -n "$out" ] || die "ssh-keyscan не получил ключ хоста"
		fp=$(ssh-keygen -lf - <<<"$out") || die "ключ хоста не разобран"
		echo "отпечаток ключа хоста: $fp"
		confirm "Совпадает с веб-консолью провайдера?" || die "ключ хоста не принят"
		(umask 077 && printf '%s\n' "$out" >>"$kh")
	fi
	mapfile -t opts < <(ssh_host_opts server)
	ssh "${opts[@]}" -o BatchMode=yes -o ConnectTimeout=10 -T "$SA_ADMIN_USER@$ip" true </dev/null >/dev/null 2>&1
	rc=$?
	case $rc in
		0) ghcr_probe "$SA_ADMIN_USER" "$ip" ;;
		255)
			# 255 — и «входа администратора ещё нет», и «ключ хоста сменился» (StrictHostKeyChecking=yes).
			# Различаем сверкой: тот же ключ, что в known_hosts, — идём в bootstrap; иначе отказ.
			scanned=$(ssh-keyscan -t ed25519 "$ip" 2>/dev/null | hostkey_fields)
			known=$(ssh-keygen -F "$ip" -f "$kh" 2>/dev/null | hostkey_fields)
			if [ -z "$scanned" ] || [ "$scanned" != "$known" ]; then
				die "ключ хоста не совпал с известным (сервер пересоздан или подмена): сверить отпечаток в веб-консоли, затем provision.sh forget-host"
			fi
			ghcr_probe "$SA_BOOTSTRAP_USER" "$ip"
			echo "входа администратора нет: bootstrap.yml под $SA_BOOTSTRAP_USER"
			ansible_run bootstrap.yml || die "bootstrap.yml не прошёл"
			;;
		*) die "пробный вход администратора: код $rc" ;;
	esac
	# Образ — только -e (выше любого файла inventory): ровно тот, что проверил image-verify для этого HEAD.
	ansible_run site.yml -e "albums_svc_image=$img_ref" || die "site.yml не прошёл"
	image_deployed_write || die "deployed-image не записан"
	git -C "$repo" rev-parse HEAD >"$SA_INVENTORY/deployed-commit"
	install_libexec || die "не поставились скрипты в ~/.local/libexec/sa"
}

# --- backup, forget-host ---------------------------------------------------------------------------------

cmd_backup() {
	local ip stamp final part last opts
	ip=$(host_ip) || exit 1
	tcp22 "$ip" || {
		echo "provision: нет сети до сервера (tcp/22)" >&2
		return 4
	}
	(umask 077 && mkdir -p "$SA_BACKUP_DIR") || die "каталог копий"
	chmod 700 "$SA_BACKUP_DIR"
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	final="$SA_BACKUP_DIR/$stamp.tar.zst.age"
	part="$final.part"
	mapfile -t opts < <(ssh_host_opts backup)
	# Команда на сервере задана ключом (restrict,command=): здесь её нет, зашифрованный поток — в файл .part.
	if ! (umask 077 && ssh "${opts[@]}" -o BatchMode=yes -T "$SA_BACKUP_USER@$ip" </dev/null >"$part"); then
		rm -f -- "$part"
		die "забор копии не удался"
	fi
	# Последний член — метка SA-END (её пишут только при успешном tar); расшифровка только в поток, имена
	# (в них id альбомов) не сохраняются: остаётся последняя строка.
	last=$(prov_run verify --network=none --preserve-fds=1 -v "$part:/in/copy:ro" "$ctl_id" bash -c \
		'set -o pipefail; age -d -i /dev/fd/3 </in/copy | zstd -dc | tar -t --ignore-zeros 2>/dev/null | tail -n 1' \
		3< <(printf '%s' "$s_age_identity")) || last=''
	if [ "$last" != SA-END ]; then
		rm -f -- "$part"
		die "в копии нет метки конца SA-END: копия отвергнута, .part удалён"
	fi
	mv -- "$part" "$final" || die "переименование копии"
	echo "копия сохранена и проверена"
	backup_prune "$SA_BACKUP_DIR"
}

cmd_forget_host() {
	local ip
	ip=$(host_ip) || exit 1
	confirm "Забыть ключ хоста $ip (сервер пересоздан)?" || die "отменено"
	ssh-keygen -R "$ip" -f "$(known_hosts)" >/dev/null 2>&1 || true
	rm -f -- "$(known_hosts).old"
	echo "ключ хоста забыт"
}

# --- main ------------------------------------------------------------------------------------------------

cmd=${1:-}
[ $# -eq 0 ] || shift
SA_OPS_USER=${SA_OPS_USER:-sa-ops}
SA_INVENTORY=${SA_INVENTORY:-$HOME/inventory}
SA_SECRETS=${SA_SECRETS:-$HOME/secrets.age}
SA_BACKUP_DIR=${SA_BACKUP_DIR:-$HOME/backups}

# Первыми — машина, учётная запись и сессия: остальное не имеет смысла вне машины раскатки.
guard_machine
guard_user
guard_session
guard_env
guard_repo
case $cmd:$# in
	up:0 | configure:0 | backup:0 | forget-host:0 | apply:0 | prepare:0 | image-verify:1 | hosts:0 | hosts:1 | smoke:0 | albums:0) ;;
	smoke:1) [ "$1" = clean ] || refuse "smoke принимает только clean" ;;
	image-verify:2) [ "$2" = --allow-downgrade ] || refuse "image-verify принимает только --allow-downgrade" ;;
	apply:1) [ "$1" = --replace-server ] || refuse "apply принимает только --replace-server" ;;
	*) refuse "неизвестная команда или лишние аргументы (prepare, image-verify vX.Y.Z, up, apply [--replace-server], hosts [IPv4], configure, backup, forget-host, smoke [clean], albums)" ;;
esac
guard_paths

# Заданное в окружении побеждает файл (load_kv не затирает), поэтому значения здесь не обнуляются.
: "${SA_DOMAIN:=}" "${SA_START:=}" "${SA_PROVIDER:=}" "${SA_ADMIN_USER:=}" "${SA_BACKUP_USER:=}" "${SA_BOOTSTRAP_USER:=}" "${SA_IMAGE_USER:=}"
SA_TOFU_IMAGE=''
tf_entry='' # точку входа tofu задаёт только код (prepare → validate), не окружение
load_kv "$SA_INVENTORY/provision.env" SA_DOMAIN SA_START SA_PROVIDER SA_ADMIN_USER SA_BACKUP_USER SA_BOOTSTRAP_USER SA_IMAGE_USER
load_kv "$here/ops/images.env" SA_TOFU_IMAGE
SA_START=${SA_START:-bootstrap}
SA_PROVIDER=${SA_PROVIDER:-clo}
SA_BOOTSTRAP_USER=${SA_BOOTSTRAP_USER:-root}
SA_BACKUP_USER=${SA_BACKUP_USER:-sa-backup}
if [ "$SA_START" != bootstrap ] || [ "$SA_PROVIDER" != clo ]; then
	echo "provision: не реализовано: в этой версии только способ bootstrap и провайдер clo" >&2
	exit 2
fi
# Значения идут в -e контейнеров и в аргументы: только то, что похоже на домен и имя пользователя.
[[ -z $SA_DOMAIN || $SA_DOMAIN =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || refuse "SA_DOMAIN не похож на домен"
for u in SA_ADMIN_USER SA_BACKUP_USER SA_BOOTSTRAP_USER SA_IMAGE_USER; do
	[[ -z ${!u} || ${!u} =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || refuse "$u: допустимы строчные латинские имена"
done
# prepare — без секретов: файла может ещё не быть (первый раз подготовка идёт до него), проверка token в *.tfvars остаётся.
if [ "$cmd" = prepare ] || [ "$cmd" = image-verify ] || [ "$cmd" = hosts ]; then guard_files nosecrets; else guard_files; fi

empty_tree=4b825dc642cb6eb9a060e54bf8d69288fbee4904
deployed=''
if [ -s "$SA_INVENTORY/deployed-commit" ]; then
	deployed=$(head -n 1 "$SA_INVENTORY/deployed-commit")
	git -C "$repo" cat-file -e "$deployed^{commit}" 2>/dev/null || die "deployed-commit неизвестен клону: обновить клон из bundle"
fi
guard_content "$deployed"
# Предупреждение о копиях — только после первой раскатки (есть deployed-commit) и не в prepare: до сервера копий быть не
# может, и «забор остановлен?» пугает ложно; prepare с бэкапом не связан вовсе.
if [ "$cmd" != prepare ] && [ -n "$deployed" ]; then backup_freshness_warn "$SA_BACKUP_DIR"; fi

for t in podman git ssh ssh-agent ssh-add ssh-keygen ssh-keyscan less timeout install flock ps; do
	command -v "$t" >/dev/null || die "нужен $t"
done
if [ "$cmd" = image-verify ]; then
	for t in sha256sum tar cmp od dd stat; do command -v "$t" >/dev/null || die "нужен $t"; done
fi
if [ "$cmd" = prepare ]; then
	for t in curl sha256sum awk tar; do command -v "$t" >/dev/null || die "нужен $t"; done
fi
[[ $SA_TOFU_IMAGE =~ @sha256:[0-9a-f]{64}$ ]] || die "образ tofu не по дайджесту (ops/images.env)"
if [ "$cmd" != backup ] && [ "$cmd" != forget-host ] && [ "$cmd" != prepare ] && [ "$cmd" != image-verify ]; then
	if [ -z "$SA_DOMAIN" ] || [ -z "$SA_ADMIN_USER" ]; then die "в provision.env нужны SA_DOMAIN и SA_ADMIN_USER"; fi
fi

# Один прогон за раз: два параллельных писали бы в state, hosts.yml и known_hosts одновременно.
(umask 077 && mkdir -p "$SA_INVENTORY") || die "каталог inventory"
exec {lock_fd}>"$SA_INVENTORY/.lock"
flock -n "$lock_fd" || die "другой прогон provision.sh уже идёт (блокировка inventory/.lock)"
# Остатки прошлого прогона (убитого без trap): файл токена и сокеты агентов — только те, чьего PID уже нет.
for f in "$XDG_RUNTIME_DIR"/sa-token-* "$XDG_RUNTIME_DIR"/sa-agent-*.sock "$XDG_RUNTIME_DIR"/sa-plan-*.json "$XDG_RUNTIME_DIR"/sa-state-*.json; do
	[ -e "$f" ] || continue
	pid=${f##*-}
	pid=${pid%.*}
	if [[ $pid =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then continue; fi
	rm -f -- "$f"
done
trap cleanup EXIT
trap 'exit 130' INT TERM
trap 'exit 129' HUP

if [ "$cmd" = forget-host ]; then
	cmd_forget_host
	exit $?
fi
if [ "$cmd" = prepare ]; then
	cmd_hint=''
	cmd_prepare
	exit $?
fi
case $cmd in
	image-verify)
		ctl_prepare
		cmd_image_verify "$@"
		exit $?
		;;
	up | configure) image_gate || exit $? ;; # до вопросов и затрат: образ проверен для этого HEAD и нигде больше не задан
esac
review_code
# hosts — после просмотра кода (tofu output исполняет обёртку из клона), но до контроллера и секретов: ни то, ни другое не нужно.
if [ "$cmd" = hosts ]; then
	cmd_hosts "$@"
	exit $?
fi
ctl_prepare
secrets_load "$ctl_id" || exit 1
case $cmd in
	backup) load_keys backup ;;
	smoke | albums) ;; # по ssh не ходят
	*) load_keys server ;;
esac
case $cmd in
	smoke) cmd_api smoke "${1:-}" ;;
	albums) cmd_api albums ;;
	apply) cmd_apply "${1:-}" ;;
	up)
		cmd_apply && cmd_configure
		;;
	configure) cmd_configure ;;
	backup) cmd_backup ;;
esac
exit $?
