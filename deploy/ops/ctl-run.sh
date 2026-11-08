#!/usr/bin/env bash
# Запуск контроллера Ansible: единственное место, где собирается argv `podman run … ansible-playbook …`.
# Его source'ят provision.sh (раскатка) и стенд (deploy/tests/provision/converge.sh), поэтому зелёный стенд
# доказывает именно тот запуск, который выполнит provision.sh configure: флаги контейнера, тома, окружение,
# контракт -e (решение И3-5), пароль vault каналом — одни и те же строки кода, а не две копии, которые можно
# сверять. Проверка по построению — шаг `ctl_shared` стенда (ctl_static.sh).
#
# Граница доверия (решения И2-1, И2-2): файл лежит в deploy/ops/ рядом с guard.sh и secrets.sh — его читает
# просмотр кода (ops/review.sh, review_code), его режим и содержимое проверяют guard_modes и guard_content, а
# provision.sh подключает его только по пути из просмотренного клона (`$here/ops/…`). В ~/.local/libexec/sa
# он не копируется: там лежат только скрипты, которые идут без консоли, а этот исполняется лишь внутри
# provision.sh. Из тестового дерева provision.sh ничего не подключает — стенд подключает этот файл, не наоборот.
#
# Параметры — переменные вызывающего (local в его функции, не export: значение пароля не должно попасть в
# окружение podman):
#   ctl_repo        каталог с deploy/ и web/viewer/notfound.html (клон; у стенда — снимок рабочей копии)
#   ctl_image       образ контроллера
#   ctl_name        имя контейнера
#   ctl_inv         каталог inventory Ansible (hosts.yml, group_vars/) → /inv, только чтение
#   ctl_agent_sock  сокет ssh-agent с ключом входа на сервер
#   ctl_known_hosts файл known_hosts
#   ctl_pubfile     открытая часть ключа входа (одна строка ed25519)
#   ctl_vault       пароль vault (значение; уходит каналом на дескриптор 3)
#   ctl_domain ctl_admin ctl_backup ctl_bootstrap ctl_imguser   albums_domain, admin_user, backup_user,
#                   bootstrap_user, image_user
#   ctl_podman_extra  (массив, необязателен) добавочные флаги podman — только у стенда: сеть
# shellcheck disable=SC2154  # ctl_*: параметры задаёт вызывающий

ctl_podman_extra=()
ctl_cmd=()

# nolock <команда…>: запуск с закрытым дескриптором блокировки provision.sh (lock_fd — файл inventory/.lock).
# Блокировку наследуют все дети; демон среди них (агент ключей, conmon podman) пережил бы SIGKILL provision.sh и
# держал бы блокировку, а следующий прогон отказывал бы. У стенда lock_fd нет — запуск как есть.
nolock() {
	if [ -n "${lock_fd:-}" ]; then "$@" {lock_fd}>&-; else "$@"; fi
}

ctl_fail() {
	echo "контроллер: $*" >&2
	return 1
}

# Файл hosts.yml для inventory: одна цель по имени server. Пишет provision.sh (apply, hosts: адрес из tofu output) и
# стенд (псевдоним цели в сети стенда) — один формат, который читает host_ip. Запись — hosts_write из ops/hosts.sh
# (временный файл, mv -T, симлинк — отказ): его подключает вызывающий рядом с этим файлом.
ctl_write_hosts() { # <путь> <адрес>
	hosts_write "$1" "$2"
}

ctl_json_list() {
	local first=1 a
	printf '['
	for a in "$@"; do
		[ "$first" = 1 ] || printf ','
		first=0
		printf '"%s"' "$a"
	done
	printf ']'
}

