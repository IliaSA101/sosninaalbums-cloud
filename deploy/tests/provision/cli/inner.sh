#!/usr/bin/env bash
# provision_cli внутри контейнера (design 002 §12): provision.sh из клона (bundle → git clone) под
# учётной записью saops без сети (верные /etc/sa-ops-machine и /etc/machine-id смонтированы снаружи, design
# §16.8). Поддельные podman, tofu, ssh, loginctl, less (cli/shim); настоящие age, ssh-agent, ssh-add, ssh-keygen,
# git, zstd, tar. Фразы и метки — fixtures/values.env. Печатает ok/FAIL, код — число провалов.
# shellcheck source-path=SCRIPTDIR disable=SC1091,SC2016  # bash -c '…$1…': раскрытие нужно внутри
set -uo pipefail

R=/ref/deploy/tests/provision/cli
# shellcheck source=fixtures/values.env
. "$R/fixtures/values.env"
. "$R/inner_image.sh"
. "$R/inner_probe.sh"
export PATH="$R/shim:$PATH" SHIM_LOG=/tmp/shim.log SHIM_PS=/tmp/shim.ps SHIM_OUT=/tmp/shim-out
mkdir -p "$SHIM_OUT"
: >"$SHIM_LOG"
: >"$SHIM_PS"
fails=0
ok() { echo "ok   $1"; }
bad() {
	echo "FAIL $1"
	fails=$((fails + 1))
}
check() { # <имя> <команда…>: команда истинна — ok
	local n=$1
	shift
	if "$@"; then ok "$n"; else bad "$n"; fi
}
gitc() { git -c user.name=t -c user.email=t@example.org "$@"; }

inv=$HOME/inventory
export BK=$HOME/bk
clone=$HOME/src/sosninaalbums-cloud
prov=$clone/deploy/provision.sh
base=(XDG_SESSION_ID=1 XDG_RUNTIME_DIR=/run/user/1000 SA_OPS_USER=saops SA_INVENTORY="$inv" SA_SECRETS="$HOME/secrets.age"
	SA_BACKUP_DIR="$BK" SA_WAIT_SSH_SEC=5)

# --- клон из bundle, inventory, ключи, секреты ----------------------------------------------------------
mkdir -p "$HOME/origin" "$HOME/.ssh" "$BK" # $HOME/src создаёт review.sh сам (ниже проверяется его umask)
chmod 700 "$HOME" "$HOME/.ssh"
# Копия кода в «origin» — без кэша python и *.pyc: клон, который проверяет review.sh, обязан состоять из исходников.
tar -C /ref --exclude=__pycache__ --exclude='*.pyc' -cf - deploy | tar -C "$HOME/origin" -xf -
mkdir -p "$HOME/origin/web/viewer"
cp /ref/web/viewer/notfound.html "$HOME/origin/web/viewer/"
image_origin_files "$HOME/origin"
gitc -C "$HOME/origin" init -q -b main
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m init
gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle main 2>/dev/null
printf 'expect yes/no\nsend yes\n' >/tmp/sc.yes
# Просмотр до checkout (И2-1): клон создаёт и переводит на коммит сам review.sh; первый раз — от пустого дерева.
review() { # <ветка> [env…]: под pty, «yes» по сценарию; вывод — /tmp/review.txt
	local br=$1
	shift
	python3 -I "$R/pty_drive.py" /tmp/sc.yes "$R/fixtures/values.env" -- env SA_INBOX=/srv/sa-inbox SA_INVENTORY="$inv" SA_OPS_USER=saops "$@" \
		bash /ref/deploy/ops/review.sh "$br" >/tmp/review.txt 2>&1
}
no_leftovers() { ! compgen -G "$HOME/.review.*" >/dev/null && [ ! -e "$HOME/in.bundle" ]; }
# Ubuntu: у обычных пользователей umask 002 (USERGROUPS_ENAB); без umask 077 в review.sh ~/src и клон вышли бы 775, и
# owned_safe отказывал бы следующему запуску. Каталога ~/src до первого просмотра нет — его создаёт сам скрипт.
[ ! -e "$HOME/src" ] || bad "стенд: $HOME/src уже есть до первого просмотра"
(umask 002 && review main)
review_rc=$?
# Ранний явный провал с причиной: без него отказ просмотра кода (например, невидимый или управляющий символ в файле deploy/)
# виден только по цепочке следствий («нет less.txt», «HEAD не найден»). Печатаются хвост вывода review.sh и, если причина
# в невидимых символах, файл и строка (чистые коды юникода: сам файл теста остаётся в белом списке).
if [ "$review_rc" != 0 ]; then
	bad "review: первый просмотр кода отказал (код $review_rc) — остальные провалы ниже его следствие; причина:"
	tail -n 5 /tmp/review.txt
	LC_ALL=C.UTF-8 grep -rnP '[\x{00}-\x{08}\x{0B}\x{0C}\x{0E}-\x{1F}\x{7F}-\x{9F}\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2069}\x{FEFF}]' \
		/ref/deploy /ref/web /ref/.github 2>/dev/null | cut -c1-120 | head -n 5 | sed 's/^/  подозрительный символ: /'
