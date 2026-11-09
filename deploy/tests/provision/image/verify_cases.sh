#!/usr/bin/env bash
# image_verify_cases (спека 004, design §12): проверка образа на настоящем gh в Podman без сети. Фикстура — публичная
# аттестация выпуска cli/cli (image/fixtures): константы подставляются КОПИЕЙ файла констант в копии дерева (не флагом и не
# переменной боевого пути). Положительный путь с боевыми константами — фикстура после выпуска v0.1.0 (спека, «После v0.1.0»).
#
#   verify_cases.sh <корень репозитория> <рабочий каталог> <архив gh>
#
# Печатает ok/FAIL, код выхода — число провалов. Образ gh собирает тот же код окна Б (tools_gh_fetch), сеть подменена
# копированием архива. Тома — только в рабочем каталоге.
# shellcheck source-path=SCRIPTDIR disable=SC2154,SC1091,SC2016,SC2030,SC2031  # переменные библиотек; источники — в копии дерева
set -uo pipefail

root=$1 w=$2 archive=$3
fx=$root/deploy/tests/provision/image/fixtures
tree=$w/tree
fails=0
ok() { echo "ok   $1"; }
bad() {
	echo "FAIL $1"
	fails=$((fails + 1))
}
check() { # <имя> <команда…>
	local n=$1
	shift
	if "$@"; then ok "$n"; else bad "$n"; fi
}
want3() { # <имя> <команда…>: команда обязана вернуть ровно 3 (отказ до действий)
	local n=$1 rc
	shift
	"$@" >/dev/null 2>&1
	rc=$?
	if [ "$rc" = 3 ]; then ok "$n"; else bad "$n: код $rc, ждали 3"; fi
}
die() {
	echo "verify_cases: $*" >&2
	exit 1
}

mkdir -p "$tree/deploy/ops" "$w/inv" || die "каталоги"
# Рабочие каталоги окна Б (iv_workdir) — в настоящем XDG_RUNTIME_DIR (подмена переменной ломает Podman), убираются по выходу.
trap '[ -z "$iv_tmp" ] || rm -rf -- "$iv_tmp"' EXIT
for f in image.sh tools.sh mirror.sh secrets.sh ctl-run.sh tools.env; do cp -- "$root/deploy/ops/$f" "$tree/deploy/ops/$f" || die "копия $f"; done

# mkconsts <каталог> <KEY=VAL…>: копия боевых констант с заменой строк (awk + ENVIRON: без обработки «\»); ключи GH_* — в
# tools.env. Сначала подставляются значения под фикстуру cli/cli.
fixture_kv=(IC_REPO=cli/cli IC_REPO_ID=212613049 IC_SIGNER_WORKFLOW=cli/cli/.github/workflows/deployment.yml
	IC_TRIGGER=workflow_dispatch IC_REF_PREFIX=refs/heads/ IC_SUBJECT=gh_2.103.3_linux_amd64.tar.gz IC_EV_MAX=20000000)
mkconsts() {
	local d=$1 kv k f
	shift
	mkdir -p "$d/deploy/ops" && cp -- "$tree/deploy/ops/tools.env" "$d/deploy/ops/" || return 1
	cp -- "$root/deploy/ops/image-consts.env" "$d/deploy/ops/image-consts.env" || return 1
	for kv in "${fixture_kv[@]}" "$@"; do
		k=${kv%%=*}
		case $k in GH_*) f=$d/deploy/ops/tools.env ;; *) f=$d/deploy/ops/image-consts.env ;; esac
		grep -q "^$k=" "$f" || return 1
		KV=$kv K=$k awk '$0 ~ ("^" ENVIRON["K"] "=") { print ENVIRON["KV"]; next } { print }' "$f" >"$f.new" && mv -- "$f.new" "$f" || return 1
	done
}
n=0
mut() { # <KEY=VAL…>: каталог ops копии дерева с изменёнными константами
	n=$((n + 1))
	mkconsts "$w/m$n" "$@" || die "mkconsts $*"
	echo "$w/m$n/deploy/ops"
}
inenv() ( # <каталог ops> <команда…>: константы из этого каталога, только в подоболочке
	iv_consts "$1" || exit 3
	shift
	"$@"
)

