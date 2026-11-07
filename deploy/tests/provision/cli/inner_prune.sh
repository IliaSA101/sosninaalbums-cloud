#!/usr/bin/env bash
# Продолжение inner.sh (source; check, drive, reset_known, переменные inv, clone, BK, R… — оттуда): чистка копий, свежесть и
# forget-host. Вынесено, чтобы inner.sh укладывался в предел размера.
# shellcheck disable=SC2154,SC2016  # переменные из inner.sh; bash -c '…$1…': раскрытие внутри

# --- чистка и свежесть -----------------------------------------------------------------------------------
stamp() { date -u -d "$1 days ago" +%Y%m%dT%H%M%SZ.tar.zst.age; }
prune() { SA_BACKUP_DIR=$1 bash "$clone/deploy/ops/sa-backup-prune.sh" >/tmp/prune.txt 2>&1; }
warns() { grep -q 'проверенной копии младше 7 дней нет' "$1"; }
rm -rf "$BK"-a && mkdir "$BK"-a && touch "$BK-a/$(stamp 31)"
prune "$BK"-a
check "чистка: единственная копия старше 30 дней удалена" bash -c '! ls $BK-a/*.age 2>/dev/null | grep -q .'
check "чистка: после удаления — предупреждение (копий нет)" warns /tmp/prune.txt
rm -rf "$BK"-b && mkdir "$BK"-b && touch "$BK-b/$(stamp 29)" "$BK-b/$(stamp 2)" "$BK"-b/other.txt
touch -d '2 days ago' "$BK"-b/old.tar.zst.age.part
touch "$BK"-b/new.tar.zst.age.part
prune "$BK"-b
check "чистка: 29 дней и свежая остались" bash -c '[ "$(ls $BK-b/*Z.tar.zst.age | wc -l)" = 2 ]'
check "чистка: .part старше суток удалён, свежий и чужой файл целы" bash -c '[ ! -e $BK-b/old.tar.zst.age.part ] && [ -e $BK-b/new.tar.zst.age.part ] && [ -e $BK-b/other.txt ]'
check "свежесть: копия моложе 7 дней — предупреждения нет" bash -c '! grep -q "проверенной копии младше 7 дней нет" /tmp/prune.txt'
check "чистка: файл с неожиданным именем — предупреждение, файл цел" bash -c 'grep -q "неожиданным именем" /tmp/prune.txt && [ -e $BK-b/other.txt ]'
rm -rf "$BK"-c && mkdir "$BK"-c && touch "$BK-c/$(stamp 8)"
prune "$BK"-c
check "свежесть: новейшая старше 7 дней — предупреждение (таймер)" warns /tmp/prune.txt
for d in "9:warn" "3:nowarn" "none:warn"; do
	rm -rf "$BK"-d && mkdir "$BK"-d
	[ "${d%%:*}" = none ] || touch "$BK-d/$(stamp "${d%%:*}")"
	reset_known
	drive "/tmp/sc.yes" SA_BACKUP_DIR="$BK"-d -- forget-host
	if [ "${d#*:}" = warn ]; then check "свежесть в provision.sh (${d%%:*}): предупреждение" warns /tmp/out.txt
	else check "свежесть в provision.sh (${d%%:*}): предупреждения нет" bash -c '! grep -q "проверенной копии младше 7 дней нет" /tmp/out.txt'; fi
done
# Ничего не раскатано (deployed-commit нет): копий быть не может, предупреждение было бы ложным.
rm -rf "$BK"-e && mkdir "$BK"-e
mv "$inv/deployed-commit" /tmp/deployed-commit.keep
reset_known
drive "/tmp/sc.yes" SA_BACKUP_DIR="$BK"-e -- forget-host
check "свежесть в provision.sh: раскатки не было (нет deployed-commit) — предупреждения нет" bash -c '! grep -q "проверенной копии младше 7 дней нет" /tmp/out.txt'
mv /tmp/deployed-commit.keep "$inv/deployed-commit"
check "forget-host: ключ хоста удалён из known_hosts" bash -c '! grep -q "^127.0.0.1 " "$1"' _ "$inv/known_hosts"
