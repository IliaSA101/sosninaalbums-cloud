#!/usr/bin/env bash
# Окно Б для проверки выпущенного образа (спека 004, design §7, §8): gh из архива выпуска с закреплённым sha256 (как
# провайдер tofu, ВР-15), корень Sigstore и сверка закреплённых действий с исходными репозиториями. Только source из
# provision.sh (prepare); нужны ops/image.sh и ops/mirror.sh. Сеть открыта только здесь: проверка образа идёт без неё.
# shellcheck disable=SC2154  # here, repo, SA_INVENTORY, nolock, prov_run: задаёт provision.sh

# tools_gh_image <архив> <каталог сборки 0700, пустой>: из архива берётся только bin/gh; образ FROM scratch с одним
# бинарником (его ID печатается). Лимит записи — на случай архива-бомбы при верном sha256 (не должно, но дёшево).
tools_gh_image() {
	local v=$GH_VERSION
	(
		ulimit -c 0 && ulimit -f 204800 &&
			tar -xzf "$1" --no-same-owner -O "gh_${v}_linux_amd64/bin/gh" >"$2/gh"
	) || iv_fail "в архиве gh нет bin/gh или он не распаковался" || return 3
	chmod 0755 "$2/gh" && printf 'FROM scratch\nCOPY gh /gh\nUSER 65532:65532\nENTRYPOINT ["/gh"]\n' >"$2/Containerfile" ||
		iv_fail "Containerfile gh" || return 3
	nolock podman build -q --network=none -f "$2/Containerfile" "$2" || iv_fail "образ gh не собрался" || return 3
}

# tools_gh_fetch: архив gh и gh_<версия>_checksums.txt выпуска; sha256 архива обязан совпасть и с tools.env, и со строкой
# checksums (расхождение с любой — отказ, образ не трогается); версия — не ниже минимума. ID образа — в inventory.
tools_gh_fetch() {
	local v=$GH_VERSION name base stage got want id
	name=gh_${v}_linux_amd64.tar.gz
	base=https://github.com/$IC_GH_REPO/releases/download/v$v
	if iv_lt "$v" "$IC_GH_MIN"; then iv_fail "версия gh в tools.env ниже минимальной"; return 3; fi
	{ iv_workdir gh && stage=$iv_wd && (umask 077 && mkdir -p -- "$stage/ctx"); } || iv_fail "каталог сборки gh" || return 3
	mirror_get "$base/$name" "$stage/a.tgz" 104857600 || { rm -rf -- "$stage"; iv_fail "не скачался архив gh: $base/$name"; return 3; }
	mirror_get "$base/gh_${v}_checksums.txt" "$stage/sums" 1048576 || { rm -rf -- "$stage"; iv_fail "не скачан checksums.txt выпуска gh"; return 3; }
	got=$(sha256sum -- "$stage/a.tgz" | awk '{print $1}')
	want=$(awk -v n="$name" '$2 == n {print $1}' "$stage/sums")
	if [ "$got" != "$GH_SHA256" ] || [ "$got" != "$want" ]; then
		rm -rf -- "$stage"
		iv_fail "sha256 архива gh не совпал с tools.env или checksums.txt выпуска"
		return 3
	fi
	id=$(tools_gh_image "$stage/a.tgz" "$stage/ctx") || { rm -rf -- "$stage"; return 3; }
	rm -rf -- "$stage"
	[[ $id =~ ^(sha256:)?[0-9a-f]{64}$ ]] || iv_fail "podman build не вернул ID образа" || return 3
	(umask 077 && printf '%s\n' "$id" >"$SA_INVENTORY/.gh-image-id.new") && mv -f -- "$SA_INVENTORY/.gh-image-id.new" "$SA_INVENTORY/gh-image-id" ||
		iv_fail "gh-image-id не записан" || return 3
	echo "gh $v: архив с GitHub, sha256 совпал с tools.env и checksums.txt — образ без сети собран"
}

