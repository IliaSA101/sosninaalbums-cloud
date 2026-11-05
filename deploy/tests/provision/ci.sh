#!/usr/bin/env bash
# Обёртка полного стенда для CI (.github/workflows/ci.yml, задание stand-full; design 002 §12, решение П2a-5): шаги
# run.sh идут от root (`sudo`, rootful Podman раннера) с SA_STAND_FULL=1. Только раннер GitHub: одноразовая машина, где
# права root не страшны; на машине разработки не запускать — ей хватает run.sh без sudo (rootless, без albums-runtime).
#
#   bash deploy/tests/provision/ci.sh prepare        сведения о Podman раннера; сеть netavark; диапазон subuid для userns=auto
#   bash deploy/tests/provision/ci.sh run <шаг>      один шаг run.sh (up, converge_image, converge_site, … down)
#   bash deploy/tests/provision/ci.sh diag           состояние цели и журнал для разбора упавшего прогона (секретов в них нет)
#
# Цель запускается с --userns=auto (root цели — не root хоста), поэтому нужен диапазон `containers` в /etc/subuid и
# /etc/subgid, больше чем у вложенных пользователей цели (albums: начало + 65536 = 265536). Остальные флаги — ради
# вложенного rootless Podman в цели: AppArmor раннера запрещает mount внутри контейнера, маскировка /proc мешает
# смонтировать свежий procfs, /dev/fuse нужен fuse-overlayfs (гипотезы: подтверждает первый прогон, design §12).
# seccomp стандартный: гипотеза П2-8 (профиль режет вложенный crun) опровергнута — «exit status 255» давал exec проверки
# здоровья через /bin/sh, которого нет в distroless-образе (П2-9; HealthCmd теперь в exec-форме). Причину любого нового
# провала albums-runtime назовёт albums_diag.sh (выводится при провале converge_site и в diag).
# Переопределяется SA_STAND_TARGET_FLAGS. Ключи стенда — в $RUNNER_TEMP, вне рабочей копии; секретов задания нет.
set -euo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work="${RUNNER_TEMP:-/tmp}/sa-stand"
flags="${SA_STAND_TARGET_FLAGS:---userns=auto:size=300000 --security-opt=apparmor=unconfined --security-opt=unmask=ALL --device=/dev/fuse}"

stand() { # <аргументы run.sh…>
	# SA_STAND_KEEP_ON_FAIL: провалившийся up не убирает цель, чтобы шаг diag увидел её состояние (run.sh, up_cleanup).
	local envs=(SA_STAND_FULL=1 SA_TGT_ID=ci "SA_TGT_WORK=$work" "SA_STAND_TARGET_FLAGS=$flags" SA_STAND_KEEP_ON_FAIL=1)
	[ -z "${SA_STAND_VERBOSE:-}" ] || envs+=("SA_STAND_VERBOSE=$SA_STAND_VERBOSE")
	sudo env "${envs[@]}" bash "$here/run.sh" "$@"
}

cmd_prepare() {
	local f backend restrict
	if ! command -v podman >/dev/null; then echo "ci: на раннере нет podman" >&2; return 1; fi
	podman --version
	for f in /etc/subuid /etc/subgid; do
		if ! grep -q '^containers:' "$f"; then echo 'containers:2000000:1000000' | sudo tee -a "$f" >/dev/null; fi
	done
	if ! getent passwd containers >/dev/null; then
		sudo useradd --system --no-create-home --shell /usr/sbin/nologin containers
	fi
	# На ubuntu-24.04 ядро по умолчанию ограничивает создание userns для непривилегированных процессов (AppArmor):
	# вложенный rootless Podman пользователя albums внутри цели не смог бы создать userns, и albums-runtime упал бы.
	# Раннер одноразовый, поэтому ограничение снимается; делать это нужно до up.
	restrict=$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || echo нет)
	echo "ci: kernel.apparmor_restrict_unprivileged_userns = $restrict"
	if [ "$restrict" = 1 ]; then sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0; fi
	# ГИПОТЕЗА (прогон 2: «No route to host» между контейнерами одной сети): на образе раннера стоит Docker, он загружает
	# br_netfilter и ставит политику FORWARD=DROP; кадры между контейнерами одного моста тогда идут через iptables и могут
	# быть сброшены правилами не для сети Podman. При 0 мостовой трафик в iptables не попадает; выход в интернет идёт
	# маршрутизацией (FORWARD сохраняется), раннер одноразовый.
	echo "ci: net.bridge.bridge-nf-call-iptables = $(sysctl -n net.bridge.bridge-nf-call-iptables 2>/dev/null || echo нет)"
	if [ "$(sysctl -n net.bridge.bridge-nf-call-iptables 2>/dev/null || echo 0)" = 1 ]; then
		sudo sysctl -w net.bridge.bridge-nf-call-iptables=0
	fi
	backend=$(sudo podman info --format '{{.Host.NetworkBackend}}')
	if [ "$backend" != netavark ]; then
		# Псевдоним цели в сети (sa-target) и имя реестра требуют DNS сети: у CNI его нет без плагина.
		echo "ci: сеть Podman — «$backend», нужен netavark: ставлю netavark и aardvark-dns"
		sudo apt-get update
		sudo apt-get install -y --no-install-recommends netavark aardvark-dns
		sudo podman system reset --force
		backend=$(sudo podman info --format '{{.Host.NetworkBackend}}')
		[ "$backend" = netavark ] || { echo "ci: после установки сеть Podman — «$backend»" >&2; return 1; }
	fi
	sudo podman info --format 'podman {{.Version.Version}}, сеть {{.Host.NetworkBackend}}, cgroups {{.Host.CgroupsVersion}}, хранилище {{.Store.GraphDriverName}}'
	grep '^containers:' /etc/subuid /etc/subgid
}

