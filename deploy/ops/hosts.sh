#!/usr/bin/env bash
# inventory/ansible/hosts.yml: проверка адреса, запись и команда `provision.sh hosts` (спека 005, design §4.2, 005-13,
# 005-24). Только source: provision.sh (apply, hosts) и стенд (converge.sh, через ctl_write_hosts из ops/ctl-run.sh).
#
# Зачем отдельная проверка: адрес из hosts.yml — это то, куда ssh отправит пароль sudo и куда пойдёт плей; опечатка или
# нетипичная запись (`0101.2.3.4` ssh читает как восьмеричное) отправили бы их не на тот сервер. Проверка одна на
# `hosts` и `apply` (вывод tofu проходит через неё же); обхода по переменной окружения в боевом коде нет — тесты CLI
# получают адрес вне служебных диапазонов на lo тестового контейнера (cli/inner.sh).
# shellcheck disable=SC2154  # SA_INVENTORY, die, refuse, tf: задаёт provision.sh

# hosts_check_ip <адрес>: 0 — каноническая десятичная запись IPv4 вне служебных диапазонов; иначе 1 и причина в stderr.
hosts_check_ip() {
	local a=$1 re='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])' o1 o2 o3
	# Без ведущих нулей, без восьмеричной и шестнадцатеричной записи, без сокращений (127.1): только четыре десятичных октета.
	if ! [[ $a =~ ^$re\.$re\.$re\.$re$ ]]; then
		echo "hosts: адрес не в канонической десятичной записи IPv4 (четыре числа 0-255 без ведущих нулей)" >&2
		return 1
	fi
	o1=${BASH_REMATCH[1]} o2=${BASH_REMATCH[2]} o3=${BASH_REMATCH[3]}
	# Служебные диапазоны (RFC 6890 и соседние): публичный сервер в них оказаться не может, значит, это опечатка или подмена.
	if [ "$o1" -eq 0 ] || [ "$o1" -eq 10 ] || [ "$o1" -eq 127 ] || [ "$o1" -ge 224 ] ||
		{ [ "$o1" -eq 100 ] && [ "$o2" -ge 64 ] && [ "$o2" -le 127 ]; } ||
		{ [ "$o1" -eq 169 ] && [ "$o2" -eq 254 ]; } ||
		{ [ "$o1" -eq 172 ] && [ "$o2" -ge 16 ] && [ "$o2" -le 31 ]; } ||
		{ [ "$o1" -eq 192 ] && [ "$o2" -eq 168 ]; } ||
		{ [ "$o1" -eq 192 ] && [ "$o2" -eq 0 ] && { [ "$o3" -eq 0 ] || [ "$o3" -eq 2 ]; }; } ||
		{ [ "$o1" -eq 192 ] && [ "$o2" -eq 88 ] && [ "$o3" -eq 99 ]; } ||
		{ [ "$o1" -eq 198 ] && { [ "$o2" -eq 18 ] || [ "$o2" -eq 19 ]; }; } ||
		{ [ "$o1" -eq 198 ] && [ "$o2" -eq 51 ] && [ "$o3" -eq 100 ]; } ||
		{ [ "$o1" -eq 203 ] && [ "$o2" -eq 0 ] && [ "$o3" -eq 113 ]; }; then
		echo "hosts: адрес в служебном диапазоне (частный, loopback, link-local, CGNAT, документационный, multicast, зарезервированный)" >&2
		return 1
	fi
}

# hosts_write <путь> <адрес или имя цели>: тот же формат, что читает host_ip. Адрес здесь НЕ проверяется — стенд пишет имя
# цели в своей сети (sa-target); проверка адреса — в hosts_cmd и cmd_apply. Временный файл в том же каталоге и mv -T:
# оборванная запись не оставит половину файла, а симлинк на месте файла или каталога ansible/ не уведёт запись за
# пределы inventory (как в ctl_build).
hosts_write() {
	local p=$1 d tmp
	d=$(dirname -- "$p")
	if [ -L "$d" ] || [ -L "$p" ]; then
		echo "hosts: $p или его каталог — симлинк, запись отменена" >&2
		return 1
	fi
	tmp=$(mktemp "$d/.hosts.XXXXXX") || return 1
	if ! { printf 'all:\n  hosts:\n    server:\n      ansible_host: %s\n' "$2" >"$tmp" && chmod 0644 "$tmp" && mv -T -- "$tmp" "$p"; }; then
		rm -f -- "$tmp"
		return 1
	fi
}

# hosts_report <адрес>: что владельцу сверить и что передать администратору ops-ВМ (README §1, §5). Порт 443 — для
# дымовой проверки и списка альбомов (provision.sh smoke, albums).
hosts_report() {
	echo "адрес сервера: $1"
	echo "сверить с панелью провайдера; правила администратора ops-ВМ (README §1):"
	echo "  sudo ufw allow out to $1 port 22 proto tcp"
	echo "  sudo ufw allow out to $1 port 443 proto tcp"
}

# cmd_hosts [<IPv4>]: записать inventory/ansible/hosts.yml, когда apply упал после создания сервера. Без аргумента —
# адрес из state (`tofu output` без сети), с аргументом — адрес проверяется и сверяется с выводом tofu, если он есть.
# Секретов и ключей не нужно, но `tofu output` исполняет обёртку из клона, поэтому команда идёт после review_code.
cmd_hosts() {
	local arg=${1:-} out ip
	guard_tofu_files
	out=$(tf --network=none -- output -raw ipv4 2>/dev/null) || out=''
	if [ -n "$out" ] && ! hosts_check_ip "$out"; then refuse "адрес из вывода tofu не прошёл проверку"; fi
	if [ -z "$arg" ]; then
		[ -n "$out" ] || die "в state нет адреса: apply не дошёл до создания сервера; адрес можно указать аргументом (hosts <IPv4>)"
		ip=$out
	else
		hosts_check_ip "$arg" || refuse "адрес не прошёл проверку"
		if [ -n "$out" ] && [ "$out" != "$arg" ]; then refuse "адрес расходится с выводом tofu (state): сверить с панелью провайдера"; fi
		ip=$arg
	fi
	mkdir -p "$SA_INVENTORY/ansible" || die "каталог inventory/ansible"
	hosts_write "$SA_INVENTORY/ansible/hosts.yml" "$ip" || die "hosts.yml не записан"
	echo "hosts.yml записан в inventory/ansible"
	hosts_report "$ip"
}
