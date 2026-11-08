#!/usr/bin/env bash
# Шаг ctl_shared стенда раскатки: запуск контроллера собирается в одном месте (deploy/ops/ctl-run.sh), и его
# используют provision.sh и стенд (converge.sh) — иначе зелёный стенд не доказывал бы, что `provision.sh
# configure` пройдёт (повторное ревью И2, п. 3). Здесь же — защита границы доверия (решения И2-1, И2-2): общий
# код остаётся в просмотренном клоне и не уезжает в libexec или в тестовое дерево. argv `podman run` сравнивается
# с эталоном целиком (порядок и состав, решение Р-16), а не списком запретов: «ничего лишнего» списком не
# проверить. Тремя копиями белого списка символов (guard.sh, review.sh, runbook) шаг тоже занят: они обязаны
# совпадать побайтово; так же — две копии проверки метки машины (guard_machine, design §16.4), и в review.sh с
# README обязана быть копия bundle через dd с nofollow, nonblock и fullblock под timeout (§16.8, ВР-10). Без podman и сети: bash и grep.
# Только source из run.sh (после lib.sh и static.sh).
# shellcheck source-path=SCRIPTDIR disable=SC2154,SC2016  # root, cache, skip — из lib.sh и static.sh; SC2016 — шаблоны ищут буквальный $here

# Построчный argv, который соберёт ctl_build с выдуманными параметрами (в подоболочке: ничего не остаётся).
# ctl_sample_argv <репозиторий> <файл ключа> <добавочные флаги podman через пробел или пусто> [аргументы плея…]
ctl_sample_argv() {
	local repo=$1 pub=$2 extra=$3
	shift 3
	(
		# shellcheck source=../../ops/ctl-run.sh
		. "$root/deploy/ops/ctl-run.sh"
		ctl_repo=$repo ctl_image=localhost/sample:1 ctl_name=sa-prov-ansible-1 ctl_inv=/sample/inv ctl_agent_sock=/sample/agent.sock
		ctl_known_hosts=/sample/known_hosts ctl_pubfile=$pub ctl_vault=VAULT-MARKER-0c1d
		ctl_domain=albums.example.org ctl_admin=admin-example ctl_backup=sa-backup ctl_bootstrap=root ctl_imguser=''
		[ -z "$extra" ] || read -r -a ctl_podman_extra <<<"$extra"
		ctl_build site.yml "$@" || exit 1
		printf '%s\n' "${ctl_cmd[@]}"
	)
}

