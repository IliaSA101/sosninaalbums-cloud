#!/usr/bin/env bash
# Локальное зеркало провайдера tofu (design 002 §16.10, ВР-15): реестр OpenTofu из России закрыт (403), поэтому
# провайдер берётся архивом выпуска с GitHub и ставится из каталога (`tofu init -plugin-dir`), без реестра. Только
# source из provision.sh; вызывается в prepare (окно Б, сеть открыта) и секретов не касается.
# shellcheck disable=SC2154  # die: provision.sh

# mirror_get <url> <файл> <предел в байтах>: только https (и после переадресаций), без ~/.curlrc (-q), с таймаутом;
# предел размера — и заголовком (--max-filesize), и жёстко (ulimit -f: запись сверх предела убивает curl сигналом).
mirror_get() {
	(
		ulimit -c 0 || exit 1 # без дампа памяти (сбой curl не оставляет файла)
		ulimit -f $((($3 + 1023) / 1024)) || exit 1
		nolock curl -q --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --connect-timeout 20 --max-time 300 \
			--max-filesize "$3" -o "$2" "$1"
	)
}

# Убирает черновик (stage — local mirror_fetch, видна по динамической области) и отказывает.
mirror_fail() {
	rm -rf -- "$stage"
	die "$*"
}

# mirror_fetch <lock-файл> <каталог зеркала>: версия, источник и платформа — только из lock (одно место, без
# литералов, которые могли бы разойтись). Архив и SHA256SUMS — с GitHub; sha256 архива обязан совпасть и со строкой
# SHA256SUMS, и с zh: в lock (расхождение с любым — отказ, зеркало не трогается). Упаковка — packed layout:
# <зеркало>/registry.opentofu.org/<ns>/<тип>/terraform-provider-<тип>_<версия>_<платформа>.zip. init из каталога
# принимает пакет, совпавший с любым из хэшей lock (проверено на tofu 1.13: при верном zh неверный h1 он пропускает),
# поэтому h1 сверяет `tofu validate` в prepare: кэш установленного провайдера против lock.
mirror_fetch() {
	local lock=$1 dest=$2 src ns type ver plat=linux_amd64 name base stage zip got want
	src=$(sed -n 's/^provider "\([^"]*\)" {$/\1/p' "$lock")
	[[ $src =~ ^registry\.opentofu\.org/([a-z0-9-]+)/([a-z0-9-]+)$ ]] ||
		die "lock: ожидается ровно один провайдер registry.opentofu.org/<ns>/<тип>"
	ns=${BASH_REMATCH[1]} type=${BASH_REMATCH[2]}
	ver=$(sed -n 's/^  version *= *"\([^"]*\)"$/\1/p' "$lock")
	[[ $ver =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "lock: нет версии провайдера вида X.Y.Z"
	name=terraform-provider-${type}_${ver}_${plat}.zip
	base=https://github.com/$ns/terraform-provider-$type/releases/download/v$ver
	stage="$(dirname -- "$dest")/.mirror-stage"
	zip="$stage/m/registry.opentofu.org/$ns/$type/$name"
	rm -rf -- "$stage"
	(umask 077 && mkdir -p -- "${zip%/*}") || die "каталог зеркала провайдера"
	mirror_get "$base/$name" "$zip" 134217728 || mirror_fail "не скачался архив провайдера: $base/$name"
	mirror_get "$base/terraform-provider-${type}_${ver}_SHA256SUMS" "$stage/sums" 1048576 || mirror_fail "не скачался SHA256SUMS провайдера"
	got=$(sha256sum -- "$zip" | awk '{print $1}')
	want=$(awk -v n="$name" '$2 == n {print $1}' "$stage/sums")
	[[ $got =~ ^[0-9a-f]{64}$ && $got == "$want" ]] || mirror_fail "sha256 архива провайдера не совпал со строкой SHA256SUMS"
	grep -qF "\"zh:$got\"" "$lock" || mirror_fail "sha256 архива провайдера нет среди zh: в lock"
	# Замена без окна, в котором зеркала нет: прежнее — в .old, новое — на место, при неудаче .old возвращается.
	rm -rf -- "$dest.old"
	if [ -e "$dest" ]; then mv -- "$dest" "$dest.old" || mirror_fail "прежнее зеркало провайдера не отодвинуто"; fi
	if ! mv -- "$stage/m" "$dest"; then
		if [ -e "$dest.old" ]; then mv -- "$dest.old" "$dest"; fi
		mirror_fail "зеркало провайдера не установлено"
	fi
	rm -rf -- "$dest.old" "$stage"
	echo "провайдер $ns/$type $ver: архив с GitHub, sha256 совпал с SHA256SUMS и lock — $dest"
}
