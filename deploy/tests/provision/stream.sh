#!/usr/bin/env bash
# Шаг стенда verify_backup_stream (design 002 §11, §12, И4-10, И4-15): поток копии через сокет службы забора, как его
# берёт ключ забора, но от root и без ssh: забор -> age -d тестовым ключом -> zstd -> tar -t -> последний член SA-END.
# Только source из run.sh (после lib.sh, converge.sh, verify.sh, service.sh).
#
#   verify_backup_stream
#     1. Поток расшифровывается тестовым ключом (получатель на время шага подменяется), состав: data/ и conf/,
#        последний член SA-END; подложенная «приманка» секрета Podman в архиве отсутствует и в содержимом не встречается.
#     2. В журнале службы за время шага — ровно одна строка о коде tar и числе файлов, путей данных нет, stdout и stderr
#        не смешаны (поток расшифровался: в него не попала ни одна строка журнала).
#     3. Отказ: drop-in стенда SystemCallFilter=~getdents64 + SystemCallErrorNumber=EIO — tar не читает каталоги,
#        код 2, метки в потоке нет, служба с ошибкой.
#
# Каталог данных: нет (локальный стенд, задачи albums-runtime пропущены) — шаг создаёт его с выдуманными файлами
# (uid 59000, 0600) и удаляет; есть (SA_STAND_FULL=1, его создала роль) — берёт существующий и кладёт файлы в свой
# временный подкаталог с владельцем каталога. Приманка секрета — в доме albums; всё созданное и подменённое
# возвращается при любом выходе. Реальных данных нет. В полном режиме шаг по логике тот же, но на rootful-цели не
# запускался (первый прогон — CI, задание stand-full).
# shellcheck disable=SC2154  # tgt — из run.sh и lib.sh