fi
check "review: первый просмотр прошёл, клон создан" test -d "$clone/.git"
check "review: при umask 002 ~/src и клон созданы с правами 700" bash -c '[ "$(stat -c %a "$1" "$2" | tr "\n" " ")" = "700 700 " ]' _ "$HOME/src" "$clone"
check "review: diff от пустого дерева непустой и содержит весь код" grep -q 'diff --git a/deploy/ops/guard.sh' /tmp/shim-out/less.txt
check "review: просмотр в less без R и с LESSSECURE" bash -c 'grep -q "LESSSECURE=1" /tmp/shim-out/less.env && ! grep -Eq "^LESS=[^ ]*[Rr]" /tmp/shim-out/less.env'
check "review: после «yes» HEAD отсоединён, дерево чистое" bash -c 'cd "$1" && ! git symbolic-ref -q HEAD >/dev/null && [ -z "$(git status --porcelain)" ]' _ "$clone"
head0=$(git -C "$clone" rev-parse HEAD)
check "review: копия bundle и временный каталог (скрытый .review.*) убраны из дома" no_leftovers

mkdir -p "$inv/tofu" "$inv/ansible"
printf 'SA_DOMAIN=host.example.org\nSA_ADMIN_USER=admin-example\n' >"$inv/provision.env"
printf 'api_url = "https://api.invalid"\nproject_id = "p"\n' >"$inv/tofu/clo.tfvars"
printf 'all:\n  hosts:\n    server:\n      ansible_host: %s\n' "$CLI_IP" >"$inv/ansible/hosts.yml"
printf '%s\n' "$head0" >"$inv/deployed-commit"
image_seed "$head0"
# Второй просмотр: diff от развёрнутого коммита; плохие коммиты — отказ до checkout.
newbundle() { gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle main 2>/dev/null; }
printf 'second-pass-line\n' >"$HOME/origin/deploy/newfile.txt"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m second
newbundle
(umask 002 && review main)
check "review: второй просмотр (umask 002) проходит owned_safe — клон и ~/src не 775" bash -c '! grep -q "запись группе" /tmp/review.txt'
check "review: второй просмотр — diff от развёрнутого, только новое" bash -c 'grep -q second-pass-line /tmp/shim-out/less.txt && ! grep -q "diff --git a/deploy/ops/guard.sh" /tmp/shim-out/less.txt'
check "review: клон на новом коммите" bash -c '[ "$(git -C "$1" rev-parse HEAD)" != "$2" ]' _ "$clone" "$head0"
chmod g+w "$HOME/src"
review main
check "review: запись группе на ~/src — отказ с подсказкой chmod go-w и этим путём" bash -c '[ "$1" = 3 ] && grep -qF "chmod go-w $2" /tmp/review.txt' _ "$?" "$HOME/src"
chmod go-w "$HOME/src"
git -C "$clone" checkout -q --detach "$head0"
rm -f "$HOME/origin/deploy/newfile.txt"
printf 'a\033b\n' >"$HOME/origin/deploy/bad.txt"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m badctl
newbundle
review main
rc=$?
check "review: управляющий символ — отказ до checkout" bash -c '[ "$1" = 3 ] || exit 1; [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$rc" "$clone" "$head0"
printf 'a\xe2\x80\xaeb\n' >"$HOME/origin/deploy/bad.txt"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m badbidi
newbundle
review main
check "review: символ смены направления (bidi) — отказ" bash -c '[ "$1" = 3 ]' _ "$?"
printf 'a\xc2\x85b\n' >"$HOME/origin/deploy/bad.txt"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m badc1
newbundle
review main
check "review: управляющий символ C1 — отказ" bash -c '[ "$1" = 3 ]' _ "$?"
ln -s code.bundle /srv/sa-inbox/rlink.bundle
review main SA_SRC_BUNDLE=/srv/sa-inbox/rlink.bundle
check "review: bundle — симлинк — отказ" bash -c '[ "$1" = 3 ]' _ "$?"
review no-such-branch
check "review: нет такой ветки — ошибка, клон не тронут" bash -c '[ "$1" != 0 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
touch /tmp/out.bundle
review main SA_SRC_BUNDLE=/tmp/out.bundle
check "review: bundle вне каталога входящих — отказ" bash -c '[ "$1" = 3 ]' _ "$?"
review -x
check "review: ветка с ведущим «-» — отказ" bash -c '[ "$1" = 3 ]' _ "$?"
# Отдельные ветки от развёрнутого коммита: битый объект, изменённый .gitattributes, симлинк в дереве.
empty=$(gitc -C "$HOME/origin" hash-object -t tree -w /dev/null)
badc=$(printf 'tree %s\nauthor A U Thor 0 +0000\ncommitter A U Thor 0 +0000\n\nbroken\n' "$empty" | gitc -C "$HOME/origin" hash-object -t commit -w --literally --stdin)
gitc -C "$HOME/origin" update-ref refs/heads/broken "$badc"
gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle broken 2>/dev/null
review broken
check "review: битый объект — fsck --strict, клон не тронут, временный репозиторий убран" bash -c '[ "$1" = 1 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ] && ! ls -d "$HOME"/.review.* 2>/dev/null | grep -q . && [ ! -e "$HOME/in.bundle" ] && ! git -C "$2" cat-file -e "$4" 2>/dev/null' _ "$?" "$clone" "$head0" "$badc"
gitc -C "$HOME/origin" checkout -q -b attrs "$head0"
printf '* -text\n' >"$HOME/origin/deploy/.gitattributes"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m attrs
gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle attrs 2>/dev/null
review attrs
check "review: изменён .gitattributes — отказ до checkout" bash -c '[ "$1" = 3 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
gitc -C "$HOME/origin" checkout -q -b lnk "$head0"
ln -s /etc/passwd "$HOME/origin/deploy/ops/evil-link"
gitc -C "$HOME/origin" add -A
gitc -C "$HOME/origin" commit -q -m lnk
gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle lnk 2>/dev/null
review lnk
check "review: симлинк в раскатываемых путях — отказ до checkout" bash -c '[ "$1" = 3 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
gitc -C "$HOME/origin" checkout -q main
ssh-keygen -q -t ed25519 -N "$PHRASE_SSH" -f "$HOME/.ssh/id_server"
ssh-keygen -q -t ed25519 -N "$PHRASE_BACKUP" -f "$HOME/.ssh/id_backup"
reset_known() { printf '%s %s\n' "$CLI_IP" "$(cut -d' ' -f1,2 "$HOME/.ssh/id_server.pub")" >"$inv/known_hosts"; }
reset_known

