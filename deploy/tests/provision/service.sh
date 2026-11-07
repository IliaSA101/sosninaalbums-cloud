#!/usr/bin/env bash
# Шаги стенда для ролей И4 (design 002 §8-§11, §12): проверки podman, caddy, albums_svc, backup_access и мутации,
# на которых каждая защитная проверка обязана краснеть. Только source из run.sh (после lib.sh, converge.sh, verify.sh).
#
#   verify_service        verify_podman, verify_caddy (+ caddy_key_fingerprint), verify_albums, verify_backup,
#                         порт 80 → 308 из отдельного контейнера, verify_restore_rejects (хороший архив проходит, плохие
#                         отвергаются)
#   mutate_restore_a/b    sa-restore ослабляется по одному правилу (метки «# rule:» в скрипте): verify_restore_rejects
#                         обязана покраснеть на соответствующем архиве; на целом скрипте — снова зелёная
#   mutate_caddy          ключ репозитория, Caddyfile, закрепление версии, остановленная служба, порт 80 → 308
#   mutate_host           podman, config.json, оболочка albums, чужой subuid, ACL каталога сокета
#   mutate_backup         authorized_keys, subuid пользователя бэкапа, получатель age, юнит службы копии
#
# Мутация применяется на цели, затем проверка ДОЛЖНА упасть с ожидаемым текстом; после — возврат и зелёный прогон:
# проверка, которую мутация не красит, ничего не защищает (как самопроверка tofu_static).
# shellcheck disable=SC2154  # tgt, net, tgt_image — из run.sh и lib.sh; play_out, skip_reason — из converge.sh и run.sh

# Порт 80: ответ 308 на https:// того же узла (автоматическое перенаправление Caddy), 443 принимает соединения.
caddy_redirect_check() {
	local ip out
	ip=$(target_ip)
	[ -n "$ip" ] || return 1
	out=$(podman run --rm --network "$net" --read-only --cap-drop=ALL --security-opt=no-new-privileges --tmpfs /tmp \
		--entrypoint python3 "$tgt_image" -I -c '
import http.client, socket, sys
c = http.client.HTTPConnection(sys.argv[1], 80, timeout=5)
c.request("GET", "/", headers={"Host": sys.argv[2]})
r = c.getresponse()
print(r.status, r.getheader("Location"))
s = socket.create_connection((sys.argv[1], 443), timeout=5)
s.close()
' "$ip" albums.example.org 2>&1) || {
		echo "  FAIL verify_caddy_redirect: $out"
		return 1
	}
	[ "$out" = "308 https://albums.example.org/" ] || {
		echo "  FAIL verify_caddy_redirect: ожидали «308 https://albums.example.org/», получили «$out»"
		return 1
	}
	verify_ok "verify_caddy_redirect (80 -> 308, 443 принимает)"
}

step_verify_service() {
	need_target || return 1
	stand_secrets || return 1
	ansible_verify "verify_podman verify_caddy verify_albums verify_backup" \
		verify_podman,verify_caddy,caddy_key_fingerprint,verify_albums,verify_backup || return 1
	caddy_redirect_check || return 1
	ansible_verify verify_restore_rejects verify_restore_rejects || return 1
}

# on_target <команда sh>: от root внутри цели (стенд, не роль).
on_target() { podman exec "$tgt" sh -c "$1"; }

# mutate <имя> <теги> <ожидаемый текст в выводе> <применить> <вернуть> [аргументы play…]: применить на цели,
# убедиться, что verify.yml с этими тегами падает с ожидаемым текстом, вернуть. Возврат из вернуть выполняется
# всегда, даже если проверка не покраснела (цель не остаётся испорченной).
mutate() {
	local name=$1 tags=$2 want=$3 apply=$4 revert=$5 rc=0
	shift 5
	on_target "$apply" || {
		echo "  FAIL мутация $name: не применилась"
		on_target "$revert" >/dev/null 2>&1
		return 1
	}
	if play verify.yml --tags "$tags" "$@" >/dev/null 2>&1; then
		echo "  FAIL мутация $name: проверка $tags осталась зелёной"
		rc=1
	elif ! printf '%s\n' "$play_out" | grep -Fq -- "$want"; then
		echo "  FAIL мутация $name: проверка упала не там («$want» нет в выводе):"
		printf '%s\n' "$play_out" | grep -E 'fatal|FAILED|failed:' | head -n 5
		rc=1
	else
		verify_ok "мутация $name: $tags красная («$want»)"
	fi
	on_target "$revert" || {
		echo "  FAIL мутация $name: возврат не удался"
		rc=1
	}
	return "$rc"
}

