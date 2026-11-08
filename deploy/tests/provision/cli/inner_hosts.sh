#!/usr/bin/env bash
# Продолжение inner.sh (source; check, drive, drive_rc, переменные inv, clone, R, SHIM_*, CLI_IP — оттуда): сохранённый план
# apply (ops/plan.sh, ops/plan_check.py), provision.sh hosts (ops/hosts.sh), smoke и albums (ops/api.sh); спека 005,
# design §4.1, §4.2, §5.2, §6. Вынесено, чтобы inner.sh укладывался в предел размера.
# shellcheck disable=SC2154,SC2016,SC1091  # переменные из inner.sh; bash -c '…$1…': раскрытие внутри; inner_plan.sh — рядом

. "$R/inner_plan.sh" # plan_rules (правила plan_check.py)
PF=$HOME/pf
mkdir -p "$PF"
planfile="$inv/tofu/.terraform-clo/sa.plan"
fxs() { python3 -I "$R/plan_fixtures.py" state "$PF/state.json" "$@"; }
fxp() { python3 -I "$R/plan_fixtures.py" plan "$PF/plan.json" "$@"; }
printf 'expect yes/no\nsend yes\nexpect Enter passphrase:\nsend $PHRASE_SECRETS\nexpect yes/no\nsend no\n' >/tmp/sc.apply.no
printf 'expect yes/no\nsend no\n' >/tmp/sc.no

# Прогон apply [флаг] с подготовленными $PF/state.json и $PF/plan.json: 3 — отказ проверки плана (apply не запускался), 0 — применён
# именно сохранённый план. И в том и в другом случае файла плана не остаётся (cleanup по trap). Для отказа третий аргумент —
# фрагмент причины из plan_check.py (иначе отказ мог бы прийти от другого правила, и случай ничего бы не доказывал);
# четвёртый — флаг apply (--replace-server): без него любое удаление сервера отказывает по режиму, а не по правилу случая.
plan_case() { # <имя> <ожидаемый код> [<фрагмент причины> [--replace-server]]
	local n=$1 want=$2 why=${3:-} flag=${4:-} rc ap=(apply)
	[ -z "$flag" ] || ap+=("$flag")
	: >"$SHIM_LOG"
	drive /tmp/sc.apply FAKE_STATE_JSON="$PF/state.json" FAKE_PLAN_JSON="$PF/plan.json" -- "${ap[@]}"
	rc=$?
	if [ "$want" = 3 ]; then
		check "план: $n — отказ (код 3), apply не запускался, файла плана нет" bash -c '[ "$1" = 3 ] && grep -q "проверка плана" /tmp/out.txt &&
			grep -qF -- "$4" /tmp/out.txt && ! grep -q "^TOFU apply" "$2" && [ ! -e "$3" ]' _ "$rc" "$SHIM_LOG" "$planfile" "$why"
	else
		check "план: $n — проходит, применён сохранённый план, файла плана нет" bash -c '[ "$1" = 0 ] && grep -q TOFU-APPLY-SAVED-PLAN "$2" &&
			[ ! -e "$3" ]' _ "$rc" "$SHIM_LOG" "$planfile"
	fi
}

