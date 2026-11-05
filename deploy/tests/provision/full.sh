#!/usr/bin/env bash
# Шаги полного режима стенда (SA_STAND_FULL=1, design 002 §12, задача П2): то, что локальная rootless-цель не тянет
# (в её карте uid нет uid процесса сервиса, вложенный rootless Podman не стартует) и что идёт в CI на rootful-цели.
# Только source из run.sh (после lib.sh, converge.sh, verify.sh). Без SA_STAND_FULL=1 каждый шаг — SKIP с причиной.
#
#   converge_image       образ сервиса собирается из deploy/Containerfile, кладётся в одноразовый реестр стенда
#                        (контейнер в сети цели, без TLS), цель получает его адрес в registries.conf.d; ссылка вида
#                        <адрес>:5000/albums-svc@sha256:<дайджест> уходит в inventory Ansible (converge.sh, stand_inv)
#   (albums_diag — не шаг: сведения о albums-runtime при провале converge_site, см. albums_diag.sh)
#   idempotence_full     idempotence с albums-runtime: changed=0 и задачи runtime не пропущены (healthcheck — ok)
#   verify_acl           verify_acl роли albums_svc: процесс сервиса читает config.json по ACL, Caddy и albums — нет
#   verify_health        /v1/health по сокету от имени caddy — 200; от nobody — отказ доступа
#   verify_after_restart перезапуск цели (как перезагрузка сервера): tmpfiles, linger, Quadlet, ACL сокета и Caddy
#                        встают сами, /v1/health отвечает, данные и их владелец те же
#
# Образ собирается тем же Containerfile, что и боевой, но не тем артефактом: боевой берётся из реестра владельца по
# дайджесту. Здесь проверяется, что ссылка по дайджесту, Quadlet, ACL и healthcheck работают с настоящим бинарником.
# shellcheck disable=SC2154  # tgt, net, id, root, sd, tgt_image — из run.sh, lib.sh и converge.sh; skip, skip_reason — из static.sh

# Реестр: распространение distribution v3, образ по дайджесту индекса (public.ecr.aws, как база цели: у docker.io
# лимиты на анонимные скачивания с общих адресов раннеров). Тег можно переписать на стороне реестра — только дайджест.
REGISTRY_IMAGE="${REGISTRY_IMAGE:-public.ecr.aws/docker/library/registry@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3}"
case "$REGISTRY_IMAGE" in
	*@sha256:????????????????????????????????????????????????????????????????) ;;
	*) echo "provision: REGISTRY_IMAGE должен быть вида имя@sha256:<64 hex>, а не тег" >&2; exit 1 ;;
esac

full_reg() { echo "sa-tgt-registry-$id"; }
full_img() { echo "localhost/sa-tgt-albums:$id"; }

full_required() {
	[ -n "${SA_STAND_FULL:-}" ] || skip 'только SA_STAND_FULL=1 (rootful-цель, CI)'
}

# Убрать реестр (и, с --images, собранный образ сервиса); вызывает cmd_down до удаления сети: пока реестр в сети, её не снять.
full_down() { # <0|1>
	local rc=0 reg img
	reg=$(full_reg)
	img=$(full_img)
	if podman container exists "$reg"; then podman rm -f -t 5 "$reg" >/dev/null || rc=1; fi
	if [ "${1:-0}" = 1 ] && podman image exists "$img"; then podman rmi "$img" >/dev/null || rc=1; fi
	return "$rc"
}

# albums_diag: сведения о albums-runtime на цели (albums_diag.sh выполняется внутри цели; скрипт — аргументом sh -c, а не
# через stdin: команды внутри не должны съесть его). Только чтение, секретов нет. Вызывается при провале converge_site
# в полном режиме и из ci.sh diag.
albums_diag() {
	podman exec "$tgt" sh -c "$(cat "$here/albums_diag.sh")" </dev/null 2>&1 || true
}