# Зелёный прогон после возврата: цель снова целая.
green_after() { # <тег,…>
	play verify.yml --tags "$1" || return 1
	verify_ok "после мутаций $1 — зелёная"
}

# Ослабленная копия sa-restore: мутация — по имени, правка скриптом внутри цели; не изменился файл — ошибка.
restore_mutation() {
	podman exec -i "$tgt" python3 -I - "$1" <<'PY'
import re, sys
src = "/usr/local/libexec/sa-restore"
dst = "/var/tmp/sa-restore-mut"
text = open(src, encoding="utf-8").read()
drop = {
    "type": ["type"], "fifo": ["type"], "deep": ["depth"], "namelen": ["namelen"], "fanout": ["implicit"],
    "root": ["root"], "gid_range": ["range_gid"], "dotdot": ["dotdot"], "absolute": ["abs", "prefix"], "foreign": ["prefix"],
    "nomarker": ["marker"], "toolong": ["limit", "sum", "space"], "mtime": ["fields"], "charset": ["charset"],
    "dirs": ["sum"], "many": ["count"], "space": ["space"], "uid_range": ["range_uid"],
}
name = sys.argv[1]
if name in drop:
    for rule in drop[name]:
        text = re.sub(r"(?m)^.*# rule:%s$\n" % rule, "", text)
elif name == "reject_all":
    text = text.replace("\nok=1\n", "\nexit 3\n")
elif name == "no_privs":
    text = text.replace(" --no-new-privs", "")
elif name == "root_worker":
    text = re.sub(r'(?m)^setpriv --reuid=.*\\$', "env \\\\", text)
else:
    sys.exit("неизвестная мутация")
if text == open(src, encoding="utf-8").read():
    sys.exit("мутация не изменила скрипт")
open(dst, "w", encoding="utf-8").write(text)
import os
os.chmod(dst, 0o755)
PY
}

restore_mutate() { # <мутация> <ожидаемый текст>
	local name=$1 want=$2
	# «item=X» есть и у прошедших элементов цикла («ok: … (item=X)»): красным считается только «failed: … (item=X)».
	case $want in item=*) want="failed: [server] ($want)" ;; esac
	restore_mutation "$name" || {
		echo "  FAIL мутация sa-restore $name: не применилась"
		return 1
	}
	mutate "sa-restore/$name" verify_restore_rejects "$want" true 'rm -f /var/tmp/sa-restore-mut' \
		-e backup_access_restore_script=/var/tmp/sa-restore-mut
}

step_mutate_restore_a() {
	local rc=0
	need_target || return 1
	stand_secrets || return 1
	green_after verify_restore_rejects || return 1
	restore_mutate fifo 'item=fifo' || rc=1
	restore_mutate dotdot 'item=dotdot' || rc=1
	restore_mutate absolute 'item=absolute' || rc=1
	restore_mutate foreign 'item=foreign' || rc=1
	restore_mutate deep 'item=deep' || rc=1
	restore_mutate namelen 'item=namelen' || rc=1
	return "$rc"
}

step_mutate_restore_b() {
	local rc=0
	need_target || return 1
	stand_secrets || return 1
	restore_mutate nomarker 'item=nomarker' || rc=1
	restore_mutate toolong 'item=toolong' || rc=1
	restore_mutate mtime 'item=mtime' || rc=1
	restore_mutate charset 'item=charset' || rc=1
	restore_mutate dirs 'item=dirs' || rc=1
	restore_mutate fanout 'item=fanout' || rc=1
	return "$rc"
}

step_mutate_restore_c() {
	local rc=0
	need_target || return 1
	stand_secrets || return 1
	restore_mutate many 'item=many' || rc=1
	restore_mutate space 'item=space' || rc=1
	restore_mutate uid_range 'item=uid_range' || rc=1
	restore_mutate gid_range 'item=gid_range' || rc=1
	restore_mutate root 'item=root' || rc=1
	return "$rc"
}

