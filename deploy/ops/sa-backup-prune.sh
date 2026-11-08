#!/usr/bin/env bash
# Суточная чистка копий учётной записи раскатки (sa-backup-prune.timer, design 002 §11): удаляет копии старше
# 30 дней и печатает предупреждение, если новейшая проверенная копия старше 7 дней. Вывод — в журнал
# пользователя раскатки. Каталог — SA_BACKUP_DIR (по умолчанию ~/backups).
# shellcheck source-path=SCRIPTDIR
set -uo pipefail
here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=backup-lib.sh
. "$here/backup-lib.sh"
dir=${SA_BACKUP_DIR:-$HOME/backups}
backup_prune "$dir"
# Предупреждение — и в stdout: журнал юнита собирает оба потока, а проверка читает один.
backup_freshness_warn "$dir" 2>&1
exit 0
