#!/usr/bin/env bash
# Продолжение inner.sh (source; общие функции check, want_rc, want_msg, drive_rc, run_plain, gitc и переменные inv, clone,
# SHIM_LOG… — оттуда): исходящая сеть ops-ВМ (design §16.10, ВР-14, ВР-15). prepare — окно подготовки: без секретов,
# агента и токена; провайдер — архивом с GitHub (поддельный curl), оба хэша сверены, init и validate из зеркала без сети;
# apply — init без сети из того же зеркала, адрес сервера на экране. Печатает ok/FAIL через check/bad.
# shellcheck disable=SC2154,SC2016  # переменные из inner.sh; bash -c '…$1…': раскрытие внутри

# Строка RUN контейнера, в котором выполнилась подкоманда tofu (RUN пишет shim podman, сразу за ним — TOFU <подкоманда>).
sub_run() { awk -v s="^TOFU $2" '/^RUN /{r=$0} $0 ~ s{print r}' "$1"; }
init_offline() { sub_run "$1" init | grep -q -- '--network=none'; }
no_agent_or_token() { ! grep -q '^FD agent ' "$1" && ! grep -q 'TOFU-TOKEN\|TOFU-SECRET\|^TOFU apply' "$1" && ! compgen -G '/run/user/1000/sa-agent-*' >/dev/null && ! compgen -G '/run/user/1000/sa-token-*' >/dev/null; }
# prepare: контейнеров ровно два (init и validate), оба sa-prov-* и без сети; сборка контроллера — не RUN.
only_offline_init_validate() { [ "$(grep -c '^RUN' "$1")" = 2 ] && ! grep '^RUN' "$1" | grep -qv 'name=sa-prov-' && ! grep '^RUN' "$1" | grep -qv -- '--network=none'; }

# lock теста: версия 9.9.9 (URL curl выводится из неё), zh — хэш фикстурного архива; коммит в клоне (HEAD отсоединён, дерево
# чистое — guard_repo доволен). extra=second — второй провайдер (отказ разбора lock).
set_lock() { # <zh> [second] [версия: по умолчанию 9.9.9]
	local v=${3:-9.9.9}
	{
		printf 'provider "registry.opentofu.org/clo-ru/clo" {\n  version     = "%s"\n  constraints = "%s"\n  hashes = [\n    "h1:AAAA",\n    "zh:%s",\n  ]\n}\n' "$v" "$v" "$1"
		[ "${2:-}" != second ] || printf 'provider "registry.opentofu.org/example/other" {\n  version = "1.0.0"\n  hashes = []\n}\n'
	} >"$clone/deploy/tofu/clo/.terraform.lock.hcl"
	gitc -C "$clone" commit -q -a -m 'test lock'
}
mirror_is() { cmp -s "$1" "$mirror/registry.opentofu.org/clo-ru/clo/$pname" && [ ! -e "$inv/tofu/.terraform-clo/.mirror-stage" ] && [ ! -e "$mirror.old" ]; }
# Отказ до init: ни tofu, ни сборки, зеркало прежнее (архив A), черновика нет.
prep_untouched() { ! grep -q '^TOFU \|^BUILD ' "$SHIM_LOG" && mirror_is /tmp/prov-a.zip; }

