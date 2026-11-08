#!/usr/bin/env bash
# Продолжение inner.sh (подключается из него через source: общие функции check, want_rc, review, gitc и переменные
# clone, head0, inv, MARKER… — оттуда; отдельный файл — из-за предела размера скрипта): симлинки на месте самого
# deploy, web и web/viewer, белый список символов, предел размера bundle, блокировка inventory/.lock и её
# наследование демонами, юниты таймера. Печатает ok/FAIL через check/bad inner.sh.
# shellcheck source-path=SCRIPTDIR disable=SC2154,SC2016  # переменные из inner.sh; bash -c '…$1…': раскрытие внутри

# Машина и сессия (design §16.8; метка и machine-id смонтированы верные, прочие метки — отдельные прогоны в
# provision_cli.sh). want_msg сверяет и код, и текст отказа: отказ мог бы прийти из другой проверки.
want_msg() { # <имя> <код> <текст> <env…> -- <команда>
	local n=$1 want=$2 msg=$3 rc
	shift 3
	run_plain "$@"
	rc=$?
	if [ "$rc" = "$want" ] && grep -qF -- "$msg" /tmp/out.txt; then ok "$n"; else
		bad "$n: код $rc, ждали $want и «$msg»"
		tail -n 4 /tmp/out.txt
	fi
}
# Графика разрешена (ВР-6): проверка сессии пройдена, отказ приходит позже — от неизвестной команды (после guard_repo).
want_msg "сессия: wayland разрешена" 3 "неизвестная команда" FAKE_TYPE=wayland WAYLAND_DISPLAY=wayland-0 -- frobnicate
want_msg "сессия: x11 разрешена" 3 "неизвестная команда" FAKE_TYPE=x11 DISPLAY=:0 -- frobnicate
want_msg "отказ: сессия unspecified" 3 "тип сессии" FAKE_TYPE=unspecified -- up
# Сессия без XDG_SESSION_ID (ВР-13; терминал GNOME): берётся Display учётной записи (shim: «3»), дальше те же проверки.
# sid_is сверяет, по какой сессии спрашивали: без этого путь Display и путь XDG_SESSION_ID (shim: «1») не различить.
sid_is() { [ "$(cat /tmp/shim-out/loginctl.sid 2>/dev/null)" = "$1" ]; }
rm -f /tmp/shim-out/loginctl.sid
want_msg "сессия: старый путь — XDG_SESSION_ID задан, Display не нужен" 3 "неизвестная команда" FAKE_DISPLAY= -- frobnicate
check "сессия: старый путь спросил сессию из XDG_SESSION_ID" sid_is 1
rm -f /tmp/shim-out/loginctl.sid
want_msg "сессия: без XDG_SESSION_ID — Display активной x11-сессии своего uid" 3 "неизвестная команда" XDG_SESSION_ID= FAKE_TYPE=x11 -- frobnicate
check "сессия: новый путь спросил сессию из Display" sid_is 3
want_msg "сессия: без XDG_SESSION_ID, wayland" 3 "неизвестная команда" XDG_SESSION_ID= FAKE_TYPE=wayland -- frobnicate
want_msg "сессия: без XDG_SESSION_ID, tty" 3 "неизвестная команда" XDG_SESSION_ID= -- frobnicate
want_msg "отказ: нет XDG_SESSION_ID и Display пуст" 3 "нет сессии: ни XDG_SESSION_ID, ни графической сессии" XDG_SESSION_ID= FAKE_DISPLAY= -- up
want_msg "отказ: без XDG_SESSION_ID, сессия Display чужого uid" 3 "сессия принадлежит другой" XDG_SESSION_ID= FAKE_USER=0 -- up
want_msg "отказ: без XDG_SESSION_ID, сессия Display удалённая" 3 "сессия удалённая" XDG_SESSION_ID= FAKE_REMOTE=yes -- up
want_msg "отказ: без XDG_SESSION_ID, сессия Display unspecified" 3 "тип сессии" XDG_SESSION_ID= FAKE_TYPE=unspecified -- up
want_msg "отказ: без XDG_SESSION_ID, сессия Display закрывается" 3 "сессия не активна" XDG_SESSION_ID= FAKE_STATE=closing -- up
want_msg "отказ: без XDG_SESSION_ID, чужой XDG_RUNTIME_DIR" 3 "XDG_RUNTIME_DIR не /run/user" XDG_SESSION_ID= XDG_RUNTIME_DIR=/tmp -- up
# State: active и online проходят, closing и прочее — отказ (оба пути).
want_msg "отказ: сессия по XDG_SESSION_ID закрывается" 3 "сессия не активна" FAKE_STATE=closing -- up
want_msg "отказ: State сессии пуст" 3 "сессия не активна" FAKE_STATE= -- up
want_msg "сессия: online (без фокуса) проходит" 3 "неизвестная команда" FAKE_STATE=online -- frobnicate
want_msg "сессия: без XDG_SESSION_ID, online проходит" 3 "неизвестная команда" XDG_SESSION_ID= FAKE_STATE=online -- frobnicate
# Процесс claude, claude-* (claude-desktop), node или cowork* любого пользователя: копия sleep под этим именем (comm берётся из имени файла), фон,
# остановка по своему PID. Сначала проверяется, что ps его видит: без этого отказ ничего бы не доказывал.
mkdir -p /tmp/proc
for pn in node claude claude-desktop cowork-vm; do
	cp "$(command -v sleep)" "/tmp/proc/$pn"
	"/tmp/proc/$pn" 300 &
	ppid=$!
	check "процесс $pn: ps его видит" bash -c 'ps -e -o comm= | grep -qx "$1"' _ "$pn"
	want_msg "отказ: процесс $pn" 3 "не машина раскатки: идёт процесс claude, node или cowork" -- up
	review main
	check "review: отказ: процесс $pn" bash -c '[ "$1" = 3 ] && grep -q "не машина раскатки: идёт процесс" /tmp/review.txt' _ "$?"
	kill "$ppid"
	wait "$ppid" 2>/dev/null