. "$tree/deploy/ops/ctl-run.sh"
. "$tree/deploy/ops/secrets.sh"
. "$tree/deploy/ops/mirror.sh"
. "$tree/deploy/ops/image.sh"
. "$tree/deploy/ops/tools.sh"

mkconsts "$w/base" || die "базовые константы"
base=$w/base/deploy/ops
iv_consts "$base" || die "константы фикстуры"
commit=63837fb778d6a39f02be0ce76ff89f2bc8d74bdc
ref=refs/heads/trunk

# --- образ gh: код окна Б (tools_gh_fetch) с подменой сети ----------------------------------------------------------
sums_bad=0
mirror_get() { # <url> <файл> <предел>
	case $1 in
		*_linux_amd64.tar.gz) cp -- "$archive" "$2" ;;
		*_checksums.txt)
			if [ "$sums_bad" = 1 ]; then printf '%064d  gh_%s_linux_amd64.tar.gz\n' 0 "$GH_VERSION" >"$2"; else
				printf '%s  gh_%s_linux_amd64.tar.gz\n' "$(sha256sum <"$archive" | cut -d' ' -f1)" "$GH_VERSION" >"$2"
			fi
			;;
		*) return 1 ;;
	esac
}
SA_INVENTORY=$w/inv
mkdir -p "$SA_INVENTORY" && chmod 700 "$SA_INVENTORY"
if tools_gh_fetch >"$w/fetch.out" 2>&1; then ok "окно Б: образ gh собран из архива (sha256 совпал с tools.env и checksums.txt)"; else
	cat "$w/fetch.out"
	bad "окно Б: образ gh не собрался"
	echo "image_verify_cases: без образа gh остальное невозможно"
	exit 1
fi
id=$(iv_gh_id "$SA_INVENTORY/gh-image-id") || die "gh-image-id"
check "окно Б: ID образа записан в inventory (0600)" test "$(stat -c %a "$SA_INVENTORY/gh-image-id")" = 600
sums_bad=1
want3 "окно Б: sha256 не совпал со строкой checksums.txt выпуска — отказ" tools_gh_fetch
sums_bad=0
want3 "окно Б: sha256 архива не равен tools.env — отказ" inenv "$(mut GH_SHA256="$(printf '%064d' 1)")" tools_gh_fetch
want3 "окно Б: версия gh в tools.env ниже минимума — отказ" inenv "$(mut IC_GH_MIN=99.0.0)" tools_gh_fetch

check "gh: версия образа = tools.env и не ниже минимума" inenv "$base" iv_gh_version_ok "$id"
want3 "gh: ниже минимума (IC_GH_MIN выше версии) — отказ" inenv "$(mut IC_GH_MIN=99.0.0)" iv_gh_version_ok "$id"
want3 "gh: версия образа ≠ tools.env — отказ" inenv "$(mut GH_VERSION=2.103.4)" iv_gh_version_ok "$id"

# --- проверка на настоящей аттестации -------------------------------------------------------------------------------
ev=$w/ev
mkdir -m 0700 "$ev"
cp -- "$archive" "$ev/release.manifest.json"
cp -- "$fx/cli.attestation.jsonl" "$ev/release.attestation.jsonl"
cp -- "$fx/trusted_root.jsonl" "$w/root.jsonl"
D=$(sha256sum <"$ev/release.manifest.json" | cut -d' ' -f1)

