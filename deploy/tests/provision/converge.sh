#!/usr/bin/env bash
# Шаги converge стенда раскатки (design 002 §12): плеи Ansible на цели. Только source из run.sh (после lib.sh).
#
#   converge_bootstrap  bootstrap.yml под пользователем образа debian → вход администратора по ключу стенда и
#                       sudo по паролю → повтор bootstrap.yml уже от администратора: changed=0 → пользователь
#                       образа заблокирован
#   converge_site       site.yml под администратором (в нём пользователь образа удаляется)
#   idempotence         site.yml ещё раз: changed=0, failed=0
#
# Плеи идут в контейнере контроллера (образ из deploy/ansible/Containerfile) тем же кодом, что у provision.sh:
# argv podman run, тома, окружение, контракт -e и канал vault собирает deploy/ops/ctl-run.sh (ctl_playbook).
# Здесь только своё: адрес цели (псевдоним sa-target в hosts.yml), каталог ключей, сеть стенда, ssh-agent с
# ключом стенда и снимок кода. Каталог inventory Ansible стенда (<каталог стенда>/ansible) повторяет настоящий: hosts.yml,
# group_vars/all/vars.yml (образец — deploy/tests/provision/inventory) и зашифрованный vault.yml (пароль
# администратора и его хэш). Секреты стенда случайные на каждый запуск (после `run.sh down` исчезают), в
# вывод не идут; admin_password читает только verify.sh снаружи, в контроллер он не попадает.
# shellcheck source-path=SCRIPTDIR disable=SC2154  # net, tgt, tgt_image, work, sd, here, root, id — из run.sh и lib.sh; skip_reason читает run.sh
# shellcheck source=../../ops/ctl-run.sh
. "$root/deploy/ops/ctl-run.sh"

# Собственный каталог стенда для снимка кода, inventory Ansible и сокета агента: <каталог ключей>/ctl-<id>, внутри
# файл-метка. Удаляется (run.sh down, пересборка снимка) только каталог с меткой: при SA_TGT_WORK, указывающем на
# чужое место (дом, рабочий каталог), rm -rf "$work/src" снёс бы чужое имя.
stand_ctl() { echo "$work/ctl-$id"; }

stand_ctl_init() {
	local d
	d=$(stand_ctl)
	if [ -e "$d" ] || [ -L "$d" ]; then
		if [ -d "$d" ] && [ ! -L "$d" ] && [ -f "$d/.sa-tgt-owned" ] && [ ! -L "$d/.sa-tgt-owned" ]; then return 0; fi
		echo "$d занят не стендом (нет метки .sa-tgt-owned): не трогаю"
		return 1
	fi
	(umask 077 && mkdir -p "$d" && : >"$d/.sa-tgt-owned") || return 1
}

# Версия состава vault стенда: поднимать при добавлении секретов.
stand_vault_ver=2

# Имя администратора стенда — то же, что admin_user в inventory/group_vars/all/vars.yml.
stand_admin=standadmin

need_target() {
	podman container exists "$tgt" || {
		echo "цель не поднята (run.sh up)"
		return 1
	}
}

