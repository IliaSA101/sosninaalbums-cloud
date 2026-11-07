#!/usr/bin/env bash
# Проверка выпущенного образа в ops-ВМ без сети и раскатка ровно проверенного образа (спека 004, design §7, §8).
# Только source (provision.sh и тесты стенда). Функции возвращают код 3 («отказ до действий») с сообщением без
# значений; решение принимается только по JSON, который выдаёт сам gh (--jq), и только по полям сертификата Fulcio.
# Константы — ops/image-consts.env и ops/tools.env: читаются как данные из того же просмотренного клона.
# shellcheck disable=SC2154,SC2034  # here, repo, SA_*, ctl_id, nolock: задаёт точка входа (в тестах — обвязка); iv_tmp, img_*: читает она же

iv_tmp='' # рабочий каталог прогона (0700, в XDG_RUNTIME_DIR: не в inventory, который коммитится); убирает cleanup точки входа

# iv_workdir <имя>: пустой подкаталог рабочего каталога прогона (рабочий каталог создаётся при первом обращении); путь — в iv_wd
# (не на stdout: в подоболочке iv_tmp потерялся бы, и cleanup не убрал бы каталог).
iv_wd=''
iv_workdir() {
	if [ -z "$iv_tmp" ]; then
		iv_tmp=$(umask 077 && mktemp -d "${XDG_RUNTIME_DIR:?}/sa-iv.XXXXXX") || return 1
	fi
	rm -rf -- "${iv_tmp:?}/$1"
	(umask 077 && mkdir -- "$iv_tmp/$1") || return 1
	iv_wd=$iv_tmp/$1
}

iv_fail() {
	echo "image-verify: $*" >&2
	return 3
}

# iv_kv <файл> <ключ…>: каждое имя читается из KEY=VALUE одной строкой и присваивается переменной; код из файла не
# исполняется, значение — ограниченный набор символов. Заданное в окружении НЕ учитывается (в отличие от load_kv).
iv_kv() {
	local f=$1 k v want ok
	shift
	{ [ -f "$f" ] && [ ! -L "$f" ]; } || iv_fail "нет файла констант ${f##*/}" || return 3
	for want in "$@"; do
		ok=0
		while IFS='=' read -r k v; do
			if [ "$k" = "$want" ]; then
				[[ $v =~ ^[A-Za-z0-9._/:@+-]+$ ]] || iv_fail "константа $k: недопустимые символы" || return 3
				printf -v "$k" '%s' "$v"
				ok=1
			fi
		done <"$f"
		[ "$ok" = 1 ] || iv_fail "в ${f##*/} нет $want" || return 3
	done
}

