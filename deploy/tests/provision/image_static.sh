#!/usr/bin/env bash
# Шаги image_verify и actions_pins стенда раскатки (спека 004, design §12): проверка образа и сверка закреплённых действий.
# Только source из run.sh (после lib.sh и static.sh). Оба идут на машине с настоящим Podman и без сети (gh — в контейнере
# --network=none); образ gh собирает тот же код окна Б из архива выпуска, который кладёт владелец или CI в
# .gotmp/provision/gh/ (в git архива нет: ~15 МБ). Нет архива: вне CI — SKIP с причиной (остальные шаги не страдают), в CI (CI=true, GITHUB_ACTIONS) — провал.
# shellcheck disable=SC2154  # root, here, cache, skip: из lib.sh и static.sh

step_image_verify() {
	local ver archive w rc
	[ -f "$root/deploy/ops/tools.env" ] || skip 'нет deploy/ops/tools.env (задача И6b)' || return $?
	command -v python3 >/dev/null || {
		echo "нет python3 на машине: подмена бита подписи в фикстуре без него невозможна — шаг не пропускается"
		return 1
	}
	ver=$(sed -n 's/^GH_VERSION=//p' "$root/deploy/ops/tools.env")
	archive="$cache/gh/gh_${ver}_linux_amd64.tar.gz"
	if [ ! -f "$archive" ]; then
		# В CI проверки на настоящем gh обязаны идти: отсутствие архива — провал (его скачивает и сверяет workflow), не SKIP.
		if [ "${CI:-}" = true ] || [ -n "${GITHUB_ACTIONS:-}" ]; then
			echo "нет архива gh $ver: $archive (в CI обязателен: скачать выпуск cli/cli и сверить sha256 с GH_SHA256 из deploy/ops/tools.env)"
			return 1
		fi
		skip "нет архива gh $ver: положить gh_${ver}_linux_amd64.tar.gz из выпуска в .gotmp/provision/gh/ (sha256 сверяет сам шаг по tools.env)" || return $?
	fi
	mkdir -p "$cache"
	w=$(mktemp -d "$cache/iv.XXXXXX") || return 1
	chmod 700 "$w"
	bash "$here/image/verify_cases.sh" "$root" "$w" "$archive"
	rc=$?
	if [ -s "$w/inv/gh-image-id" ]; then podman rmi "$(head -n 1 "$w/inv/gh-image-id")" >/dev/null 2>&1 || true; fi
	rm -rf -- "$w"
	return "$rc"
}

step_actions_pins() {
	local w rc
	[ -f "$root/deploy/ops/tools.sh" ] || skip 'нет deploy/ops/tools.sh (задача И6b)' || return $?
	# runs.using разбирает PyYAML в образе контроллера (как в окне Б): образ собран из закреплённых зависимостей.
	ensure_lint_image || return 1
	mkdir -p "$cache"
	w=$(mktemp -d "$cache/pins.XXXXXX") || return 1
	chmod 700 "$w"
	bash "$here/image/pins_cases.sh" "$root" "$w" "$(lint_image)"
	rc=$?
	rm -rf -- "$w"
	return "$rc"
}
