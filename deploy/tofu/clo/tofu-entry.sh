#!/bin/sh
# Точка входа контейнера tofu (design 002 §2): токен из файла-тома — в переменную только этого процесса, потом
# exec tofu. Токена нет в `-e`, `podman inspect` и argv. Файл на хосте удаляет provision.sh. Подкоманды, которым
# токен не нужен (init — без сети, из зеркала провайдера, ВР-15; show, output, fmt, version), идут без него; остальным
# (apply, validate, test, …) нужен читаемый непустой файл — иначе отказ, а не запуск с пустым токеном. provision.sh prepare
# запускает validate мимо этой обёртки (--entrypoint tofu: сверка h1 провайдера с lock, токен не нужен); сама обёртка
# validate без файла токена по-прежнему не пускает (проверяет tofu_static).
set -eu
case ${1:-} in
	init | show | output | fmt | version) ;;
	*)
		if [ ! -r /run/sa/token ] || [ ! -s /run/sa/token ]; then
			echo "tofu-entry: нет читаемого непустого файла токена /run/sa/token" >&2
			exit 1
		fi
		;;
esac
if [ -r /run/sa/token ]; then
	TF_VAR_clo_api_token=$(cat /run/sa/token)
	export TF_VAR_clo_api_token
fi
exec tofu "$@"