# Секреты стенда (на каждый запуск, в каталоге ключей, 0600): пароль администратора, хэш (yescrypt, mkpasswd в
# контроллере) и пароль vault. Пароль и хэш лежат в зашифрованном ansible-vault vault.yml внутри inventory
# Ansible стенда (<каталог стенда>/ansible/group_vars/all); пароль vault — файл, значение которого ctl_playbook отдаёт
# контроллеру каналом /dev/fd/3, как у provision.sh. admin_password нужен только verify.sh (sudo снаружи). Хэш
# хранится внутри vault.yml, а не пересчитывается: у каждого вызова своя соль, повтор менял бы пароль и ломал
# идемпотентность.
stand_secrets() {
	local pw hash vp plain vf master
	stand_ctl_init || return 1
	vf="$(stand_ctl)/ansible/group_vars/all/vault.yml"
	# Версия состава секретов: старый vault (без vault_albums_master и т. п.) пересоздаётся; на живой цели это значит
	# run.sh down и up, пароль администратора на ней прежний.
	[ -s "$vf" ] && [ -s "$sd/vault_pass" ] && [ -s "$sd/admin_password" ] && [ "$(cat "$sd/vault_ver" 2>/dev/null)" = "$stand_vault_ver" ] && return 0
	pw=$(head -c 24 /dev/urandom | base64 | tr -d '+/=\n')
	vp=$(head -c 24 /dev/urandom | base64 | tr -d '+/=\n')
	if [ "${#pw}" -lt 16 ] || [ "${#vp}" -lt 16 ]; then
		echo "не получилось случайных паролей"
		return 1
	fi
	hash=$(printf '%s' "$pw" | podman run --rm -i --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--network=none --log-driver=none "$(lint_image)" mkpasswd --method=yescrypt --stdin) || {
		echo "mkpasswd не сработал"
		return 1
	}
	(umask 077 && mkdir -p "${vf%/*}" && printf '%s' "$pw" >"$sd/admin_password" && printf '%s' "$vp" >"$sd/vault_pass") || return 1
	# Главный секрет сервиса (base64url, 43 знака = 32 байта): роль albums_svc требует его в vault.
	master=$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')
	plain=$(printf "vault_admin_password: '%s'\nvault_admin_password_hash: '%s'\nvault_albums_master: '%s'\n" "$pw" "$hash" "$master")
	# Шифрование — в контроллере, файл только в его tmpfs; пароль vault — дескриптором 3, открытый текст — stdin.
	(umask 077 && printf '%s\n' "$plain" | podman run --rm -i --preserve-fds=1 --read-only --cap-drop=ALL \
		--security-opt=no-new-privileges --network=none --log-driver=none --tmpfs /tmp:rw,mode=1777 \
		-e HOME=/tmp -e ANSIBLE_HOME=/tmp/ansible "$(lint_image)" sh -c '
			cat >/tmp/v.yml && ansible-vault encrypt --vault-password-file=/dev/fd/3 /tmp/v.yml >/dev/null && cat /tmp/v.yml
		' 3< <(cat "$sd/vault_pass") >"$vf") || {
		echo "ansible-vault не зашифровал секреты стенда"
		rm -f -- "$vf"
		return 1
	}
	head -n 1 "$vf" | grep -q '^[$]ANSIBLE_VAULT;' || {
		echo "vault.yml стенда не зашифрован"
		rm -f -- "$vf"
		return 1
	}
	printf '%s' "$stand_vault_ver" >"$sd/vault_ver"
}

# Снимок кода для контроллера: как клон у provision.sh, но из рабочей копии (стенд проверяет и то, что ещё не
# закоммичено). Приватный deploy/inventory в снимок не попадает; страница 404 — на своём месте, как в клоне.
# Обновляется перед каждым плеем: правки кода между шагами видны сразу.
stand_src() {
	local s
	stand_ctl_init || return 1
	s="$(stand_ctl)/src"
	rm -rf -- "$s" && mkdir -p "$s/web/viewer" || return 1
	tar -C "$root" -c --exclude=deploy/inventory deploy | tar -C "$s" -x || return 1
	cp -- "$root/web/viewer/notfound.html" "$s/web/viewer/notfound.html"
}

# Inventory Ansible стенда: hosts.yml в том же формате, что пишет provision.sh apply; vars.yml — образец из
# репозитория (ключ администратора — тот же файл /run/sa/id_server.pub, что монтирует ctl-run.sh).
stand_inv() {
	local d
	d="$(stand_ctl)/ansible/group_vars/all"
	(umask 077 && mkdir -p "$d") || return 1
	ctl_write_hosts "$(stand_ctl)/ansible/hosts.yml" sa-target || return 1
	cp -- "$here/inventory/group_vars/all/vars.yml" "$d/vars.yml"
	# Полный режим: образ сервиса — тот, что converge_image собрал и положил в реестр стенда (full.sh). Файл читается
	# после vars.yml (порядок по имени), поэтому его значение перекрывает выдуманный дайджест образца. Нет файла —
	# отказ: молча взять образец значило бы, что полный прогон идёт не с тем образом.
	rm -f -- "$d/zz-stand-image.yml"
	if [ -n "${SA_STAND_FULL:-}" ]; then
		[ -s "$sd/albums_image" ] || { echo "SA_STAND_FULL=1: нет образа сервиса (сначала run.sh converge_image)"; return 1; }
		printf 'albums_svc_image: "%s"\n' "$(cat "$sd/albums_image")" >"$d/zz-stand-image.yml"
	fi
}

