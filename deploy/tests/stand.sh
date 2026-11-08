#!/usr/bin/env bash
# Локальный стенд П1: сервис (контейнер как в Quadlet) + Caddy (Caddyfile боя) на rootless Podman, без sudo,
# без сервера. На нём гоняется матрица доступа (deploy/tests/access/, pytest + httpx в контейнере Python по
# дайджесту) и проверка по контракту через Caddy (deploy/tests/conformance/); листалку открывают в браузере.
#
#   bash deploy/tests/stand.sh up [matrix|browser]   поднять (по умолчанию matrix)
#   bash deploy/tests/stand.sh test                  матрица + smoke + Conformance (нужен up matrix); сервис в конце запущен
#   bash deploy/tests/stand.sh seed                  опубликовать альбомы для браузера (без пароля и с паролем)
#   bash deploy/tests/stand.sh edit                  опубликовать правку альбома «для правки» из seed
#   bash deploy/tests/stand.sh conformance           только Conformance контракта через Caddy
#   bash deploy/tests/stand.sh smoke                 дымовая проверка deploy/ops/smoke.py (upload, clean, ls) токеном
#                                                    владельца smoke стенда против Caddy (нужен up matrix); входит и в test
#   bash deploy/tests/stand.sh pytest [стадия] [арг…] один запуск pytest (отладка; стадии: main, damage, down, logs)
#   bash deploy/tests/stand.sh env                   адреса, каталоги, где токены (значения не печатаются)
#   bash deploy/tests/stand.sh down [--images]       остановить и убрать всё (--images: и образы localhost/sa-stand-*)
#
# Чем стенд отличается от боя (всё остальное — те же флаги и файлы):
#  1) Caddyfile — копия deploy/caddy/Caddyfile с двумя правками (sed ниже, число изменённых строк проверяется):
#     к имени сайта из ALBUMS_DOMAIN добавлен вход http://127.0.0.1:8080 (для браузера) и «tls internal» (у имени
#     .test нет публичного сертификата; без этой строки Caddy пошёл бы за ним в Let's Encrypt); в глобальный блок
#     добавлен skip_install_trust (в контейнере некуда ставить корень CA, а попытка пишет в журнал предупреждения
#     с «password»). Сами обработчики — те же байты.
#  2) Caddy — в контейнере с непривилегированным пользователем и sysctl на порты от 0; в бою он процесс хоста.
#  3) Режим matrix: public_base = https://<домен стенда>, ровно как в бою (CSP, заголовки 404 совпадают с
#     handle_errors Caddy). Режим browser: public_base = http://127.0.0.1:<порт> — тогда CSP листалки разрешает
#     скрипты с этого адреса; Chromium считает 127.0.0.1 безопасным контекстом и принимает cookie «Secure».
#     Матрица в этом режиме не идёт (test откажется): CSP Caddy ссылается на https-имя.
#  4) Тестовые контейнеры подключаются к сетевому пространству контейнера Caddy: так Caddy видит адрес клиента
#     (127.x.y.z по выбору теста), и лимит попыток по IP проверяется честно. В бою клиентов видно по настоящим
#     адресам; публикация порта через slirp4netns подменяла бы все адреса на один.
#  5) У сервиса нет HealthCmd и HealthOnFailure=kill из Quadlet: перезапуск по проверке здоровья на стенде не нужен
#     (готовность ждёт сам stand.sh через `albums-svc health`).
#  6) Режим matrix: кроме https все маршруты открыты ещё и по http://127.0.0.1:8080 внутри контейнера Caddy (порт
#     наружу публикуется всегда, для браузера); в бою у сайта только https и переадресация с 80.
# Рабочий каталог — короткий путь в XDG_RUNTIME_DIR (sun_path ≤ 107 байт; без неё стенд не поднимается: /tmp общий,
# а в нём лежат токены); каталог и out/ — 0700 (читать их может только владелец; процессам контейнеров с чужими uid
# из subuid нужен только проход, его даёт 0711 корня стенда и ACL на подкаталогах). Токены — в private/, не в выводе.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[ -n "${XDG_RUNTIME_DIR:-}" ] || {
	echo "stand: нет XDG_RUNTIME_DIR (сеанс пользователя systemd): рабочий каталог стенда — в нём, не в общем /tmp" >&2
	exit 1
}
work="$XDG_RUNTIME_DIR/sa-stand"
# Образы — только по дайджесту. CADDY_IMAGE — как в caddy-validate.sh; GO_IMAGE — как в Containerfile;
# образ Python пинит deploy/tests/access/Containerfile.
CADDY_IMAGE="${CADDY_IMAGE:-docker.io/library/caddy@sha256:36c24ad91a7ff7bc4c87fe22bfa8df3c4b18da0773d73fc12ee2c52b98c0bb7e}"
GO_IMAGE="${GO_IMAGE:-docker.io/library/golang@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b}"
SVC_IMAGE=localhost/sa-stand-svc:stand
ACCESS_IMAGE=localhost/sa-stand-access:stand
svc=sa-stand-svc
cad=sa-stand-caddy
secret=sa-stand-secret
domain=albums.test
port="${STAND_PORT:-18480}"
svc_cuid=65532
cad_cuid=5000
tst_cuid=5001 # тестовые контейнеры: не root в контейнере, чужой uid из subuid, не владелец файлов пользователя
state="$work/state"