# Гипотеза П2a-3 (ssh-add): SSH_ASKPASS_REQUIRE=force и унаследованный дескриптор 3 доходят до помощника.
eval "$(ssh-agent -s)" >/dev/null
SSH_ASKPASS_REQUIRE=force SSH_ASKPASS=/ref/deploy/ops/phrase-fd.sh ssh-add "$HOME/.ssh/id_server" 3< <(printf '%s' "$PHRASE_SSH") </dev/null >/tmp/h2.txt 2>&1
check "П2a-3: ssh-add передаёт дескриптор 3 помощнику phrase-fd.sh (SSH_ASKPASS_REQUIRE=force)" grep -q 'Identity added' /tmp/h2.txt
ssh-agent -k >/dev/null 2>&1

age-keygen -o /plain/age.id 2>/dev/null
ident=$(grep '^AGE-SECRET-KEY' /plain/age.id)
pub=$(age-keygen -y /plain/age.id)
(umask 077 && printf 'clo_token=%s\nvault_pass=%s\nssh_key_phrase=%s\nbackup_key_phrase=%s\nage_identity=%s\n' \
	"$MARKER" "$VAULT" "$PHRASE_SSH" "$PHRASE_BACKUP" "$ident" >/plain/s.txt)
printf 'expect Enter passphrase \\(leave\nsend $PHRASE_SECRETS\nexpect Confirm passphrase\nsend $PHRASE_SECRETS\n' >/tmp/sc.make
python3 -I "$R/pty_drive.py" /tmp/sc.make "$R/fixtures/values.env" -- sh -c 'age -p -o "$HOME/secrets.age" </plain/s.txt' >/dev/null
chmod 600 "$HOME/secrets.age"
rm -f /plain/s.txt /plain/age.id
check "секреты: файл age создан" test -s "$HOME/secrets.age"

