#!/bin/sh
# Диагностика albums-runtime на цели стенда (П2-8): запускается ВНУТРИ цели от root через `sh -s` из full.sh
# (albums_diag) и ci.sh (diag). Ничего не меняет, секретов не печатает: ни `podman secret`, ни окружение контейнера,
# ни значение vault; пробные контейнеры идут без секрета и с пустым смыслом (`health` с конфигом, который ничего не
# запускает). Каждая секция — «как получится»: упавшая команда не отменяет остальные.
# Зачем: crun пишет причину отказа создать контейнер в stderr юнита albums-svc (журнал пользователя albums), а
# системный `journalctl -p warning` её не показывает; ручной запуск с --log-level=debug даёт её сразу.
uid=$(id -u albums 2>/dev/null) || { echo "albums_diag: пользователя albums нет"; exit 0; }
home=$(getent passwd albums | cut -d: -f6)
[ -n "$home" ] || { echo "albums_diag: у albums нет домашнего каталога"; exit 0; }
rt=/run/user/$uid
sec() { printf '\n-- %s\n' "$*"; }

# От имени albums, с шиной и каталогом сеанса (linger поднял их сам).
as_albums() {
	runuser -u albums -- env "XDG_RUNTIME_DIR=$rt" "DBUS_SESSION_BUS_ADDRESS=unix:path=$rt/bus" "HOME=$home" "$@"
}

echo "== albums_diag: albums-runtime на цели (uid albums = $uid) =="

sec "карты идентификаторов: цель и albums"
echo "uid_map цели:"
cat /proc/self/uid_map
echo "uid_map albums:"
as_albums cat /proc/self/uid_map 2>&1
echo "/etc/subuid:"; cat /etc/subuid
echo "/etc/subgid:"; cat /etc/subgid
echo "newuidmap: $(ls -l /usr/bin/newuidmap 2>&1)"

sec "cgroup v2: корень цели и user@$uid.service"
echo "controllers: $(cat /sys/fs/cgroup/cgroup.controllers 2>&1)"
echo "subtree_control: $(cat /sys/fs/cgroup/cgroup.subtree_control 2>&1)"
echo "cgroup процесса: $(cat /proc/self/cgroup)"
find /sys/fs/cgroup -maxdepth 4 -type d -name "user@$uid.service" 2>/dev/null | while read -r d; do
	echo "$d controllers: $(cat "$d/cgroup.controllers" 2>&1)"
	echo "$d subtree_control: $(cat "$d/cgroup.subtree_control" 2>&1)"
done

sec "podman albums: cgroup, OCI-runtime, хранилище, карта"
as_albums podman info --format 'cgroupManager={{.Host.CgroupManager}} cgroups={{.Host.CgroupsVersion}} oci={{.Host.OCIRuntime.Name}} {{.Host.OCIRuntime.Version}} graph={{.Store.GraphDriverName}} rootless={{.Host.Security.Rootless}} uidmap={{.Host.IDMappings.UIDMap}}' 2>&1
as_albums podman --version 2>&1
echo "fuse-overlayfs: $(command -v fuse-overlayfs || echo нет); /dev/fuse: $(ls -l /dev/fuse 2>&1)"

sec "юнит albums-svc: состояние и сгенерированный Quadlet-юнит"
as_albums systemctl --user status albums-svc --no-pager -l 2>&1 | tail -n 30
as_albums systemctl --user cat albums-svc 2>&1

sec "журнал пользователя albums: albums-svc (там stderr crun и conmon)"
journalctl "_UID=$uid" -n 200 --no-pager -o cat 2>&1 | tail -n 200
journalctl "_SYSTEMD_USER_UNIT=albums-svc.service" -n 100 --no-pager 2>&1 | tail -n 100

image=$(sed -n 's/^Image=//p' "$home"/.config/containers/systemd/albums-svc.container 2>/dev/null | head -n 1)
echo "образ по Quadlet: ${image:-нет}"
if [ -n "$image" ]; then
	sec "ручной запуск A: минимум (podman --log-level=debug run --rm, без сети)"
	timeout 90 runuser -u albums -- env "XDG_RUNTIME_DIR=$rt" "DBUS_SESSION_BUS_ADDRESS=unix:path=$rt/bus" "HOME=$home" \
		podman --log-level=debug run --rm --network=none "$image" health -config /nonexistent 2>&1 | tail -n 40
	sec "ручной запуск B: флаги юнита (read-only, без capabilities, no-new-privileges, лимиты, uid 65532)"
	timeout 90 runuser -u albums -- env "XDG_RUNTIME_DIR=$rt" "DBUS_SESSION_BUS_ADDRESS=unix:path=$rt/bus" "HOME=$home" \
		podman --log-level=debug run --rm --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--user 65532:65532 --memory=256m --pids-limit=128 --ulimit nofile=4096:4096 \
		"$image" health -config /nonexistent 2>&1 | tail -n 40
	sec "ручной запуск C: B плюс тома юнита (конфиг, данные, каталог сокета); секрет не подключается"
	timeout 90 runuser -u albums -- env "XDG_RUNTIME_DIR=$rt" "DBUS_SESSION_BUS_ADDRESS=unix:path=$rt/bus" "HOME=$home" \
		podman --log-level=debug run --rm --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges \
		--user 65532:65532 --memory=256m --pids-limit=128 --ulimit nofile=4096:4096 \
		-v /etc/albums-svc:/etc/albums-svc:ro,noexec,nosuid,nodev -v /var/lib/albums-svc/data:/data:rw,noexec,nosuid,nodev \
		-v /run/albums-svc:/run/albums-svc:rw,noexec,nosuid,nodev \
		"$image" health -config /etc/albums-svc/config.json 2>&1 | tail -n 40
fi
echo "== albums_diag: конец =="
