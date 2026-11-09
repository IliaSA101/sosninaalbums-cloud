#!/bin/bash
# Разбор и распаковка копии при возврате (design 002 §11, решения 002-14, И4-3, И4-11). Запускает Ansible с become
# (пароль из vault; NOPASSWD нет) от root:
#
#   sa-restore <файл.tar.zst> <uid> <gid> <предел_байт> <каталог_рядом_с_data> [запас_байт]
#
# Файл — уже расшифрованный поток (age снят в контроллере), в каталоге администратора, вне data/. От root скрипт
# только создаёт временный каталог рядом с data/ (umask 077, chown на uid сервиса), открывает файл и отдаёт
# его дескриптором 3. Всё, что разбирает недоверенный поток (zstd, tar), идёт от uid сервиса через setpriv:
# age не удостоверяет отправителя, копию может собрать любой, у кого есть открытый ключ владельца. Работник
# запускается только с uid из диапазона subuid пользователя albums (защита в глубину: не root и не чужой uid).
#
# Успех: код 0, последняя строка вывода — каталог с распакованными data/ и conf/ (подмена data/, verify и
# after-restore — плей возврата, этап 7). Отказ: код 1, причина в stderr, временный каталог удалён, data/ не тронут.
# conf/ из копии в действующий /etc/albums-svc НЕ возвращается: конфиг — только из inventory (иначе подложенный
# conf/config.json с чужим sha256 дал бы токен загрузки); скрипт других каталогов, кроме временного, не пишет.
#
# Окружение (необязательно): SA_RESTORE_MAX_MEMBERS — предел числа членов (по умолчанию 500000),
# SA_RESTORE_SUBUID_FILE и SA_RESTORE_SUBGID_FILE — файлы subuid и subgid вместо /etc/subuid и /etc/subgid
# (проверки стенда).
#
# Правила разбора помечены «# rule:<имя>»: проверка verify_restore_rejects ослабляет скрипт по одному правилу
# (правка по пометке) и требует, чтобы соответствующий плохой архив перестал отвергаться.
set -u -o pipefail
umask 077

die() {
	echo "sa-restore: отказ: $*" >&2
	exit 1
}

# --- Работник: от uid сервиса, stdin не используется, поток — fd 3 ---------------------------------------------
worker() {
	local tmp=$1 max=$2 reserve=$3 parent=$4 maxm=$5 cap size avail
	cap=$((max + max / 16 + 1048576))
	cd "$tmp" || die "нет временного каталога"

	# Одна копия сжатого потока: разбор и распаковка читают один и тот же файл (не гонка с подменой).
	head -c $((cap + 1)) <&3 >in.zst || die "чтение копии"
	size=$(wc -c <in.zst)
	[ "$size" -le "$cap" ] || die "сжатый поток длиннее предела"  # rule:limit
	[ "$size" -gt 0 ] || die "пустой поток"

	# Распакованный поток длиннее предела — отказ (zstd получит SIGPIPE от head, поэтому без pipefail).
	size=$(
		set +o pipefail
		zstd -dc -q in.zst 2>/dev/null | head -c $((max + 1)) | wc -c
	)
	[ "$size" -le "$max" ] || die "поток длиннее предела $max байт"  # rule:limit

	# Разбор целиком до первой записи на диск: только обычные файлы и каталоги под data/ и conf/, последний член —
	# SA-END. --quoting-style=escape: перевод строки в имени не подделывает строку списка. Строка списка — ровно шесть
	# полей (права, владелец, размер, дата, время, имя): огромный mtime в pax-заголовке печатается одним числом и
	# сдвинул бы первое слово имени в «дату»; имя с пробелом тоже даёт не шесть полей. Каждый член весит 4096 плюс
	# размер, округлённый вверх до 4096: пустой каталог занимает inode и блок, а размер у него 0.
	zstd -dc -q in.zst 2>/dev/null | head -c $((max + 1)) |
		tar --list --verbose --numeric-owner --ignore-zeros --quoting-style=escape 2>/dev/null |
		awk -v max="$max" -v maxm="$maxm" -v maxd=8 '
		function bad(msg) { print "sa-restore: отказ: " msg > "/dev/stderr"; failed = 1; exit 1 }
		{
			count++
			if (count > maxm) bad("слишком много членов архива")  # rule:count
			type = substr($1, 1, 1)
			if (type != "-" && type != "d") bad("в архиве не только файлы и каталоги (символическая или жёсткая ссылка, устройство)")  # rule:type
			if (NF != 6) bad("строка списка не из шести полей")  # rule:fields
			if ($4 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/ || $5 !~ /^[0-9][0-9]:[0-9][0-9]$/) bad("дата или время не в обычном виде")  # rule:fields
			size = $3 + 0
			name = $6
			if (ended) bad("после SA-END есть ещё члены")  # rule:marker
			if (name !~ /^[A-Za-z0-9._\/-]+$/) bad("недопустимые знаки в имени")  # rule:charset
			if (substr(name, 1, 1) == "/") bad("абсолютный путь")  # rule:abs
			n = split(name, parts, "/")
			if (n > maxd) bad("слишком глубокий путь")  # rule:depth
			if (length(name) > 255) bad("слишком длинное имя")  # rule:namelen
			for (i = 1; i <= n; i++) if (parts[i] == "..") bad("компонент .. в пути")  # rule:dotdot
			if (name == "SA-END") { if (type != "-") bad("SA-END не файл"); ended = 1 }
			else if (name !~ /^(data|conf)(\/|$)/) bad("путь вне data/ и conf/")  # rule:prefix
			total += 4096 + int((size + 4095) / 4096) * 4096
			# Неявные каталоги: tar создаёт все промежуточные каталоги пути, хотя в списке одна строка. Каждый новый
			# префикс весит как член (4096) и считается в числе членов.
			last = n; if (parts[n] == "") last = n - 1
			for (i = 1; i < last; i++) { pre = (i == 1) ? parts[1] : pre "/" parts[i]; if (!(pre in seen)) { seen[pre] = 1; total += 4096; count++ } }  # rule:implicit
			if (total > max) bad("сумма размеров больше предела")  # rule:sum
		}
		END {
			if (failed) exit 1
			if (count > maxm) bad("слишком много членов архива")  # rule:count
			if (!ended) bad("нет метки конца SA-END (копия неполная)")  # rule:marker
			print total
		}' >total.txt || die "разбор архива"
	read -r size <total.txt
	[ -n "$size" ] || die "разбор не дал суммы"

	# Место: распакованное плюс запас должно поместиться (хуже всего — копия на тот же раздел, что и data/).
	avail=$(df --output=avail -B1 "$parent" | tail -n 1 | tr -d ' ')
	[ $((size + reserve)) -le "$avail" ] || die "не хватает места: нужно $((size + reserve)), свободно $avail"  # rule:space

	mkdir new || die "mkdir"
	zstd -dc -q in.zst 2>/dev/null | head -c $((max + 1)) |
		tar --extract --ignore-zeros --no-same-owner --no-same-permissions --no-overwrite-dir -C new 2>/dev/null || die "распаковка"
	rm -f -- new/SA-END in.zst total.txt
}