die() {
	echo "stand: $*" >&2
	exit 1
}

need() {
	local t
	for t in podman curl setfacl awk; do
		command -v "$t" >/dev/null || die "нужен $t"
	done
	subuid_base="$(awk -F: -v u="$(id -un)" '$1==u{print $2; exit}' /etc/subuid)"
	[ -n "$subuid_base" ] || die "у пользователя нет диапазона subuid"
}
# Контейнерный uid N (N ≥ 1) на хосте — subuid_base + N - 1.
host_uid() { echo $((subuid_base + $1 - 1)); }

state_get() { sed -n "s/^$1=//p" "$state" 2>/dev/null | head -n 1; }

running() { [ -f "$state" ]; }

wait_health() { # имя контейнера сервиса; ждём до ~20 с
	for _ in $(seq 1 40); do
		if podman exec "$svc" /albums-svc health -config /etc/albums-svc/config.json >/dev/null 2>&1; then return 0; fi
		sleep 0.5
	done
	return 1
}

# Флаги — как в Quadlet (deploy/quadlet/albums-svc.container.j2).
start_svc() {
	podman run -d --name "$svc" --network=none --read-only --read-only-tmpfs=false --cap-drop=ALL \
		--security-opt=no-new-privileges --user "$svc_cuid:$svc_cuid" --memory=256m --pids-limit=128 \
		-e GOMEMLIMIT=200MiB --ulimit nofile=4096:4096 \
		--secret "$secret,type=mount,target=albums_master,mode=0400,uid=$svc_cuid,gid=$svc_cuid" \
		-v "$work/cfg:/etc/albums-svc:ro,noexec,nosuid,nodev" -v "$work/data:/data:rw,noexec,nosuid,nodev" \
		-v "$work/run:/run/albums-svc:rw,noexec,nosuid,nodev" "$SVC_IMAGE" >/dev/null
}

