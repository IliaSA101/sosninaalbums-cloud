#!/usr/bin/env bash
# Проверка Quadlet-шаблона без установки: рендер (python3 + jinja2), `quadlet -dryrun`, `systemd-analyze verify`,
# отказ на образе без дайджеста. Нужны podman (с quadlet), systemd-analyze, python3 с jinja2.
# Запуск: bash deploy/tests/quadlet-validate.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tpl="$root/deploy/quadlet/albums-svc.container.j2"
render="$root/deploy/tests/quadlet-render.py"
quadlet=/usr/libexec/podman/quadlet
work="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/sa-quadlet.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/in" "$work/out"

digest="sha256:$(printf '0%.0s' {1..64})"
python3 -I "$render" "$tpl" "registry.example.org/albums/albums-svc@$digest" >"$work/in/albums-svc.container"
echo "OK: шаблон рендерится с образом по дайджесту"

for bad in "registry.example.org/albums/albums-svc:latest" "registry.example.org/albums/albums-svc@sha256:abc" ""; do
	if python3 -I "$render" "$tpl" "$bad" >/dev/null 2>&1; then
		echo "FAIL: шаблон принял образ «$bad»" >&2
		exit 1
	fi
done
echo "OK: образ без полного дайджеста отвергнут"

QUADLET_UNIT_DIRS="$work/in" "$quadlet" -dryrun -user >"$work/dryrun.txt" 2>&1 || {
	cat "$work/dryrun.txt" >&2
	echo "FAIL: quadlet -dryrun" >&2
	exit 1
}
echo "OK: quadlet -dryrun"

# Ключевые флаги итоговой команды podman run — то, ради чего юнит написан.
for flag in --network=none --read-only --read-only-tmpfs=false --cap-drop=all --security-opt=no-new-privileges \
	--pids-limit --memory=256m --user=65532:65532 --health-on-failure kill; do
	grep -q -- "$flag" "$work/dryrun.txt" || {
		echo "FAIL: в команде нет $flag" >&2
		exit 1
	}
done
# Проверка здоровья — только exec-формой: строка уходит в `/bin/sh -c`, а в образе оболочки нет (CMD-SHELL падал бы).
# В выводе quadlet аргумент заключён в кавычки systemd: --health-cmd "[\"/albums-svc\",…]" (после разбора — чистый JSON).
grep -qF -- '--health-cmd "[\"/albums-svc\"' "$work/dryrun.txt" || {
	echo "FAIL: HealthCmd не в exec-форме (JSON-массив): в образе без оболочки проверка здоровья не запустится" >&2
	grep -- 'health-cmd' "$work/dryrun.txt" >&2 || true
	exit 1
}
echo "OK: HealthCmd в exec-форме"
if grep -q -- '--userns' "$work/dryrun.txt"; then
	echo "FAIL: в юните не должно быть --userns (обычная карта subuid)" >&2
	exit 1
fi
echo "OK: флаги изоляции на месте, --userns нет"

QUADLET_UNIT_DIRS="$work/in" "$quadlet" -user "$work/out" >/dev/null 2>&1
systemd-analyze --user verify "$work/out/albums-svc.service"
echo "OK: systemd-analyze verify"