plan_checks() {
	# Рабочая дорога: порядок plan → show → apply, apply ровно по файлу плана без -var-file и -replace, показ и проверка без сети.
	: >"$SHIM_LOG"
	drive_rc "apply: обычный прогон (сохранённый план)" 0 /tmp/sc.apply -- apply
	check "apply: порядок plan → show плана → show -json плана → apply плана" bash -c '
		p=$(grep -n "^TOFU plan " "$1" | head -n 1 | cut -d: -f1); s=$(grep -n "^TOFU show /state/.terraform-clo/sa.plan" "$1" | head -n 1 | cut -d: -f1)
		j=$(grep -n "^TOFU show -json /state/.terraform-clo/sa.plan" "$1" | head -n 1 | cut -d: -f1); a=$(grep -n "^TOFU apply " "$1" | head -n 1 | cut -d: -f1)
		[ -n "$p" ] && [ "$p" -lt "$s" ] && [ "$s" -lt "$j" ] && [ "$j" -lt "$a" ]' _ "$SHIM_LOG"
	check "apply: plan -out в TF_DATA_DIR, apply — тот же файл и ничего больше" bash -c '
		grep -qxF "TOFU plan -input=false -var-file=/state/clo.tfvars -out=/state/.terraform-clo/sa.plan" "$1" &&
		grep -qxF "TOFU apply -input=false /state/.terraform-clo/sa.plan" "$1" && grep -q TOFU-APPLY-SAVED-PLAN "$1"' _ "$SHIM_LOG"
	check "apply: план показан человеку и проверка плана прошла" bash -c 'grep -q "OpenTofu will perform" /tmp/out.txt && ! grep -q "проверка плана" /tmp/out.txt' _
	check "apply: показ и разбор плана — контейнеры без сети; plan — с токеном" bash -c '
		! grep "^RUN.* show " "$1" | grep -qv -- "--network=none" && grep "^RUN.* sa-prov-plan-" "$1" | grep -q -- "--network=none" &&
		grep -c TOFU-TOKEN-OK "$1" | grep -qx 2' _ "$SHIM_LOG"
	check "apply: файла плана и временных JSON после выхода нет" bash -c '[ ! -e "$1" ] && ! compgen -G "/run/user/1000/sa-plan-*" >/dev/null && ! compgen -G "/run/user/1000/sa-state-*" >/dev/null' _ "$planfile"
	check "apply: метка-токен не попадает в файл плана (поддельный tofu не пишет токен; настоящий — шаг tofu_plan_label)" bash -c '! grep -rqF -- "$1" "$2"' _ "$MARKER" "$inv/tofu"
	# Прежний план не переживает ни сбой нового plan, ни отказ.
	printf 'OLD-PLAN\n' >"$planfile"
	: >"$SHIM_LOG"
	drive_rc "apply: plan не прошёл — ошибка" 1 /tmp/sc.apply FAKE_PLAN_FAIL=1 -- apply
	check "apply: старый план стёрт до plan -out, apply не запускался" bash -c '[ ! -e "$1" ] && ! grep -q "^TOFU apply" "$2"' _ "$planfile" "$SHIM_LOG"
	: >"$SHIM_LOG"
	drive_rc "apply: на вопрос о плане «no» — отмена" 1 /tmp/sc.apply.no -- apply
	check "apply: отмена — apply не запускался, файла плана нет" bash -c '! grep -q "^TOFU apply" "$1" && [ ! -e "$2" ]' _ "$SHIM_LOG" "$planfile"
	: >"$SHIM_LOG"
	rm -f "$inv/ansible/hosts.yml"
	drive_rc "apply: tofu apply упал — ошибка с подсказкой hosts" 1 /tmp/sc.apply FAKE_APPLY_FAIL=1 -- apply
	check "apply: при падении apply — подсказка provision.sh hosts, hosts.yml не записан, плана нет" bash -c '
		grep -q "provision.sh hosts" /tmp/out.txt && [ ! -e "$1/ansible/hosts.yml" ] && [ ! -e "$2" ]' _ "$inv" "$planfile"

	# Отпечаток плана: подмена файла между показом и apply — отказ (код 1), apply не запускался.
	: >"$SHIM_LOG"
	drive /tmp/sc.apply FAKE_PLAN_TAMPER=1 -- apply
	check "apply: файл плана изменён после проверки — отказ (сверка sha256), apply не запускался" bash -c '[ "$1" = 1 ] && grep -q "изменился" /tmp/out.txt &&
		! grep -q "^TOFU apply" "$2" && [ ! -e "$3" ]' _ "$?" "$SHIM_LOG" "$planfile"
	# «План проверен» — до вопроса «yes».
	: >"$SHIM_LOG"
	drive /tmp/sc.apply -- apply
	check "apply: «план проверен» напечатано до вопроса «Применить именно этот план?»" bash -c '
		p=$(grep -n "план проверен" /tmp/out.txt | head -n 1 | cut -d: -f1); q=$(grep -n "Применить именно этот план" /tmp/out.txt | head -n 1 | cut -d: -f1)
		[ -n "$p" ] && [ -n "$q" ] && [ "$p" -lt "$q" ]'
	plan_rules
}