cmd_up() {
	local mode="${1:-matrix}" public_base
	case "$mode" in matrix | browser) ;; *) die "режим: matrix или browser" ;; esac
	need
	if running || podman container exists "$svc" 2>/dev/null || podman container exists "$cad" 2>/dev/null; then
		die "стенд уже поднят или остался с прошлого раза: bash deploy/tests/stand.sh down"
	fi
	case "$mode" in
	matrix) public_base="https://$domain" ;;
	browser) public_base="http://127.0.0.1:$port" ;;
	esac

	# Корень стенда и out/ — 0700: читает только владелец. Подкаталоги монтируются в контейнеры целиком (источник
	# монтирования разрешает сам podman от имени пользователя), поэтому процессам с чужими uid проход через корень не нужен.
	mkdir -p "$work"
	chmod 0700 "$work"
	mkdir -p "$work/private" "$work/cfg" "$work/data" "$work/run" "$work/errors" "$work/out" "$work/gosrc"
	chmod 0700 "$work/private" "$work/data" "$work/out"
	chmod 0750 "$work/run" "$work/cfg"
	chmod 0755 "$work/errors" "$work/gosrc"
	# Тестовый контейнер пишет в out/ (секреты прогона, состояние между стадиями): ACL на его uid.
	setfacl -m "u:$(host_uid "$tst_cuid"):rwx" "$work/out"

	echo "стенд: сборка образов"
	podman build -q --ignorefile "$root/deploy/Containerfile.containerignore" -f "$root/deploy/Containerfile" \
		-t "$SVC_IMAGE" "$root" >/dev/null
	podman build -q -f "$root/deploy/tests/access/Containerfile" -t "$ACCESS_IMAGE" "$root/deploy/tests/access" >/dev/null

	# Токены двух владельцев и мастер-секрет — выдаёт сам бинарник; в вывод не идут.
	local t1 t2 t3 h1 h2 h3 out
	out="$(podman run --rm --network=none "$SVC_IMAGE" -gen-token)"
	t1="$(sed -n 's/^token: *//p' <<<"$out")"
	h1="$(sed -n 's/^sha256: *//p' <<<"$out")"
	out="$(podman run --rm --network=none "$SVC_IMAGE" -gen-token)"
	t2="$(sed -n 's/^token: *//p' <<<"$out")"
	h2="$(sed -n 's/^sha256: *//p' <<<"$out")"
	out="$(podman run --rm --network=none "$SVC_IMAGE" -gen-token)"
	t3="$(sed -n 's/^token: *//p' <<<"$out")"
	h3="$(sed -n 's/^sha256: *//p' <<<"$out")"
	if [ -z "$t1" ] || [ -z "$h1" ] || [ -z "$t2" ] || [ -z "$h2" ] || [ -z "$t3" ] || [ -z "$h3" ]; then die "токены не выданы"; fi
	# Токен владельца smoke — файлом (его читает smoke.py с тома ro, как на ops-ВМ), не в окружении и не в env-файле.
	(umask 077 && printf '%s' "$t3" >"$work/private/smoke_token")
	setfacl -m "u:$(host_uid "$tst_cuid"):r" "$work/private/smoke_token"
	podman run --rm --network=none "$SVC_IMAGE" -gen-secret >"$work/private/master"
	chmod 0600 "$work/private/master"
	podman secret rm "$secret" >/dev/null 2>&1 || true
	podman secret create "$secret" "$work/private/master" >/dev/null

	# Подпись студии первого владельца — с разметкой: листалка и форма обязаны вывести её текстом.
	local studio1='Студия <b>x</b><script>'
	# Сайт студии первого владельца (спека 005): view.json отдаёт его, у второго владельца сайта нет — ответ как в v0.1.1.
	local site1='https://studio.example/albums'
	cat >"$work/cfg/config.json" <<EOF
{
  "listen": "unix:/run/albums-svc/http.sock",
  "public_base": "$public_base",
  "data_dir": "/data",
  "secret_file": "/run/secrets/albums_master",
  "disk_reserve_bytes": 1048576,
  "owners": [
    {"slug": "stand-a", "studio": "$studio1", "site": "$site1", "tokens": [{"name": "stand", "sha256": "$h1"}]},
    {"slug": "stand-b", "studio": "Студия Два", "tokens": [{"name": "stand", "sha256": "$h2"}]},
    {"slug": "smoke", "studio": "Дымовая проверка", "quota_bytes": 1048576, "max_albums": 1, "tokens": [{"name": "smoke", "sha256": "$h3"}]}
  ]
}
EOF
	chmod 0640 "$work/cfg/config.json"
	cat >"$work/private/env" <<EOF
