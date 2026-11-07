#!/usr/bin/env bash
# Шаг provision_cli стенда раскатки (design 002 §12, решения П2a-3, 002-2): deploy/provision.sh без сети и
# настоящего сервера. Три части:
#   1. h1 — на настоящем Podman: `age -d -o /dev/fd/3` в `podman run -it` спрашивает фразу с терминала и отдаёт
#      значения каналом в оболочку (гипотеза П2a-3), у sa-prov-* драйвер журнала none (podman inspect);
#   2. inner — provision.sh из клона (git bundle → clone) в контейнере `cli` под пользователем saops, с
#      поддельными podman, tofu, ssh, loginctl, less и настоящими age, ssh-agent, ssh-add, git (cli/inner.sh):
#      код 3 на каждый отказ design §2, код 2 на B/C, код 4 без сети, нет утечек, метка копии, чистка, свежесть.
#      Верные метка машины раскатки и machine-id подставлены монтированием (design §16.8);
#   3. cli_label_cases — отдельные прогоны контейнера под метки машины: по одному на случай, код 3 И текст отказа.
# Фразы и метки — только в cli/fixtures/values.env. Только source из run.sh (после lib.sh и static.sh).
# shellcheck disable=SC2154,SC2016  # root, here, cache, skip, hflags: из lib.sh и static.sh

cli_image() {
	echo "localhost/sa-tgt-cli:$(sha256sum <"$here/cli/Containerfile" | cut -c1-12)"
}

ensure_cli_image() {
	local img out
	img=$(cli_image)
	podman image exists "$img" && return 0
	echo "сборка $img (первый раз: apt, нужна сеть)" >&2
	if ! out=$(podman build -q -t "$img" -f "$here/cli/Containerfile" "$here/cli" 2>&1); then
		printf '%s\n' "$out" | tail -n 20
		echo "не собрался $img"
		return 1
	fi
}

# Идентификатор машины в тестовых метках: выдуманный, 32 строчных hex.
cli_id=0123456789abcdef0123456789abcdef

# Контейнер «учётной записи раскатки»: те же ограничения во всех прогонах; перед «--» — добавочные флаги podman
# (монтирование метки), после — команда. Код выхода — команды.
cli_run() { # <образ> <эталонная копия кода> [флаги…] -- <команда…>
	local img=$1 ref=$2 extra=()
	shift 2
	while [ "$1" != -- ]; do
		extra+=("$1")
		shift
	done
	shift
	podman run --rm --network=none --user 1000:1000 --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=512 \
		--read-only --sysctl net.ipv4.ip_unprivileged_port_start=0 \
		--tmpfs /home/saops:rw,mode=0700,U --tmpfs /run/user/1000:rw,mode=0700,U \
		--tmpfs /run/sa:rw,U --tmpfs /in:rw,U \
		--tmpfs /srv/sa-inbox:rw,mode=0755,U --tmpfs /plain:rw,mode=0700,U \
		--tmpfs /tmp:rw,mode=1777 -e HOME=/home/saops -e TERM=xterm "${extra[@]}" \
		-v "$ref:/ref:ro" "$img" "$@"
}

# Файлы метки в <каталог>: machine-id и sa-ops-machine (содержимое — формат printf %b; «-» — метки нет), режим
# метки и её владелец в контейнере (0 — root: файл хоста, смонтированный в rootless Podman, виден как uid 0;
# иной uid — через podman unshare chown). Симлинк метки так не воспроизвести: -v разрешает симлинк источника
# (ВР-9). Результат — массив CLI_LABEL_MOUNTS (флаги -v … :ro).
cli_label_files() { # <каталог> <machine-id> <метка|-> <режим> <uid в контейнере>
	local d=$1
	mkdir -p "$d" && chmod 755 "$d" || return 1
	printf '%b' "$2" >"$d/machine-id" && chmod 644 "$d/machine-id" || return 1
	CLI_LABEL_MOUNTS=(-v "$d/machine-id:/etc/machine-id:ro")
	[ "$3" != - ] || return 0
	printf '%b' "$3" >"$d/sa-ops-machine" && chmod "$4" "$d/sa-ops-machine" || return 1
	if [ "$5" != 0 ]; then podman unshare chown "$5:$5" "$d/sa-ops-machine" || return 1; fi
	CLI_LABEL_MOUNTS+=(-v "$d/sa-ops-machine:/etc/sa-ops-machine:ro")
}