# tools_root <ID gh> <файл>: корень Sigstore — ровно вывод `gh attestation trusted-root` (единственное место с сетью:
# TUF; часы машины должны быть верны). Заменяется только проверенный файл.
tools_root() {
	local tmp="$2.new"
	(umask 077 && mkdir -p -- "${2%/*}" && : >"$tmp") || iv_fail "каталог корня Sigstore" || return 3
	# В образе FROM scratch нет корневых сертификатов (gh их не встраивает): без этого тома TLS к TUF не поднимается. Подлинность
	# корня даёт подпись TUF, TLS здесь только транспорт.
	[ -f /etc/ssl/certs/ca-certificates.crt ] || { iv_fail "нет /etc/ssl/certs/ca-certificates.crt у ops-ВМ"; return 3; }
	if ! prov_run gh --pull=never --user 65532:65532 -e HOME=/tmp -v /etc/ssl/certs/ca-certificates.crt:/etc/ssl/certs/ca-certificates.crt:ro \
		"$1" attestation trusted-root >"$tmp" </dev/null 2>/dev/null; then
		rm -f -- "$tmp"
		iv_fail "gh attestation trusted-root не выполнился (нет сети до TUF?)"
		return 3
	fi
	iv_root_ok "$tmp" || { rm -f -- "$tmp"; return 3; }
	mv -f -- "$tmp" "$2" || return 3
	echo "корень Sigstore обновлён $(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

# --- закреплённые действия (design §7) -------------------------------------------------------------------------

# Ссылки самого исходного репозитория: коммит из форка сюда не попадает (ls-remote его не видит). Конфигурация git
# пользователя не читается; без запросов пароля.
pins_lsremote() { # <имя действия> <тег>
	GIT_TERMINAL_PROMPT=0 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null nolock timeout 60 git -C / ls-remote -- \
		"https://github.com/actions/$1" "refs/tags/$2" "refs/tags/$2^{}"
}

# pins_pick <тег> (stdin: вывод ls-remote): коммит по имени ссылки РОВНО refs/tags/<тег>^{} (аннотированная метка —
# коммит, а не объект метки), иначе РОВНО refs/tags/<тег>. Хвост вроде refs/heads/x/refs/tags/<тег> (шаблон ls-remote
# совпадает по хвосту имени) не принимается.
pins_pick() {
	awk -F '\t' -v t="refs/tags/$1" '
		$2 == (t "^{}") { peeled = $1 }
		$2 == t { plain = $1 }
		END { if (peeled != "") print peeled; else if (plain != "") print plain }'
}

# action.yml закреплённого коммита (raw, по SHA); .yaml — запасное имя.
pins_action_yml() { # <имя> <sha>
	local t
	local u=https://raw.githubusercontent.com/actions/$1/$2 code
	iv_workdir pins || return 1
	t=$iv_wd
	if mirror_get "$u/action.yml" "$t/a" 65536 2>/dev/null; then
		cat -- "$t/a"
		return 0
	fi
	# action.yaml — только если action.yml нет (404): раннер предпочитает action.yml, и любая другая ошибка не повод менять файл.
	code=$(pins_http_code "$u/action.yml")
	[ "$code" = 404 ] || return 1
	mirror_get "$u/action.yaml" "$t/a" 65536 2>/dev/null && cat -- "$t/a"
}

# Код ответа HTTP на HEAD-подобный запрос (только для различения 404 и прочих ошибок).
pins_http_code() {
	nolock curl -q --proto '=https' --proto-redir '=https' --tlsv1.2 -sS -o /dev/null -w '%{http_code}' --connect-timeout 20 --max-time 60 "$1" 2>/dev/null
}

# pins_using_ok (stdin: action.yml): runs.using — ровно node<N>; composite (плавающие uses: внутри) и docker (образ по тегу) —
# отказ. Разбор — PyYAML (безопасный загрузчик, повтор ключа — отказ) в контейнере контроллера без сети, а не строкой: приманка `using: node20` в описании
# обманула бы построчный разбор. Нужен ctl_id (prepare собирает контроллер до окна Б).
pins_using_ok() {
	local f
	iv_workdir using || return 1
	f=$iv_wd/action.yml
	cat >"$f"
	prov_run pins --network=none -v "$f:/in/action.yml:ro" "$ctl_id" python3 -I -c '
import re, sys, yaml
class Strict(yaml.SafeLoader):
    pass
def mapping(loader, node, deep=False):
    seen = set()
    for k, _ in node.value:
        key = loader.construct_object(k, deep=True)
        if key in seen:
            raise yaml.constructor.ConstructorError(None, None, "duplicate key", k.start_mark)
        seen.add(key)
    return yaml.SafeLoader.construct_mapping(loader, node, deep)
Strict.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, mapping)
text = open(sys.argv[1], encoding="utf-8").read()
doc = yaml.load(text, Loader=Strict)
runs = doc.get("runs") if isinstance(doc, dict) else None
using = runs.get("using") if isinstance(runs, dict) else None
ok = isinstance(using, str) and re.fullmatch(r"node[0-9]+", using) and "docker://" not in text
sys.exit(0 if ok else 1)' /in/action.yml </dev/null >/dev/null 2>&1
}