SA_BASE=https://$domain
SA_HOST=$domain
SA_CA=/stand/root.crt
SA_PUBLIC_BASE=$public_base
SA_BROWSER_BASE=http://127.0.0.1:$port
SA_STUDIO1=$studio1
SA_SITE1=$site1
SA_TOKEN1=$t1
SA_TOKEN2=$t2
EOF
	chmod 0600 "$work/private/env"
	printf '127.0.0.1 localhost\n127.0.0.1 %s\n' "$domain" >"$work/hosts"
	# Страница «Альбом не найден» — тот же файл, что развёртывание кладёт для handle_errors (design §1).
	install -m 0644 "$root/web/viewer/notfound.html" "$work/errors/notfound.html"

	# Caddyfile стенда: две правки (см. пункт 1 в шапке); число изменённых строк сверяется.
	# shellcheck disable=SC2016 # «$» в шаблоне sed — буквальный текст Caddyfile ({$ALBUMS_DOMAIN}), а не переменная
	sed -e 's|^{\$ALBUMS_DOMAIN} {$|{$ALBUMS_DOMAIN}, http://127.0.0.1:8080 {\n\ttls internal|' \
		-e 's|^\tadmin off$|\tadmin off\n\tskip_install_trust|' \
		"$root/deploy/caddy/Caddyfile" >"$work/Caddyfile"
	[ "$(diff "$root/deploy/caddy/Caddyfile" "$work/Caddyfile" | grep -c '^[<>]')" = 4 ] ||
		die "правки Caddyfile не применились (изменились строки «admin off» или адреса сайта?)"

	# Каталог сокета — как в проде: ACL для Caddy (tmpfiles: «u:caddy:x,d:u:caddy:rw»), затем владелец — uid
	# сервиса в карте subuid (ExecStartPre юнита); ACL при chown сохраняется. Конфиг — ACL на uid процесса.
	local cuid suid
	cuid="$(host_uid "$cad_cuid")"
	suid="$(host_uid "$svc_cuid")"
	setfacl -m "u:$cuid:x,d:u:$cuid:rw" "$work/run"
	podman unshare chown "$svc_cuid:$svc_cuid" "$work/run" "$work/data"
	setfacl -m "u:$suid:rx,d:u:$suid:r" "$work/cfg"
	setfacl -m "u:$suid:r" "$work/cfg/config.json"
	printf 'mode=%s\nport=%s\npublic_base=%s\n' "$mode" "$port" "$public_base" >"$state"

	start_svc
	wait_health || {
		podman logs "$svc" 2>&1 | tail -n 20 >&2
		die "сервис не стал здоровым"
	}

	# --cap-add=NET_BIND_SERVICE нужен не для портов (их открывает sysctl), а для запуска: у бинарника /usr/bin/caddy
	# в образе файловая capability cap_net_bind_service, и при cap-drop=ALL exec падает с «Operation not permitted»
	# (проверено: без флага Caddy не стартует).
	podman run -d --name "$cad" --user "$cad_cuid:$cad_cuid" --read-only --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
		--security-opt=no-new-privileges --sysctl net.ipv4.ip_unprivileged_port_start=0 \
		-p "127.0.0.1:$port:8080" -e "ALBUMS_DOMAIN=$domain" \
		-e XDG_DATA_HOME=/tmp/caddy-data -e XDG_CONFIG_HOME=/tmp/caddy-config --tmpfs /tmp:rw,size=32m,mode=1777 \
		-v "$work/Caddyfile:/etc/caddy/Caddyfile:ro,Z" -v "$work/errors:/usr/share/albums-svc/errors:ro,Z" \
		-v "$work/run:/run/albums-svc" \
		"$CADDY_IMAGE" caddy run --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
	local ok=0
	for _ in $(seq 1 60); do
		if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$port/v1/health" 2>/dev/null || true)" = 200 ] &&
			podman cp "$cad:/tmp/caddy-data/caddy/pki/authorities/local/root.crt" "$work/root.crt" >/dev/null 2>&1; then
			ok=1
			break
		fi
		sleep 0.5
	done
	[ "$ok" = 1 ] || {
		podman logs "$cad" 2>&1 | tail -n 20 >&2
		die "Caddy не ответил через сокет к сервису"
	}
	chmod 0644 "$work/root.crt"
	echo "стенд поднят (режим $mode). Подробности: bash deploy/tests/stand.sh env"
}

cmd_down() {
	need
	local n
	for n in "$cad" "$svc"; do
		podman rm -f -t 2 "$n" >/dev/null 2>&1 || true
	done
	podman secret rm "$secret" >/dev/null 2>&1 || true
	if [ -d "$work" ]; then
		# Каталоги с чужим (subuid) владельцем чистим изнутри пользовательского пространства имён.
		podman unshare rm -rf "$work" >/dev/null 2>&1 || true
		rm -rf "$work"
	fi
	if [ "${1:-}" = "--images" ]; then
		podman rmi -f "$SVC_IMAGE" "$ACCESS_IMAGE" >/dev/null 2>&1 || true
	fi
	echo "стенд убран"
}