# --- hosts -----------------------------------------------------------------------------------------------
hosts_yml_want() { printf 'all:\n  hosts:\n    server:\n      ansible_host: %s\n' "$1"; }

hosts_checks() {
	local ip bad ar10 ar127 fw1 fw4 loc hy="$inv/ansible/hosts.yml"
	: >"$SHIM_LOG"
	rm -f "$hy"
	drive_rc "hosts: без аргумента — адрес из state" 0 /tmp/sc.yes -- hosts
	check "hosts: файл в том же формате, что у apply; временных файлов нет" bash -c 'cmp -s <(printf "all:\n  hosts:\n    server:\n      ansible_host: %s\n" "$2") "$1/hosts.yml" && ! compgen -G "$1/.hosts.*" >/dev/null' _ "$inv/ansible" "$CLI_IP"
	check "hosts: напечатаны адрес, сверка с панелью, команды ufw (22 и 443) и строка /etc/hosts (005-38)" bash -c '
		grep -qF "адрес сервера: $1" /tmp/out.txt && grep -qF "сверить с панелью" /tmp/out.txt &&
		grep -qF "sudo ufw allow out to $1 port 22 proto tcp" /tmp/out.txt && grep -qF "sudo ufw allow out to $1 port 443 proto tcp" /tmp/out.txt &&
		grep -qF "grep -qwF '"'"'host.example.org'"'"' /etc/hosts || echo '"'"'$1 host.example.org'"'"' | sudo tee -a /etc/hosts" /tmp/out.txt &&
		grep -qF "заменить строку" /tmp/out.txt' _ "$CLI_IP"
	check "hosts: ни секретов, ни контроллера, ни ключей (нет age, сборки, агента)" bash -c '! grep -q "sa-prov-age\|^BUILD\|^FD agent" "$1" && grep -q "^TOFU output" "$1"' _ "$SHIM_LOG"
	rm -f "$hy"
	drive_rc "hosts: с адресом, совпадающим с выводом tofu" 0 /tmp/sc.yes -- hosts "$CLI_IP"
	check "hosts: записан верный адрес" bash -c 'cmp -s <(printf "all:\n  hosts:\n    server:\n      ansible_host: %s\n" "$2") "$1"' _ "$hy" "$CLI_IP"
	hosts_yml_want sentinel-unchanged >"$hy"
	for bad in 0101.2.3.4 010.0.0.1 10.1.2.3 192.168.1.1 172.16.0.1 172.31.255.254 100.64.0.1 127.0.0.1 127.1 0x7f.1 169.254.1.1 0.1.2.3 \
		224.0.0.1 240.0.0.1 255.255.255.255 192.0.0.9 192.0.2.1 192.88.99.1 198.18.0.1 198.19.0.1 198.51.100.1 203.0.113.1 256.1.1.1 1.2.3 \
		11.22.33.44.5 ' 11.22.33.44' '11.22.33.44 ' example.org; do
		drive "/tmp/sc.yes" -- hosts "$bad"
		check "hosts: «$bad» — отказ (код 3), hosts.yml не тронут" bash -c '[ "$1" = 3 ] && cmp -s <(printf "all:\n  hosts:\n    server:\n      ansible_host: sentinel-unchanged\n") "$2"' _ "$?" "$hy"
	done
	# Не-ASCII цифры под UTF-8-локалью (аудит И7b): в en_US.UTF-8 `[0-9]` их принимал, а числовое сравнение падало с кодом 2
	# и цепочка `||` считала адрес «не служебным». Локаль в образе есть (cli/Containerfile); контроль — что она действительно
	# принимает такие цифры, иначе случаи ничего не доказывали бы. Проверка обязана отказать и в этой локали, и в C.
	# Цифры собраны из байтов UTF-8: просмотр кода (review.sh) отвергает не-ASCII символы в файлах deploy/.
	ar10=$(printf '\xd9\xa1\xd9\xa0') ar127=$(printf '\xd9\xa1\xd9\xa2\xd9\xa7') fw1=$(printf '\xef\xbc\x91') fw4=$(printf '\xef\xbc\x94')
	check "локаль стенда en_US.UTF-8 принимает арабско-индийские цифры в [0-9] (случаи ниже содержательны)" env LC_ALL=en_US.UTF-8 bash -c '[[ $1 =~ ^[0-9]+$ ]]' _ "$ar10"
	for bad in "$ar10.0.0.1" "127.0.0.$fw1" "11.22.33.$fw4" "$ar127.0.0.1"; do
		for loc in en_US.UTF-8 C; do
			drive "/tmp/sc.yes" LC_ALL=$loc -- hosts "$bad"
			check "hosts: «$bad» под $loc — отказ (код 3), hosts.yml не тронут" bash -c '[ "$1" = 3 ] && cmp -s <(printf "all:\n  hosts:\n    server:\n      ansible_host: sentinel-unchanged\n") "$2"' _ "$?" "$hy"
		done
	done
	drive "/tmp/sc.yes" LC_ALL=en_US.UTF-8 FAKE_IP="$ar10.0.0.1" -- hosts
	check "hosts: не-ASCII цифры в выводе из state под en_US.UTF-8 — отказ (код 3)" bash -c '[ "$1" = 3 ] && grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive "/tmp/sc.yes" -- hosts 11.22.33.45
	check "hosts: адрес расходится с выводом tofu — отказ (код 3)" bash -c '[ "$1" = 3 ] && grep -q "расходится" /tmp/out.txt && grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive "/tmp/sc.yes" FAKE_IP=10.0.0.5 -- hosts
	check "hosts: служебный адрес в выводе tofu — отказ (код 3)" bash -c '[ "$1" = 3 ] && grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive "/tmp/sc.yes" FAKE_NO_OUTPUT=1 -- hosts
	check "hosts: в state нет адреса и аргумента нет — ошибка (код 1) с подсказкой" bash -c '[ "$1" = 1 ] && grep -q "hosts <IPv4>" /tmp/out.txt && grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	check "hosts: подсказка про отсутствие адреса — «не дошёл до создания адреса»" grep -q "не дошёл до создания адреса" /tmp/out.txt
	# Ошибка самого инструмента — не «нет адреса»: ни без аргумента, ни с аргументом hosts.yml не пишется.
	drive "/tmp/sc.yes" FAKE_OUTPUT_FAIL=1 -- hosts
	check "hosts: ошибка чтения выходов state без аргумента — ошибка (код 1), hosts.yml не тронут" bash -c '[ "$1" = 1 ] && grep -q "не прошёл (ошибка" /tmp/out.txt &&
		grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive "/tmp/sc.yes" FAKE_OUTPUT_FAIL=1 -- hosts 11.22.33.46
	check "hosts: ошибка чтения выходов при адресе аргументом — тоже ошибка (код 1), адрес без сверки не принят" bash -c '[ "$1" = 1 ] && grep -q "не прошёл (ошибка" /tmp/out.txt &&
		grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive "/tmp/sc.yes" FAKE_OUTPUT_RAW_FAIL=1 -- hosts
	check "hosts: выход ipv4 есть, но -raw не прошёл — ошибка (код 1), hosts.yml не тронут" bash -c '[ "$1" = 1 ] && grep -q sentinel-unchanged "$2"' _ "$?" "$hy"
	drive_rc "hosts: state без адреса, адрес аргументом — записан" 0 /tmp/sc.yes FAKE_NO_OUTPUT=1 -- hosts 11.22.33.46
	check "hosts: записан адрес из аргумента" grep -qF 'ansible_host: 11.22.33.46' "$hy"
	want_rc "hosts: лишний аргумент — отказ" 3 -- hosts a b
	: >"$SHIM_LOG"
	drive /tmp/sc.no -- hosts
	check "hosts: «no» на просмотре кода — отмена до tofu (код 1, tofu не запускался)" bash -c '[ "$1" = 1 ] && ! grep -q "^TOFU" "$2"' _ "$?" "$SHIM_LOG"
	# Симлинки: на месте файла и на месте каталога ansible/ запись отказывает и ничего не создаёт за пределами inventory.
	mv "$hy" "$inv/hosts.keep"
	ln -s /tmp/victim "$hy"
	drive "/tmp/sc.yes" -- hosts
	check "hosts: hosts.yml — симлинк: отказ, цель симлинка не создана" bash -c '[ "$1" = 1 ] && [ ! -e /tmp/victim ] && [ -L "$2" ]' _ "$?" "$hy"
	rm -f "$hy"
	mv "$inv/ansible" "$inv/ansible.keep"
	ln -s "$inv/ansible.keep" "$inv/ansible"
	drive "/tmp/sc.yes" -- hosts
	check "hosts: каталог ansible/ — симлинк: отказ, внутри ничего не записано" bash -c '[ "$1" = 1 ] && [ -z "$(ls -A "$2")" ]' _ "$?" "$inv/ansible.keep"
	rm -f "$inv/ansible"
	mv "$inv/ansible.keep" "$inv/ansible"
	mv "$inv/hosts.keep" "$hy"
	ip=$CLI_IP
	hosts_yml_want "$ip" >"$hy"
	check "hosts: inventory восстановлен для следующих проверок" grep -qF "ansible_host: $ip" "$hy"
}