step_verify_backup_stream() {
	local out rc
	need_target || return 1
	out=$(podman exec -i "$tgt" sh -s 2>&1 <<'SH'
set -u
data=/var/lib/albums-svc/data
house=/home/albums
decoy_dir="$house/.local/share/containers/storage/secrets"
dropin=/etc/systemd/system/sa-backup@.service.d
work=$(mktemp -d /var/tmp/sa-stream-test.XXXXXX) || exit 1
chmod 0755 "$work"
cp -p /etc/sa-backup/recipients "$work/recipients.sav" || exit 1
had_data=0
[ -e "$data" ] && had_data=1
# Верхний из каталогов приманки, которых ещё нет: его (со всем внутри) удалит cleanup.
decoy_top=""
p=$decoy_dir
while [ ! -e "$p" ]; do decoy_top=$p; p=$(dirname "$p"); done
sub=""
cleanup() {
	cp -p "$work/recipients.sav" /etc/sa-backup/recipients
	if [ "$had_data" -eq 0 ]; then rm -rf "$data"; elif [ -n "$sub" ]; then rm -rf "$sub"; fi
	if [ -n "$decoy_top" ]; then rm -rf "$decoy_top"; else rm -f "$decoy_dir/decoy"; fi
	rm -rf "$dropin"
	systemctl daemon-reload
	systemctl reset-failed 'sa-backup@*' 2>/dev/null
	rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "  FAIL verify_backup_stream: $*"; exit 1; }

age-keygen -o "$work/key.txt" 2>/dev/null || fail "age-keygen"
age-keygen -y "$work/key.txt" >/etc/sa-backup/recipients || fail "получатель"
if [ "$had_data" -eq 0 ]; then
	mkdir -p "$data" || fail "mkdir data"
	sub="$data/sa-test"
	owner=59000 group=59000
else
	sub="$data/sa-test.$$"
	owner=$(stat -c %u "$data") group=$(stat -c %g "$data")
fi
mkdir -p "$sub/albums/abc" || fail "mkdir"
printf '{"fake": true}\n' >"$sub/albums/abc/meta.json"
printf 'page' >"$sub/albums/abc/0123.jpg"
# Подкаталог — всегда. Сам data/ — только если его создал шаг (как в бою: владелец и 0700); в полном режиме он
# принадлежит роли, его владельца и права шаг не трогает.
chown -R "$owner:$group" "$sub"
chmod -R go-rwx "$sub"
if [ "$had_data" -eq 0 ]; then
	chown "$owner:$group" "$data"
	chmod go-rwx "$data"
fi
mkdir -p "$decoy_dir" || fail "mkdir decoy"
printf 'TOPSECRETVALUE' >"$decoy_dir/decoy"
subname=${sub#"$data"/}

# take <имя файла>: поток через сокет, как у ключа забора (socat -u UNIX-CONNECT:... STDOUT), от root.
take() { socat -u UNIX-CONNECT:/run/sa-backup/stream.sock STDOUT >"$work/$1"; }
# list <файл потока>: расшифровка тестовым ключом и список членов.
list() { age -d -i "$work/key.txt" <"$work/$1" 2>"$work/age.err" | zstd -dc -q | tar -t --ignore-zeros; }

since=$(date '+%Y-%m-%d %H:%M:%S')
take good.age || fail "забор через сокет не удался"
list good.age >"$work/good.list" || fail "поток не расшифровался и не разобрался (age: $(head -c 200 "$work/age.err"))"
[ "$(tail -n 1 "$work/good.list")" = SA-END ] || fail "последний член не SA-END (нет метки SA-END)"
grep -qx "data/$subname/albums/abc/meta.json" "$work/good.list" || fail "в архиве нет файла из data/"
grep -qx 'conf/config.json' "$work/good.list" || fail "в архиве нет conf/config.json"
if grep -q 'secrets\|decoy' "$work/good.list"; then fail "в архиве приманка секрета Podman (имя)"; fi
if age -d -i "$work/key.txt" <"$work/good.age" | zstd -dc -q | grep -aq TOPSECRETVALUE; then fail "в архиве приманка секрета Podman (содержимое)"; fi
[ "$(grep -c '^SA-END$' "$work/good.list")" -eq 1 ] || fail "SA-END не один"
echo "  ok   verify_backup_stream: поток расшифрован, data/ и conf/ на месте, SA-END последний, приманки секрета нет"

journalctl -u 'sa-backup@*' --since "$since" --no-pager -o cat >"$work/good.journal" 2>&1
[ "$(grep -c '^sa-backup: tar code=' "$work/good.journal")" -eq 1 ] || fail "в журнале за шаг не ровно одна строка «tar code=»: $(tail -n 3 "$work/good.journal")"
grep -q '^sa-backup: tar code=[01] files=[0-9][0-9]*$' "$work/good.journal" || fail "строка журнала не вида «tar code=0|1 files=N»"
if grep -q "$subname\|meta.json\|0123" "$work/good.journal"; then fail "в журнале службы есть пути данных"; fi
echo "  ok   verify_backup_stream: журнал службы — ровно одна строка о коде tar и числе файлов, путей нет"

# Отказ: tar не читает каталоги (EIO) - код 2, метки нет, служба с ошибкой.
mkdir -p "$dropin"
printf '[Service]\nSystemCallFilter=~getdents64\nSystemCallErrorNumber=EIO\n' >"$dropin/fail.conf"
systemctl daemon-reload
since2=$(date '+%Y-%m-%d %H:%M:%S')
take bad.age || true
if list bad.age >"$work/bad.list" 2>/dev/null && [ "$(tail -n 1 "$work/bad.list")" = SA-END ]; then
	fail "при отказе tar в потоке есть метка SA-END"
fi
journalctl -u 'sa-backup@*' --since "$since2" --no-pager -o cat >"$work/bad.journal" 2>&1
[ "$(grep -c '^sa-backup: tar code=2 ' "$work/bad.journal")" -eq 1 ] || fail "в журнале нет ровно одной строки «tar code=2» при отказе tar"
echo "  ok   verify_backup_stream: при отказе tar метки SA-END нет, код 2 в журнале"
SH
	)
	rc=$?
	printf '%s\n' "$out"
	return "$rc"
}