step_mutate_restore_d() {
	local rc=0
	need_target || return 1
	stand_secrets || return 1
	restore_mutate reject_all 'Положительный контроль' || rc=1
	restore_mutate root_worker 'Положительный контроль' || rc=1
	restore_mutate no_privs 'setpriv' || rc=1
	green_after verify_restore_rejects || rc=1
	return "$rc"
}

step_mutate_caddy() {
	local rc=0 ip key=/etc/apt/keyrings/caddy-stable.asc
	need_target || return 1
	stand_secrets || return 1
	mutate caddy/Caddyfile verify_caddy '/etc/caddy/Caddyfile' \
		'cp -p /etc/caddy/Caddyfile /var/tmp/Caddyfile.sav && echo "# mut" >> /etc/caddy/Caddyfile' \
		'mv -f /var/tmp/Caddyfile.sav /etc/caddy/Caddyfile' || rc=1
	# Строка 6 armor — внутри пакета основного ключа: буквы сдвинуты, base64 остался верным, но CRC24 не сходится.
	mutate caddy/key_crc caddy_key_fingerprint 'CRC24' \
		"cp -p $key /var/tmp/key.sav && sed -i '6y/ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz/BCDEFGHIJKLMNOPQRSTUVWXYZAbcdefghijklmnopqrstuvwxyza/' $key" \
		"mv -f /var/tmp/key.sav $key" || rc=1
	# Второй armor-блок в файле ключа: apt доверил бы и ему.
	mutate caddy/key_second_block caddy_key_fingerprint 'не ровно один armor-блок' \
		"cp -p $key /var/tmp/key.sav && cat /var/tmp/key.sav >> $key" \
		"mv -f /var/tmp/key.sav $key" || rc=1
	# Данные ключа в строке «заголовка» armor (до пустой строки): разбор без строгих заголовков пропустил бы их, а apt прочёл бы.
	mutate caddy/key_smuggle caddy_key_fingerprint 'заголовка armor' \
		"cp -p $key /var/tmp/key.sav && sed -i '1a mQINBFb+quEBEACl3/YkFekflvauEASL' $key" \
		"mv -f /var/tmp/key.sav $key" || rc=1
	# Отпечаток в роли не тот, что у файла: проверяется сама сверка, а не разбор.
	mutate caddy/key_fingerprint caddy_key_fingerprint 'подменён' true true \
		-e caddy_key_fingerprint=0000000000000000000000000000000000000000 || rc=1
	mutate caddy/pin verify_caddy 'Caddy не закреплён на' \
		"cp -p /etc/apt/preferences.d/caddy /var/tmp/pref.sav && sed -i 's/1001/500/' /etc/apt/preferences.d/caddy" \
		'mv -f /var/tmp/pref.sav /etc/apt/preferences.d/caddy' || rc=1
	mutate caddy/stopped verify_caddy 'не запущен' 'systemctl stop caddy.service' 'systemctl start caddy.service' || rc=1
	# Порт 80 -> 308: на остановленной службе проверка обязана краснеть.
	on_target 'systemctl stop caddy.service' || rc=1
	if caddy_redirect_check >/dev/null 2>&1; then
		echo "  FAIL мутация caddy/stopped: verify_caddy_redirect осталась зелёной"
		rc=1
	else
		verify_ok "мутация caddy/stopped: verify_caddy_redirect красная"
	fi
	on_target 'systemctl start caddy.service' || rc=1
	ip=$(target_ip) && [ -n "$ip" ] || rc=1
	green_after verify_caddy,caddy_key_fingerprint || rc=1
	caddy_redirect_check || rc=1
	return "$rc"
}