# Потоки копий: с меткой конца и без неё (метка — второй архив, как у sa-backup-stream).
mkdir -p /tmp/s/data
echo fixture >/tmp/s/data/f.txt
echo files=1 >/tmp/s/SA-END
tar -C /tmp/s -cf - data | zstd -q | age -r "$pub" >/tmp/nomark.stream
{ tar -C /tmp/s -cf - data; tar -C /tmp/s -cf - SA-END; } | zstd -q | age -r "$pub" >/tmp/good.stream

# --- запуск ----------------------------------------------------------------------------------------------
run_plain() { # <env…> -- <команда>: без терминала (отказы наступают до всех вопросов)
	local envs=()
	while [ "$1" != -- ]; do
		envs+=("$1")
		shift
	done
	shift
	env "${base[@]}" "${envs[@]}" bash "$prov" "$@" >/tmp/out.txt 2>&1
}
want_rc() { # <имя> <код> <env…> -- <команда>
	local n=$1 want=$2 rc
	shift 2
	run_plain "$@"
	rc=$?
	if [ "$rc" = "$want" ]; then ok "$n"; else
		bad "$n: код $rc, ждали $want"
		tail -n 4 /tmp/out.txt
	fi
}
drive() { # <сценарий> <env…> -- <команда>: под pty, фраза и yes по сценарию
	local sc=$1 envs=()
	shift
	while [ "$1" != -- ]; do
		envs+=("$1")
		shift
	done
	shift
	python3 -I "$R/pty_drive.py" "$sc" "$R/fixtures/values.env" -- env "${base[@]}" "${envs[@]}" bash "$prov" "$@" >/tmp/out.txt 2>&1
	local rc=$?
	cat /tmp/out.txt >>/tmp/all.out
	return "$rc"
}
printf 'expect yes/no\nsend yes\nexpect Enter passphrase:\nsend $PHRASE_SECRETS\n' >/tmp/sc.full
printf 'expect yes/no\nsend yes\n' >/tmp/sc.yes
# apply: просмотр кода, фраза секретов и вопрос «Применить именно этот план?» (ops/plan.sh).
printf 'expect yes/no\nsend yes\nexpect Enter passphrase:\nsend $PHRASE_SECRETS\nexpect yes/no\nsend yes\n' >/tmp/sc.apply
drive_rc() { # <имя> <код> <сценарий> <env…> -- <команда>
	local n=$1 want=$2 rc
	shift 2
	drive "$@"
	rc=$?
	if [ "$rc" = "$want" ]; then ok "$n"; else
		bad "$n: код $rc, ждали $want"
		tail -n 6 /tmp/out.txt
	fi
}