# Отдельные прогоны под метки (design §16.8): по контейнеру на случай, сверяются код 3 И собственный текст отказа
# (иначе отказ мог бы прийти из другой проверки того же префикса, и случай ничего не доказывал). Контрольный — с верной меткой:
# проверка машины пройдена, отказ приходит позже (нет сессии) и с другим текстом.
cli_label_cases() { # <образ> <эталонная копия кода> <каталог>
	local img=$1 ref=$2 lab=$3 n=0 rc=0 name cmd mid label mode uid want out got dir
	local other=fedcba9876543210fedcba9876543210 upper=0123456789ABCDEF0123456789ABCDEF
	while IFS='|' read -r name cmd mid label mode uid want; do
		n=$((n + 1))
		dir="$lab/c$n"
		cli_label_files "$dir" "${mid//@ID@/$cli_id}" "${label//@ID@/$cli_id}" "$mode" "$uid" || {
			echo "FAIL $name: не собрались файлы метки"
			rc=1
			continue
		}
		case $cmd in
			up) out=$(cli_run "$img" "$ref" "${CLI_LABEL_MOUNTS[@]}" -e SA_OPS_USER=saops -- bash /ref/deploy/provision.sh up 2>&1) ;;
			*) out=$(cli_run "$img" "$ref" "${CLI_LABEL_MOUNTS[@]}" -e SA_OPS_USER=saops -- bash /ref/deploy/ops/review.sh main 2>&1) ;;
		esac
		got=$?
		if [ "$want" = pass ]; then
			if [ "$got" = 3 ] && grep -qF 'нет сессии' <<<"$out" && ! grep -qF 'не машина раскатки' <<<"$out"; then echo "ok   $name"; else
				echo "FAIL $name: код $got, ждали 3 после проверки машины (нет сессии)"
				tail -n 3 <<<"$out"
				rc=1
			fi
		elif [ "$got" = 3 ] && grep -qF -- "$want" <<<"$out"; then echo "ok   $name"; else
			echo "FAIL $name: код $got, ждали 3 и «$want»"
			tail -n 3 <<<"$out"
			rc=1
		fi
	done <<EOF
метка: верная|up|@ID@\n|sa-ops-machine @ID@\n|644|0|pass
отказ: нет метки машины|up|@ID@\n|-|644|0|не машина раскатки: нет файла метки
отказ: метка чужой машины|up|@ID@\n|sa-ops-machine $other\n|644|0|не машина раскатки: метка другой машины
отказ: machine-id не 32 hex|up|abc123\n|sa-ops-machine abc123\n|644|0|не машина раскатки: /etc/machine-id не 32
отказ: machine-id в верхнем регистре|up|$upper\n|sa-ops-machine $upper\n|644|0|не машина раскатки: /etc/machine-id не 32
отказ: machine-id без перевода строки|up|@ID@|sa-ops-machine @ID@\n|644|0|не машина раскатки: /etc/machine-id не 32
отказ: метка не одна строка (лишняя строка)|up|@ID@\n|sa-ops-machine @ID@\nextra\n|644|0|не машина раскатки: метка другой машины или не одна строка
отказ: метка не одна строка (без перевода строки)|up|@ID@\n|sa-ops-machine @ID@|644|0|не машина раскатки: метка другой машины или не одна строка
отказ: метка не root|up|@ID@\n|sa-ops-machine @ID@\n|644|1000|не машина раскатки: метка не root или с записью
отказ: метка с записью группе|up|@ID@\n|sa-ops-machine @ID@\n|664|0|не машина раскатки: метка не root или с записью
отказ: метка с записью прочим|up|@ID@\n|sa-ops-machine @ID@\n|646|0|не машина раскатки: метка не root или с записью
review: отказ: нет метки машины|review|@ID@\n|-|644|0|не машина раскатки: нет файла метки
review: отказ: метка чужой машины|review|@ID@\n|sa-ops-machine $other\n|644|0|не машина раскатки: метка другой машины
EOF
	return "$rc"
}