step_mutate_host() {
	local rc=0
	need_target || return 1
	stand_secrets || return 1
	mutate podman/unmask verify_podman 'замаскированы' 'rm -f /etc/systemd/user/podman.socket' \
		'ln -s /dev/null /etc/systemd/user/podman.socket' || rc=1
	mutate albums/config_mode verify_albums 'config.json' 'chmod 0644 /etc/albums-svc/config.json' \
		'chmod 0640 /etc/albums-svc/config.json' || rc=1
	mutate albums/shell verify_albums 'albums не как в design' 'usermod -s /bin/bash albums' \
		'usermod -s /usr/sbin/nologin albums' || rc=1
	mutate albums/subuid_overlap verify_albums 'albums не как в design' \
		"cp -p /etc/subuid /var/tmp/subuid.sav && echo 'intruder:210000:65536' >> /etc/subuid" \
		'mv -f /var/tmp/subuid.sav /etc/subuid' || rc=1
	mutate albums/socket_acl verify_albums '/run/albums-svc расходится' 'setfacl -x u:caddy /run/albums-svc' \
		'setfacl -m u:caddy:x /run/albums-svc' || rc=1
	green_after verify_podman,verify_albums || rc=1
	return "$rc"
}

step_mutate_backup() {
	local rc=0 unit=/etc/systemd/system/sa-backup@.service bk_home=/home/sa-backup script=/usr/local/libexec/sa-backup-stream bdrop=/etc/systemd/system/sa-backup@.service.d
	need_target || return 1
	stand_secrets || return 1
	mutate backup/authorized_keys verify_backup 'authorized_keys' \
		"cp -p $bk_home/.ssh/authorized_keys /var/tmp/ak.sav && sed -i 's/^restrict,//' $bk_home/.ssh/authorized_keys" \
		"mv -f /var/tmp/ak.sav $bk_home/.ssh/authorized_keys" || rc=1
	mutate backup/subuid verify_backup 'пользователь бэкапа не как' \
		"cp -p /etc/subuid /var/tmp/subuid.sav && echo 'sa-backup:300000:65536' >> /etc/subuid" \
		'mv -f /var/tmp/subuid.sav /etc/subuid' || rc=1
	mutate backup/recipients verify_backup '/etc/sa-backup/recipients' \
		"cp -p /etc/sa-backup/recipients /var/tmp/rcp.sav && echo extra >> /etc/sa-backup/recipients" \
		'mv -f /var/tmp/rcp.sav /etc/sa-backup/recipients' || rc=1
	# Чужой drop-in: правка своего файла краснела бы раньше времени на сверке sha256, а так до systemctl show доходит.
	mutate backup/foreign_dropin verify_backup 'служба копии' \
		"mkdir -p $bdrop && printf '[Service]\\nProtectHome=no\\n' > $bdrop/zz-mut.conf && systemctl daemon-reload" \
		"rm -rf $bdrop && systemctl daemon-reload" || rc=1
	# Поток копии: без StandardError=journal сообщения службы встают внутрь потока (И4-1), а метка при коде tar 2 -
	# признак неполной копии: обе мутации обязаны покраснеть на verify_backup_stream (stream.sh).
	stream_mutate backup/stderr_in_stream 'не расшифровался' "cp -p $unit /var/tmp/unit.sav && sed -i '/^StandardError=journal/d' $unit && systemctl daemon-reload" \
		"mv -f /var/tmp/unit.sav $unit && systemctl daemon-reload" || rc=1
	stream_mutate backup/marker_on_failure 'метка SA-END' "cp -p $script /var/tmp/stream.sav && sed -i 's/\"\$code\" -gt 1/\"\$code\" -gt 99/' $script" \
		"mv -f /var/tmp/stream.sav $script" || rc=1
	green_after verify_backup || rc=1
	return "$rc"
}

# stream_mutate <имя> <ожидаемая причина> <применить> <вернуть>: verify_backup_stream на ослабленной цели обязан упасть
# с этой причиной (а не по случайной ошибке цели).
stream_mutate() {
	local name=$1 want=$2 rc=0 out
	on_target "$3" || {
		echo "  FAIL мутация $name: не применилась"
		on_target "$4" >/dev/null 2>&1
		return 1
	}
	if out=$(step_verify_backup_stream 2>&1); then
		echo "  FAIL мутация $name: verify_backup_stream осталась зелёной"
		rc=1
	elif ! printf '%s\n' "$out" | grep -Fq -- "$want"; then
		echo "  FAIL мутация $name: verify_backup_stream упала не по той причине («$want» нет в выводе):"
		printf '%s\n' "$out" | grep FAIL | head -n 3
		rc=1
	else
		verify_ok "мутация $name: verify_backup_stream красная («$want»)"
	fi
	on_target "$4" || rc=1
	return "$rc"
}