done
want_msg "отказ: ps не выполнился" 3 "не машина раскатки: ps не выполнился" PATH="$R/shim-ps:$PATH" -- up
want_msg "отказ: ps ничего не вернул" 3 "не машина раскатки: ps ничего не вернул" PATH="$R/shim-ps:$PATH" FAKE_PS=empty -- up
review main SA_OPS_USER=sa-ops-other
check "review: отказ: не та учётная запись" bash -c '[ "$1" = 3 ] && grep -q "не та учётная запись" /tmp/review.txt' _ "$?"
check "review: после отказов копии bundle и временного каталога в доме нет" no_leftovers

# mutate_link <репозиторий> <deploy|web|web/viewer> <ext|int>: путь p становится симлинком — на внешний каталог
# (ext) или на копию дерева внутри репозитория (int, x/<p>). pathspec deploy/ такой записи не видит, поэтому
# просмотр обязан смотреть режимы по всему дереву и типы deploy и страницы 404 явно.
mutate_link() {
	local r=$1 p=$2 how=$3
	rm -rf -- "${r:?}/x"
	if [ "$how" = ext ]; then
		git -C "$r" rm -rq --cached "$p"
		rm -rf -- "${r:?}/$p"
		ln -s /etc "$r/$p"
	else
		mkdir -p "$r/x/$(dirname "$p")"
		mv "$r/$p" "$r/x/$p"
		ln -s "$r/x/$p" "$r/$p"
	fi
	gitc -C "$r" add -A
	gitc -C "$r" commit -q -m lnk
}