step_converge_image() {
	local reg img out port ip digest ref _
	full_required || return $?
	need_target || return 1
	need python3 || return 1
	stand_ctl_init || return 1
	reg=$(full_reg)
	img=$(full_img)
	out=$(podman build -q --ignorefile "$root/deploy/Containerfile.containerignore" -f "$root/deploy/Containerfile" \
		-t "$img" "$root" 2>&1) || {
		printf '%s\n' "$out" | tail -n 20
		echo "образ сервиса не собрался (deploy/Containerfile)"
		return 1
	}
	echo "  образ сервиса собран из deploy/Containerfile"
	# Реестр: корень только для чтения, без capabilities, данные — tmpfs (одноразовый); порт — только на 127.0.0.1
	# хоста (для push), цель берёт образ по адресу реестра в сети стенда.
	if ! podman container exists "$reg"; then
		podman run -d --name "$reg" --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges \
			--pids-limit=256 --memory=256m --tmpfs /var/lib/registry:rw -p 127.0.0.1::5000 "$REGISTRY_IMAGE" >/dev/null || {
			echo "реестр стенда не запустился"
			return 1
		}
	fi
	port=$(podman port "$reg" 5000/tcp | head -n 1 | sed 's/.*://')
	[[ $port =~ ^[0-9]+$ ]] || { echo "у реестра нет порта на 127.0.0.1: «$port»"; return 1; }
	for _ in $(seq 30); do
		python3 -I -c 'import sys, urllib.request; sys.exit(0 if urllib.request.urlopen(sys.argv[1], timeout=3).status == 200 else 1)' \
			"http://127.0.0.1:$port/v2/" 2>/dev/null && break
		sleep 1
	done
	rm -f -- "$sd/push.digest"
	podman push -q --tls-verify=false --digestfile "$sd/push.digest" "$img" "127.0.0.1:$port/albums-svc:stand" >/dev/null || {
		echo "push образа в реестр стенда не удался"
		return 1
	}
	digest=$(cat "$sd/push.digest")
	[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "дайджест после push не sha256:<64 hex>: «$digest»"; return 1; }
	ip=$(podman inspect --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" "$reg")
	[[ $ip =~ ^[0-9.]+$ ]] || { echo "нет адреса реестра в сети стенда: «$ip»"; return 1; }
	ref="$ip:5000/albums-svc@$digest"
	# Цель ходит к реестру без TLS: запись только для адреса стенда, роли файл не трогают и не проверяют.
	podman exec -i "$tgt" sh -c 'install -d -m 0755 /etc/containers/registries.conf.d &&
		cat >/etc/containers/registries.conf.d/50-sa-stand.conf' <<EOF || return 1
[[registry]]
location = "$ip:5000"
insecure = true
EOF
	(umask 077 && printf '%s' "$ref" >"$sd/albums_image") || return 1
	verify_ok "converge_image: $ref"
}

step_idempotence_full() {
	local first
	full_required || return $?
	step_idempotence || return 1
	# Повтор без изменений мог бы получиться и от пропущенных задач: healthcheck обязан быть «ok», а не «skipping».
	first=$(printf '%s\n' "$play_out" |
		awk '/^TASK \[albums_svc : Готовность — healthcheck контейнера\]/ {on = 1; next}
			on && /^(ok|changed|skipping|fatal|failed):/ {print $1; exit}')
	[ "$first" = 'ok:' ] || {
		echo "idempotence_full: healthcheck контейнера не выполнялся (итог задачи: «${first:-нет}»), albums-runtime пропущен?"
		return 1
	}
	verify_ok "idempotence_full: albums-runtime выполнен, повтор — changed=0"
}

step_verify_acl() {
	full_required || return $?
	need_target || return 1
	stand_secrets || return 1
	ansible_verify verify_acl verify_acl
}

# health_probe <пользователь> <группа>: GET /v1/health по сокету от имени пользователя на цели. Печатает первую строку
# ответа и тело через «|» или «denied» (нет прав на каталог или сокет); любая другая ошибка — код 1 и текст.
health_probe() {
	podman exec -i "$tgt" setpriv --reuid="$1" --regid="$2" --init-groups python3 -I -c '
import socket, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(5)
try:
    s.connect("/run/albums-svc/http.sock")
except PermissionError:
    print("denied")
    sys.exit(0)
s.sendall(b"GET /v1/health HTTP/1.1\r\nHost: albums-svc\r\nConnection: close\r\n\r\n")
data = b""
while True:
    chunk = s.recv(4096)
    if not chunk:
        break
    data += chunk
head, _, body = data.partition(b"\r\n\r\n")
print(head.split(b"\r\n")[0].decode() + "|" + body.decode().strip())
' 2>&1
}

# health_wait: ждёт ответ 200 с «api»: «v1» от caddy (до ~2 мин: после перезапуска контейнер сервиса стартует не сразу).
health_wait() {
	local out='' _
	for _ in $(seq 40); do
		out=$(health_probe caddy caddy) || out="ошибка: $out"
		case $out in 'HTTP/1.1 200 '*'"api":"v1"'*) return 0 ;; esac
		sleep 3
	done
	echo "  FAIL verify_health: от caddy нет ответа 200 с api v1: «$out»"
	return 1
}

step_verify_health() {
	local out
	full_required || return $?
	need_target || return 1
	health_wait || return 1
	verify_ok "verify_health: /v1/health по сокету от caddy — 200, api v1"
	# Отрицательный контроль: постороннему пользователю каталог сокета закрыт (other::---, ACL только у caddy).
	# Для albums отказ не проверяется: он следует из тех же прав (verify_albums сверяет ACL и режим точно), а зависит
	# от chown каталога на uid сервиса в карте subuid — лишняя ставка на первый прогон.
	out=$(health_probe nobody nogroup) || true
	[ "$out" = denied ] || {
		echo "  FAIL verify_health: от nobody ожидали отказ доступа, получили «$out»"
		return 1
	}
	verify_ok "verify_health: от nobody сокет закрыт"
}

# Состояние данных: владелец, права каталога и хэш имён верхнего уровня — до и после перезапуска. Нет каталога —
# ошибка (sh -e), а не совпадение двух пустых хэшей.
data_state() {
	podman exec "$tgt" sh -ec 'd=/var/lib/albums-svc/data; [ -d "$d" ]; stat -c "%u:%g:%a" "$d"; ls -A "$d" | sort | sha256sum'
}

# Ключ хоста цели переживает перезапуск, а адрес мог смениться: known_hosts переписывается, как при up.
refresh_known_hosts() {
	local ip hostkey
	ip=$(target_ip)
	hostkey=$(podman exec "$tgt" cat /etc/ssh/ssh_host_ed25519_key.pub | cut -d' ' -f1,2)
	if [ -z "$ip" ] || [ -z "$hostkey" ]; then return 1; fi
	(umask 077 && printf '%s %s\nsa-target %s\n' "$ip" "$hostkey" "$hostkey" >"$sd/known_hosts")
}

step_verify_after_restart() {
	local before after state _
	full_required || return $?
	need_target || return 1
	stand_secrets || return 1
	before=$(data_state) || return 1
	podman restart -t 30 "$tgt" >/dev/null || {
		echo "  FAIL verify_after_restart: цель не перезапустилась"
		return 1
	}
	for _ in $(seq 90); do
		state=$(podman exec "$tgt" systemctl is-system-running 2>&1) || true
		[ "$state" = running ] && break
		sleep 1
	done
	if [ "$state" != running ]; then
		echo "  FAIL verify_after_restart: systemd после перезапуска в состоянии «$state»"
		podman exec "$tgt" systemctl --failed --no-legend --no-pager >&2 || true
		return 1
	fi
	refresh_known_hosts || { echo "  FAIL verify_after_restart: нет адреса или ключа хоста цели"; return 1; }
	health_wait || return 1
	verify_ok "verify_after_restart: после перезапуска /v1/health от caddy — 200"
	after=$(data_state) || return 1
	[ "$before" = "$after" ] || {
		echo "  FAIL verify_after_restart: данные или их владелец изменились"
		printf 'до:\n%s\nпосле:\n%s\n' "$before" "$after"
		return 1
	}
	verify_ok "verify_after_restart: каталог данных — владелец, права и состав те же"
	# Healthcheck самого контейнера — от albums, с шиной его сеанса (linger поднял её сам).
	podman exec "$tgt" sh -c 'uid=$(id -u albums) && runuser -u albums -- env XDG_RUNTIME_DIR=/run/user/$uid podman healthcheck run albums-svc' ||
		{ echo "  FAIL verify_after_restart: healthcheck контейнера albums-svc не прошёл"; return 1; }
	verify_ok "verify_after_restart: healthcheck контейнера — ok"
	ansible_verify "verify_after_restart: проверки роли и Caddy" \
		verify_albums,verify_acl,verify_caddy,verify_listeners,verify_nft_ports,verify_nft_no_iface_exceptions
}
