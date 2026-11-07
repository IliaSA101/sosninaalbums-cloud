#!/usr/bin/env bash
# Шаг tofu_static стенда раскатки (design 002 §3, решения П2a-2, И2-4): проверки HCL без tofu и сети — состав
# каталога deploy/tofu/clo (только *.tf, tofu-entry.sh, lock-файл, tests/*.tftest.hcl), ephemeral у токена,
# validation у api_url, prevent_destroy у адреса, ignore_changes и нет addresses/password у сервера, нет
# провижинеров, terraform_data, null_resource, external, http, модулей с нелокальным source и чужих
# провайдеров; lock-файл — только clo. Разбор — tofu_static.py в образе проверок. Плюс `validate` через
# tofu-entry.sh в настоящем образе tofu без сети (токен — файл из fixtures; без файла обёртка отказывает).
# Только source из run.sh (после lib.sh и static.sh).
# shellcheck disable=SC2154  # tofu_dir, skip, root, here, cache, hflags, TOFU_IMAGE: из static.sh и lib.sh

step_tofu_static() {
	local fx="$here/cli/fixtures/entry_token.txt" run
	stray_tofu || return 1
	has_tofu || skip 'нет конфигурации deploy/tofu/clo (задача И2)' || return $?
	[ -f "$tofu_dir/.terraform.lock.hcl" ] || {
		echo "нет .terraform.lock.hcl"
		return 1
	}
	ensure_lint_image || return 1
	lint_run /work python3 -I /work/deploy/tests/provision/tofu_static.py /work/deploy/tofu/clo /work/deploy || return 1
	tofu_init "$tofu_dir" clo || return 1
	run=(podman run "${hflags[@]}" --network=none -e HOME=/tmp -e TF_DATA_DIR=/data -e TF_INPUT=0 -e CHECKPOINT_DISABLE=1
		-v "$tofu_dir:/work:ro" -v "$cache/tofu-clo:/data" -w /work --entrypoint /work/tofu-entry.sh)
	"${run[@]}" -v "$fx:/run/sa/token:ro" "$TOFU_IMAGE" validate -no-color || return 1
	expect_fail 'нет читаемого непустого файла токена' "${run[@]}" "$TOFU_IMAGE" validate -no-color
}