for v in deploy:ext deploy:int web:ext web:int web/viewer:ext web/viewer:int; do
	p=${v%:*} how=${v#*:} n=${v//[\/:]/_}
	gitc -C "$HOME/origin" checkout -q -b "ln_$n" "$head0"
	mutate_link "$HOME/origin" "$p" "$how"
	gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle "ln_$n" 2>/dev/null
	review "ln_$n"
	check "review: симлинк на месте $p ($how) — отказ до checkout" bash -c '[ "$1" = 3 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
	gitc -C "$HOME/origin" checkout -q -f main
	gitc -C "$HOME/origin" clean -fdq
	# Сторона раскатки: guard_modes из эталонной копии кода на клоне с таким же коммитом (provision.sh из клона с
	# симлинком на месте deploy сам не нашёлся бы).
	mutate_link "$clone" "$p" "$how"
	bash -c '. /ref/deploy/ops/guard.sh; repo=$1; guard_modes HEAD' _ "$clone" >/dev/null 2>&1
	check "guard_modes: симлинк на месте $p ($how) — отказ 3" test "$?" = 3
	git -C "$clone" reset -q --hard "$head0"
	git -C "$clone" clean -fdq
done
check "guard_modes: чистый коммит проходит" bash -c '. /ref/deploy/ops/guard.sh; repo=$1; guard_modes HEAD' _ "$clone"

# Белый список символов: теги U+E0000, селектор вариантов U+FE0F, разделитель строк U+2028, заполнитель U+3164 —
# перечнем запретов их не поймать. Хороший текст (кириллица, тире, кавычки-ёлочки, №) проходит.
for zw in '\xf3\xa0\x80\x80' '\xef\xb8\x8f' '\xe2\x80\xa8' '\xe3\x85\xa4' '\xe2\x80\x93' '\xe2\x80\x9c' '\xc3\x97'; do
	printf 'a%bb\n' "$zw" >"$HOME/origin/deploy/bad.txt"
	gitc -C "$HOME/origin" add -A
	gitc -C "$HOME/origin" commit -q -m wl
	gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle main 2>/dev/null
	review main
	check "review: символ вне белого списка ${zw//\\/} — отказ до checkout" bash -c '[ "$1" = 3 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
	gitc -C "$HOME/origin" reset -q --hard HEAD~1
	printf 'a%bb\n' "$zw" >"$clone/deploy/probe.txt"
	gitc -C "$clone" add -A
	gitc -C "$clone" commit -q -m wl
	want_rc "отказ: символ вне белого списка ${zw//\\/} в deploy/" 3 -- up
	git -C "$clone" reset -q --hard "$head0"
done
printf 'Привет — «мир» № 5 ≥ 3 → ok\n' >"$clone/deploy/probe.txt"
gitc -C "$clone" add -A
gitc -C "$clone" commit -q -m wl-ok
check "белый список: кириллица, тире, ёлочки, №, стрелки проходят" bash -c '
	. /ref/deploy/ops/guard.sh; LC_ALL=C git -C "$1" grep -a -l -P "$ctl_pattern" HEAD -- deploy/probe.txt; [ $? = 1 ]' _ "$clone"
git -C "$clone" reset -q --hard "$head0"

# Предел размера bundle: на один байт больше — отказ 3 до любой проверки, копия в доме не остаётся.
truncate -s 209715201 /srv/sa-inbox/big.bundle
review main SA_SRC_BUNDLE=/srv/sa-inbox/big.bundle
check "review: bundle на байт больше предела — отказ, копии в доме нет" bash -c '[ "$1" = 3 ] && ! ls -d "$HOME"/.review.* "$HOME/in.bundle" 2>/dev/null | grep -q .' _ "$?"
rm -f /srv/sa-inbox/big.bundle

# Блокировка inventory/.lock: занята — код 1 до любых действий.
exec {lk}>"$inv/.lock"
flock -n "$lk"
want_rc "блокировка inventory/.lock занята — отказ кода 1" 1 -- configure
check "блокировка: сообщение называет inventory/.lock" grep -q 'inventory/.lock' /tmp/out.txt
exec {lk}>&-

# ext_configure_checks: проверки после успешного configure (вызываются из inner.sh): юниты таймера — копии из
# клона; демон агента ключей и podman не держат дескриптор блокировки (иначе сирота после SIGKILL держал бы её).
ext_configure_checks() {
	local f
	for f in sa-backup-prune.service sa-backup-prune.timer; do
		check "configure: юнит $f в ~/.config/systemd/user совпадает с клоном" cmp -s "$clone/deploy/ops/$f" "$HOME/.config/systemd/user/$f"
	done
	check "агент: в журнале shim есть открытые файлы агента (проверка не пустая)" grep -q '^FD agent ' "$SHIM_LOG"
	check "блокировка: podman build тоже виден в журнале shim (проверка не пустая)" grep -q '^FD build ' "$SHIM_LOG"
	check "блокировка: ни агент, ни podman (run и build) не держат inventory/.lock" bash -c '! grep "^FD " "$1" | grep -q "/\.lock$"' _ "$SHIM_LOG"
}
