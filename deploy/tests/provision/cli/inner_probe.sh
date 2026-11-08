#!/usr/bin/env bash
# Продолжение inner.sh (source; drive_rc, check, ok, bad, переменные inv, clone, SHIM_LOG — оттуда): проба сети сервера до
# реестра образов в configure (спека 004, design §11, решение 004-17). Скрипт пробы здесь не исполняется: ssh поддельный и
# отдаёт заданный код (FAKE_PROBE_RC) и вывод (FAKE_PROBE_OUT); сама проба на реестре-заглушке - шаг ghcr_probe_cases
# (deploy/tests/release/test_ghcr_probe.py). Здесь проверяется ход configure: порядок, пользователь, код 3, сервер не тронут.
# shellcheck disable=SC2154,SC2016  # переменные из inner.sh; bash -c '…$1…': раскрытие внутри

# Строки журнала shim, как они шли: номер первой строки по шаблону (grep -n) или пусто.
first_line() { # <шаблон> <файл>
	grep -n -m 1 -- "$1" "$2" | cut -d: -f1
}

probe_cases() {
	local want_sha pl al
	want_sha=$(sha256sum <"$clone/deploy/ops/ghcr_probe.py" | cut -d' ' -f1)
	cp "$inv/deployed-image" /tmp/di.keep
	cp "$inv/deployed-commit" /tmp/dc.keep

	# --- успех: вход администратора есть ---------------------------------------------------------------------
	: >"$SHIM_LOG"
	drive_rc "проба: сервер скачал образ — configure идёт дальше (вход администратора есть)" 0 /tmp/sc.full FAKE_SSH_RC=0 \
		FAKE_PROBE_OUT='ghcr_probe: ok, блобов 4, байт 1, 0.1 с' -- configure
	check "проба: на сервер ушёл скрипт из клона (хэш stdin), команда — python3 -I - <образ>@дайджест --deadline 600" bash -c '
		grep -qx "SSHIN $2" "$1" && grep "^SSH " "$1" | grep -q "python3 -I - .ghcr.io/iliasa101/albums-svc@sha256:[0-9a-f]\{64\}. --deadline 600"' _ "$SHIM_LOG" "$want_sha"
	check "проба: образ в команде - ровно проверенный (дайджест из verified-image)" bash -c '
		d=$(sed -n "s/^digest=//p" "$2"); [ -n "$d" ] && grep "^SSH .*python3 -I -" "$1" | grep -qF "$d"' _ "$SHIM_LOG" "$inv/verified-image"
	check "проба: вход под администратором, без смены прав; на сервере под timeout -k 5 615 (срок 600 + 15)" bash -c '
		l=$(grep "^SSH .*python3 -I -" "$1"); grep -q "admin-example@" <<<"$l" && ! grep -q runuser <<<"$l" && grep -q "BatchMode=yes" <<<"$l" && grep -q "exec timeout -k 5 615 python3 -I - " <<<"$l"' _ "$SHIM_LOG"
	pl=$(first_line '^SSHIN ' "$SHIM_LOG")
	al=$(first_line '^ANSIBLE ' "$SHIM_LOG")
	check "проба: идёт ДО первого плея Ansible" bash -c '[ -n "$1" ] && [ -n "$2" ] && [ "$1" -lt "$2" ]' _ "$pl" "$al"
	check "проба: вывод сервера показан" grep -q 'сервер: ghcr_probe: ok' /tmp/out.txt

	# --- отказ пробы: код 3, сервер не изменён ---------------------------------------------------------------
	cp "$inv/deployed-image" /tmp/di.keep
	cp "$inv/deployed-commit" /tmp/dc.keep
	: >"$SHIM_LOG"
	drive_rc "проба: образ скачан не целиком — отказ кода 3" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=1 \
		FAKE_PROBE_OUT='ghcr_probe: образ не получен целиком: блоб abc: получено 16384 байт из 102400' -- configure
	check "проба: отказ называет причину сервера и подсказку про план Б, «сервер не изменён»" bash -c '
		grep -q "получено 16384 байт из 102400" /tmp/out.txt && grep -q "Сервер не изменён" /tmp/out.txt && grep -q "план Б" /tmp/out.txt'
	check "проба: после отказа ни одного плея, ни одного контейнера контроллера, учёт раскатки прежний" bash -c '
		! grep -q "^ANSIBLE \|name=sa-prov-ansible" "$1" && cmp -s "$2/deployed-image" /tmp/di.keep && cmp -s "$2/deployed-commit" /tmp/dc.keep' _ "$SHIM_LOG" "$inv"
	drive_rc "проба: предел времени (код 124 от timeout) — отказ кода 3" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=124 -- configure
	check "проба: код 124 назван в отказе как срок, с подсказкой про план Б" bash -c 'grep -q "не уложилась в срок (код 124" /tmp/out.txt && grep -q "план Б" /tmp/out.txt'
	drive_rc "проба: код 137 (добит KILL) — тоже срок" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=137 -- configure
	check "проба: код 137 назван как срок" grep -q 'не уложилась в срок (код 137' /tmp/out.txt
	drive_rc "проба: ssh не вошёл (255) — отказ кода 3, плеев нет" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=255 \
		FAKE_PROBE_OUT='Permission denied (publickey).' -- configure
	check "проба: 255 - про вход/ключ хоста, не про сеть и не про план Б; строка ssh без метки «сервер:»" bash -c '
		grep -q "ssh не вошёл на сервер под admin-example (вход, ключ или ключ хоста)" /tmp/out.txt && ! grep -q "план Б" /tmp/out.txt &&
		grep -q "ssh: Permission denied" /tmp/out.txt && ! grep -q "сервер: Permission" /tmp/out.txt'
	drive_rc "проба: нет python3 на сервере (127) — отказ кода 3" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=127 -- configure
	check "проба: 127 - на сервере нет python3, не про сеть и не про план Б" bash -c '
		grep -q "на сервере нет python3, timeout или runuser (код 127)" /tmp/out.txt && ! grep -q "план Б" /tmp/out.txt'
	drive_rc "проба: ошибка вызова (2) — отказ кода 3" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=2 -- configure
	check "проба: 2 - ошибка вызова, не про сеть" bash -c 'grep -q "ошибка вызова пробы (код 2" /tmp/out.txt && ! grep -q "план Б" /tmp/out.txt'
	check "проба: и в отказе по коду 2 сказано «Сервер не изменён»" grep -q 'Сервер не изменён' /tmp/out.txt
	: >"$SHIM_LOG"
	drive_rc "проба: управляющие символы в выводе сервера не доходят до терминала" 3 /tmp/sc.full FAKE_SSH_RC=0 FAKE_PROBE_RC=1 \
		FAKE_PROBE_OUT="$(printf 'ghcr_probe: \033[31mкрасный\007 текст')" -- configure
	# Остальной вывод через pty несёт собственные ESC (перерисовка строки age), поэтому смотрим только строки сервера.
	check "проба: в строках сервера нет ESC и BEL (заменены на «?»), текст сохранён" bash -c '
		l=$(grep "сервер: ghcr_probe:" /tmp/out.txt); [ -n "$l" ] && ! grep -qP "\x1b|\x07" <<<"$l" && grep -qF "?[31mкрасный? текст" <<<"$l"'

	# --- первый подъём: входа администратора нет (255) --------------------------------------------------------
	: >"$SHIM_LOG"
	drive_rc "проба: входа администратора нет — проба под пользователем образа ДО bootstrap.yml" 0 /tmp/sc.full FAKE_SSH_RC=255 \
		FAKE_HOSTKEY="$HOME/.ssh/id_server.pub" -- configure
	check "проба при первом подъёме: вход bootstrap-пользователем (root), права опущены до nobody, cd /tmp" bash -c '
		l=$(grep "^SSH .*python3 -I -" "$1"); grep -q "root@" <<<"$l" && grep -q "cd /tmp && exec timeout -k 5 615 runuser -u nobody -- python3 -I - " <<<"$l"' _ "$SHIM_LOG"
	pl=$(first_line '^SSHIN ' "$SHIM_LOG")
	al=$(first_line '^ANSIBLE .*bootstrap.yml' "$SHIM_LOG")
	check "проба при первом подъёме: раньше bootstrap.yml" bash -c '[ -n "$1" ] && [ -n "$2" ] && [ "$1" -lt "$2" ]' _ "$pl" "$al"
	: >"$SHIM_LOG"
	drive_rc "проба: первый подъём, образ не скачан — отказ кода 3 до bootstrap.yml" 3 /tmp/sc.full FAKE_SSH_RC=255 \
		FAKE_HOSTKEY="$HOME/.ssh/id_server.pub" FAKE_PROBE_RC=1 -- configure
	check "проба: при первом подъёме отказ — ни bootstrap.yml, ни site.yml" bash -c '! grep -q "^ANSIBLE " "$1"' _ "$SHIM_LOG"
	: >"$SHIM_LOG"
	drive_rc "проба: ключ хоста сменился — отказ до пробы (скрипт на чужой сервер не уходит)" 1 /tmp/sc.full FAKE_SSH_RC=255 \
		FAKE_HOSTKEY="$HOME/.ssh/id_backup.pub" -- configure
	check "проба: при смене ключа хоста скрипт не отправлялся" bash -c '! grep -q "^SSHIN " "$1"' _ "$SHIM_LOG"
	cp /tmp/di.keep "$inv/deployed-image"
	cp /tmp/dc.keep "$inv/deployed-commit"
}