# --- коды 3 (design §2) ---------------------------------------------------------------------------------
want_rc "отказ: не та учётная запись" 3 SA_OPS_USER=sa-ops-other -- up
chmod 755 "$HOME"
want_rc "отказ: дом не 0700" 3 -- up
chmod 700 "$HOME"
# Отказы по DISPLAY, WAYLAND_DISPLAY и «сессия не tty» сняты решением владельца (ВР-6): в ops-ВМ графика разрешена.
# Вместо них — положительные случаи сессии и отказы по метке машины и процессам (inner_ext.sh).
want_rc "отказ: сессия чужого uid" 3 FAKE_USER=0 -- up
want_rc "отказ: сессия удалённая" 3 FAKE_REMOTE=yes -- up
want_rc "отказ: чужой XDG_RUNTIME_DIR" 3 XDG_RUNTIME_DIR=/tmp -- up
chmod 755 /run/user/1000
want_rc "отказ: XDG_RUNTIME_DIR не 0700" 3 -- up
chmod 700 /run/user/1000
want_rc "отказ: CLAUDECODE" 3 CLAUDECODE=1 -- up
want_rc "отказ: токен в TF_VAR" 3 TF_VAR_clo_api_token=x -- up
want_rc "отказ: токен в CLO_API_AUTH_TOKEN" 3 CLO_API_AUTH_TOKEN=x -- up
want_rc "отказ: TF_LOG" 3 TF_LOG=debug -- up
want_rc "отказ: TF_LOG_PATH" 3 TF_LOG_PATH=/tmp/x -- up
want_rc "отказ: ANSIBLE_KEEP_REMOTE_FILES" 3 ANSIBLE_KEEP_REMOTE_FILES=1 -- up
want_rc "отказ: ANSIBLE_*WORLD_READABLE*" 3 ANSIBLE_SHELL_ALLOW_WORLD_READABLE_TEMP=1 -- up
want_rc "отказ: неизвестная команда" 3 -- frobnicate
want_rc "отказ: лишний аргумент" 3 -- apply --bogus
touch "$clone/stray.txt"
want_rc "отказ: грязное дерево" 3 -- up
rm -f "$clone/stray.txt"
git -C "$clone" checkout -q -b attached
want_rc "отказ: HEAD не отсоединён" 3 -- up
git -C "$clone" checkout -q --detach
echo /nonexistent >"$clone/.git/objects/info/alternates"
want_rc "отказ: alternates" 3 -- up
rm -f "$clone/.git/objects/info/alternates"
git -C "$clone" config transfer.fsckObjects false
want_rc "отказ: fsck выключен" 3 -- up
git -C "$clone" config transfer.fsckObjects true
mkdir -p "$clone/deploy/probe"
printf 'x\n' >"$clone/deploy/probe/.gitattributes"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m attrs
want_rc "отказ: изменён .gitattributes" 3 -- up
git -C "$clone" reset -q --hard "$head0"
printf 'a\033b\n' >"$clone/deploy/probe.txt"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m ctl
want_rc "отказ: управляющий символ в deploy/" 3 -- up
git -C "$clone" reset -q --hard "$head0"
printf 'a\xe2\x80\xaeb\n' >"$clone/deploy/probe.txt"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m bidi
want_rc "отказ: символ смены направления в deploy/" 3 -- up
git -C "$clone" reset -q --hard "$head0"
printf 'a\xc2\x85b\n' >"$clone/web/viewer/notfound.html"
gitc -C "$clone" commit -q -a -m c1
want_rc "отказ: управляющий символ C1 в странице 404" 3 -- up
git -C "$clone" reset -q --hard "$head0"
for zw in '\xe2\x80\x8b' '\xef\xbb\xbf' '\xe2\x81\xa0' '\xd8\x9c' '\xc2\xad'; do
	printf 'a%bb\n' "$zw" >"$clone/deploy/probe.txt"
	gitc -C "$clone" add -A
	gitc -C "$clone" commit -q -m zw
	want_rc "отказ: невидимый символ ${zw//\\/} в deploy/" 3 -- up
	git -C "$clone" reset -q --hard "$head0"
