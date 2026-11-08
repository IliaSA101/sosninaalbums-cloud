#!/usr/bin/env bash
# Чистка и свежесть копий (design 002 §11, решение 002-15): общее для provision.sh и sa-backup-prune.sh. Только source.
# Копия — <UTC-дата>.tar.zst.age, возраст считается по дате в имени, а не по mtime (mtime меняет любой перенос).
# Копии старше 30 дней удаляются без исключений, даже единственная: фото клиентов не живут в копиях дольше срока
# (001-20). От потери всех копий защищает предупреждение, если новейшая проверенная копия старше 7 дней.

backup_keep_days=30
backup_warn_days=7

# Метка времени из имени копии (секунды Unix) или пусто, если имя не по формату.
backup_name_epoch() {
	local n=${1##*/}
	[[ $n =~ ^([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})Z\.tar\.zst\.age$ ]] || return 0
	date -u -d "${BASH_REMATCH[1]}-${BASH_REMATCH[2]}-${BASH_REMATCH[3]} ${BASH_REMATCH[4]}:${BASH_REMATCH[5]}:${BASH_REMATCH[6]}" +%s 2>/dev/null
}

# Удаляет копии старше срока и забытые .part (старше суток по mtime: их имя — время начала забора, но
# переименование делает только успешная проверка метки).
backup_prune() {
	local dir=$1 now f e n=0
	[ -d "$dir" ] || return 0
	now=$(date -u +%s)
	# Файл не по формату имени чистка не считает копией и не удаляет: возраст по имени не определить — молча
	# копиться он не должен (там могут оказаться фото клиентов дольше срока).
	for f in "$dir"/*; do
		[ -f "$f" ] || continue
		case $f in *.tar.zst.age.part) continue ;; esac
		[ -n "$(backup_name_epoch "$f")" ] || echo "ПРЕДУПРЕЖДЕНИЕ: в каталоге копий есть файл с неожиданным именем (чистка его не трогает)"
	done
	for f in "$dir"/*.tar.zst.age; do
		[ -f "$f" ] || continue
		e=$(backup_name_epoch "$f")
		[ -n "$e" ] || continue
		if [ $((now - e)) -gt $((backup_keep_days * 86400)) ]; then
			rm -f -- "$f" && n=$((n + 1))
		fi
	done
	find "$dir" -maxdepth 1 -type f -name '*.tar.zst.age.part' -mmin +1440 -delete
	echo "чистка копий: удалено $n"
}

# Предупреждение (не отказ): configure при остановленном заборе должен работать.
backup_freshness_warn() {
	local dir=$1 now f e newest=0
	now=$(date -u +%s)
	for f in "$dir"/*.tar.zst.age; do
		[ -f "$f" ] || continue
		e=$(backup_name_epoch "$f")
		[ -n "$e" ] && [ "$e" -gt "$newest" ] && newest=$e
	done
	if [ "$newest" -eq 0 ] || [ $((now - newest)) -gt $((backup_warn_days * 86400)) ]; then
		echo "ПРЕДУПРЕЖДЕНИЕ: проверенной копии младше $backup_warn_days дней нет — забор копий остановлен?" >&2
	fi
}