cmd_env() {
	running || die "стенд не поднят: bash deploy/tests/stand.sh up"
	cat <<EOF
режим:            $(state_get mode)
https (матрица):  https://$domain (имя видно только контейнерам стенда: сетевое пространство контейнера $cad)
http для браузера: http://127.0.0.1:$(state_get port)  (public_base в конфиге: $(state_get public_base))
рабочий каталог:  $work
токены загрузки:  $work/private/env (SA_TOKEN1, SA_TOKEN2; файл 0600, в вывод не идут)
токен smoke:      $work/private/smoke_token (владелец smoke: 1 альбом, 1 МиБ; для stand.sh smoke)
журнал Caddy:     podman logs $cad
журнал сервиса:   podman logs $svc
корневой сертификат Caddy (tls internal): $work/root.crt
EOF
}

# Тестовый контейнер: в сетевом пространстве Caddy (127.0.0.1:443 — его сайт; адрес источника выбирает тест),
# файловая система только для чтения, без capability; свой /etc/hosts с именем стенда.
run_py() { # stage команда…
	local stage="$1"
	shift
	podman run --rm --network="container:$cad" --user "$tst_cuid:$tst_cuid" --read-only --tmpfs /tmp:rw,size=64m --cap-drop=ALL \
		--security-opt=no-new-privileges --memory=512m --pids-limit=256 \
		-v "$root/deploy/tests/access:/t:ro" -v "$work/out:/out:rw" -v "$work/root.crt:/stand/root.crt:ro" \
		-v "$work/hosts:/etc/hosts:ro" -v "$root/deploy/ops/smoke.py:/src/deploy/ops/smoke.py:ro" \
		--env-file "$work/private/env" -e "SA_STAGE=$stage" -e SA_OUT=/out \
		-e PYTHONDONTWRITEBYTECODE=1 -w /t "$ACCESS_IMAGE" "$@"
}

run_pytest() { # stage
	run_py "$1" python -I -m pytest -p no:cacheprovider -q --no-header -rfE /t
}

# Conformance контракта через Caddy: Go-тест deploy/tests/conformance/ в контейнере golang (по дайджесту), модули —
# из кэша хоста только на чтение (сеть не нужна, GOPROXY=off), адрес и токены — из окружения.
run_conformance() {
	local modcache
	modcache="$(go env GOMODCACHE 2>/dev/null || true)"
	[ -d "$modcache" ] || modcache="${HOME}/go/pkg/mod"
	[ -d "$modcache" ] || die "нет кэша модулей Go хоста ($modcache): Conformance идёт без сети"
	# Исходники — только то, что нужно тесту (go.mod, go.sum, сам тест), копией: ни .git, ни docs/ в контейнер не идут.
	rm -rf "$work/gosrc"
	mkdir -p "$work/gosrc/deploy/tests"
	cp "$root/go.mod" "$root/go.sum" "$work/gosrc/"
	cp -r "$root/deploy/tests/conformance" "$work/gosrc/deploy/tests/"
	chmod -R a+rX "$work/gosrc"
	podman run --rm --network="container:$cad" --user "$tst_cuid:$tst_cuid" --read-only --tmpfs /tmp:rw,size=512m,exec --cap-drop=ALL \
		--security-opt=no-new-privileges --memory=2g --pids-limit=512 \
		-v "$work/gosrc:/src:ro" -v "$modcache:/modcache:ro" -v "$work/root.crt:/stand/root.crt:ro" \
		-v "$work/hosts:/etc/hosts:ro" --env-file "$work/private/env" \
		-e GOMODCACHE=/modcache -e GOPROXY=off -e GOFLAGS=-mod=readonly -e GOTOOLCHAIN=local -e GOCACHE=/tmp/gocache \
		-e CGO_ENABLED=0 -e HOME=/tmp -e SSL_CERT_FILE=/stand/root.crt -w /src \
		"$GO_IMAGE" go test -v -count=1 -timeout 200s ./deploy/tests/conformance/
}