done
ln -s /etc/passwd "$clone/deploy/ops/evil-link"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m symlink
want_rc "отказ: симлинк в deploy/ops/" 3 -- up
git -C "$clone" reset -q --hard "$head0"
rm -f "$clone/web/viewer/notfound.html"
ln -s /etc/passwd "$clone/web/viewer/notfound.html"
gitc -C "$clone" commit -q -a -m nf-symlink
want_rc "отказ: симлинк вместо notfound.html" 3 -- up
git -C "$clone" reset -q --hard "$head0"
mkdir -p "$clone/deploy/tofu/clo/sub"
printf 'x\n' >"$clone/deploy/tofu/clo/sub/m.tf"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m tofusub
drive_rc "отказ: лишний файл в deploy/tofu/clo (apply)" 3 /tmp/sc.full -- apply
git -C "$clone" reset -q --hard "$head0"
chmod g+w "$inv"
want_rc "отказ: inventory доступен группе на запись" 3 -- up
chmod 755 "$inv"
chmod 777 "$HOME/bk"
want_rc "отказ: каталог копий доступен всем на запись" 3 -- up
chmod 755 "$HOME/bk"
want_rc "отказ: родитель SA_BACKUP_DIR доступен всем на запись (/tmp)" 3 SA_BACKUP_DIR=/tmp/bk -- up
want_rc "отказ: файл секретов чужой (владелец root)" 3 SA_SECRETS=/etc/hostname -- up
want_rc "отказ: SA_DOMAIN не домен" 3 'SA_DOMAIN=x;y' -- up
want_rc "отказ: SA_INVENTORY внутри git-дерева" 3 SA_INVENTORY="$clone/inv" -- up
want_rc "отказ: SA_BACKUP_DIR внутри git-дерева" 3 SA_BACKUP_DIR="$clone/bk" -- up
want_rc "отказ: SA_SECRETS внутри git-дерева" 3 SA_SECRETS="$clone/s.age" -- up
chmod 644 "$HOME/secrets.age"
want_rc "отказ: файл секретов 0644" 3 -- up
chmod 600 "$HOME/secrets.age"
want_rc "отказ: нет файла секретов" 3 SA_SECRETS="$HOME/none.age" -- up
printf 'token = "x"\n' >>"$inv/tofu/clo.tfvars"
want_rc "отказ: token в *.tfvars" 3 -- up
printf 'api_url = "https://api.invalid"\nproject_id = "p"\n' >"$inv/tofu/clo.tfvars"
. "$R/inner_ext.sh"
. "$R/inner_prepare.sh"
. "$R/inner_hosts.sh"
# --- код 2 -----------------------------------------------------------------------------------------------
want_rc "код 2: способ B" 2 SA_START=existing-ssh -- up
want_rc "код 2: способ C" 2 SA_START=provider-only -- up
want_rc "код 2: другой провайдер" 2 SA_PROVIDER=other -- up

# --- код 4: нет tcp/22 (слушателя ещё нет) ---------------------------------------------------------------
drive_rc "код 4: сервер недоступен (configure)" 4 /tmp/sc.full -- configure

# --- apply: секреты, контейнеры, журнал ------------------------------------------------------------------
python3 -I -c 'import socket, sys
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind((sys.argv[1], 22))
s.listen(8)
while True:
    c, _ = s.accept()
    c.close()' "$CLI_IP" &
