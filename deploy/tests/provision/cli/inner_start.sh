#!/usr/bin/env bash
# Точка входа основного прогона provision_cli (спека 005, 005-33): добавляет на lo адрес CLI_IP и передаёт управление
# inner.sh уже без capabilities — NET_ADMIN нужен только на эти секунды, остальному коду он ни к чему.
# shellcheck source-path=SCRIPTDIR disable=SC1091
set -uo pipefail
R=/ref/deploy/tests/provision/cli
. "$R/fixtures/values.env"
python3 -I "$R/lo_addr.py" "$CLI_IP" || exit 1
# Ambient и inheritable сброшены: execve обычного пользователя без файловых capabilities оставляет пустой набор.
exec setpriv --ambient-caps=-all --inh-caps=-all bash "$R/inner.sh"