# chain <ops> <корень> <ref> <коммит> [каталог улик] [имя bundle]: gh → сертификат; 3 при любом отказе. Подоболочка:
# мутированные константы не утекают дальше.
chain() (
	local c=$1 r=$2 rf=$3 cm=$4 e=${5:-$ev} b=${6:-release.attestation.jsonl} tsv
	iv_consts "$c" || exit 3
	tsv=$(iv_gh_run "$id" "$e" release.manifest.json "$b" "$r" "$rf" "$cm") || exit 3
	printf '%s\n' "$tsv" >"$w/last.tsv"
	iv_cert_rows "$rf" <<<"$tsv"
)
# Только gh (без сверки URI в iv_cert_rows): отказать обязан сам gh. Нужно для случаев --signer-workflow: gh сравнивает значение
# буквально (экранирует сам), поэтому «\.», префикс и «.» как любой символ не проходят; при смене версии gh на префикс или
# регулярку эти случаи покраснеют, а не спрячутся за сверкой URI. 0 - gh принял, 3 - отказал.
gh_only() ( # <значение IC_SIGNER_WORKFLOW>
	iv_consts "$base" || exit 3
	IC_SIGNER_WORKFLOW=$1
	iv_gh_run "$id" "$ev" release.manifest.json release.attestation.jsonl "$w/root.jsonl" "$ref" "$commit" >/dev/null || exit 3
)
flags_ok() {
	local f
	for f in --deny-self-hosted-runners --signer-digest --source-digest --custom-trusted-root --signer-workflow; do
		declare -f iv_gh_run | grep -qF -- "$f" || return 1
	done
	! declare -f iv_gh_run | grep -qF -- --cert-identity
}
container_ok() {
	local f
	for f in --pull=never --network=none '--user 65532:65532'; do declare -f iv_gh | grep -qF -- "$f" || return 1; done
}

check "верный → 0 (gh, флаги, поля сертификата)" chain "$base" "$w/root.jsonl" "$ref" "$commit"
want3 "неверен -R (репозиторий) → 3" chain "$(mut IC_REPO=cli/clx)" "$w/root.jsonl" "$ref" "$commit"
want3 "неверен --signer-workflow → 3" chain "$(mut IC_SIGNER_WORKFLOW=cli/cli/.github/workflows/deployment.yaml)" "$w/root.jsonl" "$ref" "$commit"
want3 "gh сам отказывает: значение с «\.» (gh экранирует само, константа — путь без экранирования)" gh_only 'cli/cli/\.github/workflows/deployment\.yml'
check "--signer-workflow: верное значение принимает сам gh (контроль для четырёх случаев ниже)" gh_only cli/cli/.github/workflows/deployment.yml
want3 "gh сам отказывает: префикс пути (…/deployment.ym)" gh_only cli/cli/.github/workflows/deployment.ym
want3 "gh сам отказывает: «.» не любой символ (…/deployment.y.l)" gh_only cli/cli/.github/workflows/deployment.y.l
want3 "gh сам отказывает: «.» не любой символ в конце (…/deployment.ym.)" gh_only cli/cli/.github/workflows/deployment.ym.
want3 "неверен --cert-oidc-issuer → 3" chain "$(mut IC_ISSUER=https://issuer.invalid)" "$w/root.jsonl" "$ref" "$commit"
want3 "неверна метка (--source-ref) → 3" chain "$base" "$w/root.jsonl" refs/heads/main "$commit"
want3 "неверен коммит (--source-digest, --signer-digest) → 3" chain "$base" "$w/root.jsonl" "$ref" 0000000000000000000000000000000000000000
want3 "id репозитория в сертификате ≠ константы → 3" chain "$(mut IC_REPO_ID=1440574753)" "$w/root.jsonl" "$ref" "$commit"
want3 "buildTrigger ≠ push (фикстура — workflow_dispatch) → 3" chain "$(mut IC_TRIGGER=push)" "$w/root.jsonl" "$ref" "$commit"
want3 "runnerEnvironment ≠ константы → 3" chain "$(mut IC_RUNNER=self-hosted)" "$w/root.jsonl" "$ref" "$commit"
want3 "видимость в момент подписи ≠ константы → 3" chain "$(mut IC_VISIBILITY=private)" "$w/root.jsonl" "$ref" "$commit"
check "флаги gh: --deny-self-hosted-runners, --signer-digest, --source-digest, --custom-trusted-root; --cert-identity нет" flags_ok
check "контейнер gh: --pull=never, --network=none, не root" container_ok
want3 "в утверждении много субъектов, а не один образ → решение отказывает (ровно один субъект)" inenv "$base" iv_decide "$D" "$ref" <"$w/last.tsv"
check "сертификат: buildConfigURI и buildSignerURI = workflow@ref точным равенством (настоящий вывод gh)" inenv "$base" iv_cert_rows "$ref" <"$w/last.tsv"
want3 "сертификат: ожидаемая метка другая (без флага gh) → 3" inenv "$base" iv_cert_rows refs/heads/other <"$w/last.tsv"
want3 "сертификат: константа workflow другая (без флага gh) → 3" inenv "$(mut IC_SIGNER_WORKFLOW=cli/cli/.github/workflows/other.yml)" iv_cert_rows "$ref" <"$w/last.tsv"