# ctl_ansible <плей> [аргументы ansible-playbook…]: плей на цели через ctl_playbook (код общий с provision.sh).
# boot_user (необязательно) — bootstrap_user: по умолчанию пользователь образа. ssh-agent — свой на каждый
# запуск, с ключом стенда (как у provision.sh: ключ в агенте, контейнеру — только сокет и открытая часть);
# завершается по своему PID при любом выходе из подоболочки.
ctl_ansible() {
	stand_ctl_init || return 1
	local ctl_repo ctl_image ctl_name="sa-tgt-ctl-$id-$$" ctl_inv ctl_agent_sock ctl_known_hosts="$sd/known_hosts" ctl_pubfile="$sd/id_stand.pub" ctl_vault
	local ctl_domain=albums.example.org ctl_admin=$stand_admin ctl_backup=sa-backup ctl_bootstrap=${boot_user:-debian}
	local ctl_imguser=debian ctl_podman_extra=(--network "$net")
	ctl_repo="$(stand_ctl)/src" ctl_inv="$(stand_ctl)/ansible" ctl_agent_sock="$(stand_ctl)/agent.sock"
	stand_src && stand_inv || return 1
	ctl_image=$(lint_image)
	ctl_vault=$(cat "$sd/vault_pass") || return 1
	(
		trap '[ -z "${apid:-}" ] || kill "$apid" 2>/dev/null; rm -f -- "$ctl_agent_sock"' EXIT
		trap 'exit 130' INT TERM
		rm -f -- "$ctl_agent_sock"
		out=$(ssh-agent -a "$ctl_agent_sock" -s) || exit 1
		apid=$(sed -n 's/^SSH_AGENT_PID=\([0-9][0-9]*\);.*/\1/p' <<<"$out")
		[ -n "$apid" ] || exit 1
		SSH_AUTH_SOCK=$ctl_agent_sock ssh-add -q "$sd/id_stand" </dev/null || exit 1
		ctl_playbook "$@"
	)
}

# play <аргументы ansible-playbook…>: запуск с разбором PLAY RECAP. Заполняет play_out, play_changed, play_failed.
# Возврат 0 — ansible вернул 0 и итог найден; вывод при провале показывается целиком.
play_out='' play_changed='' play_failed=''
play() {
	local rc recap
	play_out=$(ctl_ansible "$@" 2>&1)
	rc=$?
	# SA_STAND_VERBOSE=1 — вывод плея целиком (разбор; значения секретов скрыты no_log).
	[ -z "${SA_STAND_VERBOSE:-}" ] || printf '%s\n' "$play_out"
	recap=$(printf '%s\n' "$play_out" | grep -E '^server +: ok=' | tail -n 1)
	play_changed=$(printf '%s' "$recap" | sed -n 's/.* changed=\([0-9]*\).*/\1/p')
	play_failed=$(printf '%s' "$recap" | sed -n 's/.* failed=\([0-9]*\).*/\1/p')
	if [ "$rc" -ne 0 ] || [ -z "$play_changed" ] || [ "$play_failed" != 0 ]; then
		printf '%s\n' "$play_out" | tail -n 80
		echo "плей $1: код $rc, итог: ${recap:-нет}"
		return 1
	fi
}

# Какие задачи изменили хост (для разбора, почему повтор не дал changed=0).
changed_tasks() {
	printf '%s\n' "$play_out" | awk '/^TASK \[/ {t=$0} /^changed: / {print t}' | sort -u
}

expect_unchanged() { # <имя прогона>
	[ "$play_changed" = 0 ] && return 0
	echo "$1: ожидали changed=0, получили changed=$play_changed; изменили:"
	changed_tasks
	return 1
}

