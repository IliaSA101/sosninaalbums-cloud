#!/usr/bin/env bash
# Шаги verify стенда раскатки (design 002 §12): проверки результата converge. Только source из run.sh
# (после lib.sh и converge.sh). Шаг требует цель после converge_site; каждая проверка печатает «ok …».
#
#   verify_ssh   verify_ssh_admin_key, verify_admin_sudo, verify_sshd_effective — положительные;
#                затем verify_ssh_root_denied, verify_ssh_password_denied, verify_ssh_foreign_key_denied,
#                verify_ssh_image_user_denied
#   verify_nft   verify_nft_ports (22, 80, 443 — да; 8080, 9999 — нет, при включённом слушателе),
#                verify_nft_no_iface_exceptions, verify_nft_reload_only, verify_listeners
#                (должна ловить слушатель 8080, а без него — проходить)
#   verify_host  verify_users, verify_resolved (LLMNR и mDNS выключены), verify_upgrades, verify_journald, verify_guest_agent, verify_cloud_init
#
# Положительные проверки идут раньше отрицательных: неудачные входы увеличивают штраф источника
# (PerSourcePenalties), поэтому отрицательные — из отдельного контейнера со своим адресом.
# shellcheck disable=SC2154  # work, sd, tgt_image, net, here — из run.sh и lib.sh; skip_reason читает run.sh

verify_ok() { echo "  ok   $1"; }

# verify_fail <проверка> <причина>
verify_fail() {
	echo "  FAIL $1: $2"
	return 1
}

# Состояние порта цели из отдельного контейнера: open (соединились), refused (фильтр пропустил, слушателя
# нет), filtered (таймаут: пакеты сброшены фильтром). port_states <порт…> → строки «порт состояние».
port_states() {
	local ip
	ip=$(target_ip)
	[ -n "$ip" ] || return 1
	podman run --rm --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges --tmpfs /tmp \
		--entrypoint python3 "$tgt_image" -I -c '
import socket, sys
for p in sys.argv[2:]:
    s = socket.socket()
    s.settimeout(3)
    try:
        s.connect((sys.argv[1], int(p)))
        r = "open"
    except ConnectionRefusedError:
        r = "refused"
    except OSError:
        r = "filtered"
    finally:
        s.close()
    print(p, r)
' "$ip" "$@"
}

# ansible_verify <шаг> <теги> [ещё аргументы]: verify.yml с тегами; провал печатает вывод.
ansible_verify() {
	local name=$1 tags=$2
	shift 2
	play verify.yml --tags "$tags" "$@" || return 1
	verify_ok "$name"
}

# Вход администратора чужим ключом: ключ рождается и умирает в контейнере (tmpfs), на хосте его нет.
foreign_key_login() {
	local ip
	ip=$(target_ip)
	[ -n "$ip" ] || return 1
	podman run --rm --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--tmpfs /tmp --tmpfs /root -v "$sd/known_hosts:/kh:ro" --entrypoint sh "$tgt_image" -c '
			ssh-keygen -q -t ed25519 -N "" -f /tmp/other || exit 1
			exec ssh -F /dev/null -i /tmp/other -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
				-o UserKnownHostsFile=/kh -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o LogLevel=ERROR \
				"$0@$1" true
		' "$stand_admin" "$ip" </dev/null 2>&1
}