# Испорченный бит подписи, формат bundle.
python3 -I - "$fx/cli.attestation.jsonl" "$w/bitflip.jsonl" "$w/dup.jsonl" <<'PY'
import json, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
first = json.loads(lines[0])
sig = first["dsseEnvelope"]["signatures"][0]["sig"]
first["dsseEnvelope"]["signatures"][0]["sig"] = ("B" if sig[0] != "B" else "C") + sig[1:]
second = json.loads(lines[1])
sig = second["dsseEnvelope"]["signatures"][0]["sig"]
second["dsseEnvelope"]["signatures"][0]["sig"] = ("B" if sig[0] != "B" else "C") + sig[1:]
with open(sys.argv[2], "w", encoding="utf-8") as f:
    f.write(json.dumps(first, separators=(",", ":")) + "\n" + json.dumps(second, separators=(",", ":")) + "\n")
with open(sys.argv[3], "w", encoding="utf-8") as f:
    f.write(lines[1] + "\n" + lines[1] + "\n")
PY
mkdir -m 0700 "$w/ev2"
cp -- "$archive" "$w/ev2/release.manifest.json"
cp -- "$w/bitflip.jsonl" "$w/ev2/release.attestation.jsonl"
want3 "испорчен бит подписи → 3" chain "$base" "$w/root.jsonl" "$ref" "$commit" "$w/ev2"
cp -- "$fx/cli.attestation.jsonl" "$w/ev2/two.json"
want3 "bundle .json из двух строк → 3" chain "$base" "$w/root.jsonl" "$ref" "$commit" "$w/ev2" two.json
cp -- "$fx/cli.attestation.jsonl" "$w/ev2/noext"
want3 "bundle без расширения → 3" chain "$base" "$w/root.jsonl" "$ref" "$commit" "$w/ev2" noext
cp -- "$w/dup.jsonl" "$w/ev2/release.attestation.jsonl"
if chain "$base" "$w/root.jsonl" "$ref" "$commit" "$w/ev2"; then
	ok "две одинаковые строки → 0"
	check "две одинаковые строки: результатов два (проверены оба)" test "$(wc -l <"$w/last.tsv")" = 2
else bad "две одинаковые строки → 0"; fi
: >"$w/ev2/empty.jsonl"
want3 "пустой bundle (нет результатов) → 3" chain "$base" "$w/root.jsonl" "$ref" "$commit" "$w/ev2" empty.jsonl

# ESC и BEL в mediaType улики: gh повторяет их в ошибке, а показ ошибки (iv_show_err) обязан их заменить.
python3 -I - "$fx/cli.attestation.jsonl" "$w/ev2/esc.jsonl" <<'PY'
import json, sys
first = json.loads(open(sys.argv[1], encoding="utf-8").readline())
first["mediaType"] = "application/vnd.dev.sigstore.bundle\x1b]0;PWNED\x07\x1b[31m"
open(sys.argv[2], "w", encoding="utf-8").write(json.dumps(first, separators=(",", ":")) + "\n")
PY
esc_case() (
	iv_consts "$base" || exit 3
	iv_gh_err=$w/esc.err
	if iv_gh_run "$id" "$w/ev2" release.manifest.json esc.jsonl "$w/root.jsonl" "$ref" "$commit" >/dev/null; then exit 1; fi
)
show_clean() {
	local out
	out=$(iv_show_err "$w/esc.err" 2>&1)
	[ -n "$out" ] && ! grep -qP '\x1b|\x07' <<<"$out"
}
check "ESC в mediaType: gh отказал" esc_case
check "сырой stderr gh содержит байт 0x1b (иначе проверка ниже ничего не доказывает)" grep -qP '\x1b' "$w/esc.err"
check "iv_show_err: ни 0x1b, ни 0x07 в выводе, текст ошибки остался" show_clean
# --- корень Sigstore ------------------------------------------------------------------------------------------------
check "корень: фикстура принимается" iv_root_ok "$w/root.jsonl"
head -c -1 "$w/root.jsonl" >"$w/root-nonl.jsonl"
want3 "корень без \\n в конце → 3" iv_root_ok "$w/root-nonl.jsonl"
sed -n 2p "$w/root.jsonl" >"$w/root-nopg.jsonl"
want3 "корень без строки public good → 3" iv_root_ok "$w/root-nopg.jsonl"
sed 's|"baseUrl":"https://rekor.sigstore.dev"|"baseUrl":"https://rekor.invalid"|' "$w/root.jsonl" >"$w/root-norekor.jsonl"
want3 "корень без ключа Rekor → 3" iv_root_ok "$w/root-norekor.jsonl"
check "корень: отказ подсказывает окно Б" bash -c "$(declare -f iv_fail iv_root_ok); iv_root_ok \"\$1\" 2>&1 | grep -q 'окно Б'" _ "$w/root-nopg.jsonl"
want3 "корня нет → 3" iv_root_ok "$w/нет-такого.jsonl"