# Эталон argv: порядок и состав те же, что у ctl_build. Правка ctl-run.sh без правки эталона красит шаг —
# это и нужно: каждое изменение запуска контроллера должно пройти через ревью этого файла.
# ctl_golden <канонический корень> <файл ключа> <добавочные слова podman> [аргументы плея…]
ctl_golden() {
	local base=$1 pub=$2 extra=$3 key ex=()
	shift 3
	key=$(head -n 1 "$pub")
	[ -z "$extra" ] || read -r -a ex <<<"$extra"
	printf '%s\n' podman run --rm --log-driver=none --name sa-prov-ansible-1 --read-only --cap-drop=ALL \
		--security-opt=no-new-privileges --pids-limit=512 --tmpfs /tmp:rw,mode=1777 --preserve-fds=1
	[ "${#ex[@]}" -eq 0 ] || printf '%s\n' "${ex[@]}"
	printf '%s\n' -v "$base/deploy:/src/deploy:ro" -v "$base/web/viewer/notfound.html:/src/web/viewer/notfound.html:ro" \
		-w /src/deploy/ansible -v /sample/inv:/inv:ro -v /sample/agent.sock:/run/sa/agent.sock \
		-v /sample/known_hosts:/run/sa/known_hosts:ro -v "$pub:/run/sa/id_server.pub:ro" \
		-e HOME=/tmp/home -e ANSIBLE_HOME=/tmp/ansible -e ANSIBLE_NOCOLOR=1 -e ANSIBLE_CONFIG=/src/deploy/ansible/ansible.cfg \
		localhost/sample:1 ansible-playbook -i /inv/hosts.yml site.yml \
		-e albums_domain=albums.example.org -e admin_user=admin-example -e backup_user=sa-backup \
		-e bootstrap_user=root -e image_user= \
		-e "{\"sa_ssh_args\":[\"-F\",\"/dev/null\",\"-o\",\"IdentitiesOnly=yes\",\"-o\",\"IdentityFile=/run/sa/id_server.pub\",\"-o\",\"IdentityAgent=/run/sa/agent.sock\",\"-o\",\"UserKnownHostsFile=/run/sa/known_hosts\",\"-o\",\"StrictHostKeyChecking=yes\",\"-o\",\"ConnectTimeout=10\"],\"sa_controller_pubkey\":\"$key\"}"
	[ $# -eq 0 ] || printf '%s\n' "$@"
	printf '%s\n' --vault-password-file=/dev/fd/3
}

# ctl_diff <эталон> <фактический argv> <метка>: код 0 — совпадают; иначе показ различий.
ctl_diff() {
	local out
	out=$(diff <(printf '%s\n' "$1") <(printf '%s\n' "$2")) && return 0
	printf '%s: argv отличается от эталона (< эталон, > фактический):\n%s\n' "$3" "$out"
	return 1
}

# Вставка строк после «--pids-limit=512» в argv: образцы того, чем argv мог бы обрасти. Сравнение обязано их
# заметить (самопроверка сравнения).
ctl_insert_after_pids() { # <argv построчно> <строки вставки…>
	local argv=$1
	shift
	awk -v ins="$(printf '%s\n' "$@")" '{print} $0 == "--pids-limit=512" {print ins}' <<<"$argv"
}

# Файлы и каталоги по канонике: realpath-ветка ctl_build ловит симлинк на месте web, web/viewer, deploy, когда сам
# файл — обычный (guard_modes в этом тесте не участвует: дерево не из git).
ctl_realpath_cases() { # <рабочий каталог> <файл ключа>
	local d=$1 pub=$2 out rc=0 name
	mkdir -p "$d/w/deploy" "$d/realweb/viewer" "$d/v/web" "$d/v/deploy" "$d/realviewer" "$d/p/web/viewer" "$d/realdeploy"
	: >"$d/realweb/viewer/notfound.html"
	: >"$d/realviewer/notfound.html"
	: >"$d/p/web/viewer/notfound.html"
	ln -s "$d/realweb" "$d/w/web"
	ln -s "$d/realviewer" "$d/v/web/viewer"
	ln -s "$d/realdeploy" "$d/p/deploy"
	for name in w v p; do
		if out=$(ctl_sample_argv "$d/$name" "$pub" '' 2>&1); then
			echo "realpath: ctl_build принял дерево с симлинком ($name)"
			rc=1
		else
			case $name:$out in
				w:*симлинк* | v:*симлинк* | p:*deploy*) ;;
				*)
					echo "realpath ($name): отказ без ожидаемого сообщения: $out"
					rc=1
					;;
			esac
		fi
	done
	return "$rc"
}

# Три копии белого списка символов (guard.sh, review.sh, runbook README.md) — одна строка, побайтово.
ctl_pattern_copies() {
	local g r d
	g=$(sed -n "s/^ctl_pattern='\(.*\)'\$/\1/p" "$root/deploy/ops/guard.sh")
	r=$(sed -n "s/^ctl_pattern='\(.*\)'\$/\1/p" "$root/deploy/ops/review.sh")
	d=$(grep -o "git grep -a -l -P '[^']*'" "$root/deploy/README.md" | sed "s/^git grep -a -l -P '//; s/'\$//")
	if [ -z "$g" ] || [ -z "$r" ] || [ -z "$d" ]; then
		echo "белый список: шаблон не найден в guard.sh, review.sh или README.md"
		return 1
	fi
	if [ "$g" != "$r" ] || [ "$g" != "$d" ]; then
		echo "белый список: копии шаблона в guard.sh, review.sh и README.md различаются (должны совпадать побайтово)"
		return 1
	fi
}

# Две копии guard_machine (guard.sh и review.sh: review.sh самодостаточен и подключить guard.sh не может) — текст
# функции побайтово одинаковый; правка в одном файле без другого красит шаг.
ctl_machine_copies() {
	local g r
	g=$(sed -n '/^guard_machine() {$/,/^}$/p' "$root/deploy/ops/guard.sh")
	r=$(sed -n '/^guard_machine() {$/,/^}$/p' "$root/deploy/ops/review.sh")
	if [ -z "$g" ] || [ -z "$r" ]; then
		echo "метка машины: функция guard_machine не найдена в guard.sh или review.sh"
		return 1
	fi
	if [ "$g" != "$r" ]; then
		echo "метка машины: guard_machine в guard.sh и review.sh различается (копии должны совпадать побайтово)"
		return 1
	fi
}