# --- smoke и albums ----------------------------------------------------------------------------------------
api_checks() {
	local sec="$HOME/secrets-api.age" want='python3 -I /src/deploy/ops/smoke.py' f
	(umask 077 && printf 'clo_token=%s\nvault_pass=%s\nssh_key_phrase=%s\nbackup_key_phrase=%s\nage_identity=%s\nsmoke_token=%s\nls_token=%s\n' \
		"$MARKER" "$VAULT" "$PHRASE_SSH" "$PHRASE_BACKUP" AGE-SECRET-KEY-FIXTURE "$SMOKE_TOKEN" "$LS_TOKEN" >/plain/s2.txt)
	python3 -I "$R/pty_drive.py" /tmp/sc.make "$R/fixtures/values.env" -- sh -c 'age -p -o "$1" </plain/s2.txt' _ "$sec" >/dev/null
	rm -f /plain/s2.txt
	chmod 600 "$sec"
	: >"$SHIM_LOG"
	drive_rc "smoke: в файле секретов нет smoke_token — ошибка с подсказкой" 1 /tmp/sc.full -- smoke
	check "smoke без ключа: подсказка называет smoke_token, контейнера клиента не было" bash -c 'grep -q smoke_token /tmp/out.txt && ! grep -q "sa-prov-api-" "$1"' _ "$SHIM_LOG"
	drive_rc "albums: в файле секретов нет ls_token — ошибка с подсказкой" 1 /tmp/sc.full -- albums
	check "albums без ключа: подсказка называет ls_token, контейнера клиента не было" bash -c 'grep -q ls_token /tmp/out.txt && ! grep -q "sa-prov-api-" "$1"' _ "$SHIM_LOG"
	want_rc "smoke: неизвестный аргумент — отказ" 3 -- smoke upload
	want_rc "smoke: лишний аргумент — отказ" 3 -- smoke clean x
	want_rc "albums: аргумент — отказ" 3 -- albums x
	for f in "smoke|smoke_token|$SMOKE_TOKEN|upload" "smoke clean|smoke_token|$SMOKE_TOKEN|clean" "albums|ls_token|$LS_TOKEN|ls"; do
		IFS='|' read -r cmdline key tok sub <<<"$f"
		: >"$SHIM_LOG"
		: >"$SHIM_PS"
		rm -f "$SHIM_OUT/api-token"
		# shellcheck disable=SC2086  # cmdline — одно-два слова команды
		drive_rc "$cmdline: токен из файла секретов ($key), клиент запущен" 0 /tmp/sc.full SA_SECRETS="$sec" -- $cmdline
		check "$cmdline: клиент получил токен $key файлом /run/sa/token и команду $sub" bash -c '
			[ "$(cat "$1/api-token")" = "$2" ] && grep "^RUN name=sa-prov-api-" "$3" | grep -q -- "-e SA_DOMAIN=host.example.org" &&
			grep "^RUN name=sa-prov-api-" "$3" | grep -q -- "python3 -I /src/deploy/ops/smoke.py $4\$" &&
			grep "^RUN name=sa-prov-api-" "$3" | grep -q -- "-v [^ ]*/deploy/ops/smoke.py:/src/deploy/ops/smoke.py:ro" &&
			grep "^RUN name=sa-prov-api-" "$3" | grep -q -- "-v /run/user/1000/sa-token-api-[0-9]*:/run/sa/token:ro"' _ "$SHIM_OUT" "$tok" "$SHIM_LOG" "$sub"
		check "$cmdline: токена нет в argv, окружении контейнера, ps, журнале shim и выводе; файл токена убран; ssh-агента не было" bash -c '
			! grep -qF -- "$1" "$2" "$3" /tmp/out.txt && ! grep "^RUN name=sa-prov-api-" "$2" | grep -qF -- "$4" && ! grep -q "^FD agent " "$2" &&
			! compgen -G "/run/user/1000/sa-token-*" >/dev/null && ! compgen -G "/run/user/1000/sa-agent-*" >/dev/null' _ "$tok" "$SHIM_LOG" "$SHIM_PS" "${tok:0:12}"
		check "$cmdline: контейнер без журнала, под именем sa-prov-*, база — только SA_DOMAIN из окружения, не аргумент" bash -c '
			grep "^RUN name=sa-prov-api-" "$1" | grep -q "log=none" && ! grep "^RUN name=sa-prov-api-" "$1" | grep -q "https://"' _ "$SHIM_LOG"
	done
	: >"$SHIM_LOG"
	drive_rc "smoke: клиент вернул ошибку — код передан, подсказка с правилом ufw" 7 /tmp/sc.full SA_SECRETS="$sec" FAKE_API_RC=7 -- smoke
	check "smoke при отказе: подсказка с готовой командой ufw на адрес сервера" grep -qF "sudo ufw allow out to $CLI_IP port 443 proto tcp" /tmp/out.txt
	check "smoke при отказе: подсказка со строкой /etc/hosts (DNS на ops-ВМ закрыт, 005-38)" grep -qF "grep -qwF 'host.example.org' /etc/hosts || echo '$CLI_IP host.example.org' | sudo tee -a /etc/hosts" /tmp/out.txt
	check "smoke при отказе: подсказка — заменить старую строку домена, не дописывать" grep -qF "заменить строку" /tmp/out.txt
	rm -f "$SHIM_OUT/api-token"
}

