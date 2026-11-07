#!/usr/bin/env bash
# Проверка Caddyfile без установки Caddy: `caddy validate` в официальном образе по дайджесту (v2.11.7).
# Дополнительно — `caddy adapt` и deploy/tests/caddy-adapt-check.py: каждый reverse_proxy идёт после среза X-Sa-* и
# X-Forwarded-* (поведением его не увидеть: сервис эти заголовки не читает).
# Запуск: bash deploy/tests/caddy-validate.sh   (нужны podman и python3; на сервер не ходит, образ тянется, если его нет)
set -euo pipefail

# Образ Caddy — та же версия, что ставит роль Ansible из apt-репозитория Caddy (deploy/README.md).
CADDY_IMAGE="${CADDY_IMAGE:-docker.io/library/caddy@sha256:36c24ad91a7ff7bc4c87fe22bfa8df3c4b18da0773d73fc12ee2c52b98c0bb7e}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
caddyfile="$root/deploy/caddy/Caddyfile"

validate() {
	podman run --rm --network=none --read-only --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
		-e "ALBUMS_DOMAIN=$1" \
		-v "$caddyfile:/etc/caddy/Caddyfile:ro,Z" \
		"$CADDY_IMAGE" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
}

# Выдуманный домен — только для проверки синтаксиса (в файлах зоны настоящего домена нет).
validate albums.example.org >/dev/null
echo "OK: Caddyfile проходит caddy validate"

# Без домена Caddy обязан отказаться стартовать, а не подняться с пустым именем сайта.
if validate "" >/dev/null 2>&1; then
	echo "FAIL: Caddyfile принят без ALBUMS_DOMAIN" >&2
	exit 1
fi
echo "OK: без ALBUMS_DOMAIN Caddyfile отвергнут"

# Срез входящих X-Sa-* и X-Forwarded-* стоит в цепочке раньше каждого reverse_proxy.
podman run --rm --network=none --read-only --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
	-e "ALBUMS_DOMAIN=albums.example.org" -v "$caddyfile:/etc/caddy/Caddyfile:ro,Z" \
	"$CADDY_IMAGE" caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile 2>/dev/null |
	python3 "$root/deploy/tests/caddy-adapt-check.py"