# Копия bundle (design §16.2, ВР-4, ВР-10): в review.sh и в runbook — dd с nofollow (симлинк, подложенный после
# проверки -L, даёт ELOOP; head -c его бы открыл), nonblock (FIFO не вешает открытие), count_bytes и fullblock (без
# него короткие чтения FUSE молча режут копию), под timeout; в коде review.sh head -c для копии не остаётся. Гонку
# тестом не поймать, поэтому проверяется сама строка; простой симлинк ловит [ -L ] (случай «bundle — симлинк»).
ctl_dd_copy() {
	local rc=0
	grep -Eq '^[[:space:]]*\(umask 077 && timeout 120 dd if="\$src" of="\$copy" iflag=nofollow,nonblock,count_bytes,fullblock count=\$\(\(max_bytes \+ 1\)\)' "$root/deploy/ops/review.sh" || {
		echo "review.sh: нет копии bundle через timeout 120 dd … iflag=nofollow,nonblock,count_bytes,fullblock"
		rc=1
	}
	grep -Eq 'timeout 120 dd .*iflag=nofollow,nonblock,count_bytes,fullblock' "$root/deploy/README.md" || {
		echo "README.md: нет команды timeout 120 dd … iflag=nofollow,nonblock,count_bytes,fullblock (первое чтение bundle в runbook)"
		rc=1
	}
	# Без конвейера с grep -q: он выходит на первом совпадении, а code_lines под pipefail получает SIGPIPE (флак).
	if grep -Eq 'head -c' < <(code_lines "$root/deploy/ops/review.sh"); then
		echo "review.sh: head -c в коде (копия bundle — только dd с nofollow)"
		rc=1
	fi
	return "$rc"
}

# Исходящая сеть ops-ВМ (design §16.10, ВР-14): инструкция в README — переключатель режимов и сама команда prepare.
# Без них владелец не получит «раскатку» с закрытой сетью, а код (prepare, init без сети) опирается на неё.
ctl_net_readme() {
	local rc=0 s
	for s in 'ufw default reject outgoing' 'ufw default allow outgoing' 'provision.sh prepare' \
		'net.ipv6.conf.all.disable_ipv6 = 1' 'loginctl disable-linger sa-ops' 'из панели провайдера' \
		'/etc/cron.allow' 'ipv6.method disabled' 'sudo ls -A /var/spool/cron/crontabs/' \
		'providers lock -fs-mirror=/mirror -platform=linux_amd64' 'test "$A" = "$S"' 'grep -qF "\"zh:$A\"" "$L"'; do
		grep -qF -- "$s" "$root/deploy/README.md" || {
			echo "README.md: нет «$s» (исходящая сеть ops-ВМ, design §16.10, ВР-14, ВР-15)"
			rc=1
		}
	done
	# После cron.allow `crontab -l -u sa-ops` отвечает отказом (находка И5h-1): проверка — только каталог crontabs; сверка lock — не через реестр.
	if grep -Eq '^[[:space:]]*(sudo )?crontab -l' "$root/deploy/README.md"; then
		echo "README.md: команда crontab -l в runbook (ВР-14, И5h-1)"
		rc=1
	fi
	# images.env — файл из bundle: исполнять его (`. …`) нельзя, значение извлекается sed по строгому образцу (аудит Н1).
	if grep -Eq '^[[:space:]]*\.[[:space:]].*images\.env' "$root/deploy/README.md"; then
		echo "README.md: images.env исполняется через «.» (значение — только sed по строгому образцу)"
		rc=1
	fi
	# curl в runbook — без ~/.curlrc (-q первым аргументом, аудит Н2).
	if grep -E '^[[:space:]]*curl ' "$root/deploy/README.md" | grep -qvE '^[[:space:]]*curl -q '; then
		echo "README.md: команда curl без -q первым аргументом"
		rc=1
	fi
	if grep -F 'providers lock' "$root/deploy/README.md" | grep -qvF -- '-fs-mirror='; then
		echo "README.md: providers lock без -fs-mirror (через реестр из России нельзя, ВР-15)"
		rc=1
	fi
	return "$rc"
}

# Строки кода без комментариев (для поиска запрещённых мест запуска).
code_lines() { grep -vE '^[[:space:]]*#' "$1"; }

