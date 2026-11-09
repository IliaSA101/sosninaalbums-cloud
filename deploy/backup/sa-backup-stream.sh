#!/bin/bash
# Поток копии (design 002 §11): запускается службой sa-backup@.service (DynamicUser, CAP_DAC_READ_SEARCH, в
# пустом корне видны только данные, конфиг и программы), вывод — в сокет клиента. Аргументов нет: ничто из
# того, что прислал клиент, сюда не попадает.
#
#   { tar data conf; при коде tar ≤ 1 — второй архив с одним файлом SA-END } | zstd | age -R recipients
#
# Код tar 1 («файл изменился») допустим: файлы пишутся атомарно, после возврата — albums-svc verify. При коде ≥ 2
# метки нет, служба завершается с ошибкой: копия без SA-END отвергается при забое и при возврате.
# Сообщения tar в журнал не идут (в них полные пути, а id альбома — секрет ссылки, 001 §7): в журнал — одна
# строка, код tar и число файлов. Список файлов tar пишет в RuntimeDirectory (tmpfs, исчезает с юнитом).
set -u -o pipefail
umask 077

data_dir=${SA_BACKUP_DATA_DIR:-/var/lib/albums-svc}
conf_dir=${SA_BACKUP_CONF_DIR:-/etc/albums-svc}
recipients=${SA_BACKUP_RECIPIENTS:-/etc/sa-backup/recipients}
work=${RUNTIME_DIRECTORY:-}

if [ -z "$work" ] || [ ! -d "$work" ]; then
	echo "sa-backup: нет каталога метки (RuntimeDirectory)" >&2
	exit 70
fi
if [ ! -s "$recipients" ]; then
	echo "sa-backup: нет получателя age" >&2
	exit 70
fi

list="$work/list"
mark="$work/mark"
mkdir -p "$mark" || exit 70

# Группа команд: код выхода — 0 при коде tar ≤ 1 и успешной метке, иначе 1; конвейер при отказе тоже падает.
make_archive() {
	tar --numeric-owner --create --file=- --verbose --index-file="$list" \
		-C "$data_dir" data \
		-C "$(dirname "$conf_dir")" --transform='s,^'"$(basename "$conf_dir")"'\(/\|$\),conf\1,' "$(basename "$conf_dir")" \
		2>/dev/null
	code=$?
	files=$(wc -l <"$list" 2>/dev/null || echo 0)
	files=${files##* }
	echo "sa-backup: tar code=$code files=$files" >&2
	if [ "$code" -gt 1 ]; then
		return 1
	fi
	printf 'date=%s\nfiles=%s\ntar=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$files" "$code" >"$mark/SA-END" || return 1
	tar --numeric-owner --create --file=- -C "$mark" SA-END 2>/dev/null
}

make_archive | zstd -q -c | age -R "$recipients"