lpid=$!
: >"$SHIM_LOG"
: >"$SHIM_PS"
rm -f "$inv/ansible/hosts.yml"
drive_rc "apply проходит" 0 /tmp/sc.apply -- apply
check "apply: адрес из output записан в hosts.yml" grep -qF "ansible_host: $CLI_IP" "$inv/ansible/hosts.yml"
check "apply: init строго по lock-файлу" grep -q 'TOFU init -lockfile=readonly' "$SHIM_LOG"
check "apply: токен дошёл до tofu через tofu-entry.sh" grep -q TOFU-TOKEN-OK "$SHIM_LOG"
check "apply: без -replace" bash -c '! grep -q -- "-replace" "$1"' _ "$SHIM_LOG"
check "каждый контейнер — sa-prov-*" bash -c '! grep "^RUN" "$1" | grep -qv "name=sa-prov-"' _ "$SHIM_LOG"
check "каждый контейнер — --log-driver=none" bash -c '! grep "^RUN" "$1" | grep -qv "log=none"' _ "$SHIM_LOG"
check "токен: файла в XDG_RUNTIME_DIR нет после выхода" bash -c '! ls /run/user/1000/sa-token-* 2>/dev/null | grep -q .'
check "агент: сокета нет после выхода" bash -c '! ls /run/user/1000/sa-agent-* 2>/dev/null | grep -q .'
drive_rc "apply --replace-server проходит" 0 /tmp/sc.apply -- apply --replace-server
check "apply --replace-server: ровно -replace сервера — в плане, apply плана без флагов" bash -c 'grep -q "TOFU plan .*-replace=clo_compute_instance.srv" "$1" && ! grep "^TOFU apply" "$1" | grep -q -- "-replace"' _ "$SHIM_LOG"
drive_rc "apply: private_key в state — ошибка" 1 /tmp/sc.apply FAKE_STATE_LEAK=1 -- apply
check "apply: сообщение называет private_key" grep -q private_key /tmp/out.txt
prepare_checks
hosts_all_checks
: >"$SHIM_LOG"
rm -f "$inv/deployed-commit"
rm -f "$SHIM_OUT/less.txt"
drive_rc "configure: вход администратора есть — только site.yml (deployed-commit нет: путь от пустого дерева)" 0 /tmp/sc.full FAKE_SSH_RC=0 -- configure
check "configure: без deployed-commit показан весь код (diff от пустого дерева)" grep -q 'diff --git a/deploy/ops/guard.sh' "$SHIM_OUT/less.txt"
check "configure: deployed-commit появился после успешного прогона" test -s "$inv/deployed-commit"
check "configure: site.yml с каналом vault, без bootstrap.yml" bash -c 'grep -q "ANSIBLE -i /inv/hosts.yml site.yml" "$1" && grep -q ANSIBLE-VAULT-OK "$1" && ! grep -q "ANSIBLE.* bootstrap.yml" "$1"' _ "$SHIM_LOG"
check "configure: deployed-commit записан" bash -c '[ "$(cat "$1")" = "$2" ]' _ "$inv/deployed-commit" "$head0"
pubkey=$(cat "$HOME/.ssh/id_server.pub")
want_args='-i /inv/hosts.yml site.yml -e albums_domain=host.example.org -e admin_user=admin-example -e backup_user=sa-backup'
want_args="$want_args -e bootstrap_user=root -e image_user= -e "
want_args="$want_args{\"sa_ssh_args\":[\"-F\",\"/dev/null\",\"-o\",\"IdentitiesOnly=yes\",\"-o\",\"IdentityFile=/run/sa/id_server.pub\","
want_args="$want_args\"-o\",\"IdentityAgent=/run/sa/agent.sock\",\"-o\",\"UserKnownHostsFile=/run/sa/known_hosts\",\"-o\","
want_args="$want_args\"StrictHostKeyChecking=yes\",\"-o\",\"ConnectTimeout=10\"],\"sa_controller_pubkey\":\"$pubkey\"} -e albums_svc_image=$IMG_REF --vault-password-file=/dev/fd/3"
check "configure: точный набор -e и аргументов ansible-playbook (контракт с контроллером)" bash -c 'grep -qxF "ANSIBLE $2" "$1"' _ "$SHIM_LOG" "$want_args"
check "configure: нет ansible_ssh_common_args" bash -c '! grep -q ansible_ssh_common_args "$1"' _ "$SHIM_LOG"
check "configure: окружение и тома контейнера ansible (ANSIBLE_CONFIG, только deploy/ и страница 404)" bash -c '
	grep -q -- "-e ANSIBLE_CONFIG=/src/deploy/ansible/ansible.cfg" "$1" && grep -q -- "-v [^ ]*/deploy:/src/deploy:ro" "$1" &&
	grep -q -- "-v [^ ]*/notfound.html:/src/web/viewer/notfound.html:ro" "$1" && ! grep -q -- ":/src:ro" "$1"' _ "$SHIM_LOG"
check "configure: флаги контейнера ansible те же, что собирает ops/ctl-run.sh (общий со стендом код)" bash -c '
	grep "^RUN name=sa-prov-ansible-" "$1" | grep -q -- "--read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=512 --tmpfs /tmp:rw,mode=1777 --preserve-fds=1 "' _ "$SHIM_LOG"