# --- улики из входящих: ровно два имени -----------------------------------------------------------------------------
inbox=$w/inbox
mkdir -m 0755 "$inbox"
cp -- "$ev/release.manifest.json" "$ev/release.attestation.jsonl" "$inbox/"
printf 'подложенный корень\n' >"$inbox/trusted_root.jsonl"
printf 'x\n' >"$inbox/other.txt"
mkdir -m 0700 "$w/col"
check "улики: скопированы два имени" inenv "$base" iv_collect "$inbox" "$w/col"
check "улики: trusted_root.jsonl и прочее из входящих не берутся (в каталоге ровно два файла)" test "$(find "$w/col" -type f -printf '%f\n' | sort | tr '\n' ' ')" = 'release.attestation.jsonl release.manifest.json '
mkdir -m 0700 "$w/col2"
want3 "улики: выше предела размера → 3" inenv "$(mut IC_EV_MAX=100)" iv_collect "$inbox" "$w/col2"
rm -f "$inbox/release.attestation.jsonl"
ln -s "$ev/release.attestation.jsonl" "$inbox/release.attestation.jsonl"
mkdir -m 0700 "$w/col3"
want3 "улики: симлинк вместо файла → 3" inenv "$base" iv_collect "$inbox" "$w/col3"
rm -f "$inbox/release.attestation.jsonl"
mkdir -m 0700 "$w/col4"
want3 "улики: нет второго файла → 3" inenv "$base" iv_collect "$inbox" "$w/col4"