# Точки входа, которые запускаются по пути (README: `~/src/…/deploy/provision.sh <команда>`, таймер systemd, ssh на
# цели), обязаны лежать в индексе git с режимом 100755. Режим файла в рабочем дереве не доказательство: клон на
# ops-ВМ берёт его из индекса, и 100644 давал «Permission denied», код 126 (И5j), а стенд этого не видел — он
# запускает `bash файл`. Подключаемые библиотеки (ctl-run.sh, mirror.sh…) сюда не входят: их source-ят.
ctl_entry_points='deploy/provision.sh deploy/ops/review.sh deploy/ops/sa-backup-prune.sh deploy/ops/phrase-fd.sh deploy/backup/sa-backup-stream.sh deploy/backup/sa-restore.sh deploy/tests/caddy-validate.sh deploy/tofu/clo/tofu-entry.sh scripts/check.sh scripts/git-hooks/pre-commit scripts/git-hooks/pre-push'

# ctl_entry_modes <листинг `git ls-files -s`>: ошибка на каждую точку входа без режима 100755 или вне индекса.
ctl_entry_modes() {
	local f rc=0 mode
	for f in $ctl_entry_points; do
		mode=$(awk -v f="$f" '$4 == f {print $1}' <<<"$1")
		[ "$mode" = 100755 ] || {
			echo "$f: в индексе git режим «${mode:-нет в индексе}», нужен 100755 (запуск по пути, git update-index --chmod=+x)"
			rc=1
		}
	done
	return "$rc"
}

# Листинг индекса: шаг идёт в клоне (локально и в CI). Без git или вне репозитория — отказ, а не пропуск: тихий
# пропуск оставил бы дыру там, где проверка и нужна.
ctl_entry_check() {
	local listing bad rc=0 f
	# shellcheck disable=SC2086  # список точек входа — слова по пробелам
	listing=$(git -C "$root" ls-files -s -- $ctl_entry_points) || {
		echo "git ls-files в $root не сработал: режимы точек входа не проверены (нужен клон с .git)"
		return 1
	}
	ctl_entry_modes "$listing" || rc=1
	# Самопроверка: режим 100644 и отсутствие записи обязаны краснеть.
	for f in $ctl_entry_points; do
		bad=$(awk -v f="$f" '$4 == f {$1 = "100644"} {print}' <<<"$listing")
		[ "$bad" != "$listing" ] || continue
		if ctl_entry_modes "$bad" >/dev/null; then
			echo "самопроверка режимов: 100644 у $f не замечен"
			rc=1
		fi
		bad=$(grep -vF "$f" <<<"$listing")
		if ctl_entry_modes "$bad" >/dev/null; then
			echo "самопроверка режимов: отсутствие $f в индексе не замечено"
			rc=1
		fi
	done
	return "$rc"
}