if [ "${1:-}" = --worker ]; then
	shift
	worker "$@"
	exit 0
fi

# --- Корень: аргументы, диапазон uid, каталог, запуск работника -------------------------------------------------
if [ $# -lt 5 ] || [ $# -gt 6 ]; then
	echo "usage: sa-restore <файл> <uid> <gid> <предел_байт> <каталог> [запас_байт]" >&2
	exit 2
fi
incoming=$1 uid=$2 gid=$3 max=$4 parent=$5 reserve=${6:-1073741824}
maxm=${SA_RESTORE_MAX_MEMBERS:-500000}
subuid_file=${SA_RESTORE_SUBUID_FILE:-/etc/subuid}
subgid_file=${SA_RESTORE_SUBGID_FILE:-/etc/subgid}
for v in "$uid" "$gid" "$max" "$reserve" "$maxm"; do
	[[ $v =~ ^[0-9]{1,15}$ ]] || die "uid, gid, предел, запас и число членов — числа"
done
# Десятичное значение явно: «059000» в $(( )) и в [[ -gt ]] читалось бы как восьмеричное.
uid=$((10#$uid)) gid=$((10#$gid)) max=$((10#$max)) reserve=$((10#$reserve)) maxm=$((10#$maxm))
[ "$max" -gt 0 ] || die "предел должен быть больше нуля"
[ "$maxm" -gt 0 ] || die "предел числа членов должен быть больше нуля"
{ [ -f "$incoming" ] && [ ! -L "$incoming" ]; } || die "файл копии не обычный файл"
{ [ -d "$parent" ] && [ ! -L "$parent" ]; } || die "каталог рядом с data/ не обычный каталог"

# Работник — только uid и gid из диапазонов subuid и subgid пользователя albums: не root и не чужой пользователь.
if [ "$uid" -le 0 ] || [ "$gid" -le 0 ]; then die "работник не может быть root"; fi  # rule:root
range_of() { awk -F: '$1 == "albums" && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { print $2 " " $3; exit }' "$1" 2>/dev/null; }
range=$(range_of "$subuid_file")
[ -n "$range" ] || die "в $subuid_file нет диапазона subuid пользователя albums"
read -r sub_start sub_count <<<"$range"
sub_start=$((10#$sub_start)) sub_end=$((10#$sub_start + 10#$sub_count))
if [ "$uid" -lt "$sub_start" ] || [ "$uid" -ge "$sub_end" ]; then die "uid работника вне диапазона subuid albums"; fi  # rule:range_uid
range=$(range_of "$subgid_file")
[ -n "$range" ] || die "в $subgid_file нет диапазона subgid пользователя albums"
read -r sub_start sub_count <<<"$range"
sub_start=$((10#$sub_start)) sub_end=$((10#$sub_start + 10#$sub_count))
if [ "$gid" -lt "$sub_start" ] || [ "$gid" -ge "$sub_end" ]; then die "gid работника вне диапазона subgid albums"; fi  # rule:range_gid

self=$(readlink -f -- "$0")
tmp=$(mktemp -d "$parent/.restore.XXXXXX") || die "временный каталог"
ok=0
cleanup() { [ "$ok" -eq 1 ] || rm -rf -- "$tmp"; }
trap cleanup EXIT
chown "$uid:$gid" "$tmp" || die "владелец временного каталога"
chmod 0700 "$tmp" || die "права временного каталога"

# Файл открывает root (каталог администратора 0700, uid сервиса его не прочтёт); работник получает дескриптор.
setpriv --reuid="$uid" --regid="$gid" --clear-groups --no-new-privs --inh-caps=-all \
	bash "$self" --worker "$tmp" "$max" "$reserve" "$parent" "$maxm" 3<"$incoming" || die "работник вернул ошибку"
[ -d "$tmp/new" ] || die "нет результата распаковки"
ok=1
echo "$tmp/new"
