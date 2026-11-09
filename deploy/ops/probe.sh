#!/usr/bin/env bash
# Проба сети сервера до реестра образов (спека 004, design §11, решение 004-17): подключается главным скриптом раскатки
# (source), вызывается из cmd_configure. Нужны его функции plain_file и ssh_host_opts, refuse из ops/guard.sh и img_ref из
# image_gate (ops/image.sh).
# shellcheck disable=SC2154  # here, img_ref: из главного скрипта и image_gate

# Предел времени пробы, секунды: до замера на пробной аренде - 600 (design 004 §11); тот же срок - у podman pull в роли albums_svc
# (albums_svc_pull_timeout_sec). Константа кода, а не окружения.
ghcr_probe_sec=600

# Форма образа, которая уходит в команду на сервере (та же, что REF_RE в ops/ghcr_probe.py): без кавычек и пробелов, только имя@sha256.
ghcr_ref_re='^[a-z0-9][a-z0-9.:-]*/[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$'

# ghcr_probe <пользователь ssh> <адрес>: ПЕРВОЕ, что configure делает на сервере, до bootstrap.yml и любой правки (004-17).
# ops/ghcr_probe.py уходит на сервер через stdin и скачивает образ целиком (манифест и все слои, размер и sha256 каждого
# сверяются; ничего не пишется на диск): обрезка слоёв на 16 КиБ при ответе 200 (ВР-16) ловится именно этим. Отдельный
# вызов, а не задача site.yml: bootstrap.yml правит сервер раньше site.yml (004-41). На сервере процесс живёт под timeout:
# оборванный ssh без pty не сигналит потомку, и проба пережила бы сеанс. Под root права опускаются до nobody; обычный
# пользователь (администратор, пользователь образа) и так без прав, sudo не нужен (004-45). Вывод чистится от управляющих знаков.
ghcr_probe() {
	local user=$1 ip=$2 py="$here/ops/ghcr_probe.py" opts out rc run wrap='' label=сервер why
	plain_file "$py"
	[[ $img_ref =~ $ghcr_ref_re ]] || refuse "образ для пробы не той формы (имя@sha256:дайджест)"
	mapfile -t opts < <(ssh_host_opts server)
	[ "$user" != root ] || wrap='runuser -u nobody -- '
	run="cd /tmp && exec timeout -k 5 $((ghcr_probe_sec + 15)) ${wrap}python3 -I - '$img_ref' --deadline $ghcr_probe_sec"
	echo "проба сети: сервер скачивает образ из реестра целиком (до $ghcr_probe_sec с), сервер пока не изменён…"
	out=$(timeout "$((ghcr_probe_sec + 30))" ssh "${opts[@]}" -o BatchMode=yes -T "$user@$ip" "$run" <"$py" 2>&1)
	rc=$?
	[ "$rc" != 255 ] || label=ssh # 255 - сообщение самого ssh, а не сервера
	out=$(printf '%s\n' "$out" | head -n 10 | LC_ALL=C sed "s/[[:cntrl:]]/?/g; s/^/  $label: /")
	[ "$rc" -ne 0 ] || {
		echo "$out"
		return 0
	}
	printf '%s\n' "$out" >&2
	case $rc in
		255) why="ssh не вошёл на сервер под $user (вход, ключ или ключ хоста); проба не запускалась" ;;
		127) why="на сервере нет python3, timeout или runuser (код 127); проба не запускалась" ;;
		2) why="ошибка вызова пробы (код 2: аргументы ops/ghcr_probe.py)" ;;
		124 | 137) why="проба не уложилась в срок (код $rc, $ghcr_probe_sec с): сеть ЦОД до реестра зависает или режет слои - повторить позже; если стабильно - план Б, архив образа через ops-ВМ (спека 004, design 11)" ;;
		1) why="проба отказала: образ не скачан целиком (сеть ЦОД до реестра: обрыв, обрезка слоёв, медленно) - повторить позже; если стабильно - план Б, архив образа через ops-ВМ (спека 004, design 11)" ;;
		*) why="проба завершилась неожиданным кодом $rc" ;;
	esac
	refuse "$why. Сервер не изменён"
}