# SA_DOMAIN и имена пользователей из provision.env: не-ASCII буквы и цифры — отказ в любой локали (аудит И7c: под UTF-8
# `[a-z0-9]` в `[[ =~ ]]` принимал e с акутом и арабско-индийские цифры). Знаки собраны из байтов UTF-8: просмотр кода (review.sh)
# отвергает не-ASCII символы в файлах deploy/. Контроль — что локаль стенда такие знаки в диапазоне действительно принимает.
domain_checks() {
	local eac ar1 loc d
	eac=$(printf '\xc3\xa9') ar1=$(printf '\xd9\xa1')
	check "локаль стенда en_US.UTF-8 принимает e с акутом и арабско-индийскую цифру в [a-z0-9] (случаи ниже содержательны)" env LC_ALL=en_US.UTF-8 bash -c '[[ $1 =~ ^[a-z0-9]+$ ]] && [[ $2 =~ ^[a-z0-9]+$ ]]' _ "$eac" "$ar1"
	for d in "albums.${eac}xample.ru" "${ar1}.example.ru" "albums.example${ar1}.ru"; do
		for loc in en_US.UTF-8 C; do
			want_rc "отказ: SA_DOMAIN с не-ASCII знаком под $loc" 3 "SA_DOMAIN=$d" LC_ALL=$loc -- up
			check "отказ SA_DOMAIN назван в выводе" grep -q "SA_DOMAIN не похож" /tmp/out.txt
		done
	done
	want_rc "отказ: SA_ADMIN_USER с e-акутом под en_US.UTF-8" 3 "SA_ADMIN_USER=a${eac}" LC_ALL=en_US.UTF-8 -- up
	check "отказ имени пользователя назван в выводе" grep -q "SA_ADMIN_USER: допустимы" /tmp/out.txt
}

hosts_all_checks() {
	domain_checks
	plan_checks
	hosts_checks
	api_checks
}