# pins_verify <lock> <каталог actions-pins>: каждая строка `actions/<имя> <тег> <40 hex>` сверяется с меткой исходного
# репозитория и с runs.using; сверенный lock кладётся как <sha256>.lock (по нему image-verify узнаёт, что набор сверен).
pins_verify() {
	local line name tag sha got yml n=0 sum
	while IFS= read -r line; do
		case $line in '' | '#'*) continue ;; esac
		[[ $line =~ ^actions/([a-z0-9][a-z0-9._-]*)\ (v[0-9][A-Za-z0-9._-]*)\ ([0-9a-f]{40})$ ]] ||
			iv_fail "lock: строка не вида actions/<имя> <тег> <40 hex>" || return 3
		name=${BASH_REMATCH[1]} tag=${BASH_REMATCH[2]} sha=${BASH_REMATCH[3]}
		got=$(pins_lsremote "$name" "$tag" </dev/null | pins_pick "$tag") || iv_fail "git ls-remote actions/$name не выполнился" || return 3
		[ "$got" = "$sha" ] || iv_fail "actions/$name $tag: на метке исходного репозитория не закреплённый коммит" || return 3
		yml=$(pins_action_yml "$name" "$sha" </dev/null) || iv_fail "actions/$name: не получен action.yml закреплённого коммита" || return 3
		pins_using_ok <<<"$yml" || iv_fail "actions/$name: runs.using не node<N> (composite или docker)" || return 3
		n=$((n + 1))
	done <"$1"
	[ "$n" -gt 0 ] || iv_fail "lock пуст" || return 3
	sum=$(sha256sum <"$1" | cut -d' ' -f1)
	(umask 077 && mkdir -p -- "$2" && cp -- "$1" "$2/.$sum.new") && mv -f -- "$2/.$sum.new" "$2/$sum.lock" || iv_fail "сверенный lock не записан" || return 3
	echo "действия: lock сверен с метками исходных репозиториев ($n) — $sum"
}

# Окно Б целиком (после сборки контроллера): gh → корень → пины. Нет lock в этом коммите (выпуск ещё не подготовлен) —
# пины не сверяются, image-verify без них всё равно откажет.
tools_prepare() {
	local id
	iv_consts "$here/ops" || return 3
	iv_workdir window || return 3 # рабочий каталог создаётся здесь, в основной оболочке: cleanup убирает его по trap
	tools_gh_fetch || return 3
	id=$(iv_gh_id "$SA_INVENTORY/gh-image-id") || return 3
	iv_gh_version_ok "$id" || return 3
	tools_root "$id" "$SA_INVENTORY/sigstore/trusted_root.jsonl" || return 3
	if [ -f "$repo/deploy/release/actions.lock" ]; then
		pins_verify "$repo/deploy/release/actions.lock" "$SA_INVENTORY/actions-pins" || return 3
	else
		echo "в этом коммите нет deploy/release/actions.lock: действия не сверялись"
	fi
}