step_ctl_shared() {
	local d out f rc=0 files calls libexec pub exp act base m mut
	[ -f "$root/deploy/ops/ctl-run.sh" ] || skip 'нет deploy/ops/ctl-run.sh' || return $?
	mkdir -p "$cache"
	d=$(mktemp -d "$cache/ctl-shared.XXXXXX") || return 1
	pub="$d/id.pub"
	printf 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleStandKeyNotARealKeyxxxxxxxxxxxxx sa-example\n' >"$pub"
	mkdir -p "$d/repo/deploy" "$d/repo/web/viewer"
	: >"$d/repo/web/viewer/notfound.html"
	ln -s repo "$d/link"
	base=$(realpath -- "$d/repo")

	# 1. argv целиком против эталона: без добавок (provision.sh), с добавками стенда и с аргументами плея; репозиторий
	# — через симлинк: монтируется канонический путь.
	for m in "provision|$d/link||" "provision с аргументами плея|$d/repo||-vvv --tags verify_users" "стенд (сеть)|$d/repo|--network sa-sample-net|"; do
		IFS='|' read -r name repo extra plain <<<"$m"
		# shellcheck disable=SC2086  # plain — слова аргументов плея
		act=$(ctl_sample_argv "$repo" "$pub" "$extra" $plain) || {
			echo "$name: ctl_build не собрал argv"
			rc=1
			continue
		}
		# shellcheck disable=SC2086
		exp=$(ctl_golden "$base" "$pub" "$extra" $plain)
		ctl_diff "$exp" "$act" "$name" || rc=1
		if grep -qF VAULT-MARKER <<<"$act"; then
			echo "$name: значение пароля vault попало в argv"
			rc=1
		fi
	done
	# Самопроверка сравнения: лишние флаги и тома обязаны быть замечены.
	act=$(ctl_sample_argv "$d/repo" "$pub" '')
	exp=$(ctl_golden "$base" "$pub" '')
	for mut in "--security-opt seccomp=unconfined" "--mount type=bind,src=/etc,dst=/h" "--group-add keep-groups" "-v /:/host:ro" "--privileged" "--cap-add SYS_ADMIN"; do
		# shellcheck disable=SC2086
		out=$(ctl_insert_after_pids "$act" $mut)
		if ctl_diff "$exp" "$out" "мутация" >/dev/null; then
			echo "самопроверка: сравнение argv не заметило вставку «$mut»"
			rc=1
		fi
	done

	# 2. realpath-ветка ctl_build: симлинки на месте web, web/viewer, deploy (мимо guard_modes).
	ctl_realpath_cases "$d" "$pub" || rc=1
	rm -rf -- "$d"

	# 3. Копии белого списка символов и проверки метки машины совпадают; копия bundle — dd с nofollow.
	ctl_pattern_copies || rc=1
	ctl_machine_copies || rc=1
	ctl_dd_copy || rc=1
	ctl_net_readme || rc=1

	# 4. Других мест сборки запуска нет: provision.sh и converge.sh вызывают ctl_playbook и не знают ни
	# ansible-playbook, ни пути ANSIBLE_CONFIG, ни /src/deploy.
	for f in "$root/deploy/provision.sh" "$here/converge.sh"; do
		if code_lines "$f" | grep -Ec 'ansible-playbook|ANSIBLE_CONFIG|/src/deploy' >/dev/null; then
			echo "${f#"$root"/}: собирает запуск контроллера сам (ansible-playbook, ANSIBLE_CONFIG или /src/deploy): только через ops/ctl-run.sh"
			rc=1
		fi
		code_lines "$f" | grep -c 'ctl_playbook' >/dev/null || {
			echo "${f#"$root"/}: не вызывает ctl_playbook"
			rc=1
		}
	done
	# Добавочные флаги podman — только у стенда (сеть): provision.sh их не задаёт, иначе у раскатки появились бы
	# флаги, которых нет в эталоне стенда.
	if code_lines "$root/deploy/provision.sh" | grep -c 'ctl_podman_extra' >/dev/null; then
		echo "deploy/provision.sh задаёт ctl_podman_extra: добавочные флаги podman допустимы только у стенда"
		rc=1
	fi
	calls=$(cd "$root" && find deploy -type f -name '*.sh' -not -path 'deploy/inventory/*' -not -path 'deploy/ops/ctl-run.sh' \
		-not -path 'deploy/tests/provision/ctl_static.sh' | sort |
		while IFS= read -r f; do code_lines "$f" | grep -c 'ctl_playbook' >/dev/null && echo "$f"; done)
	[ "$calls" = "$(printf 'deploy/provision.sh\ndeploy/tests/provision/converge.sh')" ] || {
		echo "ctl_playbook вызывают не только provision.sh и converge.sh: $(tr '\n' ' ' <<<"$calls")"
		rc=1
	}

	# 5. Граница доверия: provision.sh подключает только файлы ops/ из своего (просмотренного) клона — не тестовое
	# дерево и не libexec; копии, которые идут без консоли, общего кода не знают.
	files=$(code_lines "$root/deploy/provision.sh" | grep -E '^(\.|source)[[:space:]]' | grep -vcE '^\. "\$here/ops/[a-z-]+\.sh"$')
	[ "$files" = 0 ] || {
		echo 'provision.sh подключает файл не из "$here/ops/…": общий код — только из просмотренного клона'
		rc=1
	}
	grep -qxF '. "$here/ops/ctl-run.sh"' < <(code_lines "$root/deploy/provision.sh") || {
		echo "provision.sh не подключает ops/ctl-run.sh"
		rc=1
	}
	libexec=$(code_lines "$root/deploy/provision.sh" | grep -E '^[[:space:]]+for f in [a-z .-]+\.sh; do$' | sed 's/.*for f in //; s/; do$//')
	[ -n "$libexec" ] || {
		echo "в provision.sh не найден список скриптов libexec (install_libexec)"
		rc=1
	}
	for f in $libexec; do
		if grep -Eq 'ctl-run|ctl_(build|playbook)' < <(code_lines "$root/deploy/ops/$f"); then
			echo "deploy/ops/$f ставится в libexec и идёт без просмотра при раскатке, а использует общий код запуска контроллера"
			rc=1
		fi
	done
	ctl_entry_check || rc=1
	return "$rc"
}