check "configure: скрипты поставлены в ~/.local/libexec/sa" bash -c 'for f in review.sh sa-backup-prune.sh backup-lib.sh; do cmp -s "$1/deploy/ops/$f" "$HOME/.local/libexec/sa/$f" || exit 1; done' _ "$clone"
ext_configure_checks
: >"$SHIM_LOG"
drive_rc "configure: входа нет (255), ключ хоста тот же — bootstrap.yml, затем site.yml" 0 /tmp/sc.full FAKE_SSH_RC=255 FAKE_HOSTKEY="$HOME/.ssh/id_server.pub" -- configure
check "configure: порядок плеев bootstrap → site" bash -c '[ "$(grep -c "^ANSIBLE " "$1")" = 2 ] && grep "^ANSIBLE " "$1" | head -n 1 | grep -q bootstrap.yml' _ "$SHIM_LOG"
: >"$SHIM_LOG"
drive_rc "configure: ключ хоста сменился — отказ с подсказкой forget-host" 1 /tmp/sc.full FAKE_SSH_RC=255 FAKE_HOSTKEY="$HOME/.ssh/id_backup.pub" -- configure
check "configure: при смене ключа хоста плеи не запускались, подсказка есть" bash -c '! grep -q "^ANSIBLE " "$1" && grep -q "forget-host" /tmp/out.txt' _ "$SHIM_LOG"
cp "$inv/known_hosts" /tmp/known_hosts.keep
: >"$inv/known_hosts"
printf 'expect yes/no\nsend yes\nexpect Enter passphrase:\nsend $PHRASE_SECRETS\nexpect веб-консолью\nsend yes\n' >/tmp/sc.first
drive_rc "configure: первый вход — отпечаток ключа хоста и «yes» (keyscan)" 0 /tmp/sc.first FAKE_SSH_RC=0 FAKE_HOSTKEY="$HOME/.ssh/id_server.pub" -- configure
check "configure: отпечаток показан, ключ записан в known_hosts" bash -c 'grep -q "SHA256:" /tmp/out.txt && grep -q "^$2 ssh-ed25519 " "$1"' _ "$inv/known_hosts" "$CLI_IP"
cp /tmp/known_hosts.keep "$inv/known_hosts"
check "diff: показан в less без R и с LESSSECURE" bash -c 'grep -q "LESSSECURE=1" "$1/less.env" && ! grep -Eq "^LESS=[^ ]*[Rr]" "$1/less.env"' _ "$SHIM_OUT"

image_cases
probe_cases

# --- утечки: метки и фразы нигде, кроме каналов и файла токена на время прогона --------------------------
for v in "$MARKER" "$VAULT" "$PHRASE_SSH" "$PHRASE_BACKUP" "$PHRASE_SECRETS"; do
	check "утечка: значение не в argv/ps/журнале shim/выводе" bash -c '! grep -qF -- "$1" /tmp/shim.log /tmp/shim.ps /tmp/all.out' _ "$v"
	check "утечка: значение не в файлах" bash -c '! grep -rlF --exclude-dir=origin --exclude-dir=src -- "$1" "$HOME" /tmp /run/user/1000 /srv/sa-inbox /in /run/sa 2>/dev/null | grep -q .' _ "$v"
done

# --- backup: метка конца, .part ---------------------------------------------------------------------------
rm -rf "$BK"
drive_rc "backup: поток с меткой — принят" 0 /tmp/sc.full FAKE_STREAM=/tmp/good.stream -- backup
check "backup: копия сохранена без .part" bash -c 'ls $BK/*.tar.zst.age >/dev/null && ! ls $BK/*.part 2>/dev/null | grep -q .'
rm -rf "$BK"
drive_rc "backup: поток без метки — отказ" 1 /tmp/sc.full FAKE_STREAM=/tmp/nomark.stream -- backup
check "backup без метки: .part удалён, копии нет" bash -c '! ls $BK/ 2>/dev/null | grep -q .'
drive_rc "backup: ssh упал — отказ" 1 /tmp/sc.full FAKE_SSH_RC=255 -- backup
check "backup при сбое ssh: .part удалён" bash -c '! ls $BK/ 2>/dev/null | grep -q .'

. "$R/inner_prune.sh"

kill "$lpid" 2>/dev/null
echo "provision_cli (внутри): провалов $fails"
[ "$fails" -eq 0 ]