# ctl_build <плей> [аргументы ansible-playbook…]: заполняет массив ctl_cmd целиком (без перенаправлений).
# Дополнительные аргументы идут перед --vault-password-file, который всегда последний.
ctl_build() {
	local play=$1 pub f base ctl_ssh
	shift
	# Обычные файлы и каталог по каноническому пути: симлинк на месте самого deploy, web или web/viewer (а не только
	# файла) вывел бы монтирование за пределы клона — поэтому realpath равен пути от канонического корня клона.
	base=$(realpath -- "$ctl_repo") || ctl_fail "нет каталога кода" || return 1
	for f in "$base/web/viewer/notfound.html" "$ctl_pubfile"; do
		if [ ! -f "$f" ] || [ -L "$f" ]; then ctl_fail "ожидается обычный файл (не симлинк): ${f##*/}" || return 1; fi
	done
	[ "$(realpath -- "$base/web/viewer/notfound.html")" = "$base/web/viewer/notfound.html" ] ||
		ctl_fail "страница 404 лежит за симлинком (web или web/viewer)" || return 1
	if [ ! -d "$base/deploy" ] || [ -L "$base/deploy" ] || [ "$(realpath -- "$base/deploy")" != "$base/deploy" ]; then
		ctl_fail "deploy/ — не обычный каталог" || return 1
	fi
	# Те же аргументы, что в ssh_host_opts провизии, но с путями внутри контейнера: список sa_ssh_args (-e, JSON) —
	# для пробного входа роли bootstrap. ansible_ssh_common_args не передаётся: extra-vars перебили бы значение плеев.
	ctl_ssh=(-F /dev/null -o IdentitiesOnly=yes -o IdentityFile=/run/sa/id_server.pub -o IdentityAgent=/run/sa/agent.sock
		-o UserKnownHostsFile=/run/sa/known_hosts -o StrictHostKeyChecking=yes -o ConnectTimeout=10)
	read -r pub <"$ctl_pubfile" || ctl_fail "нет открытого ключа входа" || return 1
	[[ $pub =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+(\ [A-Za-z0-9@._-]+)?$ ]] ||
		ctl_fail "открытый ключ входа: ожидается ed25519 без кавычек в комментарии" || return 1
	# Контейнер: без журнала (conmon иначе пишет вывод в journald), без записи в корень, без capabilities и
	# повышения прав, лимит процессов; --preserve-fds=1 — канал с паролем vault. В контейнер — только deploy/ и
	# страница 404 (роли берут файлы по путям от playbook_dir), не весь репозиторий.
	ctl_cmd=(podman run --rm --log-driver=none --name "$ctl_name" --read-only --cap-drop=ALL
		--security-opt=no-new-privileges --pids-limit=512 --tmpfs "/tmp:rw,mode=1777" --preserve-fds=1
		"${ctl_podman_extra[@]}"
		-v "$base/deploy:/src/deploy:ro" -v "$base/web/viewer/notfound.html:/src/web/viewer/notfound.html:ro"
		-w /src/deploy/ansible -v "$ctl_inv:/inv:ro" -v "$ctl_agent_sock:/run/sa/agent.sock"
		-v "$ctl_known_hosts:/run/sa/known_hosts:ro" -v "$ctl_pubfile:/run/sa/id_server.pub:ro"
		-e HOME=/tmp/home -e ANSIBLE_HOME=/tmp/ansible -e ANSIBLE_NOCOLOR=1 -e ANSIBLE_CONFIG=/src/deploy/ansible/ansible.cfg
		"$ctl_image" ansible-playbook -i /inv/hosts.yml "$play"
		-e "albums_domain=$ctl_domain" -e "admin_user=$ctl_admin" -e "backup_user=$ctl_backup"
		-e "bootstrap_user=$ctl_bootstrap" -e "image_user=$ctl_imguser"
		-e "{\"sa_ssh_args\":$(ctl_json_list "${ctl_ssh[@]}"),\"sa_controller_pubkey\":\"$pub\"}"
		"$@" --vault-password-file=/dev/fd/3)
}

# ctl_playbook <плей> [аргументы ansible-playbook…]: запуск. Пароль vault — каналом на каждый запуск (канал
# читается один раз); значение ни в argv, ни в -e.
ctl_playbook() {
	ctl_build "$@" || return 1
	nolock "${ctl_cmd[@]}" 3< <(printf '%s' "$ctl_vault")
}
