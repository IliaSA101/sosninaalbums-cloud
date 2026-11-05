#!/usr/bin/env bash
# Локальный стенд раскатки (спека 002, design §12): цель — Debian 13 с systemd в rootless Podman, куда встают
# плеи Ansible, и шаги проверок без сервера. Сети и токена провайдера здесь нет; на хост ничего не ставится
# (инструменты — в контейнерах по дайджесту, кроме shellcheck), sudo не нужен.
#
#   bash deploy/tests/provision/run.sh up                поднять цель (вход: пользователь образа debian по ключу)
#   bash deploy/tests/provision/run.sh status            состояние, адрес цели
#   bash deploy/tests/provision/run.sh probe [команда]   выполнить команду на цели по ssh из соседнего контейнера
#   bash deploy/tests/provision/run.sh listener on|off   слушатель 8080 (для verify_nft_ports)
#   bash deploy/tests/provision/run.sh down [--images]   убрать цель, сеть, ключи (--images: и собранные образы)
#   bash deploy/tests/provision/run.sh static [шаг]      проверки без цели (все или один; шаги — ниже)
#   bash deploy/tests/provision/run.sh <шаг>             шаг на поднятой цели (converge, verify; по одному за запуск)
#
# Шаги static: tofu_fmt tofu_validate tofu_test tofu_static ansible_lint yamllint syntax_check shellcheck selftest
# provision_cli ctl_shared (запуск контроллера собирается одним кодом ops/ctl-run.sh для provision.sh и стенда).
# Каждый можно запустить отдельно: `run.sh yamllint`. Чего ещё нет в дереве (deploy/tofu, deploy/ansible) —
# SKIP с причиной; появилось — шаг обязателен. Код выхода 1, если упал хоть один.
# Шаги на цели (нужен `up`; идут по порядку; сети провайдера нет, плеи — в контейнере контроллера):
# converge_bootstrap converge_site idempotence verify_ssh verify_nft verify_host verify_service
# (converge.sh, verify.sh, service.sh), verify_backup_stream (stream.sh); только SA_STAND_FULL=1 (full.sh):
# converge_image, idempotence_full, verify_acl, verify_health, verify_after_restart — без него SKIP (кроме CI, ci.sh),
# затем мутации: mutate_restore_a/b/c/d mutate_caddy mutate_caddy_b mutate_policy mutate_host mutate_backup — каждая
# защитная проверка ролей podman, caddy, albums_svc, backup_access обязана краснеть на своей мутации (service.sh).
# Локально задачи albums-runtime пропускаются (rootless-цель); SA_STAND_FULL=1 — полный прогон на rootful-цели (CI).
#
# Имена цели, сети, образа и каталога ключей несут суффикс от пути рабочей копии (SA_TGT_ID задаёт свой):
# две копии на одной машине не ломают друг другу стенд. Каталог ключей — SA_TGT_WORK (по умолчанию
# $XDG_RUNTIME_DIR/sa-tgt-<суффикс>); он обязан быть 0700, свой, не симлинк.
#
# Цель запускается с --systemd=always и --cap-add NET_ADMIN,SYS_ADMIN,DAC_READ_SEARCH,SYS_PTRACE: без NET_ADMIN nft
# даёт EPERM, без SYS_ADMIN юниты с песочницей падают с 226/NAMESPACE, DAC_READ_SEARCH нужен службе бэкапа,
# SYS_PTRACE — ради ss -p в verify_listeners (без него процессы чужих пользователей, например caddy, видны как «?»).
# Ключ стенда и known_hosts — в каталоге ключей, не в репозитории и не в выводе; ключ только для цели.
# shellcheck source-path=SCRIPTDIR
set -uo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"
# shellcheck source=static.sh
. "$here/static.sh"
# shellcheck source=converge.sh
. "$here/converge.sh"
# shellcheck source=verify.sh
. "$here/verify.sh"
# shellcheck source=service.sh
. "$here/service.sh"
# shellcheck source=stream.sh
. "$here/stream.sh"
# shellcheck source=full.sh
. "$here/full.sh"
# shellcheck source=tofu_static.sh
. "$here/tofu_static.sh"
# shellcheck source=provision_cli.sh
. "$here/provision_cli.sh"
# shellcheck source=ctl_static.sh
. "$here/ctl_static.sh"

