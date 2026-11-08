#!/usr/bin/env bash
# Проверка гипотезы И1 и Caddyfile на заглушке (локально, rootless Podman, без sudo и без сервера):
#  1) unix-сокет из контейнера с Network=none, ReadOnly, cap-drop=ALL, no-new-privileges, обычной картой subuid
#     доступен Caddy через ACL на каталоге сокета (как в deploy/systemd/albums-svc.tmpfiles.conf); тому же
#     Caddy без ACL — нет (502); хосту под другим uid — нет;
#  2) Caddy: срезает X-Sa-*/X-Forwarded-*, ставит заголовки, отдаёт единый 404/502 из notfound.html, режет тело
#     по маршрутам, не пишет в журнал id альбома (в любой записи пути), строку запроса, токен и cookie.
# Это не матрица доступа (она у П1, tests/access/): здесь только то, от чего зависит схема развёртывания.
# Запуск: bash deploy/tests/socket-probe.sh  (нужны podman, curl, setfacl; рабочие файлы — во временном каталоге)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Рабочий каталог короткий: путь unix-сокета не длиннее 107 байт (sun_path), а в репозитории путь длиннее.
work="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/sa-probe.XXXXXX")"
CADDY_IMAGE="${CADDY_IMAGE:-docker.io/library/caddy@sha256:36c24ad91a7ff7bc4c87fe22bfa8df3c4b18da0773d73fc12ee2c52b98c0bb7e}"
# Суффикс $$ — чтобы параллельный запуск или чужой контейнер с тем же именем не мешали и не гибли.
sfx=$$
svc="sa-probe-svc-$sfx"
cad="sa-probe-caddy-$sfx"
cad2="sa-probe-caddy2-$sfx"
svc3="sa-probe-svcnoacl-$sfx"
secret="sa-probe-secret-$sfx"
port=$((20000 + sfx % 20000))
port2=$((port + 1))
# Образ сервиса — в той же карте, что у Quadlet: процесс 65532, Caddy — произвольные uid 5000 (с ACL) и 5001 (без).
svc_cuid=65532
cad_cuid=5000
cad2_cuid=5001
subuid_base="$(awk -F: -v u="$(id -un)" '$1==u{print $2; exit}' /etc/subuid)"
[ -n "$subuid_base" ] || {
	echo "нет диапазона subuid у пользователя" >&2
	exit 2
}
# Контейнерный uid N (N >= 1) на хосте — subuid_base + N - 1.
host_uid() { echo $((subuid_base + $1 - 1)); }
fails=0