step_verify_ssh() {
	local out rc pw
	need_target || return 1
	stand_secrets || return 1
	# Положительные: ключ, sudo по паролю (и только по нему), действующая конфигурация sshd.
	out=$(stand_ssh "$stand_admin" -- 'id -un' </dev/null 2>&1)
	[ "$out" = "$stand_admin" ] || { verify_fail verify_ssh_admin_key "вход по ключу не дал $stand_admin: $out"; return 1; }
	verify_ok verify_ssh_admin_key
	pw=$(cat "$sd/admin_password")
	printf '%s\n' "$pw" | stand_ssh "$stand_admin" -- 'sudo -S -v' >/dev/null 2>&1 || {
		verify_fail verify_admin_sudo "sudo с паролем администратора не прошёл"
		return 1
	}
	if stand_ssh "$stand_admin" -- 'sudo -n true' </dev/null >/dev/null 2>&1; then
		verify_fail verify_admin_sudo "sudo без пароля прошёл (NOPASSWD)"
		return 1
	fi
	verify_ok verify_admin_sudo
	ansible_verify verify_sshd_effective verify_sshd_effective || return 1
	# Отрицательные: root по ключу, пароль, пользователь образа.
	out=$(stand_ssh root -- true </dev/null 2>&1)
	rc=$?
	if [ "$rc" -ne 255 ] || ! printf '%s' "$out" | grep -q 'Permission denied'; then
		verify_fail verify_ssh_root_denied "код $rc: $out"
		return 1
	fi
	verify_ok verify_ssh_root_denied
	# Пароль: ключ отключён, предлагаем только пароль; в ответе sshd среди допустимых методов — один publickey.
	out=$(stand_ssh "$stand_admin" -o PubkeyAuthentication=no -o PreferredAuthentications=password,keyboard-interactive \
		-- true </dev/null 2>&1)
	rc=$?
	if [ "$rc" -ne 255 ] || ! printf '%s' "$out" | grep -Eq 'Permission denied \(publickey\)\.'; then
		verify_fail verify_ssh_password_denied "код $rc: $out"
		return 1
	fi
	verify_ok verify_ssh_password_denied
	# Чужой ключ: свежий ed25519, нигде не авторизованный, — вход администратора отвергнут.
	out=$(foreign_key_login)
	rc=$?
	if [ "$rc" -ne 255 ] || ! printf '%s' "$out" | grep -Eq 'Permission denied \(publickey\)\.'; then
		verify_fail verify_ssh_foreign_key_denied "код $rc: $out"
		return 1
	fi
	verify_ok verify_ssh_foreign_key_denied
	out=$(stand_ssh debian -- true </dev/null 2>&1)
	rc=$?
	if [ "$rc" -ne 255 ] || ! printf '%s' "$out" | grep -q 'Permission denied'; then
		verify_fail verify_ssh_image_user_denied "код $rc: $out"
		return 1
	fi
	verify_ok verify_ssh_image_user_denied
}

step_verify_nft() {
	local states want got rc=0
	need_target || return 1
	stand_secrets || return 1
	# Слушатель на 8080 включён на время проверок портов и выключается при любом выходе.
	cmd_listener on || return 1
	sleep 1
	states=$(port_states 22 80 443 8080 9999) || {
		cmd_listener off
		return 1
	}
	# Нет caddy: 80 и 443 пропущены фильтром, но слушателя нет (refused); с caddy будет open.
	for want in '22 open' '8080 filtered' '9999 filtered'; do
		printf '%s\n' "$states" | grep -qx "$want" || {
			verify_fail verify_nft_ports "ожидали «$want», получили: $(printf '%s' "$states" | tr '\n' ';')"
			rc=1
		}
	done
	for want in 80 443; do
		got=$(printf '%s\n' "$states" | grep "^$want " | cut -d' ' -f2)
		case $got in open | refused) ;; *)
			verify_fail verify_nft_ports "порт $want закрыт фильтром ($got)"
			rc=1
			;;
		esac
	done
	if [ "$rc" -ne 0 ]; then
		cmd_listener off
		return 1
	fi
	# Слушатель 8080 виден проверке слушающих портов: verify_listeners обязана упасть.
	if play verify.yml --tags verify_listeners >/dev/null 2>&1; then
		verify_fail verify_listeners "слушатель 8080 вне lo не замечен"
		cmd_listener off
		return 1
	fi
	printf '%s\n' "$play_out" | grep -q '0.0.0.0:8080' || {
		verify_fail verify_listeners "провал без указания слушателя 8080: $(printf '%s' "$play_out" | tail -n 5)"
		cmd_listener off
		return 1
	}
	cmd_listener off || return 1
	verify_ok "verify_nft_ports (22 open; 80, 443 пропущены; 8080, 9999 filtered)"
	ansible_verify verify_listeners verify_listeners || return 1
	ansible_verify verify_nft_no_iface_exceptions verify_nft_ports,verify_nft_no_iface_exceptions || return 1
	lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py deploy/ansible || return 1
	verify_ok verify_nft_reload_only
}

step_verify_host() {
	need_target || return 1
	stand_secrets || return 1
	ansible_verify "verify_users verify_resolved verify_upgrades verify_journald verify_guest_agent verify_cloud_init" \
		verify_users,verify_resolved,verify_upgrades,verify_journald,verify_guest_agent,verify_cloud_init
}