# --- решение по синтетическому JSON (TSV от gh --jq), боевые константы ----------------------------------------------
prod=$tree/deploy/ops
mkdir -p "$prod"
cp -- "$root/deploy/ops/image-consts.env" "$prod/"
iv_consts "$prod" || die "боевые константы"
rf=refs/tags/v0.1.0
uri=https://github.com/$IC_SIGNER_WORKFLOW@$rf
row() { # <id> <событие> <раннер> <видимость> <buildConfigURI> <buildSignerURI> <субъекты>
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s' "$@"
}
good=$(row "$IC_REPO_ID" push github-hosted public "$uri" "$uri" "$IC_SUBJECT@$D")
two=$(printf '%s\n%s' "$good" "$good")
dec() { inenv "$prod" iv_decide "$D" "$rf" <<<"$1"; }
check "решение: верная строка → 0" dec "$good"
check "решение: две верные строки → 0" dec "$two"
want3 "решение: пустой ввод (пустой массив) → 3" inenv "$prod" iv_decide "$D" "$rf" </dev/null
want3 "решение: подпись другого workflow (buildSignerURI — reusable-вызов из x.yml) → 3" dec "$(row "$IC_REPO_ID" push github-hosted public "$uri" "https://github.com/${IC_SIGNER_WORKFLOW%/*}/x.yml@$rf" "$IC_SUBJECT@$D")"
want3 "решение: buildConfigURI другого workflow → 3" dec "$(row "$IC_REPO_ID" push github-hosted public "https://github.com/${IC_SIGNER_WORKFLOW%/*}/x.yml@$rf" "$uri" "$IC_SUBJECT@$D")"
want3 "решение: buildSignerURI на другой метке → 3" dec "$(row "$IC_REPO_ID" push github-hosted public "$uri" "https://github.com/$IC_SIGNER_WORKFLOW@refs/tags/v9.9.9" "$IC_SUBJECT@$D")"
want3 "решение: чужой id репозитория → 3" dec "$(row 1 push github-hosted public "$uri" "$uri" "$IC_SUBJECT@$D")"
want3 "решение: событие не push → 3" dec "$(row "$IC_REPO_ID" workflow_dispatch github-hosted public "$uri" "$uri" "$IC_SUBJECT@$D")"
want3 "решение: свой раннер → 3" dec "$(row "$IC_REPO_ID" push self-hosted public "$uri" "$uri" "$IC_SUBJECT@$D")"
want3 "решение: репозиторий приватный → 3" dec "$(row "$IC_REPO_ID" push github-hosted private "$uri" "$uri" "$IC_SUBJECT@$D")"
want3 "решение: другой дайджест субъекта → 3" dec "$(row "$IC_REPO_ID" push github-hosted public "$uri" "$uri" "$IC_SUBJECT@$(printf '%064d' 0)")"
want3 "решение: второй субъект → 3" dec "$good,other@$D"
want3 "решение: чужое имя субъекта → 3" dec "$(row "$IC_REPO_ID" push github-hosted public "$uri" "$uri" "ghcr.io/other/img@$D")"
want3 "решение: лишнее поле → 3" dec "$(printf '%s\textra' "$good")"
want3 "решение: первая строка верна, вторая — нет → 3 (проверяется каждый результат)" dec "$(printf '%s\n%s' "$good" "$(row "$IC_REPO_ID" push self-hosted public "$uri" "$uri" "$IC_SUBJECT@$D")")"

# --- порог отката и запись --------------------------------------------------------------------------------------------
inv=$w/inv3
mkdir -m 0700 "$inv"
check "откат: раскатанной версии нет — любая версия проходит" iv_downgrade_ok "$inv" v0.0.1 0
printf 'version=v0.2.0\ndigest=sha256:%064d\n' 0 >"$inv/deployed-image"
want3 "откат: ниже раскатанной без флага → 3" iv_downgrade_ok "$inv" v0.1.9 0
check "откат: ниже раскатанной с --allow-downgrade → 0" iv_downgrade_ok "$inv" v0.1.9 1
check "откат: раскатанная версия без флага → 0 (упавший configure новой версии не блокирует возврат)" iv_downgrade_ok "$inv" v0.2.0 0
check "откат: версия выше раскатанной → 0" iv_downgrade_ok "$inv" v0.10.0 0
check "откат: сравнение числовое (v0.10.0 выше v0.9.0)" bash -c "$(declare -f iv_lt); iv_lt v0.9.0 v0.10.0 && ! iv_lt v0.10.0 v0.9.0"
printf 'мусор\n' >"$inv/deployed-image"
want3 "откат: deployed-image повреждён → 3" iv_downgrade_ok "$inv" v0.3.0 0
check "запись: iv_store проходит" iv_store "$inv" v0.3.0 "$commit" "$D" "$ev"
check "запись: verified-image с версией, коммитом и дайджестом (0600)" test "$(cat "$inv/verified-image")" = "version=v0.3.0
commit=$commit
digest=sha256:$D" -a "$(stat -c %a "$inv/verified-image")" = 600
check "запись: улики версии сохранены в releases/v0.3.0 (0700, файлы 0600)" bash -c 'cmp -s "$2/release.manifest.json" "$1/releases/v0.3.0/release.manifest.json" && cmp -s "$2/release.attestation.jsonl" "$1/releases/v0.3.0/release.attestation.jsonl" && [ "$(stat -c %a "$1/releases/v0.3.0")" = 700 ] && [ "$(stat -c %a "$1/releases/v0.3.0/release.manifest.json")" = 600 ]' _ "$inv" "$ev"

echo "image_verify_cases: провалов $fails"
[ "$fails" -eq 0 ]
