#!/usr/bin/env bash
# Гипотеза П2a-3 на настоящем Podman (design 002 §2, §12; запускается из provision_cli под pty_drive.py):
# `age -d -o /dev/fd/3` в `podman run -it` спрашивает фразу с терминала, а расшифрованное доходит каналом до
# основной оболочки — через настоящую secrets_load из deploy/ops/secrets.sh. Образ — cli (в нём age).
# Аргументы: <образ> <рабочий каталог 0700 вне git> <корень репозитория>. Выход 0 и «H1-OK» — гипотеза верна.
# shellcheck source-path=SCRIPTDIR disable=SC1091,SC2034,SC2154  # переменные s_* и путь файла секретов — для secrets.sh
set -uo pipefail
img=$1 work=$2 root=$3
here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=fixtures/values.env
. "$here/fixtures/values.env"
# shellcheck source=../../../ops/ctl-run.sh
. "$root/deploy/ops/ctl-run.sh" # nolock, как в provision.sh (secrets.sh вызывает его при запуске контейнеров)
# shellcheck source=../../../ops/secrets.sh
. "$root/deploy/ops/secrets.sh"

plain="$work/plain.txt"
(umask 077 && printf 'clo_token=%s\nvault_pass=%s\nssh_key_phrase=%s\nbackup_key_phrase=%s\nage_identity=%s\n' \
	"$MARKER" "$VAULT" "$PHRASE_SSH" "$PHRASE_BACKUP" "AGE-SECRET-KEY-FIXTURE" >"$plain")
# Файл секретов создаётся так же, как в runbook: age -p в контейнере, фраза с терминала (два раза).
podman run --rm -it --log-driver=none --network=none -v "$plain:/in/p:ro" -v "$work:/out" "$img" \
	age -p -o /out/secrets.age /in/p || { echo "H1-FAIL: age -p"; exit 1; }
rm -f -- "$plain"
chmod 600 "$work/secrets.age"

SA_SECRETS="$work/secrets.age"
secrets_load "$img" || { echo "H1-FAIL: secrets_load"; exit 1; }
if [ "$s_clo_token" != "$MARKER" ] || [ "$s_vault_pass" != "$VAULT" ] || [ "$s_ssh_phrase" != "$PHRASE_SSH" ] ||
	[ "$s_backup_phrase" != "$PHRASE_BACKUP" ] || [ "$s_age_identity" != AGE-SECRET-KEY-FIXTURE ]; then
	echo "H1-FAIL: значения не совпали"
	exit 1
fi

# Драйвер журнала sa-prov-*: none (conmon не пишет вывод в journald).
n="sa-prov-test-$$"
podman create --log-driver=none --name "$n" "$img" true >/dev/null || { echo "H1-FAIL: create"; exit 1; }
t=$(podman inspect --format '{{.HostConfig.LogConfig.Type}}' "$n")
podman rm "$n" >/dev/null
[ "$t" = none ] || { echo "H1-FAIL: драйвер журнала $t"; exit 1; }
echo H1-OK