cleanup() {
	# По одному: podman rm -f с несуществующим именем (svc3 уже убран) обрывает удаление остальных.
	for n in "$svc" "$cad" "$cad2" "$svc3"; do
		podman rm -f "$n" >/dev/null 2>&1 || true
	done
	podman secret rm "$secret" >/dev/null 2>&1 || true
}
finish() {
	cleanup
	podman rmi -f "localhost/albums-svc-stub:probe-$sfx" >/dev/null 2>&1 || true
	# Каталоги с чужим (subuid) владельцем чистим изнутри пользовательского пространства имён.
	podman unshare rm -rf "$work" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap finish EXIT
cleanup

ok() { echo "OK:   $*"; }
bad() {
	echo "FAIL: $*" >&2
	fails=$((fails + 1))
}
# has/hasnt <описание> <текст> <regexp>
has() {
	if printf '%s' "$2" | grep -qiE -- "$3"; then ok "$1"; else bad "$1"; fi
}
hasnt() {
	if printf '%s' "$2" | grep -qiE -- "$3"; then bad "$1"; else ok "$1"; fi
}
# eq <описание> <факт> <ожидание>
eq() {
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (получено «$2», ждали «$3»)"; fi
}
ne() {
	if [ "$2" != "$3" ]; then ok "$1"; else bad "$1 (получено «$2»)"; fi
}
body_is_notfound() {
	if cmp -s "$work/body" "$work/errors/notfound.html"; then ok "$1"; else bad "$1"; fi
}

chmod 0755 "$work"
mkdir -p "$work/ctx/cmd/albums-svc" "$work/ctx/internal" "$work/ctx/web" "$work/errors" "$work/cfg" "$work/cfg-noacl"
mkdir -p "$work/run-noacl"
mkdir -p "$work/data"
chmod 0700 "$work/data"
mkdir -p "$work/run"
chmod 0750 "$work/run"
chmod 0755 "$work/errors"

# Контекст сборки: заглушка вместо cmd/albums-svc, остальное — как в репозитории.
cp "$root/go.mod" "$work/ctx/go.mod"
cp "$root/deploy/tests/stub/main.go.txt" "$work/ctx/cmd/albums-svc/main.go"
touch "$work/ctx/internal/.keep" "$work/ctx/web/.keep"
printf '<!doctype html><html lang="ru"><meta charset="utf-8"><title>Альбом не найден</title><p>Альбом не найден</p></html>\n' >"$work/errors/notfound.html"
chmod 0644 "$work/errors/notfound.html"
# Конфиг — с боевыми правами (/etc/albums-svc: каталог 0750, файл 0640, владелец — не процесс сервиса).
for d in cfg cfg-noacl; do
	printf '{}\n' >"$work/$d/config.json"
	chmod 0750 "$work/$d"
	chmod 0640 "$work/$d/config.json"
done

podman build -q --ignorefile "$root/deploy/Containerfile.containerignore" -f "$root/deploy/Containerfile" -t "localhost/albums-svc-stub:probe-$sfx" "$work/ctx" >/dev/null
ok "podman build по Containerfile (заглушка вместо бинарника, GOTOOLCHAIN=local)"

# Каталог сокета — как в проде: ACL для Caddy (tmpfiles: «a+ … u:caddy:x,d:u:caddy:rw»), затем владелец —
# uid сервиса в карте subuid (ExecStartPre юнита). ACL при chown сохраняется. Данные — тот же chown.
chuid="$(host_uid "$cad_cuid")"
setfacl -m "u:$chuid:x,d:u:$chuid:rw" "$work/run"
podman unshare chown "$svc_cuid:$svc_cuid" "$work/run" "$work/data"
# Конфиг читает процесс сервиса (subuid), которому он не принадлежит: ACL на каталог (rx; default r — новые
# файлы в нём, например после замены config.json, наследуют доступ) и на файл (r). Caddy и прочие — без доступа.
suid="$(host_uid "$svc_cuid")"
setfacl -m "u:$suid:rx,d:u:$suid:r" "$work/cfg"
setfacl -m "u:$suid:r" "$work/cfg/config.json"

printf 'dGVzdC1zZWNyZXQtdGVzdC1zZWNyZXQtdGVzdC1zZWNyZXQtMDEyMw' | podman secret create "$secret" - >/dev/null

# Флаги — как в Quadlet (deploy/quadlet/albums-svc.container.j2).
podman run -d --name "$svc" --network=none --read-only --read-only-tmpfs=false --cap-drop=ALL \
	--security-opt=no-new-privileges --user "$svc_cuid:$svc_cuid" --memory=256m --pids-limit=128 \
	--secret "$secret,type=mount,target=albums_master,mode=0400,uid=$svc_cuid,gid=$svc_cuid" \
	-v "$work/cfg:/etc/albums-svc:ro,noexec,nosuid,nodev" -v "$work/data:/data:rw,noexec,nosuid,nodev" \
	-v "$work/run:/run/albums-svc:rw,noexec,nosuid,nodev" "localhost/albums-svc-stub:probe-$sfx" >/dev/null
sleep 1

# Конфиг без ACL: сервис не стартует (до открытия сокета), контейнер завершается с ошибкой.
podman unshare chown "$svc_cuid:$svc_cuid" "$work/run-noacl"
podman run -d --name "$svc3" --network=none --read-only --read-only-tmpfs=false --cap-drop=ALL \
	--security-opt=no-new-privileges --user "$svc_cuid:$svc_cuid" \
	-v "$work/cfg-noacl:/etc/albums-svc:ro,noexec,nosuid,nodev" -v "$work/run-noacl:/run/albums-svc:rw" \
	"localhost/albums-svc-stub:probe-$sfx" >/dev/null
sleep 2
eq "без ACL на конфиг сервис не стартует (контейнер завершён)" "$(podman inspect "$svc3" --format '{{.State.Status}}')" exited
ne "…с кодом возврата ≠ 0" "$(podman inspect "$svc3" --format '{{.State.ExitCode}}')" 0
has "…и сообщением о конфиге" "$(podman logs "$svc3" 2>&1)" 'config'
podman rm -f "$svc3" >/dev/null
eq "сервис с ACL на конфиг работает" "$(podman inspect "$svc" --format '{{.State.Status}}')" running

# --- сокет: владелец и режим; хост под своим uid к нему не подключается (он не владелец и без ACL)
eq "владелец сокета — uid сервиса (в пространстве имён пользователя), режим 0660" "$(podman unshare stat -c '%u %a' "$work/run/http.sock" 2>/dev/null || echo none)" "$svc_cuid 660"
if curl -sS --max-time 3 --unix-socket "$work/run/http.sock" http://localhost/v1/health >/dev/null 2>&1; then
	bad "хост под обычным uid подключился к сокету"
else
	ok "хост под обычным uid к сокету не подключается"
fi
if podman exec "$svc" /albums-svc health -config /etc/albums-svc/config.json; then ok "health изнутри контейнера по сокету"; else bad "health изнутри"; fi
eq "Network=none" "$(podman inspect "$svc" --format '{{.HostConfig.NetworkMode}}')" none
eq "процесс не root" "$(podman inspect "$svc" --format '{{.Config.User}}')" "$svc_cuid:$svc_cuid"
hasnt "в образе нет оболочки (exec sh не работает)" "$(podman exec "$svc" /bin/sh -c id 2>&1 || true)" "uid="
# Корень только для чтения и без tmpfs поверх /tmp, /run, /dev: писать некуда, кроме томов.
mounts="$(podman inspect "$svc" --format '{{range .Mounts}}{{.Destination}} {{end}}')"
hasnt "нет tmpfs-монтирований помимо томов (ReadOnlyTmpfs=false)" "$(podman inspect "$svc" --format '{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.Tmpfs}}')" 'map\[[^]]+\]'
has "тома: data, сокет, config, секрет" "$mounts" '/data .*/run/albums-svc|/run/albums-svc .*/data'

# --- Caddy с ACL (uid 5000) и без ACL (uid 5001); домен — переменная среды, как в проде
# Caddy-uid конфиг прочитать не может (ни по группе, ни по ACL).
if podman run --rm --network=none --cap-drop=ALL --user "$cad_cuid:$cad_cuid" -v "$work/cfg:/c:ro" \
	"$CADDY_IMAGE" cat /c/config.json >/dev/null 2>&1; then
	bad "Caddy-uid прочитал config.json"
else
	ok "Caddy-uid не читает config.json"
fi
run_caddy() { # имя порт uid
	podman run -d --name "$1" --user "$3:$3" --read-only --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
		--sysctl net.ipv4.ip_unprivileged_port_start=0 -p "127.0.0.1:$2:443" -e ALBUMS_DOMAIN=localhost \
		-e XDG_DATA_HOME=/tmp/caddy-data -e XDG_CONFIG_HOME=/tmp/caddy-config --tmpfs /tmp:rw,size=32m,mode=1777 \
		-v "$root/deploy/caddy/Caddyfile:/etc/caddy/Caddyfile:ro,Z" \
		-v "$work/errors:/usr/share/albums-svc/errors:ro,Z" -v "$work/run:/run/albums-svc" \
		"$CADDY_IMAGE" caddy run --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
}
run_caddy "$cad" "$port" "$cad_cuid"
run_caddy "$cad2" "$port2" "$cad2_cuid"
sleep 3

c() { curl -sk --max-time 10 --resolve "localhost:$port:127.0.0.1" "$@"; }
c2() { curl -sk --max-time 10 --resolve "localhost:$port2:127.0.0.1" "$@"; }
base="https://localhost:$port"
id26=abcdefghij2345abcdefghij23
token=probe-token-0123456789
cookiev='__Secure-sa_session=1.1.9999999999.PROBECOOKIEVALUE'

eq "Caddy с ACL (uid $cad_cuid) -> сервис отвечает 200" "$(c -o /dev/null -w '%{http_code}' "$base/v1/health")" 200
eq "Caddy без ACL (uid $cad2_cuid) -> 502" "$(c2 -o /dev/null -w '%{http_code}' "https://localhost:$port2/v1/health")" 502

# заголовки от клиента не проходят; сервис видит только X-Sa-Client-IP от Caddy
out="$(c "$base/v1/health" -H 'X-Sa-Client-IP: 203.0.113.9' -H 'X-Sa-Other: 1' -H 'X-Forwarded-For: 198.51.100.8' -H 'X-Forwarded-Host: evil')"
has "через Caddy стаб ответил (сокет доступен Caddy)" "$out" '"stub":true'
has "сервис видит X-Sa-Client-Ip от Caddy" "$out" '"X-Sa-Client-Ip":"[0-9.]+"'
hasnt "подмена X-Sa-Client-IP не прошла" "$out" '203\.0\.113\.9'
hasnt "чужой X-Sa-Other срезан" "$out" 'X-Sa-Other'
hasnt "чужие X-Forwarded-For/Host срезаны (Caddy ставит свои)" "$out" '198\.51\.100\.8|evil'

# заголовки безопасности на ответ сервиса (стаб своих не ставит — значит, ставит Caddy)
hdr="$(c -D - -o /dev/null "$base/v1/health")"
has "HSTS" "$hdr" '^strict-transport-security: max-age='
has "nosniff" "$hdr" '^x-content-type-options: nosniff'
has "Referrer-Policy" "$hdr" '^referrer-policy: no-referrer'
has "X-Robots-Tag" "$hdr" '^x-robots-tag: noindex'
has "CSP" "$hdr" "^content-security-policy: default-src 'none'"
hasnt "нет заголовка Server" "$hdr" '^server:'
hasnt "нет Alt-Svc (HTTP/3 выключен)" "$hdr" '^alt-svc:'

# единый 404 вне маршрутов, листинга и meta/ нет
eq "/meta/… -> 404" "$(c -o "$work/body" -w '%{http_code}' "$base/meta/x.json")" 404
body_is_notfound "404: тело — notfound.html"
hdr="$(c -D - -o /dev/null "$base/meta/x.json")"
has "404: no-store" "$hdr" 'cache-control: no-store'
has "404: nosniff" "$hdr" 'x-content-type-options: nosniff'
hasnt "404: нет Etag, Last-Modified, Accept-Ranges" "$hdr" '^(etag|last-modified|accept-ranges):'
eq "/ -> 404" "$(c -o /dev/null -w '%{http_code}' "$base/")" 404

# пределы тела: login 4 КиБ, /a/ и /viewer/ 1 КиБ, /v1 16 МиБ
post() { # путь размер
	head -c "$2" /dev/zero | tr '\0' a >"$work/payload"
	c -o /dev/null -w '%{http_code}' -X POST --data-binary @"$work/payload" "$base$1"
}
ne "POST login 3000 Б проходит предел (не 413)" "$(post "/a/$id26/login" 3000)" 413
eq "POST login 4097 Б -> 413" "$(post "/a/$id26/login" 4097)" 413
ne "POST /a/<id>/ 1000 Б проходит предел" "$(post "/a/$id26/" 1000)" 413
eq "POST /a/<id>/ 1025 Б -> 413" "$(post "/a/$id26/" 1025)" 413
eq "POST /viewer/x 1025 Б -> 413" "$(post "/viewer/x" 1025)" 413
ne "POST /v1 2 КиБ проходит предел" "$(post "/v1/albums" 2048)" 413
eq "POST /v1 17 МиБ -> 413" "$(post "/v1/albums" $((17 * 1024 * 1024)))" 413

# запросы с «секретами» — для проверки журнала; разные записи пути к одному и тому же id
c -o /dev/null "$base/a/$id26/?pw=SECRETQUERY" -H "Cookie: $cookiev" -H "Authorization: Bearer $token" -H 'Referer: https://x/a/zzz/'
c -o /dev/null "$base/a/$id26/manifest.json"
c -o /dev/null "$base/v1/albums/$id26" -H "Authorization: Bearer $token"
c -o /dev/null --path-as-is "$base/A/$id26/"
c -o /dev/null --path-as-is "$base//a/$id26/"
c -o /dev/null --path-as-is "$base/%61/$id26/"
c -o /dev/null --path-as-is "$base/v1/../a/$id26/"
c -o /dev/null --path-as-is "$base/a/$id26%2f..%2fx"
c -o /dev/null --path-as-is "$base/a%2F$id26/"
c -o /dev/null --path-as-is "$base/a;$id26/"
c -o /dev/null --path-as-is "$base/a\\$id26\\"
c -o /dev/null --path-as-is "$base/$id26/x"

# сервис остановлен: 502 с телом notfound.html, в журнале ошибок нет id
podman stop -t 2 "$svc" >/dev/null
eq "сервис остановлен -> 502" "$(c -o "$work/body" -w '%{http_code}' "$base/a/$id26/?pw=SECRETQUERY")" 502
body_is_notfound "502: тело — notfound.html"
sleep 1

logs="$(podman logs "$cad" 2>&1)"
printf '%s\n' "$logs" >"$work/caddy.log"
mkdir -p "$root/.gotmp"
cp "$work/caddy.log" "$root/.gotmp/probe-caddy.log"
grep -c '"logger":"http.log.access' "$work/caddy.log" | sed 's/^/INFO: строк журнала доступа: /'
for s in "$id26" SECRETQUERY "$token" PROBECOOKIEVALUE 'x/a/zzz' 'Bearer'; do
	hasnt "журнал не содержит «$s»" "$logs" "$s"
done
# Любая 26-знаковая строка из a-z2-7 без учёта регистра — похожа на id (маска из design §7).
hasnt "журнал не содержит ничего, похожего на id (26 знаков a-z2-7, любой регистр)" "$logs" '[a-z2-7]{26}'
uris="$(printf '%s\n' "$logs" | grep -o '"uri":"[^"]*"' | sort -u | tr '\n' ' ')"
echo "INFO: uri в журнале: $uris"
has "журнал: путь /a/… замаскирован" "$logs" '"uri":"/a/…"'
has "журнал: путь /v1/… замаскирован" "$logs" '"uri":"/v1/…"'
# Каждый uri в журнале — «/» + до 8 знаков [A-Za-z0-9] + «/…»; при 26-знаковом id это не больше его начала.
bad_uris=0
while IFS= read -r u; do
	[[ "$u" =~ ^\"uri\":\"/[A-Za-z0-9]{0,8}/…\"$ ]] || {
		echo "      uri вне формы: $u" >&2
		bad_uris=$((bad_uris + 1))
	}
done < <(printf '%s\n' "$logs" | grep -o '"uri":"[^"]*"' | sort -u)
eq "журнал: все uri вида /<до 8 знаков>/…" "$bad_uris" 0
hasnt "журнал: нет 9 первых знаков id" "$logs" "${id26:0:9}"
has "журнал: IP с маской /24 (…\.0)" "$logs" '"remote_ip":"[0-9]+\.[0-9]+\.[0-9]+\.0"'

cleanup
if [ "$fails" -gt 0 ]; then
	echo "ИТОГ: провалов $fails (журнал Caddy — .gotmp/probe-caddy.log)" >&2
	exit 1
fi
echo "ИТОГ: всё прошло"
