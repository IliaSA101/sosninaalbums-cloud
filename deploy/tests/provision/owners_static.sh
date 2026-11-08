#!/usr/bin/env bash
# Шаг owners_rule стенда раскатки (спека 005, design §1.4; решения 005-22, 005-32): правила полей владельца в роли
# albums_svc (регэксп site, неизвестные ключи, записи токена, name, studio) согласны с правилом сервиса. Разбор —
# owners_rule_static.py в образе проверок, задачи роли гоняет настоящий Ansible, без сети. Общий файл случаев
# site_cases.json лежит в internal/config/testdata (его читает и Go-тест сервиса): монтируется только он, ro.
# Вторая часть — owners_first_static.py (005-35: проверка владельцев первая в site.yml).
# Только source из run.sh (после lib.sh и static.sh).
# shellcheck disable=SC2154  # root, skip, hflags, ansible_ready, deploy_mounts, lint_image: из lib.sh и static.sh

step_owners_rule() {
	local dmounts cases="$root/internal/config/testdata/site_cases.json"
	ansible_ready || return $?
	[ -f "$cases" ] || {
		echo "нет $cases: общий файл случаев site (internal/config/testdata) — без него роль не сверить с сервисом"
		return 1
	}
	ensure_lint_image || return 1
	deploy_mounts
	podman run "${hflags[@]}" --network=none \
		-e HOME=/tmp/home -e XDG_CACHE_HOME=/tmp/cache -e ANSIBLE_HOME=/tmp/ansible -e ANSIBLE_NOCOLOR=1 -e NO_COLOR=1 \
		"${dmounts[@]}" -v "$cases:/work/internal/config/testdata/site_cases.json:ro" -w /work "$(lint_image)" \
		python3 -I deploy/tests/provision/owners_rule_static.py deploy/ansible/roles/albums_svc internal/config/testdata/site_cases.json \
		deploy/inventory.example/ansible/group_vars/all/vars.yml deploy/tests/provision/inventory/group_vars/all/vars.yml || return 1
	# 005-35: проверка владельцев — первая в site.yml (pre_tasks), плохой владелец останавливает плей до bootstrap.
	# Настоящий Ansible против localhost (соединение local, --check): ничего не меняет, сети нет.
	podman run "${hflags[@]}" --network=none \
		-e HOME=/tmp/home -e XDG_CACHE_HOME=/tmp/cache -e ANSIBLE_HOME=/tmp/ansible -e ANSIBLE_NOCOLOR=1 -e NO_COLOR=1 \
		"${dmounts[@]}" -w /work "$(lint_image)" python3 -I deploy/tests/provision/owners_first_static.py deploy/ansible
}
