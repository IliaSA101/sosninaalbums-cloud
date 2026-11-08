#!/usr/bin/env bash
# Шаги `static` стенда раскатки (design 002 §12): формат и проверка OpenTofu, ansible-lint, yamllint,
# syntax-check плеев, shellcheck. Только source из run.sh (после lib.sh).
#
# Шаг возвращает 0 (OK), 77 (SKIP: причина в skip_reason) или другой код (FAIL). Чего ещё нет в дереве
# (deploy/tofu, deploy/ansible) — пропускается с явным сообщением, а не молча. Как только оно появилось,
# шаг обязателен: каталог deploy/ansible без плеев, модуль без tests/ или lock-файла, конфигурация tofu
# вне deploy/tofu/clo — это FAIL, а не SKIP.
# shellcheck disable=SC2154,SC2034  # root, here — из lib.sh (в check.sh файлы читаются по одному); skip_reason читает run.sh

skip_reason=''
skip() {
	skip_reason=$1
	return 77
}

tofu_dir="$root/deploy/tofu/clo"
ansible_dir="$root/deploy/ansible"

has_tofu() { [ -n "$(tofu_sources "$tofu_dir")" ]; }

# Конфигурация tofu вне deploy/tofu/clo: её не проверяет ни один шаг, поэтому она — провал.
# fixtures — намеренно плохие образцы selftest; deploy/inventory (приватный) не читается.
stray_tofu() {
	local out
	out=$(cd "$root" && find deploy -type f \( -name '*.tf' -o -name '*.tofu' -o -name '*.tf.json' -o -name '*.tofu.json' \) \
		-not -path 'deploy/tofu/clo/*' -not -path 'deploy/tests/provision/fixtures/*' -not -path 'deploy/inventory/*')
	[ -z "$out" ] && return 0
	printf '%s\n' "$out"
	echo "конфигурация OpenTofu вне deploy/tofu/clo: её не проверяет ни один шаг (design 002 §1)"
	return 1
}

# Плеи верхнего уровня: bootstrap.yml, site.yml и verify.yml (проверки на цели; роли и vars — внутри).
playbooks() {
	local p
	for p in bootstrap.yml site.yml verify.yml; do
		[ -f "$ansible_dir/$p" ] && echo "$p"
	done
	return 0
}

# Нет каталога deploy/ansible — SKIP; каталог есть, а плеев нет — FAIL (роли без плея никто бы не проверил).
ansible_ready() {
	[ -d "$ansible_dir" ] || skip 'нет deploy/ansible (задачи И3, И4)' || return $?
	[ -n "$(playbooks)" ] || {
		echo "в deploy/ansible нет ни bootstrap.yml, ни site.yml: роли без плеев ansible-lint и syntax-check не проверят"
		return 1
	}
}

step_tofu_fmt() {
	stray_tofu || return 1
	has_tofu || skip 'нет конфигурации deploy/tofu/clo (задача И2)' || return $?
	tofu_run none "$tofu_dir" clo fmt -check -diff -recursive -no-color
}

step_tofu_validate() {
	stray_tofu || return 1
	has_tofu || skip 'нет конфигурации deploy/tofu/clo (задача И2)' || return $?
	[ -f "$tofu_dir/.terraform.lock.hcl" ] || {
		echo "нет .terraform.lock.hcl: провайдер без закреплённых хэшей не принимается (design 002 §3)"
		return 1
	}
	tofu_init "$tofu_dir" clo || return 1
	tofu_run none "$tofu_dir" clo validate -no-color
}

step_tofu_test() {
	stray_tofu || return 1
	has_tofu || skip 'нет конфигурации deploy/tofu/clo (задача И2)' || return $?
	[ -d "$tofu_dir/tests" ] || {
		echo "нет deploy/tofu/clo/tests: tofu test с mock_provider обязателен (design 002 §3)"
		return 1
	}
	[ -f "$tofu_dir/.terraform.lock.hcl" ] || {
		echo "нет .terraform.lock.hcl"
		return 1
	}
	tofu_init "$tofu_dir" clo || return 1
	tofu_run none "$tofu_dir" clo test -no-color
}

# YAML раскатки: плеи и роли, пример inventory, свои файлы стенда. Образцы fixtures/ — намеренно плохие,
# их проверяет selftest. deploy/inventory (приватный) не читается никогда.
yaml_files() {
	local d
	for d in deploy/ansible deploy/inventory.example deploy/tests/provision; do
		[ -d "$root/$d" ] || continue
		find "$root/$d" -type f \( -name '*.yml' -o -name '*.yaml' -o -name '*.yml.example' -o -name '*.yaml.example' \) \
			-not -path '*/fixtures/*' | sed "s|^$root/||"
	done | sort
}