# Шаг и проверка, что он не пропущен: в CI SKIP — это непроверенное (полный режим включён, пропускать нечего).
cmd_run() { # <шаг>
	step_log=$(mktemp "${RUNNER_TEMP:-/tmp}/sa-ci-step.XXXXXX")
	# Журнал убирается при любом выходе, в том числе при провале шага (set -e выходит из pipefail до проверки SKIP);
	# переменная глобальная: к выходу из скрипта локальной уже нет.
	trap 'rm -f -- "$step_log"' EXIT
	stand "$1" | tee "$step_log"
	if grep -q '^SKIP ' "$step_log"; then
		echo "ci: шаг $1 пропущен (SKIP): в CI пропусков нет" >&2
		return 1
	fi
}

# Разбор упавшего прогона: печатает всегда, даже если контейнеров нет. Имена — sa-tgt-target-<id> и sa-tgt-registry-<id>
# при SA_TGT_ID=ci (stand()). Секретов в выводе нет.
cmd_diag() {
	echo "== ci diag: контейнеры, сети, образы =="
	sudo podman ps -a --format '{{.Names}} {{.Status}} сети={{.Networks}}' || true
	sudo podman network ls || true
	echo "== ci diag: сеть хоста (хост-мосты, соседи, фильтр) =="
	ip -br addr || true
	ip neigh || true
	sudo iptables -S FORWARD || true
	sudo nft list ruleset 2>/dev/null | head -n 120 || true
	sysctl net.bridge.bridge-nf-call-iptables net.ipv4.ip_forward || true
	if sudo podman container exists sa-tgt-target-ci; then
		echo "== ci diag: цель =="
		sudo podman inspect --format 'State: {{.State.Status}} running={{.State.Running}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} error={{.State.Error}}' sa-tgt-target-ci || true
		sudo podman logs --tail 200 sa-tgt-target-ci || true
		sudo podman exec sa-tgt-target-ci sh -c 'ip -br addr; ip -4 route; ip -6 route; ip neigh; ss -ltn; networkctl status eth0 --no-pager; tail -n 15 /run/cloud-init/ds-identify.log' || true
		sudo podman exec sa-tgt-target-ci systemctl --failed --no-pager || true
		sudo podman exec sa-tgt-target-ci journalctl -p warning -n 150 --no-pager || true
	else
		echo "ci diag: контейнера цели sa-tgt-target-ci нет"
	fi
	if sudo podman container exists sa-tgt-target-ci; then
		echo "== ci diag: albums-runtime (albums_diag.sh) =="
		sudo podman exec sa-tgt-target-ci sh -c "$(cat "$here/albums_diag.sh")" </dev/null 2>&1 || true
	fi
	if sudo podman container exists sa-tgt-registry-ci; then sudo podman logs --tail 30 sa-tgt-registry-ci || true; fi
}

case "${1:-}" in
	prepare) [ $# -eq 1 ] || { echo "ci.sh prepare" >&2; exit 2; }; cmd_prepare ;;
	run) [ $# -eq 2 ] || { echo "ci.sh run <шаг>" >&2; exit 2; }; cmd_run "$2" ;;
	diag) [ $# -eq 1 ] || { echo "ci.sh diag" >&2; exit 2; }; cmd_diag ;;
	*) echo "ci.sh prepare | run <шаг> | diag" >&2; exit 2 ;;
esac