# Вход из отдельного контейнера (свой адрес, чтобы штрафы sshd не задели адрес контроллера). Ключ — только
# стенда. stand_ssh <пользователь> [ssh-опции…] [-- команда…]: stdin — как у вызвавшего.
stand_ssh() {
	local user=$1 ip opts=()
	shift
	while [ $# -gt 0 ] && [ "$1" != -- ]; do
		opts+=("$1")
		shift
	done
	[ $# -eq 0 ] || shift
	ip=$(target_ip)
	[ -n "$ip" ] || return 1
	podman run --rm -i --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--tmpfs /tmp --tmpfs /root --entrypoint ssh \
		-v "$sd/id_stand:/key:ro" -v "$sd/known_hosts:/kh:ro" "$tgt_image" \
		-F /dev/null -o IdentityAgent=none -o BatchMode=yes -o UserKnownHostsFile=/kh \
		-o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o LogLevel=ERROR -i /key -o IdentitiesOnly=yes \
		"${opts[@]}" "$user@$ip" "$@"
}

step_converge_bootstrap() {
	local out
	need_target || return 1
	ensure_lint_image || return 1
	stand_secrets || return 1
	boot_user=debian play bootstrap.yml || return 1
	echo "  bootstrap.yml под пользователем образа: changed=$play_changed failed=$play_failed"
	[ "$play_changed" -gt 0 ] || {
		echo "первый bootstrap.yml ничего не изменил — цель не чистая?"
		return 1
	}
	# Вход администратора по ключу стенда — отдельным контейнером, после чего sudo просит пароль.
	out=$(stand_ssh "$stand_admin" -- 'id -un' </dev/null 2>&1) || {
		echo "вход $stand_admin по ключу не удался: $out"
		return 1
	}
	[ "$out" = "$stand_admin" ] || {
		echo "вошли не тем пользователем: $out"
		return 1
	}
	echo "  вход $stand_admin по ключу стенда: ok"
	# Повтор от администратора: bootstrap_user — он сам; пользователь образа уже заблокирован.
	boot_user=$stand_admin play bootstrap.yml || return 1
	echo "  bootstrap.yml повторно (от администратора): changed=$play_changed failed=$play_failed"
	expect_unchanged "bootstrap.yml повторно" || return 1
	play verify.yml --tags verify_users -e verify_image_user_state=locked || return 1
	echo "  пользователь образа заблокирован (verify_users): failed=$play_failed"
}

# Локально (rootless-цель) задачи albums-runtime пропускаются: в карте uid цели нет uid процесса сервиса
# (начало subuid + 65531), вложенный rootless Podman не стартует (design §12, research §2). SA_STAND_FULL=1 — полный
# прогон на rootful-цели (CI, задача П2).
site_tags() {
	if [ -n "${SA_STAND_FULL:-}" ]; then site_skip=(); else site_skip=(--skip-tags albums-runtime); fi
}

step_converge_site() {
	local site_skip
	need_target || return 1
	ensure_lint_image || return 1
	stand_secrets || return 1
	site_tags
	# Полный режим: причину провала albums-runtime (crun, cgroup, subuid) вывод плея не показывает — снимаем с цели.
	play site.yml "${site_skip[@]}" || {
		[ -z "${SA_STAND_FULL:-}" ] || albums_diag
		return 1
	}
	echo "  site.yml: changed=$play_changed failed=$play_failed"
}

# Пробный вход идёт через delegate_to: localhost (соединение local): с pipelining модуль едет через stdin, а
# stdin с паролем не ложится файлом в tmp контроллера. Проверка по подробному выводу (-vvv; секреты в нём
# скрыты no_log, сам вывод не печатается): в разделе задачи нет ни mkdir, ни ansible-tmp, ни передачи файла.
probe_tmp_clean() {
	local section
	section=$(printf '%s\n' "$play_out" |
		awk '/^TASK \[/ {on = ($0 ~ /Пробный вход администратора и sudo -S -v/)} on')
	if [ -z "$section" ]; then
		echo "verify_probe_no_tmp: в выводе нет задачи пробного входа"
		return 1
	fi
	# Без строки запуска python через stdin проверка прошла бы впустую (например, если задача перестала выполняться).
	if ! printf '%s\n' "$section" | grep -Eq 'EXEC /bin/sh -c .*python'; then
		echo "verify_probe_no_tmp: в разделе пробного входа нет строки запуска python (EXEC /bin/sh -c …python)"
		return 1
	fi
	if printf '%s\n' "$section" | grep -Eq 'ansible-tmp|mkdir|PUT |sftp|scp'; then
		echo "verify_probe_no_tmp: при пробном входе модуль ложится файлом в tmp контроллера:"
		printf '%s\n' "$section" | grep -E 'ansible-tmp|mkdir|PUT |sftp|scp' | head -n 5
		return 1
	fi
	echo "  ok   verify_probe_no_tmp (пробный вход без файлов модуля в tmp контроллера)"
}

step_idempotence() {
	local site_skip
	need_target || return 1
	ensure_lint_image || return 1
	stand_secrets || return 1
	site_tags
	play site.yml -vvv "${site_skip[@]}" || return 1
	echo "  site.yml повторно: changed=$play_changed failed=$play_failed"
	expect_unchanged "site.yml повторно" || return 1
	probe_tmp_clean
}