step_yamllint() {
	local files
	mapfile -t files < <(yaml_files)
	[ "${#files[@]}" -gt 0 ] || skip 'нет YAML-файлов' || return $?
	ensure_lint_image || return 1
	# Пути — от корня /work (подкаталоги deploy/ смонтированы как /work/deploy/<имя>).
	lint_run /work yamllint -s -c deploy/tests/provision/lint/yamllint.yml "${files[@]}"
}

step_ansible_lint() {
	ansible_ready || return $?
	ensure_lint_image || return 1
	lint_run /work/deploy/ansible ansible-lint -c /work/deploy/tests/provision/lint/ansible-lint.yml
}

step_syntax_check() {
	local pb
	ansible_ready || return $?
	ensure_lint_image || return 1
	# Хост-заглушка: проверяется только разбор плея и ролей; значения переменных — из inventory владельца.
	for pb in $(playbooks); do
		lint_run /work/deploy/ansible ansible-playbook --syntax-check -i localhost, "$pb" || return 1
	done
}

# Линтер shell берётся с машины (не из контейнера), как в scripts/check.sh.
step_shellcheck() {
	local files
	command -v shellcheck >/dev/null || skip 'shellcheck не найден на машине (его же требует scripts/check.sh)' || return $?
	# Поддельные утилиты provision_cli (cli/shim) — bash без расширения .sh: ищутся отдельно, иначе остались бы без проверки.
	mapfile -t files < <(cd "$root" && {
		find deploy -type f -name '*.sh' -not -path 'deploy/inventory/*'
		find deploy/tests/provision/cli/shim -type f
	} | sort)
	[ "${#files[@]}" -gt 0 ] || skip 'нет *.sh в deploy/' || return $?
	(cd "$root" && shellcheck -x "${files[@]}")
}

# Проверка самих проверок: инструменты запускаются на крошечных образцах — хорошие обязаны пройти, плохие
# обязаны упасть с ожидаемой диагностикой. Иначе на пустом дереве «зелёное» ничего бы не значило, а упавший
# из-за кривого образа/флагов инструмент выглядел бы как «нашёл замечание».
expect_fail() { # <шаблон grep -E диагностики> <команда…>
	local want=$1 out
	shift
	if out=$("$@" 2>&1); then
		echo "ожидали провал, но команда прошла: $*"
		return 1
	fi
	printf '%s\n' "$out" | grep -Eq -- "$want" && return 0
	echo "провал без ожидаемой диагностики «$want»: $*"
	printf '%s\n' "$out" | tail -n 15
	return 1
}

step_selftest() {
	local fx="$here/fixtures" fxc=/work/deploy/tests/provision/fixtures
	ensure_lint_image || return 1
	# OpenTofu: образец без провайдеров (init и tofu test — офлайн, как и в боевом шаге: -lockfile=readonly).
	tofu_run none "$fx/tofu_ok" selftest-ok fmt -check -no-color || return 1
	tofu_run none "$fx/tofu_ok" selftest-ok init -backend=false -lockfile=readonly -no-color >/dev/null || return 1
	tofu_run none "$fx/tofu_ok" selftest-ok validate -no-color >/dev/null || return 1
	tofu_run none "$fx/tofu_ok" selftest-ok test -no-color >/dev/null || return 1
	expect_fail '^main\.tf$' tofu_run none "$fx/tofu_bad" selftest-bad fmt -check -no-color || return 1
	# YAML и Ansible.
	lint_run /work yamllint -s -c deploy/tests/provision/lint/yamllint.yml deploy/tests/provision/fixtures/ansible_ok/site.yml || return 1
	expect_fail duplication lint_run /work yamllint -s -c deploy/tests/provision/lint/yamllint.yml \
		deploy/tests/provision/fixtures/yaml_bad/dup.yml || return 1
	lint_run "$fxc/ansible_ok" ansible-lint -c /work/deploy/tests/provision/lint/ansible-lint.yml || return 1
	expect_fail fqcn lint_run "$fxc/ansible_bad" ansible-lint -c /work/deploy/tests/provision/lint/ansible-lint.yml || return 1
	lint_run "$fxc/ansible_ok" ansible-playbook --syntax-check -i localhost, site.yml >/dev/null || return 1
	# verify_nft_reload_only: хороший образец проходит, плохой — шесть нарушений (restarted, stopped в цикле, systemctl stop, flush table, delete table, argv).
	lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py deploy/tests/provision/fixtures/nft_ok || return 1
	expect_fail 'state=restarted для nftables' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
	expect_fail 'state=stopped для nftables' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
	expect_fail 'команда сбрасывает правила nftables' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
	expect_fail 'Сбросить таблицу командой' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
	expect_fail 'Остановить через argv' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
	expect_fail 'Удалить таблицу командой' lint_run /work python3 -I deploy/tests/provision/nft_reload_only.py \
		deploy/tests/provision/fixtures/nft_bad || return 1
}
