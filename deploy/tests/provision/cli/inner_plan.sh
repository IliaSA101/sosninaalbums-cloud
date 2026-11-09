#!/usr/bin/env bash
# Правила plan_check.py (ops/plan_check.py) — продолжение inner_hosts.sh (source; plan_case, fxs, fxp, inv, HOME — оттуда).
# Вынесено, чтобы inner_hosts.sh укладывался в предел размера.
# shellcheck disable=SC2154  # переменные из inner.sh и inner_hosts.sh

SRV=clo_compute_keypair,clo_network_ip,clo_compute_instance
ALL=$SRV,clo_network_ip_attach

# Правила plan_check.py (005-30, 005-40): каждое — отказ со своим текстом; --replace-server там, где проверяется правило привязки, а не режим.
plan_rules() {
	local ch='clo_compute_instance.srv=clo_compute_instance:' at='clo_network_ip_attach.main=clo_network_ip_attach:' rf=--replace-server
	local ip='clo_network_ip.main=clo_network_ip:'
	local w_mode='без --replace-server' w_att='привязки адреса нет в state' w_keep='привязка адреса в том же плане не удаляется'
	local w_white='белого списка' w_noact='без списка actions' w_traces='следы прошлой раскатки'
	local keep=$HOME/trace.keep
	# Состояние: сервер есть, привязки нет.
	fxs --types "$SRV"
	fxp --prior "$SRV" --change "${ch}delete"
	plan_case "сервер без привязки, delete" 3 "$w_mode"
	plan_case "сервер без привязки, delete (с флагом)" 3 "$w_att" $rf
	fxp --prior "$SRV" --change "${ch}delete+create"
	plan_case "сервер без привязки, delete+create без флага" 3 "$w_mode"
	plan_case "сервер без привязки, delete+create (--replace-server)" 3 "$w_att" $rf
	fxp --prior "$SRV" --change "${ch}create+delete"
	plan_case "сервер без привязки, create+delete (create_before_destroy)" 3 "$w_att" $rf
	fxp --prior "$SRV" --change clo_compute_instance.renamed=clo_compute_instance:delete+create
	plan_case "сервер без привязки, переименованный ресурс (по типу, не по адресу)" 3 "$w_att" $rf
	# Мутант ревью: привязка определялась по state до плана, а не по prior_state (после обновления). Привязка есть в state,
	# но prior_state её уже не знает — удаление сервера унесло бы адрес.
	fxs --types "$ALL"
	fxp --prior "$SRV" --change "${ch}delete+create"
	plan_case "привязка есть в state, а в prior_state нет — решает prior_state" 3 "$w_att" $rf
	# Мутант ревью: ресурсы child_modules не учитывались. Привязка во вложенном модуле — учитывается.
	fxs --types "$SRV" --child-types clo_network_ip_attach
	fxp --prior "$SRV" --prior-child clo_network_ip_attach --change "${ch}delete+create" --change "${at}delete+create"
	plan_case "привязка во вложенном модуле (child_modules) учтена" 0 "" $rf
	fxp --prior "$SRV" --change "${ch}delete+create" --change "${at}delete+create"
	plan_case "привязка только во вложенном модуле state, в prior_state нет — отказ" 3 "$w_att" $rf
	# Пустое состояние: prior_state читается, если он есть; без него удаление сервера — отказ.
	fxs
	fxp --change "${ch}delete+create"
	plan_case "пустой state, без prior_state: удаление сервера" 3 "$w_att" $rf
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}delete+create"
	plan_case "пустой state, но prior_state с ресурсами — расхождение выводов, отказ" 3 'выводы tofu расходятся' $rf
	# Режим: привязка на месте, но apply без --replace-server.
	fxs --types "$ALL"
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}delete+create"
	plan_case "удаление сервера без --replace-server при привязке в state" 3 "$w_mode"
	# Привязка обязана уйти в том же плане (update/forget/no-op/нет записи — адрес уходил бы с сервером).
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}update"
	plan_case "сервер delete+create, привязка update" 3 "$w_keep" $rf
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}no-op"
	plan_case "сервер delete+create, привязка no-op" 3 "$w_keep" $rf
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}forget"
	plan_case "сервер delete+create, привязка forget (removed destroy=false)" 3 "$w_white" $rf
	fxp --prior "$ALL" --change "${ch}delete+create"
	plan_case "сервер delete+create, записи о привязке в плане нет" 3 "$w_keep" $rf
	# Белый список actions у clo_*.
	fxp --prior "$ALL" --change "${ch}forget"
	plan_case "действие forget у clo_*" 3 "$w_white"
	fxp --prior "$ALL" --change "${ch}update+delete"
	plan_case "неизвестное сочетание update+delete у clo_*" 3 "$w_white"
	fxp --prior "$ALL" --change "${ch}-"
	plan_case "пустой список actions" 3 "$w_noact"
	fxp --prior "$ALL" --change "${ch}update"
	plan_case "update у сервера — проходит" 0
	fxp --prior "$ALL" --change "${ch}no-op" --change "${at}update" --change clo_project_image.debian=clo_project_image:read
	plan_case "no-op, update, read — проходят" 0
	# errored.
	fxp --prior "$ALL" --change "${ch}no-op" --errored
	plan_case "план с errored: true" 3 errored
	# prior_state без values.root_module при непустом состоянии.
	fxp --prior "$ALL" --prior-novalues --change "${ch}no-op"
	plan_case "prior_state без values при непустом состоянии" 3 'prior_state.values.root_module'
	# Следы прошлой раскатки и пустой state: создание нового адреса — отказ. В inventory теста обе приметы есть.
	fxs
	fxp --change "${ch}create" --change "${ip}create" --change "${at}create"
	plan_case "пустой state при следах (deployed-commit и hosts.yml), создание адреса" 3 "$w_traces"
	mkdir -p "$keep"
	mv "$inv/ansible/hosts.yml" "$keep/hosts.yml"
	plan_case "пустой state, остался только deployed-commit, создание адреса" 3 "$w_traces"
	mv "$inv/deployed-commit" "$keep/deployed-commit"
	plan_case "пустой state, следов нет, создание адреса — первый apply" 0
	mv "$keep/hosts.yml" "$inv/ansible/hosts.yml"
	plan_case "пустой state, остался только hosts.yml, создание адреса" 3 "$w_traces"
	mv "$keep/deployed-commit" "$inv/deployed-commit"
	fxs --types "$SRV"
	fxp --prior "$SRV" --change "${ch}no-op" --change "${ip}update" --change "${at}create"
	plan_case "непустой state при следах, адрес не создаётся — проходит" 0
	# Оборванный первый apply: в state ключ и сервер, адреса нет (ответ API потерян), след — hosts.yml; план создаёт адрес.
	fxs --types clo_compute_keypair,clo_compute_instance
	fxp --prior clo_compute_keypair,clo_compute_instance --change "${ch}no-op" --change "${ip}create" --change "${at}create"
	plan_case "оборванный первый apply: ключ и сервер в state, следы, создание адреса" 3 "$w_traces"
	mv "$inv/ansible/hosts.yml" "$keep/hosts.yml"
	plan_case "то же, остался только deployed-commit" 3 "$w_traces"
	mv "$inv/deployed-commit" "$keep/deployed-commit"
	plan_case "то же, следов нет — проходит" 0
	mv "$keep/hosts.yml" "$inv/ansible/hosts.yml"
	mv "$keep/deployed-commit" "$inv/deployed-commit"
	# Прочее: падение закрыто на разборе.
	fxs --types "$ALL"
	fxp --prior "$ALL" --change "${ip}delete"
	plan_case "delete у clo_network_ip даже с привязкой" 3 'адрес не удаляется никогда' $rf
	fxp --prior "$ALL" --change "${ip}delete+create"
	plan_case "пересоздание clo_network_ip" 3 'адрес не удаляется никогда' $rf
	printf 'не JSON {\n' >"$PF/plan.json"
	plan_case "битый JSON плана" 3 'не разобран'
	: >"$PF/plan.json"
	plan_case "пустой вывод show -json плана" 3 'пустой вывод'
	fxp --prior "$ALL" --change "${ch}create" --format 2.0
	plan_case "format_version не 1.x" 3 format_version
	fxp --prior "$ALL" --no-changes
	plan_case "нет resource_changes" 3 resource_changes
	fxp --change "${ch}create"
	plan_case "состояние не пусто, а prior_state в плане нет" 3 'prior_state.values.root_module'
	fxp --prior "$ALL" --change "${ch}create"
	printf 'not json {\n' >"$PF/state.json"
	plan_case "битый JSON состояния" 3 'не разобран'
	# Проходят.
	fxs
	fxp --change "${ch}create" --change "${at}create"
	mv "$inv/ansible/hosts.yml" "$keep/hosts.yml"
	mv "$inv/deployed-commit" "$keep/deployed-commit"
	plan_case "первый apply (пустое состояние, prior_state нет)" 0
	mv "$keep/hosts.yml" "$inv/ansible/hosts.yml"
	mv "$keep/deployed-commit" "$inv/deployed-commit"
	rmdir "$keep"
	fxs --types "$ALL"
	fxp --prior "$ALL" --change "${ch}delete+create" --change "${at}delete+create"
	plan_case "--replace-server при привязке в state" 0 "" $rf
	fxp --prior "$ALL" --change "${ch}create+delete" --change "${at}delete+create"
	plan_case "--replace-server, create_before_destroy, привязка пересоздаётся" 0 "" $rf
	fxs --types "$SRV"
	fxp --prior "$SRV" --change "${ch}no-op" --change "${at}create"
	plan_case "восстановление привязки (сервер no-op, привязка create)" 0
}
