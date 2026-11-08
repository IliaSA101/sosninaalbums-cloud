#!/usr/bin/env bash
# apply через сохранённый план (спека 005, design §4.1, 005-18, 005-30). Только source из provision.sh.
#
# Одна дорога для всех режимов (обычный, --replace-server): план сохраняется, показывается человеку, проверяется кодом
# (ops/plan_check.py: удаление сервера при отсутствии привязки адреса в state и удаление адреса — отказ) и применяется
# ровно он — не новый план, посчитанный заново на apply. Причина: сервер пересоздаётся и без флага (поле ForceNew,
# новая схема провайдера), а вместе с ним уходит публичный адрес.
#
# Токен провайдера в файл плана не попадает: переменная clo_api_token объявлена ephemeral (deploy/tofu/clo/variables.tf,
# tofu_static не даёт это снять), поэтому план лежит рядом с state, в каталоге TF_DATA_DIR вне git-дерева inventory.
# shellcheck disable=SC2154  # SA_INVENTORY, XDG_RUNTIME_DIR, ctl_id, repo, tok_file, tmp_rm, tf, prov_run, die, refuse, confirm

# Путь плана: на хосте и в контейнере tofu (том inventory/tofu смонтирован как /state — тот же хвост, что у зеркала).
plan_tail=.terraform-clo/sa.plan
plan_ct=/state/$plan_tail

# plan_prepare <доп. аргументы tofu plan…>: сохранить план, показать, проверить, спросить «yes»; иначе выход (3 — отказ
# проверки, 1 — ошибка). Временные JSON — в XDG_RUNTIME_DIR (0700, tmpfs сессии); план и JSON убирает cleanup по trap
# (в bash нет finally: tmp_rm разбирается в cleanup при любом выходе).
plan_prepare() {
	local host="$SA_INVENTORY/tofu/$plan_tail" st="$XDG_RUNTIME_DIR/sa-state-$$.json" pj="$XDG_RUNTIME_DIR/sa-plan-$$.json" out
	tmp_rm+=("$host" "$st" "$pj")
	rm -f -- "$host" # прежний план не должен пережить ни ошибку нового plan, ни подмену
	# Состояние до плана — по нему код узнаёт, пусто ли оно (первый apply) и есть ли в нём привязка адреса.
	out=$(tf --network=none -- show -json) || die "tofu show не прошёл"
	(umask 077 && printf '%s' "$out" >"$st") || die "состояние не записано"
	tf -v "$tok_file:/run/sa/token:ro" -- plan -input=false -var-file=/state/clo.tfvars -out="$plan_ct" "$@" || die "tofu plan не завершён"
	# Человеку — обычный вывод плана (в нём то, что подтверждается); коду — JSON того же файла. План читается без сети.
	tf --network=none -- show "$plan_ct" || die "tofu show плана не прошёл"
	out=$(tf --network=none -- show -json "$plan_ct") || die "tofu show -json плана не прошёл"
	(umask 077 && printf '%s' "$out" >"$pj") || die "план (JSON) не записан"
	out=$(prov_run plan --network=none -v "$here/ops/plan_check.py:/in/plan_check.py:ro" -v "$st:/in/state.json:ro" \
		-v "$pj:/in/plan.json:ro" "$ctl_id" python3 -I /in/plan_check.py /in/state.json /in/plan.json 2>&1) ||
		refuse "проверка плана: ${out//$'\n'/ }"
	confirm "Применить именно этот план?" || die "отменено"
}