step_provision_cli() {
	local img work out rc ref lab
	[ -f "$root/deploy/provision.sh" ] || skip 'нет deploy/provision.sh (задача И2)' || return $?
	command -v python3 >/dev/null || {
		echo "нет python3 на машине: драйвер псевдотерминала (cli/pty_drive.py) без него не запустить — шаг не пропускается"
		return 1
	}
	ensure_cli_image || return 1
	img=$(cli_image)
	mkdir -p "$cache"
	work=$(mktemp -d "$cache/cli.XXXXXX") || return 1

	# 1. Гипотеза П2a-3 на настоящем Podman: фраза — трижды (создание файла: ввод и подтверждение; расшифровка).
	printf 'expect Enter passphrase \\(leave\nsend $PHRASE_SECRETS\nexpect Confirm passphrase\nsend $PHRASE_SECRETS\nexpect Enter passphrase:\nsend $PHRASE_SECRETS\n' >"$work/h1.script"
	out=$(python3 -I "$here/cli/pty_drive.py" "$work/h1.script" "$here/cli/fixtures/values.env" -- \
		bash "$here/cli/h1.sh" "$img" "$work" "$root" 2>&1)
	rc=$?
	rm -rf -- "$work"
	if [ "$rc" -ne 0 ] || ! grep -q H1-OK <<<"$out"; then
		printf '%s\n' "$out" | tail -n 15
		echo "provision_cli: гипотеза П2a-3 (age в podman run -it, канал fd 3) не подтверждена"
		return 1
	fi
	echo "ok   П2a-3: age -d -o /dev/fd/3 в podman run -it — фраза с терминала, значения каналом; журнал sa-prov-* none"

	# 2. provision.sh в контейнере: подкаталоги и файлы deploy/ (кроме приватного inventory) и страница 404 — только чтение.
	# Эталонная копия — только файлы, которые видит git (отслеживаемые и новые не игнорируемые), без приватного
	# deploy/inventory: лишний неотслеживаемый файл в рабочей копии (например, __pycache__/*.pyc) не должен менять
	# состав «клона» в тесте и ронять проверки. __pycache__ и *.pyc исключены явно: корневой .gitignore их пока не
	# знает (правка .gitignore — у оркестратора).
	ref=$(mktemp -d "$cache/ref.XXXXXX") || return 1
	(cd "$root" && git ls-files -z --cached --others --exclude-standard -- deploy web/viewer/notfound.html .github |
		tar --null --exclude=deploy/inventory --exclude=__pycache__ --exclude='*.pyc' --ignore-failed-read -cf - -T - 2>/dev/null) | tar -C "$ref" -x || {
		rm -rf -- "$ref"
		echo "не собралась эталонная копия кода для provision_cli"
		return 1
	}
	chmod 755 "$ref" # контейнер идёт под uid 1000 (subuid), mktemp дал бы 0700
	lab=$(mktemp -d "$cache/lab.XXXXXX") || {
		rm -rf -- "$ref"
		return 1
	}
	chmod 755 "$lab"
	# Основной прогон — с верными меткой и machine-id: без них все прочие случаи упали бы на метке машины.
	cli_label_files "$lab/main" "$cli_id\n" "sa-ops-machine $cli_id\n" 644 0 || {
		rm -rf -- "$ref" "$lab"
		echo "не собрались файлы метки для основного прогона"
		return 1
	}
	cli_run "$img" "$ref" "${CLI_LABEL_MOUNTS[@]}" -- bash /ref/deploy/tests/provision/cli/inner.sh
	rc=$?
	# 3. Отдельные прогоны под метки (после основного: его провал не должен скрывать их итог, но и не маскируется).
	cli_label_cases "$img" "$ref" "$lab" || rc=1
	rm -rf -- "$ref" "$lab"
	return "$rc"
}