# Повреждение для матрицы: тест кладёт в out/damage.txt путь файла страницы внутри data/ (владелец — subuid, чисто
# изнутри пространства имён); без проверки формата ничего не удаляется.
damage() {
	local rel
	[ -s "$work/out/damage.txt" ] || die "тест не оставил damage.txt"
	rel="$(head -n 1 "$work/out/damage.txt")"
	[[ "$rel" =~ ^owners/[a-z0-9-]+/albums/[a-z2-7]{26}/[0-9a-f]{64}\.jpg$ ]] || die "damage.txt: неверный путь"
	podman unshare rm -f "$work/data/$rel"
}

cmd_test() {
	running || die "стенд не поднят: bash deploy/tests/stand.sh up"
	[ "$(state_get mode)" = matrix ] || die "матрица идёт в режиме matrix: down, затем up matrix"
	local fails=0 name rc
	# Чистое состояние: лимит попыток живёт в памяти сервиса, перезапуск обнуляет его — повторный прогон
	# не наследует исчерпанные ключи прошлого.
	podman restart -t 5 "$svc" >/dev/null
	wait_health || die "сервис не поднялся после перезапуска"
	rm -f "$work/out/"*
	for name in main smoke conformance damage down logs; do
		echo "=== стенд: стадия $name ==="
		case "$name" in
		main) run_pytest main || fails=$((fails + 1)) ;;
		smoke) cmd_smoke || fails=$((fails + 1)) ;; # smoke.py против Caddy стенда; id и токен не в выводе
		conformance)
			# Итог читает test_conformance_via_caddy.py в стадии logs; вывод Go-теста id и токенов не содержит.
			rc=0
			run_conformance >"$work/out/conformance.log" 2>&1 || rc=$?
			echo "$rc" >"$work/out/conformance.rc"
			tail -n 12 "$work/out/conformance.log" | sed -E 's/[A-Za-z0-9_-]{22,}/<скрыто>/g' 
			[ "$rc" = 0 ] || fails=$((fails + 1))
			;;
		damage)
			damage
			run_pytest damage || fails=$((fails + 1))
			;;
		down)
			# Сервис остановлен: Caddy отвечает 502 с единым телом, журнал ошибок не пишет адрес альбома.
			podman stop -t 5 "$svc" >/dev/null
			run_pytest down || fails=$((fails + 1))
			podman logs "$cad" >"$work/out/caddy.log" 2>&1
			podman logs "$svc" >"$work/out/svc.log" 2>&1
			;;
		logs) run_pytest logs || fails=$((fails + 1)) ;;
		esac
	done
	podman start "$svc" >/dev/null
	wait_health || die "сервис не поднялся после стадии down"
	if [ "$fails" -gt 0 ]; then
		echo "ИТОГ: провалены стадии: $fails" >&2
		return 1
	fi
	echo "ИТОГ: все стадии прошли"
}

cmd_seed() { # seed | edit
	running || die "стенд не поднят: bash deploy/tests/stand.sh up browser"
	[ "$(state_get mode)" = browser ] || echo "стенд: внимание, режим matrix: CSP листалки не пустит скрипты на http (поднимите up browser)" >&2
	run_py seed python /t/seed.py "$1"
}

# Один запуск pytest в выбранной стадии с любыми аргументами pytest (отладка): stand.sh pytest main -k login -x
cmd_pytest() {
	running || die "стенд не поднят: bash deploy/tests/stand.sh up"
	[ "$(state_get mode)" = matrix ] || die "матрица идёт в режиме matrix: down, затем up matrix"
	local stage="${1:-main}"
	shift || true
	run_py "$stage" python -I -m pytest -p no:cacheprovider --no-header -rfE "$@" /t
}