# Префикс sa-tgt-: sa-prov-* зарезервирован за контейнерами provision.sh (у них драйвер журнала none и проверка).
id="${SA_TGT_ID:-$(printf '%s' "$root" | sha256sum | cut -c1-8)}"
case "$id" in
	'' | *[!a-z0-9]*) die "SA_TGT_ID: только строчные латинские буквы и цифры" ;;
esac
net="sa-tgt-net-$id"
tgt="sa-tgt-target-$id"
tgt_image="localhost/sa-tgt-target:$id"
steps=(tofu_fmt tofu_validate tofu_test tofu_static ansible_lint yamllint syntax_check shellcheck selftest provision_cli ctl_shared)
# Шаги на цели: в `static` без аргумента не входят (цели там нет), запускаются по имени.
stand_steps=(converge_image converge_bootstrap converge_site idempotence idempotence_full verify_ssh verify_nft verify_host
	verify_acl verify_service verify_health verify_after_restart verify_backup_stream mutate_restore_a mutate_restore_b mutate_restore_c mutate_restore_d mutate_caddy mutate_caddy_b mutate_policy mutate_host
	mutate_backup)

usage() {
	sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

# Путь каталога ключей; вызывается один раз сверху (die в $(…) вышел бы только из подоболочки).
work_path() {
	if [ -n "${SA_TGT_WORK:-}" ]; then
		case "$SA_TGT_WORK" in /*) ;; *) die "SA_TGT_WORK: нужен абсолютный путь" ;; esac
		[ "$SA_TGT_WORK" != / ] || die "SA_TGT_WORK: корень не годится"
		echo "$SA_TGT_WORK"
		return 0
	fi
	[ -n "${XDG_RUNTIME_DIR:-}" ] || die "нет XDG_RUNTIME_DIR (сеанс пользователя) и не задан SA_TGT_WORK: ключи стенда — не в общем /tmp"
	echo "$XDG_RUNTIME_DIR/sa-tgt-$id"
}

# Каталог ключей: не симлинк, владелец — текущий пользователь, права 0700. Иначе чужой процесс подложил бы
# свой ключ или знал бы наш. Нет каталога — не ошибка (down, status).
work_check() {
	[ -e "$work" ] || [ -L "$work" ] || return 0
	[ ! -L "$work" ] || die "каталог ключей $work — симлинк"
	if [ ! -d "$work" ] || [ ! -O "$work" ]; then die "каталог ключей $work не каталог или чужой"; fi
	[ "$(stat -c %a "$work")" = 700 ] || die "каталог ключей $work должен быть 0700"
}

target_ip() {
	podman inspect --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" "$tgt" 2>/dev/null
}

# Вход на цель из отдельного контейнера (тот же образ, без systemd): адрес источника — не контроллера,
# а ключ хоста сверяется со снимком, взятым при up (StrictHostKeyChecking=yes). -F /dev/null и
# IdentityAgent=none: ни настроек, ни агента из окружения — только ключ стенда.
probe() {
	local ip
	ip=$(target_ip)
	[ -n "$ip" ] || die "цель не поднята (run.sh up)"
	podman run --rm --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--tmpfs /tmp --tmpfs /root --entrypoint ssh \
		-v "$sd/id_stand:/key:ro" -v "$sd/known_hosts:/kh:ro" "$tgt_image" \
		-F /dev/null -i /key -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
		-o UserKnownHostsFile=/kh -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o LogLevel=ERROR \
		"debian@$ip" "$@"
}

# Сведения о цели при провале up, пока контейнер ещё есть (после уборки логи потеряны). Каждая команда — «как получится»:
# часть недоступна в rootless-режиме или не установлена, это не повод терять остальное. Секретов в выводе нет.
up_diag() {
	echo "== диагностика цели (up не удался) ==" >&2
	podman ps -a --format '{{.Names}} {{.Status}} сети={{.Networks}}' >&2 || true
	podman container exists "$tgt" || { echo "контейнера цели уже нет (он не запустился или был убран)" >&2; return 0; }
	podman inspect --format 'State: {{.State.Status}} running={{.State.Running}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} error={{.State.Error}}' "$tgt" >&2 || true
	echo "-- podman logs цели (последние 200 строк)" >&2
	podman logs --tail 200 "$tgt" >&2 || true
	echo "-- сеть $net" >&2
	podman network inspect "$net" >&2 || true
	echo "-- внутри цели: адреса, маршруты, слушатели, сбойные юниты, журнал" >&2
	podman exec "$tgt" sh -c 'ip -br addr; ip -4 route; ip -6 route; ip neigh; ss -ltn; networkctl status eth0 --no-pager; systemctl --failed --no-legend --no-pager; tail -n 15 /run/cloud-init/ds-identify.log; journalctl -n 80 --no-pager' >&2 || true
}

# После запуска цели любой отказ убирает стенд: брошенная цель держала бы имя и сеть. До уборки — up_diag.
# SA_STAND_KEEP_ON_FAIL=1 (CI) оставляет цель и сеть для последующего разбора (`ci.sh diag`); их уберёт down.
up_failed=1
up_cleanup() {
	[ "$up_failed" -eq 0 ] && return 0
	up_diag
	[ -n "${SA_STAND_KEEP_ON_FAIL:-}" ] || cmd_down >/dev/null 2>&1
}

cmd_up() {
	local state ip hostkey out extra=() _
	need podman ssh-keygen
	# Добавочные флаги podman run для цели (только стенд): в CI — userns, AppArmor, /dev/fuse для вложенного Podman.
	# Делятся по пробелам; значения — из окружения задания, не из ввода извне.
	[ -z "${SA_STAND_TARGET_FLAGS:-}" ] || read -r -a extra <<<"$SA_STAND_TARGET_FLAGS"
	podman container exists "$tgt" && die "цель уже поднята (run.sh down)"
	(umask 077 && mkdir -p "$work") || die "каталог ключей $work"
	work_check
	stand_ctl_init || die "каталог стенда $sd"
	[ -f "$sd/id_stand" ] || ssh-keygen -q -t ed25519 -N '' -C sa-tgt-stand -f "$sd/id_stand" || die "ssh-keygen"

	out=$(podman build -q -t "$tgt_image" -f "$here/target.Containerfile" "$here/target" 2>&1) || {
		printf '%s\n' "$out" | tail -n 20 >&2
		die "не собралась цель"
	}
	trap up_cleanup EXIT
	trap 'exit 130' INT TERM
	podman network exists "$net" || podman network create "$net" >/dev/null || die "сеть $net"
	# ACME-серверы на 127.0.0.1: Caddy на цели не должен ходить за настоящим сертификатом (design §12).
	# Псевдоним sa-target — адрес цели в inventory стенда: контроллер ходит по имени, а не по IP.
	podman run -d --name "$tgt" --network "$net" --network-alias sa-target --hostname sa-target \
		--systemd=always --cap-add NET_ADMIN,SYS_ADMIN,DAC_READ_SEARCH,SYS_PTRACE \
		--add-host acme-v02.api.letsencrypt.org:127.0.0.1 --add-host acme-staging-v02.api.letsencrypt.org:127.0.0.1 \
		--add-host acme.zerossl.com:127.0.0.1 \
		"${extra[@]}" "$tgt_image" >/dev/null || die "цель не запустилась"

	# Шина systemd поднимается не сразу: пока её нет, is-system-running сам падает (ждём до ~60 с).
	for _ in $(seq 60); do
		state=$(podman exec "$tgt" systemctl is-system-running 2>&1) || true
		case $state in running | degraded) break ;; esac
		sleep 1
	done
	if [ "$state" != running ]; then
		echo "provision: systemd на цели в состоянии «$state», ожидали running" >&2
		podman exec "$tgt" systemctl --failed --no-legend --no-pager >&2 || true
		exit 1
	fi

	# Ключ — как после cloud-init: authorized_keys пользователя образа.
	podman exec -i "$tgt" sh -c 'umask 077; h=$(getent passwd debian | cut -d: -f6)
		install -d -m 0700 -o debian -g debian "$h/.ssh" &&
		cat > "$h/.ssh/authorized_keys" && chown debian:debian "$h/.ssh/authorized_keys"' \
		<"$sd/id_stand.pub" || die "ключ не установлен"
	ip=$(target_ip)
	hostkey=$(podman exec "$tgt" cat /etc/ssh/ssh_host_ed25519_key.pub | cut -d' ' -f1,2)
	if [ -z "$ip" ] || [ -z "$hostkey" ]; then die "нет адреса или ключа хоста цели"; fi
	(umask 077 && printf '%s %s\nsa-target %s\n' "$ip" "$hostkey" "$hostkey" >"$sd/known_hosts")

	# Вход по ключу и sudo без пароля (как у пользователя образа). Сеть цели и sshd могут быть готовы позже, чем systemd
	# доложил running; «No route to host» — неотвеченный ARP, «Connection refused» — sshd ещё не слушает. Повторяем
	# до ~90 с и каждый раз проверяем, что контейнер жив: остановился — провал сразу, без ожидания остатка.
	out=''
	for _ in $(seq 30); do
		[ "$(podman inspect --format '{{.State.Running}}' "$tgt" 2>/dev/null)" = true ] || {
			echo "provision: цель остановилась во время ожидания входа (код выхода: $(podman inspect --format '{{.State.ExitCode}}' "$tgt" 2>&1))" >&2
			exit 1
		}
		out=$(probe 'sudo -n true && id -un' 2>&1) && break
		sleep 3
	done
	[ "$out" = debian ] || {
		echo "provision: вход debian не удался за ~90 с: $out" >&2
		exit 1
	}
	up_failed=0
	echo "цель готова: $tgt, адрес $ip, вход debian по ключу ($sd/id_stand), sudo без пароля, systemd running"
}

cmd_status() {
	local ip
	podman container exists "$tgt" || {
		echo "цель не поднята"
		return 0
	}
	ip=$(target_ip)
	echo "цель: $tgt, адрес $ip, systemd: $(podman exec "$tgt" systemctl is-system-running 2>&1)"
}

cmd_listener() {
	case "${1:-}" in
		on) podman exec "$tgt" systemctl start sa-tgt-listener.service ;;
		off) podman exec "$tgt" systemctl stop sa-tgt-listener.service ;;
		*) die "listener on|off" ;;
	esac
}

# Каждое действие проверяется: ошибка — код 1 (но остальное всё равно убираем).
cmd_down() {
	local images=${1:-0} rc=0 d
	need podman
	if podman container exists "$tgt"; then
		# Таймаут короткий: у цели нет данных, ради которых стоило бы ждать штатной остановки.
		podman rm -f -t 5 "$tgt" >/dev/null || rc=1
	fi
	full_down "$images" || rc=1
	if podman network exists "$net"; then
		podman network rm "$net" >/dev/null || rc=1
	fi
	# Только свои файлы: SA_TGT_WORK мог указать на каталог с чужим содержимым — rm -rf по нему недопустим.
	# Ключ, known_hosts, пароли, снимок кода, inventory Ansible и сокет агента — в собственном каталоге стенда
	# (converge.sh, stand_ctl); он удаляется целиком, но только с файлом-меткой: без неё это не наш каталог и rm -rf
	# по нему недопустим (SA_TGT_WORK мог указать на дом или рабочий каталог).
	d=$(stand_ctl)
	if [ -d "$d" ] && [ ! -L "$d" ] && [ -f "$d/.sa-tgt-owned" ] && [ ! -L "$d/.sa-tgt-owned" ]; then
		rm -rf -- "$d" || rc=1
	elif [ -e "$d" ] || [ -L "$d" ]; then
		echo "provision: $d без метки стенда: не удаляю" >&2
		rc=1
	fi
	if [ -d "$work" ]; then rmdir -- "$work" 2>/dev/null || true; fi
	if [ "$images" = 1 ]; then
		if podman image exists "$tgt_image"; then podman rmi "$tgt_image" >/dev/null || rc=1; fi
		podman images --format '{{.Repository}}:{{.Tag}}' --filter 'reference=localhost/sa-tgt-ctl' |
			xargs -r podman rmi >/dev/null || rc=1
	fi
	[ "$rc" -eq 0 ] || {
		echo "provision: стенд убран не полностью (код 1)" >&2
		return 1
	}
	echo "стенд убран"
}

# Запуск шагов: строка на шаг (OK/SKIP/FAIL), вывод инструментов — перед ней. Падение шага не обрывает
# остальные: все результаты видны в одном прогоне.
run_steps() {
	local s rc failed=() label=static
	need podman
	# Итоговая строка называется по тому, что запускали: «static: чисто» про шаги на цели было бы неправдой.
	for s in "$@"; do is_static_step "$s" || label=цель; done
	for s in "$@"; do
		skip_reason=''
		"step_$s"
		rc=$?
		case $rc in
			0) echo "OK   $s" ;;
			77) echo "SKIP $s — $skip_reason" ;;
			*)
				echo "FAIL $s (код $rc)"
				failed+=("$s")
				;;
		esac
	done
	if [ "${#failed[@]}" -gt 0 ]; then
		echo "$label: упало — ${failed[*]}"
		return 1
	fi
	echo "$label: чисто"
}

is_static_step() {
	local s
	for s in "${steps[@]}"; do [ "$s" = "$1" ] && return 0; done
	return 1
}

is_step() {
	local s
	is_static_step "$1" && return 0
	for s in "${stand_steps[@]}"; do [ "$s" = "$1" ] && return 0; done
	return 1
}

# Лишние и неизвестные аргументы — отказ (опечатка `down --image` не должна тихо сработать как `down`).
noargs() { [ "$1" -eq 0 ] || usage; }

cmd=${1:-}
[ $# -eq 0 ] || shift
case "$cmd" in
	up | down | status | probe | listener | converge_* | idempotence* | verify_* | mutate_*)
		work=$(work_path) || exit 1
		work_check
		sd=$(stand_ctl) # собственный каталог стенда с меткой: ключи, пароли, снимок кода, inventory Ansible
		;;
esac
case "$cmd" in
	up) noargs $# && cmd_up ;;
	down)
		case "${1:-}" in
			'') cmd_down 0 ;;
			--images) [ $# -eq 1 ] || usage; cmd_down 1 ;;
			*) usage ;;
		esac
		;;
	status) noargs $# && cmd_status ;;
	probe) probe "$@" ;;
	listener) [ $# -eq 1 ] || usage; cmd_listener "$1" ;;
	static)
		if [ $# -eq 0 ]; then
			run_steps "${steps[@]}"
		elif [ $# -eq 1 ] && is_static_step "$1"; then
			run_steps "$1"
		else
			die "static [шаг]; шаги: ${steps[*]}"
		fi
		;;
	*)
		if [ $# -eq 0 ] && [ -n "$cmd" ] && is_step "$cmd"; then
			run_steps "$cmd"
		else
			usage
		fi
		;;
esac