# iv_consts <каталог ops>: все константы проверки и инструментов; формат каждой сверяется.
iv_consts() {
	iv_kv "$1/image-consts.env" IC_REPO IC_REPO_ID IC_SIGNER_WORKFLOW IC_ISSUER IC_TRIGGER IC_RUNNER IC_VISIBILITY \
		IC_REF_PREFIX IC_SUBJECT IC_EV_MANIFEST IC_EV_BUNDLE IC_EV_MAX IC_GH_REPO IC_GH_MIN || return 3
	iv_kv "$1/tools.env" GH_VERSION GH_SHA256 || return 3
	{ [[ $IC_REPO_ID =~ ^[0-9]+$ ]] && [[ $IC_EV_MAX =~ ^[0-9]+$ ]] && [[ $GH_SHA256 =~ ^[0-9a-f]{64}$ ]] &&
		[[ $GH_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && [[ $IC_GH_MIN =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] &&
		[[ $IC_EV_MANIFEST =~ ^[A-Za-z0-9._-]+$ ]] && [[ $IC_EV_BUNDLE =~ ^[A-Za-z0-9._-]+\.jsonl$ ]] &&
		[[ $IC_REPO =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] && [[ $IC_GH_REPO =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; } ||
		iv_fail "константы image-consts.env или tools.env неверного вида" || return 3
}

# Версия выпуска — ровно vX.Y.Z без ведущих нулей.
iv_semver() { [[ ${1:-} =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; }

# iv_lt <a> <b>: a строго ниже b (X.Y.Z, с «v» или без).
iv_lt() {
	local a=${1#v} b=${2#v}
	[ "$a" != "$b" ] && [ "$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n 1)" = "$a" ]
}

# iv_collect <входящие> <пустой каталог 0700>: ровно два файла по имени; тот же dd, что у bundle в review.sh (nofollow:
# подложенный симлинк даёт ошибку, nonblock: FIFO не повесит открытие, предел на единицу выше — чтобы заметить превышение).
# Остальные файлы входящих не читаются; расширение .jsonl сохраняется (gh выбирает разбор по нему).
iv_collect() {
	local n
	for n in "$IC_EV_MANIFEST" "$IC_EV_BUNDLE"; do
		{ [ -f "$1/$n" ] && [ ! -L "$1/$n" ]; } || iv_fail "во входящих нет обычного файла $n (улики выпуска)" || return 3
		(umask 077 && timeout 120 dd if="$1/$n" of="$2/$n" iflag=nofollow,nonblock,count_bytes,fullblock count=$((IC_EV_MAX + 1)) bs=1M status=none) ||
			iv_fail "копия $n не удалась" || return 3
		[ "$(stat -c %s -- "$2/$n")" -le "$IC_EV_MAX" ] || iv_fail "$n больше предела размера" || return 3
	done
}

# iv_root_ok <файл>: корень Sigstore, как его пишет `gh attestation trusted-root` (ровно вывод, с \n в конце): строка
# public good — УЦ sigstore.dev и ключ журнала Rekor. Иначе — окно Б (старый корень или подмена).
iv_root_ok() {
	local hint='обновить корень Sigstore: окно Б, provision.sh prepare'
	{ [ -f "$1" ] && [ ! -L "$1" ] && [ -s "$1" ]; } || iv_fail "нет корня Sigstore ($hint)" || return 3
	[ "$(tail -c 1 -- "$1" | od -An -tx1 | tr -d ' \n')" = 0a ] || iv_fail "корень Sigstore не кончается переводом строки ($hint)" || return 3
	grep -F -e '"organization":"sigstore.dev"' -- "$1" | grep -qF -e '"baseUrl":"https://rekor.sigstore.dev"' ||
		iv_fail "в корне Sigstore нет строки public good с ключом Rekor ($hint)" || return 3
}

# --- gh в контейнере без сети ----------------------------------------------------------------------------------

iv_mounts=() # тома контейнера gh (задаёт вызывающий перед iv_gh)
iv_gh_err='' # файл для stderr gh при проверке (показывается при отказе)

# iv_gh <ID образа> <аргументы gh…>: контейнер sa-prov-* (secrets.sh: без журнала, корень только для чтения); образ только
# локальный (--pull=never), сети нет, не root.
# keep-id: каталог улик 0700 принадлежит учётной записи раскатки, а процесс контейнера — 65532; без отображения uid
# файлы были бы ему недоступны.
iv_gh() {
	local id=$1
	shift
	prov_run gh --pull=never --network=none --user 65532:65532 --userns=keep-id:uid=65532,gid=65532 -e HOME=/tmp \
		"${iv_mounts[@]}" "$id" "$@"
}

# Образ gh — ID из inventory (его пишет окно Б): 64 hex, с «sha256:» или без.
iv_gh_id() { # <файл>
	local id
	[ -s "$1" ] || iv_fail "нет образа gh: окно Б, provision.sh prepare" || return 3
	read -r id <"$1"
	[[ $id =~ ^(sha256:)?[0-9a-f]{64}$ ]] || iv_fail "gh-image-id неверного вида" || return 3
	echo "$id"
}

# iv_gh_version_ok <ID>: версия внутри образа = tools.env и не ниже минимума (подмена образа тем же ID невозможна,
# но образ мог быть собран из другого архива).
iv_gh_version_ok() {
	local out v
	iv_mounts=()
	out=$(iv_gh "$1" --version </dev/null 2>/dev/null) || iv_fail "gh не запустился (образ собран в окне Б?)" || return 3
	v=$(sed -n '1s/^gh version \([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\) .*$/\1/p' <<<"$out")
	[ "$v" = "$GH_VERSION" ] || iv_fail "версия gh не равна tools.env" || return 3
	if iv_lt "$v" "$IC_GH_MIN"; then iv_fail "версия gh ниже минимальной"; return 3; fi
}

# Поля, по которым решает код: из сертификата Fulcio (что выписано при подписи), а не из предиката; субъект —
# из утверждения. Одна строка TSV на каждый результат проверки.
# shellcheck disable=SC2016  # программа gojq, не раскрытие оболочки
IV_JQ='.[] | .verificationResult as $r | [$r.signature.certificate.sourceRepositoryIdentifier, $r.signature.certificate.buildTrigger, $r.signature.certificate.runnerEnvironment, $r.signature.certificate.sourceRepositoryVisibilityAtSigning, $r.signature.certificate.buildConfigURI, $r.signature.certificate.buildSignerURI, ([$r.statement.subject[] | .name + "@" + .digest.sha256] | join(","))] | @tsv'

# iv_gh_run <ID> <каталог улик> <имя улики> <имя bundle> <корень> <ref> <коммит>: TSV на stdout, код gh. Флаги привязывают
# подпись к репозиторию, процессу, метке и коммиту; --signer-workflow без --cert-identity (см. image-consts.env).
iv_gh_run() {
	iv_mounts=(-v "$2:/in:ro" -v "$5:/root/trusted_root.jsonl:ro")
	iv_gh "$1" attestation verify "/in/$3" --bundle "/in/$4" --custom-trusted-root /root/trusted_root.jsonl \
		-R "$IC_REPO" --signer-workflow "$IC_SIGNER_WORKFLOW" --source-ref "$6" --source-digest "$7" --signer-digest "$7" \
		--deny-self-hosted-runners --cert-oidc-issuer "$IC_ISSUER" --format json --jq "$IV_JQ" </dev/null 2>"${iv_gh_err:-/dev/null}"
}

# iv_show_err <файл>: stderr gh при отказе. Текст ошибки повторяет байты из улики (недоверенный источник): управляющие
# символы (ESC, BEL) заменяются на «?» (в C-локали кириллица не управляющая), иначе улика рисовала бы на терминале владельца.
iv_show_err() {
	head -n 20 -- "$1" | LC_ALL=C sed 's/[[:cntrl:]]/?/g; s/^/gh: /' >&2
}

# iv_cert_rows <ref> (stdin: TSV): каждая строка — результат; сертификат обязан сойтись во всех, пустой ввод — отказ. Кроме
# событий и раннера — buildConfigURI и buildSignerURI ТОЧНЫМ равенством с workflow@ref: SAN, который проверяет gh, не
# говорит, какой workflow запустил подпись (reusable-вызов release.yml из другого файла дал бы тот же SAN).
iv_cert_rows() {
	local line n=0 want tabs uri="https://github.com/$IC_SIGNER_WORKFLOW@$1"
	want=$(printf '%s\t%s\t%s\t%s\t%s\t%s' "$IC_REPO_ID" "$IC_TRIGGER" "$IC_RUNNER" "$IC_VISIBILITY" "$uri" "$uri")
	while IFS= read -r line; do
		n=$((n + 1))
		tabs=${line//[^$'\t']/}
		{ [ "${#tabs}" = 6 ] && [ "${line%$'\t'*}" = "$want" ]; } ||
			iv_fail "результат $n: сертификат не сходится (id репозитория, событие, раннер, видимость, workflow и метка)" || return 3
	done
	[ "$n" -gt 0 ] || iv_fail "пустой результат проверки" || return 3
}

# iv_subject_rows <D> (stdin: TSV): в утверждении каждого результата ровно один субъект — образ с дайджестом D.
iv_subject_rows() {
	local line n=0
	while IFS= read -r line; do
		n=$((n + 1))
		[ "${line##*$'\t'}" = "$IC_SUBJECT@$1" ] || iv_fail "результат $n: субъект утверждения не равен образу с дайджестом улики" || return 3
	done
	[ "$n" -gt 0 ] || iv_fail "пустой результат проверки" || return 3
}

iv_decide() { # <D> <ref> (stdin: TSV)
	local tsv
	tsv=$(cat)
	[ -n "$tsv" ] || iv_fail "пустой результат проверки" || return 3
	iv_cert_rows "$2" <<<"$tsv" || return 3
	iv_subject_rows "$1" <<<"$tsv"
}

# --- запись состояния ------------------------------------------------------------------------------------------

# iv_state <файл> <ключ…>: строки KEY=VALUE файла состояния ровно в заданном порядке; значения — в iv_s_<ключ>.
iv_state() {
	local f=$1 k v want
	shift
	{ [ -f "$f" ] && [ ! -L "$f" ]; } || return 1
	for want in "$@"; do
		IFS='=' read -r k v || return 1
		[ "$k" = "$want" ] || return 1
		printf -v "iv_s_$k" '%s' "$v"
	done <"$f"
}

# Раскатанная версия (нужна для порога отката): нет файла — раскатки не было.
iv_deployed_version() { # <inventory>
	iv_s_version=''
	[ -e "$1/deployed-image" ] || return 0
	iv_state "$1/deployed-image" version digest && iv_semver "$iv_s_version" || iv_fail "deployed-image повреждён" || return 3
}

# Порог — раскатанная версия, а не проверенная: упавший configure новой версии откат не блокирует. Ниже — только с флагом.
iv_downgrade_ok() { # <inventory> <версия> <разрешено 0|1>
	iv_deployed_version "$1" || return 3
	[ -z "$iv_s_version" ] && return 0
	if iv_lt "$2" "$iv_s_version" && [ "$3" != 1 ]; then
		iv_fail "версия ниже раскатанной ($iv_s_version): откат — только с --allow-downgrade"
		return 3
	fi
}

# iv_store <inventory> <версия> <коммит> <D hex> <каталог улик>: улики по версиям и verified-image (последним — им
# пользуется configure). Временный файл + mv под umask 077.
iv_store() {
	local inv=$1 ver=$2 dir n
	dir="$inv/releases/$ver"
	(umask 077 && mkdir -p "$inv/releases" && rm -rf -- "$inv/releases/.$ver.new" && mkdir "$inv/releases/.$ver.new") ||
		iv_fail "каталог releases" || return 1
	for n in "$IC_EV_MANIFEST" "$IC_EV_BUNDLE"; do
		(umask 077 && cp -- "$5/$n" "$inv/releases/.$ver.new/$n") || iv_fail "улика $n не сохранена" || return 1
	done
	rm -rf -- "$dir"
	mv -- "$inv/releases/.$ver.new" "$dir" || return 1
	(umask 077 && printf 'version=%s\ncommit=%s\ndigest=sha256:%s\n' "$ver" "$3" "$4" >"$inv/.verified-image.new") &&
		mv -f -- "$inv/.verified-image.new" "$inv/verified-image"
}

# --- configure: ровно проверенный образ ------------------------------------------------------------------------

img_ref='' img_ver='' img_dig='' # образ для Ansible (-e) и учёт раскатанного

# image_gate: verified-image есть и проверен для текущего HEAD; albums_svc_image нигде больше не задан (другой
# источник перебил бы проверенное — -e его только перекрывает, поэтому выбор неоднозначности исключён).
image_gate() {
	local head f
	iv_consts "$here/ops" || return 3
	iv_state "$SA_INVENTORY/verified-image" version commit digest ||
		iv_fail "нет проверенного образа: provision.sh image-verify vX.Y.Z" || return 3
	{ iv_semver "$iv_s_version" && [[ $iv_s_commit =~ ^[0-9a-f]{40}$ ]] && [[ $iv_s_digest =~ ^sha256:[0-9a-f]{64}$ ]]; } ||
		iv_fail "verified-image повреждён" || return 3
	head=$(git -C "$repo" rev-parse HEAD) || return 3
	[ "$iv_s_commit" = "$head" ] || iv_fail "образ проверен для другого коммита, чем HEAD: повторить image-verify" || return 3
	f=$(grep -rIl -E '^[[:space:]]*albums_svc_image[[:space:]]*:' -- "$SA_INVENTORY/ansible/group_vars" "$SA_INVENTORY/ansible/host_vars" 2>/dev/null | head -n 1)
	[ -z "$f" ] || iv_fail "albums_svc_image задан в inventory (${f#"$SA_INVENTORY"/}): уберите, образ пишет image-verify" || return 3
	img_ver=$iv_s_version img_dig=$iv_s_digest img_ref="$IC_SUBJECT@$iv_s_digest"
}

# После успешного site.yml: раскатанные версия и дайджест (порог отката).
image_deployed_write() {
	(umask 077 && printf 'version=%s\ndigest=%s\n' "$img_ver" "$img_dig" >"$SA_INVENTORY/.deployed-image.new") &&
		mv -f -- "$SA_INVENTORY/.deployed-image.new" "$SA_INVENTORY/deployed-image"
}

# --- image-verify ----------------------------------------------------------------------------------------------

# iv_tree <каталог>: файлы HEAD, которые читает guard, — .github, deploy/release, deploy/ansible/requirements.txt (по нему guard
# сверяет строку PyYAML). Только blob'ы из ls-tree + cat-file: `git archive` применил бы export-ignore/export-subst из
# .gitattributes дерева, и guard увидел бы не то, что исполнит GitHub. Пути — только [A-Za-z0-9._/-], без «..»; режимы — только 100644 и 100755.
iv_tree() {
	local rec meta path sha n=0
	while IFS= read -r -d '' rec; do
		meta=${rec%%$'\t'*} path=${rec#*$'\t'} sha=${meta##* }
		{ [[ $path =~ ^[A-Za-z0-9._/-]+$ ]] && [[ $path != *..* ]] && [[ $sha =~ ^[0-9a-f]{40}$ ]]; } ||
			iv_fail "путь в HEAD вне ASCII или небезопасный: проверка остановлена" || return 3
		# Только обычные файлы: симлинк (120000) или gitlink выгрузились бы содержимым ссылки, а не тем, что исполнит GitHub.
		[[ $meta =~ ^100(644|755)\ blob\  ]] || iv_fail "в HEAD не обычный файл (симлинк или gitlink): $path" || return 3
		mkdir -p -- "$1/${path%/*}" || return 3
		git -C "$repo" cat-file blob "$sha" >"$1/$path" || iv_fail "git cat-file не выполнился" || return 3
		n=$((n + 1))
	done < <(git -C "$repo" ls-tree -r -z HEAD -- .github deploy/release deploy/ansible/requirements.txt)
	[ "$n" -gt 0 ] || iv_fail "нет .github или deploy/release в HEAD" || return 3
	[ -f "$1/deploy/ansible/requirements.txt" ] || iv_fail "в HEAD нет deploy/ansible/requirements.txt" || return 3
}

# Договорённость с deploy/release/workflow_guard.py (design §2, §8): `python3 -I workflow_guard.py --root <дерево> --paths <файл>
# [--pins <каталог>]`, коды 0 (инварианты выполнены), 3 (отказ), 2 (ошибка вызова); любой код ≠ 0 здесь — отказ. Дерево — /src
# (iv_tree), сверенные наборы действий — /pins, список путей HEAD (git ls-tree -r -z) — /in/paths. Запуск в контроллере, без сети.
iv_workflow_guard() { # <каталог 0700>
	local d=$1 sha
	git -C "$repo" ls-tree -r -z --name-only HEAD >"$d/paths" || iv_fail "git ls-tree не выполнился" || return 3
	mkdir -p "$d/tree" && iv_tree "$d/tree" || return 3
	[ -f "$d/tree/deploy/release/actions.lock" ] || iv_fail "в HEAD нет deploy/release/actions.lock" || return 3
	sha=$(sha256sum <"$d/tree/deploy/release/actions.lock" | cut -d' ' -f1)
	[ -f "$SA_INVENTORY/actions-pins/$sha.lock" ] || iv_fail "окно Б: сверка действий (lock этого коммита не сверен), provision.sh prepare" || return 3
	cmp -s -- "$SA_INVENTORY/actions-pins/$sha.lock" "$d/tree/deploy/release/actions.lock" || iv_fail "сверенный lock не равен lock коммита" || return 3
	prov_run guard --network=none -v "$d/tree:/src:ro" -v "$SA_INVENTORY/actions-pins:/pins:ro" -v "$d/paths:/in/paths:ro" \
		"$ctl_id" python3 -I /src/deploy/release/workflow_guard.py --root /src --pins /pins --paths /in/paths 2>&1 |
		LC_ALL=C sed 's/[[:cntrl:]]/?/g' >&2 ||
		iv_fail "инварианты workflow не выполнены (workflow_guard.py)" || return 3
}

# image-verify vX.Y.Z [--allow-downgrade]: после guard_* provision.sh; ctl_id собран.
cmd_image_verify() {
	local ver=${1:-} allow=0 head d id D tsv inbox=${SA_INBOX:-/srv/sa-inbox} ref
	[ "${2:-}" != --allow-downgrade ] || allow=1
	iv_semver "$ver" || iv_fail "нужна версия вида vX.Y.Z" || return 3
	iv_consts "$here/ops" || return 3
	head=$(git -C "$repo" rev-parse HEAD) && [[ $head =~ ^[0-9a-f]{40}$ ]] || iv_fail "HEAD не определён" || return 3
	iv_workdir run || iv_fail "временный каталог" || return 1
	d=$iv_wd
	iv_workflow_guard "$d" || return 3
	mkdir -m 0700 "$d/ev" && iv_collect "$inbox" "$d/ev" || return 3
	D=$(sha256sum <"$d/ev/$IC_EV_MANIFEST" | cut -d' ' -f1)
	iv_root_ok "$SA_INVENTORY/sigstore/trusted_root.jsonl" || return 3
	id=$(iv_gh_id "$SA_INVENTORY/gh-image-id") || return 3
	iv_gh_version_ok "$id" || return 3
	ref=$IC_REF_PREFIX$ver
	iv_gh_err=$d/gh.err
	tsv=$(iv_gh_run "$id" "$d/ev" "$IC_EV_MANIFEST" "$IC_EV_BUNDLE" "$SA_INVENTORY/sigstore/trusted_root.jsonl" "$ref" "$head") || {
		iv_show_err "$d/gh.err"
		iv_fail "gh attestation verify отказал (улика не подходит коммиту и метке, или корень устарел: окно Б)"
		return 3
	}
	iv_decide "$D" "$ref" <<<"$tsv" || return 3
	iv_downgrade_ok "$SA_INVENTORY" "$ver" "$allow" || return 3
	iv_store "$SA_INVENTORY" "$ver" "$head" "$D" "$d/ev" || return 1
	echo "image-verify: $ver, коммит $head, образ $IC_SUBJECT@sha256:$D — записано в inventory/verified-image"
}
