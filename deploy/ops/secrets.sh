#!/usr/bin/env bash
# Секреты и контейнеры раскатки (design 002 §2): запуск sa-prov-*, расшифровка файла секретов, файл токена,
# собственный ssh-agent. Только source. Секреты живут в переменных оболочки и в каналах; в argv, окружении
# контейнеров (-e), файлах (кроме токена 0600 на время прогона) и журнале их нет.
# shellcheck disable=SC2154  # here, SA_*: задаёт provision.sh

# nolock (ops/ctl-run.sh) запускает без дескриптора блокировки inventory/.lock: демон (conmon, агент ключей) иначе
# пережил бы SIGKILL provision.sh и держал блокировку.
# Любой sa-prov-*: без журнала (conmon иначе пишет вывод в journald, а журнал раскатки читают члены adm и
# systemd-journal), без записи в корень, без capabilities, чистое окружение (--env-host не используется).
prov_run() {
	local role=$1
	shift
	nolock podman run --rm --log-driver=none --name "sa-prov-$role-$$" --read-only --cap-drop=ALL \
		--security-opt=no-new-privileges --pids-limit=512 --tmpfs /tmp:rw,mode=1777 "$@"
}

s_clo_token='' s_vault_pass='' s_ssh_phrase='' s_backup_phrase='' s_age_identity=''

# secrets_load <образ контроллера>: фраза age спрашивается с /dev/tty контейнера (то есть с терминала раскатки),
# расшифрованное идёт отдельным каналом (дескриптор 3, --preserve-fds=1), а не в терминал: в одном
# псевдотерминале подсказка смешалась бы с выводом, а \n стали бы \r\n. Читаем в основной оболочке (не в
# подоболочке: там переменные пропали бы). Кэша нет: каждый запуск спрашивает фразу.
secrets_load() {
	local ctl=$1 fd line
	# shellcheck disable=SC2312
	exec {fd}< <(prov_run age -it --network=none --preserve-fds=1 -v "$SA_SECRETS:/run/sa/secrets.age:ro" \
		"$ctl" age -d -o /dev/fd/3 /run/sa/secrets.age 3>&1 </dev/tty >/dev/tty)
	while IFS= read -r -u "$fd" line; do
		case $line in
			clo_token=*) s_clo_token=${line#clo_token=} ;;
			vault_pass=*) s_vault_pass=${line#vault_pass=} ;;
			ssh_key_phrase=*) s_ssh_phrase=${line#ssh_key_phrase=} ;;
			backup_key_phrase=*) s_backup_phrase=${line#backup_key_phrase=} ;;
			age_identity=*) s_age_identity=${line#age_identity=} ;;
		esac
	done
	exec {fd}<&-
	if [ -z "$s_clo_token" ] || [ -z "$s_vault_pass" ] || [ -z "$s_ssh_phrase" ] ||
		[ -z "$s_backup_phrase" ] || [ -z "$s_age_identity" ]; then
		echo "provision: секреты не расшифрованы (неверная фраза?) или в файле нет нужных строк" >&2
		return 1
	fi
}

# Токен — файл 0600 в XDG_RUNTIME_DIR, в контейнер — томом :ro (tofu-entry.sh читает его и делает exec tofu).
# Не -e: переменная окружения видна в podman inspect. Файл удаляет cleanup по trap.
tok_file=''
token_file_make() {
	tok_file="$XDG_RUNTIME_DIR/sa-token-$$"
	(umask 077 && printf '%s' "$s_clo_token" >"$tok_file")
}

agent_sock='' agent_pid=''
# Свой ssh-agent на время прогона; завершается по trap (по своему PID).
agent_start() {
	local out
	agent_sock="$XDG_RUNTIME_DIR/sa-agent-$$.sock"
	# -t: ключи в агенте живут не дольше часа, даже если прогон убит и агент остался сиротой.
	out=$(nolock ssh-agent -a "$agent_sock" -t 3600 -s) || return 1
	agent_pid=$(sed -n 's/^SSH_AGENT_PID=\([0-9][0-9]*\);.*/\1/p' <<<"$out")
	[ -n "$agent_pid" ]
}

# agent_add <файл ключа> <фраза>: одна фраза на вызов (ключи грузятся отдельными вызовами). Фраза идёт в
# SSH_ASKPASS-помощник дескриптором 3 (SSH_ASKPASS_REQUIRE=force — подача, а не подтверждение).
agent_add() {
	SSH_AUTH_SOCK=$agent_sock SSH_ASKPASS_REQUIRE=force SSH_ASKPASS="$here/ops/phrase-fd.sh" \
		nolock ssh-add -t 3600 "$1" 3< <(printf '%s' "$2") </dev/null >/dev/null 2>&1
}