# Остальные мутации Caddy: дом, остаток policy-rc.d, права юнита; чужой policy-rc.d роняет сам прогон site.yml.
step_mutate_caddy_b() {
	local rc=0 cdrop=/etc/systemd/system/caddy.service.d
	need_target || return 1
	stand_secrets || return 1
	mutate caddy/home verify_caddy 'нет каталога' 'chmod 0755 /var/lib/caddy' 'chmod 0700 /var/lib/caddy' || rc=1
	mutate caddy/policy_left verify_caddy 'policy-rc.d' "printf '#!/bin/sh\\nexit 101\\n' > /usr/sbin/policy-rc.d" \
		'rm -f /usr/sbin/policy-rc.d' || rc=1
	# Чужой drop-in (zz-mut.conf читается после нашего albums.conf): до systemctl show доходит, а не краснеет на sha256.
	mutate caddy/foreign_dropin_caps verify_caddy 'одна способность' \
		"mkdir -p $cdrop && printf '[Service]\\nAmbientCapabilities=CAP_NET_ADMIN\\n' > $cdrop/zz-mut.conf && systemctl daemon-reload" \
		"rm -f $cdrop/zz-mut.conf && systemctl daemon-reload" || rc=1
	mutate caddy/foreign_dropin_devices verify_caddy 'одна способность' \
		"printf '[Service]\\nPrivateDevices=no\\n' > $cdrop/zz-mut.conf && systemctl daemon-reload" \
		"rm -f $cdrop/zz-mut.conf && systemctl daemon-reload" || rc=1
	mutate caddy/api_unmask verify_caddy 'caddy-api.service не замаскирован' 'rm -f /etc/systemd/system/caddy-api.service' \
		'ln -s /dev/null /etc/systemd/system/caddy-api.service' || rc=1
	green_after verify_caddy || rc=1
	return "$rc"
}

# policy-rc.d и прогон site.yml: чужой файл роняет прогон до установки и остаётся нетронутым; свой (остаток оборванного
# прогона, с меткой) снимается, прогон проходит.
# Теги — как у converge_site (site_tags): локально albums-runtime пропускается, при SA_STAND_FULL=1 идёт целиком.
step_mutate_policy() {
	local rc=0 rc_file=/usr/sbin/policy-rc.d site_skip
	need_target || return 1
	stand_secrets || return 1
	site_tags
	# Чужой policy-rc.d: прогон site.yml обязан упасть до установки и не трогать файл.
	on_target "printf '#!/bin/sh\\nexit 101\\n' > $rc_file" || rc=1
	if play site.yml "${site_skip[@]}" >/dev/null 2>&1; then
		echo "  FAIL мутация caddy/policy_foreign: site.yml прошёл с чужим policy-rc.d"
		rc=1
	elif ! printf '%s\n' "$play_out" | grep -Fq 'и не наш'; then
		echo "  FAIL мутация caddy/policy_foreign: прогон упал не там"
		rc=1
	else
		verify_ok "мутация caddy/policy_foreign: site.yml красный («и не наш»)"
	fi
	on_target 'test -f /usr/sbin/policy-rc.d && rm -f /usr/sbin/policy-rc.d' || rc=1
	# Свой остаток (метка и содержимое как у роли): прогон проходит и файла после него нет.
	on_target "printf '#!/bin/sh\\n# sa-caddy-install\\nexit 101\\n' > $rc_file" || rc=1
	if play site.yml "${site_skip[@]}" >/dev/null 2>&1; then
		if on_target "test ! -e $rc_file"; then
			verify_ok "policy-rc.d: свой остаток снят, site.yml прошёл"
		else
			echo "  FAIL policy-rc.d: свой остаток остался после site.yml"
			rc=1
		fi
	else
		echo "  FAIL policy-rc.d: site.yml не прошёл со своим остатком policy-rc.d:"
		printf '%s\n' "$play_out" | tail -n 5
		rc=1
	fi
	on_target "rm -f $rc_file" || rc=1
	green_after verify_caddy || rc=1
	return "$rc"
}
