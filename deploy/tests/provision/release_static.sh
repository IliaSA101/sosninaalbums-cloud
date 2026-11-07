#!/usr/bin/env bash
# Шаги `static` выпуска образа (спека 004, design §12, задача И6a): проверки workflow, скриптов выпуска и режима
# архива стенда. Только source из run.sh (после lib.sh и static.sh). Тесты - deploy/tests/release/test_*.py, каждый
# пишет имена случаев; шаг называется по набору из design §12.
#
#   workflow_guard_bypass   мутации workflow_guard.py по каждому обходу Н3-1 (в контейнере контроллера: PyYAML по хэшу)
#   workflow_static         правила design §2(б) на мутациях и настоящие .github/workflows/* (там же)
#   release_gate_cases      gate.sh и сверка дерева перед меткой (на хосте: bash, python3, git)
#   image_composition       эталон состава образа на синтетических архивах
#   stand_archive_mode      режим архива стенда (full_push_archive) с поддельным skopeo
#   release_publish_cases   publish.sh и registry.sh с поддельным skopeo, mutate_release_artifact
#   runner_check_cases      шаг сверки раннеров из workflow на синтетических ответах API
#   ghcr_probe_cases        проба сети сервера до реестра (deploy/ops/ghcr_probe.py) на реестре-заглушке и предел podman pull в роли
#
# Хостовые наборы - только stdlib Python и bash; настоящий skopeo и реестр проверяет stand-full на ci/*.
# shellcheck disable=SC2154  # root, hflags, skip — из lib.sh и static.sh

# Тест в контейнере контроллера: без сети, корень только для чтения, .github и deploy/* — для чтения.
release_in_ctl() { # <файл теста в deploy/tests/release>
	local dmounts
	ensure_lint_image || return 1
	deploy_mounts
	podman run "${hflags[@]}" --network=none -e HOME=/tmp -e PYTHONDONTWRITEBYTECODE=1 \
		"${dmounts[@]}" -v "$root/.github:/work/.github:ro" -w /work "$(lint_image)" \
		python3 -I "deploy/tests/release/$1"
}

# Тест на хосте. Каталог теста чистится самим тестом; байт-код не пишется (дерево остаётся чистым).
release_on_host() { # <файл теста> <нужные утилиты…>
	local t=$1
	shift
	command -v python3 >/dev/null || skip 'нет python3 на машине' || return $?
	local tool
	for tool in "$@"; do
		command -v "$tool" >/dev/null || skip "нет $tool на машине" || return $?
	done
	(cd "$root" && PYTHONDONTWRITEBYTECODE=1 python3 -I "deploy/tests/release/$t")
}

step_workflow_guard_bypass() { release_in_ctl test_workflow_guard_bypass.py; }
step_workflow_static() { release_in_ctl test_workflow_static.py; }
step_release_gate_cases() { release_on_host test_release_gate.py bash git; }
step_image_composition() { release_on_host test_image_composition.py; }
step_stand_archive_mode() { release_on_host test_stand_archive.py bash; }
step_release_publish_cases() { release_on_host test_release_publish.py bash; }
step_runner_check_cases() { release_on_host test_runner_check.py bash; }
step_ghcr_probe_cases() { release_on_host test_ghcr_probe.py; }