# Дымовая проверка: тот же deploy/ops/smoke.py, что на ops-ВМ, против Caddy стенда (design 005 §5.3). Контейнер — как у
# матрицы (сетевое пространство Caddy, /etc/hosts стенда, корень tls internal), но: токен только файлом ro на
# /run/sa/token, домен — SA_DOMAIN, корень — SSL_CERT_FILE (системное хранилище читает OpenSSL), другого окружения нет.
# Контейнер не --rm: после прогона `podman inspect` обязан не показать токен ни в Env, ни в команде.
smoke_name=sa-stand-smoke
run_smoke() { # команда smoke.py → вывод в $smoke_out, код выхода в $smoke_rc; токена в выводе и inspect нет
	local tok insp
	tok="$(cat "$work/private/smoke_token")"
	podman rm -f "$smoke_name" >/dev/null 2>&1 || true
	smoke_rc=0
	smoke_out="$(podman run --name "$smoke_name" --network="container:$cad" --user "$tst_cuid:$tst_cuid" --read-only \
		--tmpfs /tmp:rw,size=16m --cap-drop=ALL --security-opt=no-new-privileges --memory=128m --pids-limit=64 \
		-v "$root/deploy/ops/smoke.py:/src/deploy/ops/smoke.py:ro" -v "$work/private/smoke_token:/run/sa/token:ro" \
		-v "$work/root.crt:/stand/root.crt:ro" -v "$work/hosts:/etc/hosts:ro" \
		-e "SA_DOMAIN=$domain" -e SSL_CERT_FILE=/stand/root.crt -e PYTHONDONTWRITEBYTECODE=1 \
		"$ACCESS_IMAGE" python3 -I /src/deploy/ops/smoke.py "$1" 2>&1)" || smoke_rc=$?
	insp="$(podman inspect "$smoke_name" 2>&1 || true)"
	podman rm -f "$smoke_name" >/dev/null 2>&1 || true
	case "$insp$smoke_out" in
	*"$tok"*) echo "стенд: токен smoke виден в podman inspect или в выводе" >&2; return 1 ;;
	esac
	[ -n "$insp" ] || { echo "стенд: podman inspect пуст — проверка токена не состоялась" >&2; return 1; }
	# Идентификатор (26 знаков) и токен (43) — строки от 22 знаков base64url: в выводе таких нет.
	# Имена классов исключений («…Error») длинны, но безопасны: их вырезаем.
	if sed -E 's/[A-Z][A-Za-z]*(Error|Exception|Exit|Interrupt)//g' <<<"$smoke_out" | grep -Eq '[A-Za-z0-9_-]{22,}'; then
		echo "стенд: в выводе smoke.py длинная строка (id, токен или ссылка?)" >&2
		return 1
	fi
}

cmd_smoke() {
	running || die "стенд не поднят: bash deploy/tests/stand.sh up"
	[ "$(state_get mode)" = matrix ] || die "дымовая проверка идёт в режиме matrix: down, затем up matrix"
	local smoke_out smoke_rc before after methods
	run_smoke upload || return 1
	echo "$smoke_out"
	[ "$smoke_rc" = 0 ] || { echo "стенд: smoke upload — код $smoke_rc" >&2; return 1; }
	run_smoke clean || return 1
	echo "$smoke_out"
	if [ "$smoke_rc" != 0 ] || ! grep -q 'убирать нечего' <<<"$smoke_out"; then
		echo "стенд: smoke clean после upload не пуст (код $smoke_rc)" >&2
		return 1
	fi
	# ls — только GET: по журналу Caddy (метод каждого запроса пишется, адрес и заголовки — нет).
	before="$(podman logs "$cad" 2>&1 | wc -l)"
	run_smoke ls || return 1
	echo "$smoke_out"
	[ "$smoke_rc" = 0 ] || { echo "стенд: smoke ls — код $smoke_rc" >&2; return 1; }
	after="$(podman logs "$cad" 2>&1 | tail -n +"$((before + 1))")"
	methods="$(grep -o '"method":"[A-Z]*"' <<<"$after" | sort -u | tr '\n' ' ')"
	[ "$methods" = '"method":"GET" ' ] || { echo "стенд: ls сделал запросы не только GET: $methods" >&2; return 1; }
	echo "ИТОГ: дымовая проверка прошла"
}

main() {
	local cmd="${1:-}"
	shift || true
	case "$cmd" in
	up) cmd_up "$@" ;;
	down) cmd_down "$@" ;;
	env) cmd_env ;;
	test) cmd_test ;;
	pytest) cmd_pytest "$@" ;;
	conformance) running || die "стенд не поднят"; run_conformance ;;
	smoke) cmd_smoke ;;
	seed) cmd_seed seed ;;
	edit) cmd_seed edit ;;
	*) die "команда: up [matrix|browser] | test | pytest | conformance | smoke | seed | edit | env | down [--images]" ;;
	esac
}

main "$@"
