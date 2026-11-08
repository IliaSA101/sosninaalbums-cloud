#!/usr/bin/env bash
# Общее для run.sh и static.sh: пути, образы по дайджесту, запуск инструментов в контейнерах. Только source.
# Инструменты идут в контейнере по дайджесту или в образе, собранном из закреплённых хэшами зависимостей;
# на хост ничего не ставится. Исключение — shellcheck: он берётся с машины (static.sh), как в scripts/check.sh.
# Ни токена провайдера, ни его сети здесь нет: сеть нужна только `tofu init` (реестр OpenTofu) и сборке образов.

here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(CDPATH='' cd -- "$here/../../.." && pwd)
# Кэш и рабочие файлы — в .gotmp проекта (вне git); ключи стенда — отдельно, в XDG_RUNTIME_DIR (run.sh).
cache="$root/.gotmp/provision"

# OpenTofu 1.13.5, индекс; не -minimal: в нём нет корневых сертификатов (research §1).
TOFU_IMAGE="${TOFU_IMAGE:-ghcr.io/opentofu/opentofu@sha256:5e6046cd8d1e2db82ce71fb56fe0a535b489b5493cfa0d5889ad5cffd2625ab6}"

# Образ — только по дайджесту: тег можно переписать на стороне реестра.
case "$TOFU_IMAGE" in
	*@sha256:????????????????????????????????????????????????????????????????) ;;
	*) echo "provision: TOFU_IMAGE должен быть вида имя@sha256:<64 hex>, а не тег" >&2; exit 1 ;;
esac

die() {
	echo "provision: $*" >&2
	exit 1
}

need() {
	local t
	for t in "$@"; do
		command -v "$t" >/dev/null || die "нужен $t"
	done
}

# Общие флаги контейнера инструмента: без записи в корень, без capabilities и повышения прав, лимит процессов.
# Корень контейнера — host-пользователь (rootless), поэтому смонтированное с :ro он не меняет.
# PYTHONDONTWRITEBYTECODE: python на смонтированном дереве не оставляет __pycache__/*.pyc (они попадали в эталонную копию кода тестов).
hflags=(--rm --read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=512 --tmpfs '/tmp:rw,mode=1777' -e PYTHONDONTWRITEBYTECODE=1)

# tofu_run <сеть: none|default> <каталог модуля> <имя кэша> <аргументы tofu…>
# Модуль — в /work только для чтения; данные init (провайдеры) — в кэше, чтобы не качать каждый раз.
# default — режим сети Podman по умолчанию (без зашитого драйвера); none — всё, где работает бинарник
# провайдера или проверяется конфигурация (решение П2a-1).
tofu_run() {
	local net=$1 dir=$2 name=$3 netflag=()
	shift 3
	[ "$net" = none ] && netflag=(--network=none)
	mkdir -p "$cache/tofu-$name"
	podman run "${hflags[@]}" "${netflag[@]}" \
		-e HOME=/tmp -e TF_DATA_DIR=/data -e TF_IN_AUTOMATION=1 -e TF_INPUT=0 -e CHECKPOINT_DISABLE=1 \
		-v "$dir:/work:ro" -v "$cache/tofu-$name:/data" -w /work \
		"$TOFU_IMAGE" "$@"
}

# Файлы конфигурации OpenTofu/Terraform в каталоге модуля (все четыре расширения, которые читает tofu).
tofu_sources() {
	local d=$1 f
	for f in "$d"/*.tf "$d"/*.tofu "$d"/*.tf.json "$d"/*.tofu.json; do
		[ -f "$f" ] && echo "$f"
	done
	return 0
}

# tofu_init <каталог> <имя>: init идёт через сеть (даже при готовом кэше он опрашивает реестр OpenTofu —
# проверено), поэтому повторяется только при смене конфигурации или .terraform.lock.hcl (отпечаток в кэше).
# Ходит только в реестр OpenTofu, не к API провайдера; токена в контейнере нет. -lockfile=readonly: провайдер
# берётся строго по .terraform.lock.hcl из репозитория (хэши), а не «последний».
tofu_init() {
	local dir=$1 name=$2 stamp files
	mapfile -t files < <(tofu_sources "$dir")
	stamp=$(cat "${files[@]}" "$dir/.terraform.lock.hcl" | sha256sum | cut -c1-64)
	[ "$(cat "$cache/tofu-$name/.sa-stamp" 2>/dev/null)" = "$stamp" ] && return 0
	tofu_run default "$dir" "$name" init -backend=false -lockfile=readonly -no-color || return 1
	printf '%s\n' "$stamp" >"$cache/tofu-$name/.sa-stamp"
}

# Образ контроллера Ansible (deploy/ansible/Containerfile): ansible, ansible-lint, yamllint, age и прочее из
# одного закреплённого набора requirements.txt с хэшами (решение П2a-4). Тег — от хэша Containerfile и
# requirements: правка любого из них пересобирает образ.
lint_image() {
	local d="$root/deploy/ansible" tag
	tag=$(cat "$d/Containerfile" "$d/requirements.txt" | sha256sum | cut -c1-12)
	echo "localhost/sa-tgt-ctl:$tag"
}

ensure_lint_image() {
	local img out
	img=$(lint_image)
	podman image exists "$img" && return 0
	echo "сборка $img (первый раз: pip с --require-hashes, нужна сеть)" >&2
	# Не die: упавший шаг не должен обрывать вывод остальных шагов static.
	if ! out=$(podman build -q -t "$img" -f "$root/deploy/ansible/Containerfile" "$root/deploy/ansible" 2>&1); then
		printf '%s\n' "$out" | tail -n 20
		echo "не собрался $img"
		return 1
	fi
}

# deploy_mounts: заполняет массив dmounts томами для контейнера контроллера.
# Подкаталоги deploy/ и web/ — только для чтения: роли берут файлы по путям от playbook_dir (../caddy,
# ../../web/viewer). Приватный deploy/inventory в контейнер не попадает никогда (монтируются подкаталоги
# по одному, кроме него); рабочее дерево целиком не монтируется (в нём .git и личное).
deploy_mounts() {
	local d
	dmounts=()
	for d in "$root"/deploy/*/; do
		d=${d%/}
		[ "${d##*/}" = inventory ] && continue
		dmounts+=(-v "$d:/work/deploy/${d##*/}:ro")
	done
	if [ -d "$root/web" ]; then dmounts+=(-v "$root/web:/work/web:ro"); fi
	return 0
}

# lint_run <рабочий каталог в контейнере> <команда…> — без сети.
lint_run() {
	local wd=$1 dmounts
	shift
	deploy_mounts
	podman run "${hflags[@]}" --network=none \
		-e HOME=/tmp/home -e XDG_CACHE_HOME=/tmp/cache -e ANSIBLE_HOME=/tmp/ansible \
		-e ANSIBLE_NOCOLOR=1 -e NO_COLOR=1 \
		"${dmounts[@]}" -w "$wd" "$(lint_image)" "$@"
}