prepare_checks() {
	local want_init='TOFU init -lockfile=readonly -input=false -plugin-dir=/state/.terraform-clo/mirror -backend-config=path=/state/clo.tfstate'
	local hint='окно подготовки, provision.sh prepare' rc cur zh_a base_url
	local mirror="$inv/tofu/.terraform-clo/mirror" pname=terraform-provider-clo_9.9.9_linux_amd64.zip
	base_url=https://github.com/clo-ru/terraform-provider-clo/releases/download/v9.9.9

	# apply: init без сети из зеркала провайдера; адрес сервера напечатан.
	: >"$SHIM_LOG"
	drive_rc "apply: проходит с init без сети" 0 /tmp/sc.full -- apply
	check "apply: init без сети из зеркала (-plugin-dir, lock readonly)" grep -qxF "$want_init" "$SHIM_LOG"
	check "apply: контейнер init — --network=none" init_offline "$SHIM_LOG"
	check "apply: адрес сервера напечатан" grep -qF 'адрес сервера: 127.0.0.1' /tmp/out.txt
	check "apply: напечатана строка: сверить с панелью, правило tcp/22 (README §1)" grep -qF 'сверить с панелью провайдера; в раскатке — правило tcp/22 (README §1)' /tmp/out.txt
	check "apply: curl не вызывался (скачивает только prepare)" bash -c '! grep -q "^CURL " "$1"' _ "$SHIM_LOG"
	: >"$SHIM_LOG"
	drive_rc "apply: tf_entry из окружения не меняет точку входа" 0 /tmp/sc.full tf_entry=/bin/evil -- apply
	check "apply: при tf_entry в окружении точка входа прежняя (tofu-entry.sh)" bash -c '! grep -q "bin/evil" "$1" && grep -q -- "--entrypoint /work/tofu-entry.sh" "$1"' _ "$SHIM_LOG"
	: >"$SHIM_LOG"
	drive_rc "apply: init не прошёл — ошибка с подсказкой окна подготовки" 1 /tmp/sc.full FAKE_INIT_FAIL=1 -- apply
	check "apply: подсказка называет окно подготовки и prepare" grep -qF "провайдер не установлен или не совпал с lock: $hint" /tmp/out.txt
	check "apply: после отказа init план не запускался" bash -c '! grep -q "^TOFU apply" "$1"' _ "$SHIM_LOG"
	drive_rc "apply: контроллер не собрался — подсказка окна подготовки" 1 /tmp/sc.full FAKE_BUILD_FAIL=1 -- apply
	check "контроллер: сообщение о сборке называет окно подготовки" grep -qF "не собрался контроллер: $hint" /tmp/out.txt

	# prepare: lock теста (9.9.9) и фикстурные архивы A (его zh) и B (чужой).
	cur=$(git -C "$clone" rev-parse HEAD)
	printf 'fixture-provider-archive-A\n' >/tmp/prov-a.zip
	printf 'fixture-provider-archive-B\n' >/tmp/prov-b.zip
	zh_a=$(sha256sum </tmp/prov-a.zip | cut -d' ' -f1)
	set_lock "$zh_a"
	export FAKE_ZIP=/tmp/prov-a.zip

	# prepare: без файла секретов, SA_DOMAIN и SA_ADMIN_USER, без терминала (никаких вопросов), агента и контейнера age.
	mv "$inv/provision.env" /tmp/provision.env.keep
	rm -f "$SHIM_OUT/less.txt"
	: >"$SHIM_LOG"
	: >"$SHIM_PS"
	run_plain SA_SECRETS="$HOME/none.age" -- prepare
	rc=$?
	if [ "$rc" = 0 ]; then ok "prepare: без фразы, агента и токена (нет файла секретов, SA_DOMAIN, SA_ADMIN_USER, терминала)"; else
		bad "prepare: без фразы, агента и токена: код $rc"
		tail -n 4 /tmp/out.txt
	fi
	mv /tmp/provision.env.keep "$inv/provision.env"
	check "prepare: архив скачан по версии из lock, только https" bash -c 'grep -q "^CURL -q --proto =https --proto-redir =https --tlsv1.2 .* $1/$2\$" "$3"' _ "$base_url" "$pname" "$SHIM_LOG"
	check "prepare: SHA256SUMS скачан по версии из lock, с таймаутом и пределом размера" bash -c 'grep -q "^CURL .*--max-time [0-9]* --max-filesize [0-9]* -o .* $1/terraform-provider-clo_9.9.9_SHA256SUMS\$" "$2"' _ "$base_url" "$SHIM_LOG"
	check "prepare: архив лежит в зеркале (packed layout), черновика нет" mirror_is /tmp/prov-a.zip
	check "prepare: init из зеркала по lock (-plugin-dir, readonly)" grep -qxF "$want_init" "$SHIM_LOG"
	check "prepare: контейнер init — без сети" init_offline "$SHIM_LOG"
	check "prepare: к init архив уже был в зеркале" bash -c 'grep -qx MIRROR-PRESENT "$1" && ! grep -q MIRROR-MISSING "$1"' _ "$SHIM_LOG"
	check "prepare: validate после init (сверка h1 с lock), без сети, мимо обёртки токена" bash -c 'r=$(awk "/^RUN /{r=\$0} /^TOFU validate/{print r}" "$1"); grep -q -- --network=none <<<"$r" && grep -q -- "--entrypoint tofu" <<<"$r"' _ "$SHIM_LOG"
	check "prepare: контроллер собран" grep -q '^BUILD ' "$SHIM_LOG"
	check "prepare: предупреждения о копиях нет (бэкап к подготовке не относится)" bash -c '! grep -q "проверенной копии младше" /tmp/out.txt'
	check "prepare: проверка журнала не пустая (открытые файлы podman build записаны)" grep -q '^FD build ' "$SHIM_LOG"
	check "prepare: контейнера age нет, секреты не расшифровывались" bash -c '! grep "^RUN" "$1" | grep -q "name=sa-prov-age"' _ "$SHIM_LOG"
	check "prepare: нет ssh-agent и токена (ни сокета, ни файла токена, ни apply)" no_agent_or_token "$SHIM_LOG"
	check "prepare: просмотр кода не запускался (less не вызывался)" test ! -e "$SHIM_OUT/less.txt"
	check "prepare: из контейнеров — только init и validate, оба sa-prov-* и без сети" only_offline_init_validate "$SHIM_LOG"

	# prepare: отказы хэшей — до init, зеркало прежнее (A), черновика нет.
	: >"$SHIM_LOG"
	want_msg "prepare: sha256 архива не совпал со строкой SHA256SUMS — отказ" 1 "не совпал со строкой SHA256SUMS" FAKE_SUMS=bad -- prepare
	check "prepare: после отказа SHA256SUMS нет init и сборки, зеркало прежнее" prep_untouched
	want_msg "prepare: в SHA256SUMS нет строки архива — отказ" 1 "не совпал со строкой SHA256SUMS" FAKE_SUMS=missing -- prepare
	want_msg "prepare: sha256 архива нет среди zh: в lock — отказ (SHA256SUMS согласован с архивом)" 1 "нет среди zh: в lock" FAKE_ZIP=/tmp/prov-b.zip -- prepare
	check "prepare: после отказа zh нет init и сборки, зеркало прежнее" prep_untouched
	want_msg "prepare: curl не скачал — отказ" 1 "не скачался архив провайдера" FAKE_CURL_FAIL=1 -- prepare
	check "prepare: после отказа curl нет init и сборки, зеркало прежнее" prep_untouched
	set_lock "$zh_a" second
	: >"$SHIM_LOG"
	want_msg "prepare: в lock два провайдера — отказ до скачивания" 1 "ожидается ровно один провайдер" -- prepare
	check "prepare: при отказе разбора lock curl не вызывался" bash -c '! grep -q "^CURL " "$1"' _ "$SHIM_LOG"
	git -C "$clone" checkout -q --detach "$cur"
	set_lock "$zh_a" '' 2.8
	: >"$SHIM_LOG"
	want_msg "prepare: версия в lock не X.Y.Z — отказ до скачивания" 1 "lock: нет версии провайдера вида X.Y.Z" -- prepare
	check "prepare: при версии не X.Y.Z curl не вызывался, зеркало прежнее" bash -c '! grep -q "^CURL " "$1"' _ "$SHIM_LOG"
	check "prepare: при версии не X.Y.Z зеркало прежнее" prep_untouched
	git -C "$clone" checkout -q --detach "$cur"
	set_lock "$zh_a"

	# prepare: отказы tofu и сборки; лишние аргументы, token в tfvars, чужая учётная запись — как раньше.
	: >"$SHIM_LOG"
	want_msg "prepare: tofu init не прошёл — ошибка" 1 "tofu init не прошёл" FAKE_INIT_FAIL=1 -- prepare
	check "prepare: после отказа init нет validate и сборки контроллера" bash -c '! grep -q "^TOFU validate\|^BUILD " "$1"' _ "$SHIM_LOG"
	: >"$SHIM_LOG"
	want_msg "prepare: validate не прошёл (h1 не совпал) — ошибка" 1 "провайдер не совпал с lock (h1)" FAKE_VALIDATE_FAIL=1 -- prepare
	check "prepare: после отказа validate контроллер не собирался" bash -c '! grep -q "^BUILD " "$1"' _ "$SHIM_LOG"
	want_msg "prepare: контроллер не собрался — ошибка" 1 "не собрался контроллер" FAKE_BUILD_FAIL=1 -- prepare
	check "prepare: в сообщении о сборке нет отсылки на сам prepare" bash -c '! grep -qF "$1" /tmp/out.txt' _ "provision.sh prepare"
	want_msg "prepare: отказ при лишних аргументах" 3 "неизвестная команда или лишние аргументы" -- prepare --bogus
	printf 'token = "x"\n' >>"$inv/tofu/clo.tfvars"
	want_msg "prepare: token в *.tfvars — отказ (проверка остаётся)" 3 "в *.tfvars упомянут token" -- prepare
	printf 'api_url = "https://api.invalid"\nproject_id = "p"\n' >"$inv/tofu/clo.tfvars"
	want_msg "prepare: не та учётная запись — отказ 3 (те же guard_*)" 3 "не та учётная запись" SA_OPS_USER=sa-ops-other -- prepare

	# Клон — на прежний коммит: дальше проверки идут по настоящему lock.
	unset FAKE_ZIP
	git -C "$clone" checkout -q --detach "$cur"
	check "клон после проверок prepare вернулся на прежний коммит, дерево чистое" bash -c '[ "$(git -C "$1" rev-parse HEAD)" = "$2" ] && [ -z "$(git -C "$1" status --porcelain)" ]' _ "$clone" "$cur"
	: >"$SHIM_LOG"
}
