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

# Токен провайдера не попадает в файл плана (спека 005, 005-18, 005-30): apply идёт по сохранённому плану, а план лежит на
# диске (inventory/tofu/.terraform-clo/sa.plan). Защита — `ephemeral = true` у переменной токена (tofu_static не даёт его
# снять). Здесь — проверка на настоящем tofu и настоящей обёртке tofu-entry.sh: конфигурация из блока переменной настоящего
# variables.tf и ресурса встроенного провайдера (план настоящего main.tf без сети и токена невозможен), фиктивный
# токен-метка — файлом-томом, как в provision.sh. Метки в плане нет; а с `ephemeral = false` она там есть — иначе проверка
# ничего не доказывала бы.
step_tofu_plan_label() {
	local w marker=fixture-plan-label-marker-tango v rc=0 run
	stray_tofu || return 1
	has_tofu || skip 'нет конфигурации deploy/tofu/clo (задача И2)' || return $?
	mkdir -p "$cache" && w=$(mktemp -d "$cache/planlab.XXXXXX") || return 1
	mkdir -p "$w/ok" "$w/mut" "$w/data-ok" "$w/data-mut" "$w/out"
	printf '%s' "$marker" >"$w/tok"
	python3 -I "$here/plan_label_prepare.py" "$tofu_dir/variables.tf" "$w" || {
		rm -rf -- "$w"
		return 1
	}
	for v in ok mut; do
		run=(podman run "${hflags[@]}" --network=none --entrypoint /entry.sh -e HOME=/tmp -e TF_DATA_DIR=/data -e TF_INPUT=0
			-e CHECKPOINT_DISABLE=1 -v "$tofu_dir/tofu-entry.sh:/entry.sh:ro" -v "$w/$v:/work:ro" -v "$w/data-$v:/data"
			-v "$w/out:/out" -v "$w/tok:/run/sa/token:ro" -w /work)
		if ! { "${run[@]}" "$TOFU_IMAGE" init -no-color && "${run[@]}" "$TOFU_IMAGE" plan -state=/data/s.tfstate -out="/out/$v.plan" -no-color; } >"$w/$v.log" 2>&1; then
			tail -n 15 "$w/$v.log"
			echo "tofu_plan_label: план ($v) не построился"
			rc=1
		fi
	done
	if [ "$rc" -eq 0 ]; then python3 -I "$here/plan_label_prepare.py" --check "$w/out" "$marker" || rc=1; fi
	rm -rf -- "$w"
	[ "$rc" -eq 0 ] || return 1
	echo "ok   метка-токен: в плане при ephemeral = true её нет, при ephemeral = false — есть"
}
