#!/usr/bin/env bash
# Дымовая проверка боевого сервера и список альбомов (спека 005, design §5.2, 005-29). Только source из provision.sh.
#
# Запуск только на ops-ВМ: токены `smoke_token` и `ls_token` лежат в зашифрованном файле секретов раскатки (секреты и
# inventory живут только в доме учётной записи раскатки, README §1). Клиент — ops/smoke.py на стандартной библиотеке
# Python в контейнере контроллера, после просмотра кода (review_code).
#
# Договорённость с ops/smoke.py (пишет пачка И7c): `python3 -I /src/deploy/ops/smoke.py <upload|clean|ls>`;
#   токен — только файл /run/sa/token (том ro; не окружение, не argv, чтобы его не показал `podman inspect`);
#   база — https://$SA_DOMAIN из окружения контейнера (аргументом не задаётся: опечатка не отправит токен чужому серверу);
#   вывод — номер шага, код ответа и итог, без токена, id и ссылки; код выхода 0 — успех.
# Сеть контейнера — по умолчанию Podman: нужна только до сервера на tcp 443 (правило ufw администратора ops-ВМ, README §1).
# shellcheck disable=SC2154  # here, repo, ctl_id, tmp_rm, SA_*, s_*_token, XDG_RUNTIME_DIR, prov_run, die: задаёт provision.sh

# cmd_api smoke [clean] | albums: токен — файлом 0600 в XDG_RUNTIME_DIR (имя sa-token-*: остатки убитого прогона чистит
# старт provision.sh), в контейнер — томом ro; удаляет cleanup по trap.
cmd_api() {
	local kind=$1 sub=upload tok key tokf rc ip
	case $kind in
		smoke)
			[ "${2:-}" != clean ] || sub=clean
			tok=$s_smoke_token key=smoke_token
			;;
		*)
			sub='ls'
			tok=$s_ls_token key=ls_token
			;;
	esac
	[ -n "$tok" ] || die "в файле секретов нет ключа $key: токен выпускается на ops-ВМ по процедуре README («Правка vault и смена секретов»)"
	tokf="$XDG_RUNTIME_DIR/sa-token-api-$$"
	tmp_rm+=("$tokf")
	(umask 077 && printf '%s' "$tok" >"$tokf") || die "файл токена не записан"
	prov_run api -e HOME=/tmp -e "SA_DOMAIN=$SA_DOMAIN" -v "$repo/deploy/ops/smoke.py:/src/deploy/ops/smoke.py:ro" \
		-v "$tokf:/run/sa/token:ro" "$ctl_id" python3 -I /src/deploy/ops/smoke.py "$sub"
	rc=$?
	if [ "$rc" -ne 0 ]; then
		# Отказ соединения чаще всего — нет разрешающего правила ufw на ops-ВМ; адрес берём из hosts.yml, если он есть.
		ip=$(host_ip 2>/dev/null) || ip='<IP сервера>'
		echo "provision: $kind не прошла (код $rc); если соединение не открывается или имя не разрешается — администратору ops-ВМ (DNS закрыт):" >&2
		echo "  sudo ufw allow out to $ip port 443 proto tcp" >&2
		# Дописываем, только если домена в файле ещё нет: при смене адреса старая строка (первая) выигрывает, и копии копятся.
		echo "  grep -qwF '$SA_DOMAIN' /etc/hosts || echo '$ip $SA_DOMAIN' | sudo tee -a /etc/hosts" >&2
		echo "  # домен в /etc/hosts уже есть с другим адресом — заменить строку (sudoedit /etc/hosts), не дописывать; сверка: getent hosts $SA_DOMAIN" >&2
	fi
	return "$rc"
}
