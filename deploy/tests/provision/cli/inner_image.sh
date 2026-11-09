#!/usr/bin/env bash
# Продолжение inner.sh (source; check, want_rc, want_msg, drive_rc, review, gitc и переменные inv, clone, head0, SHIM_LOG… — оттуда):
# проверка выпущенного образа и раскатка ровно проверенного (спека 004, design §8, §12). Криптография здесь поддельная
# (shim/fake-gh): настоящий gh на фикстуре проверяет шаг image_verify; тут — ход provision.sh: порядок и коды отказов, запись
# verified-image/deployed-image/releases, порог отката, передача образа в Ansible, окно Б, review_ctl_cases.
# Функции origin/seed вызываются из inner.sh рано (дерево и inventory), image_cases — после основных проверок.
# shellcheck disable=SC2154,SC2016,SC2034  # переменные из inner.sh (IMG_REF читает он же); bash -c '…$1…': раскрытие внутри

# Файлы выпуска в «дереве проекта» теста: workflow, go.mod/go.sum, lock действий, ЗАГЛУШКА workflow_guard.py (после слияния
# с И6a заменить настоящим: интерфейс — ops/image.sh, iv_workflow_guard), tools.env с хэшем поддельного архива gh.
image_origin_files() { # <корень дерева origin>
	local o=$1 d tgz=/tmp/gh-fake.tgz
	mkdir -p "$o/.github/workflows" "$o/deploy/release" /tmp/ghsrc/gh_2.103.3_linux_amd64/bin
	# Настоящие workflow и lock действий из дерева (эталонная копия /ref); если .github в копии нет — минимальный файл.
	if [ -d /ref/.github ]; then cp -r /ref/.github/. "$o/.github/"; else printf 'name: release\n' >"$o/.github/workflows/release.yml"; fi
	printf 'module example.org/x\n\ngo 1.22\n' >"$o/go.mod"
	printf 'example.org/y v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n' >"$o/go.sum"
	printf '%s\n' 'import os, sys' 'open(os.environ["SHIM_LOG"], "a").write("GUARD " + " ".join(sys.argv[1:]) + "\n")' \
		'sys.exit(int(os.environ.get("FAKE_GUARD_RC", "0")))' >"$o/deploy/release/workflow_guard.py"
	printf '#!fake-gh\n' >/tmp/ghsrc/gh_2.103.3_linux_amd64/bin/gh
	tar -C /tmp/ghsrc -czf "$tgz" gh_2.103.3_linux_amd64
	d=$(sha256sum <"$tgz" | cut -d' ' -f1)
	sed -i "s/^GH_SHA256=.*/GH_SHA256=$d/" "$o/deploy/ops/tools.env"
	cp "$o/deploy/release/actions.lock" /tmp/lock.fake
	printf 'runs:\n  using: node20\n  main: index.js\n' >/tmp/action.fake.yml
	printf '%s\n' '{"tlogs":[{"baseUrl":"https://rekor.sigstore.dev"}],"certificateAuthorities":[{"subject":{"organization":"sigstore.dev"}}]}' \
		'{"certificateAuthorities":[{"subject":{"organization":"GitHub, Inc."}}]}' >/tmp/root.fake
	export FAKE_GH_TGZ=$tgz FAKE_LOCK=/tmp/lock.fake FAKE_ACTION_YML=/tmp/action.fake.yml FAKE_ROOT=/tmp/root.fake
}

# Состояние окна Б и первая проверка образа для коммита $1: gh-image-id, корень, сверенный lock, улики во входящих.
image_seed() { # <HEAD>
	local sha
	mkdir -p "$inv/sigstore" "$inv/actions-pins"
	printf 'sha256:%064d\n' 0 >"$inv/gh-image-id"
	cp /tmp/root.fake "$inv/sigstore/trusted_root.jsonl"
	sha=$(git -C "$clone" show HEAD:deploy/release/actions.lock | sha256sum | cut -d' ' -f1)
	git -C "$clone" show HEAD:deploy/release/actions.lock >"$inv/actions-pins/$sha.lock"
	printf 'seed-manifest\n' >/srv/sa-inbox/release.manifest.json
	printf '{}\n' >/srv/sa-inbox/release.attestation.jsonl
	IMG_DIG=sha256:$(sha256sum </srv/sa-inbox/release.manifest.json | cut -d' ' -f1)
	IMG_REF=ghcr.io/iliasa101/albums-svc@$IMG_DIG
	printf 'version=v0.1.0\ncommit=%s\ndigest=%s\n' "$1" "$IMG_DIG" >"$inv/verified-image"
	chmod 600 "$inv/verified-image"
}

vi_same() { cmp -s "$inv/verified-image" /tmp/vi.keep; }
# Отказ image-verify (код 3), verified-image не тронут, текст отказа — по маске.
iv_refuse() { # <имя> <маска текста> <env…> -- <аргументы image-verify…>
	local n=$1 msg=$2 rc
	shift 2
	: >"$SHIM_LOG"
	run_plain "$@"
	rc=$?
	if [ "$rc" = 3 ] && grep -qF -- "$msg" /tmp/out.txt && vi_same; then ok "$n"; else
		bad "$n: код $rc, ждали 3 и «$msg», verified-image без изменений"
		tail -n 3 /tmp/out.txt
	fi
}

image_cases() {
	local d2 pins
	cp "$inv/verified-image" /tmp/vi.keep
	printf 'manifest-v0.2.0\n' >/srv/sa-inbox/release.manifest.json
	d2=$(sha256sum </srv/sa-inbox/release.manifest.json | cut -d' ' -f1)
	local env_ok=(FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.2.0)

	# --- image-verify: положительный путь ---------------------------------------------------------------------
	: >"$SHIM_LOG"
	rm -f /tmp/shim-out/* 2>/dev/null
	want_rc "image-verify: верная улика → 0" 0 "${env_ok[@]}" -- image-verify v0.2.0
	check "image-verify: verified-image — версия, коммит HEAD и дайджест из улики (0600)" bash -c '[ "$(cat "$1")" = "version=v0.2.0
commit=$2
digest=sha256:$3" ] && [ "$(stat -c %a "$1")" = 600 ]' _ "$inv/verified-image" "$head0" "$d2"
	check "image-verify: улики сохранены в releases/v0.2.0 (каталог 0700, файлы 0600)" bash -c 'cmp -s /srv/sa-inbox/release.manifest.json "$1/release.manifest.json" && cmp -s /srv/sa-inbox/release.attestation.jsonl "$1/release.attestation.jsonl" && [ "$(stat -c %a "$1")" = 700 ] && [ "$(stat -c %a "$1/release.manifest.json")" = 600 ]' _ "$inv/releases/v0.2.0"
	check "image-verify: workflow_guard.py вызван в контроллере (python3 -I, без сети) ДО gh" bash -c 'g=$(grep -n "^GUARD " "$1" | head -n 1 | cut -d: -f1); h=$(grep -n "^GH attestation verify" "$1" | head -n 1 | cut -d: -f1); [ -n "$g" ] && [ -n "$h" ] && [ "$g" -lt "$h" ] && grep "^RUN" "$1" | grep -q "name=sa-prov-guard-.*--network=none.*python3 -I /src/deploy/release/workflow_guard.py"' _ "$SHIM_LOG"
	check "image-verify: gh по образу, --pull=never, --network=none, не root, улики и корень только на чтение" bash -c 'r=$(grep "^RUN name=sa-prov-gh-" "$1" | grep -- "attestation verify"); for f in --pull=never --network=none "--user 65532:65532" ":/in:ro" ":/root/trusted_root.jsonl:ro" "--read-only" "--cap-drop=ALL" "--log-driver=none"; do grep -qF -- "$f" <<<"$r" || exit 1; done' _ "$SHIM_LOG"
	check "image-verify: флаги gh — репозиторий, workflow, метка, HEAD, раннеры, издатель" bash -c 'g=$(grep "^GH attestation verify" "$1"); for f in "-R IliaSA101/sosninaalbums-cloud" "--signer-workflow IliaSA101/sosninaalbums-cloud/.github/workflows/release.yml" "--source-ref refs/tags/v0.2.0" "--source-digest $2" "--signer-digest $2" --deny-self-hosted-runners "--cert-oidc-issuer https://token.actions.githubusercontent.com" --custom-trusted-root "/release.attestation.jsonl --custom-trusted-root"; do grep -qF -- "$f" <<<"$g" || { echo "нет: $f"; exit 1; }; done' _ "$SHIM_LOG" "$head0"
	check "image-verify: версия gh сверена (--version) до проверки" bash -c 'grep -n "^GH --version" "$1" | head -n 1 | cut -d: -f1 | { read -r a; b=$(grep -n "^GH attestation verify" "$1" | head -n 1 | cut -d: -f1); [ -n "$a" ] && [ "$a" -lt "$b" ]; }' _ "$SHIM_LOG"
	check "image-verify: ни секретов, ни агента (age не запускался), контейнеры только sa-prov-*" bash -c '! grep -q "name=sa-prov-age" "$1" && ! grep -q "^FD agent " "$1" && ! grep "^RUN" "$1" | grep -qv "name=sa-prov-"' _ "$SHIM_LOG"
	cp "$inv/verified-image" /tmp/vi.keep

	# --- image-verify: отказы (код 3, verified-image прежний) -------------------------------------------------
	iv_refuse "image-verify: workflow_guard.py вернул ≠ 0 → 3" "инварианты workflow" "${env_ok[@]}" FAKE_GUARD_RC=1 -- image-verify v0.2.0
	pins=$inv/actions-pins
	mv "$pins" /tmp/pins.keep
	iv_refuse "image-verify: lock коммита не сверен в окне Б → 3, подсказка про окно Б" "окно Б: сверка действий" "${env_ok[@]}" -- image-verify v0.2.0
	check "image-verify: без сверенного lock контроллер с guard не запускался" bash -c '! grep -q "^GUARD " "$1"' _ "$SHIM_LOG"
	mv /tmp/pins.keep "$pins"
	iv_refuse "image-verify: gh вернул ≠ 0 (подпись, метка или коммит не подходят) → 3" "gh attestation verify отказал" "${env_ok[@]}" FAKE_GH_RC=1 -- image-verify v0.2.0
	check "image-verify: при отказе stderr gh показан (причину видно владельцу)" grep -qF 'gh: fake gh: проверка не прошла' /tmp/out.txt
	iv_refuse "image-verify: ESC и BEL в ошибке gh не доходят до терминала (заменены на «?»)" "gh: unsupported media type" "${env_ok[@]}" FAKE_GH_RC=1 FAKE_GH_ESC=1 -- image-verify v0.2.0
	check "ни одного байта 0x1b и 0x07 в выводе image-verify" bash -c '! grep -qP "\x1b|\x07" /tmp/out.txt'
	iv_refuse "image-verify: улика для другого коммита → 3" "gh attestation verify отказал" FAKE_GH_COMMIT=0000000000000000000000000000000000000000 -- image-verify v0.2.0
	iv_refuse "image-verify: улика для другой метки → 3" "gh attestation verify отказал" FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.9.9 -- image-verify v0.2.0
	iv_refuse "image-verify: пустой ответ gh → 3" "пустой результат" "${env_ok[@]}" FAKE_GH_MODE=empty -- image-verify v0.2.0
	iv_refuse "image-verify: событие не push в сертификате → 3" "сертификат не сходится" "${env_ok[@]}" FAKE_GH_MODE=bad_trigger -- image-verify v0.2.0
	iv_refuse "image-verify: подпись другого workflow (buildSignerURI не release.yml) → 3" "сертификат не сходится" "${env_ok[@]}" FAKE_GH_MODE=bad_uri -- image-verify v0.2.0
	iv_refuse "image-verify: два субъекта в утверждении → 3" "субъект утверждения" "${env_ok[@]}" FAKE_GH_MODE=two_subjects -- image-verify v0.2.0
	iv_refuse "image-verify: версия gh ≠ tools.env → 3" "версия gh не равна tools.env" "${env_ok[@]}" FAKE_GH_VERSION=2.0.0 -- image-verify v0.2.0
	cp "$inv/sigstore/trusted_root.jsonl" /tmp/root.keep
	head -c -1 /tmp/root.keep >"$inv/sigstore/trusted_root.jsonl"
	iv_refuse "image-verify: корень без \\n в конце → 3, подсказка про окно Б" "окно Б" "${env_ok[@]}" -- image-verify v0.2.0
	sed -n 2p /tmp/root.keep >"$inv/sigstore/trusted_root.jsonl"
	iv_refuse "image-verify: корень без строки public good → 3" "public good" "${env_ok[@]}" -- image-verify v0.2.0
	cp /tmp/root.keep "$inv/sigstore/trusted_root.jsonl"
	mv "$inv/gh-image-id" /tmp/ghid.keep
	iv_refuse "image-verify: нет образа gh (окно Б не выполнялось) → 3" "нет образа gh" "${env_ok[@]}" -- image-verify v0.2.0
	mv /tmp/ghid.keep "$inv/gh-image-id"
	mv /srv/sa-inbox/release.attestation.jsonl /tmp/att.keep
	iv_refuse "image-verify: нет файла аттестации во входящих → 3" "нет обычного файла" "${env_ok[@]}" -- image-verify v0.2.0
	ln -s /tmp/att.keep /srv/sa-inbox/release.attestation.jsonl
	iv_refuse "image-verify: аттестация — симлинк → 3" "нет обычного файла" "${env_ok[@]}" -- image-verify v0.2.0
	rm -f /srv/sa-inbox/release.attestation.jsonl
	mv /tmp/att.keep /srv/sa-inbox/release.attestation.jsonl
	for v in v1.2 1.2.3 v01.2.3 v1.2.3-rc1; do
		iv_refuse "image-verify: версия «$v» не vX.Y.Z → 3" "vX.Y.Z" "${env_ok[@]}" -- image-verify "$v"
	done
	want_rc "image-verify: лишний аргумент → 3" 3 -- image-verify v0.2.0 --bogus
	want_rc "image-verify: без версии → 3" 3 -- image-verify

	# --- порог отката и упавший configure (configure_image_cases) ---------------------------------------------
	printf 'version=v0.2.0\ndigest=%s\n' "sha256:$d2" >"$inv/deployed-image"
	: >"$SHIM_LOG"
	want_msg "откат: ниже раскатанной без флага → 3" 3 "--allow-downgrade" FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.1.0 -- image-verify v0.1.0
	check "откат: отказ не тронул verified-image" vi_same
	want_rc "откат: с --allow-downgrade → 0" 0 FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.1.0 -- image-verify v0.1.0 --allow-downgrade
	want_rc "откат: раскатанная версия без флага → 0" 0 "${env_ok[@]}" -- image-verify v0.2.0
	printf 'version=v0.1.0\ndigest=%s\n' "sha256:$d2" >"$inv/deployed-image"
	: >"$SHIM_LOG"
	drive_rc "configure: плей упал (новая версия v0.2.0) → ошибка" 1 /tmp/sc.full FAKE_SSH_RC=0 FAKE_ANSIBLE_RC=1 -- configure
	check "configure: после упавшего плея раскатанная версия прежняя (v0.1.0), порог не сдвинут" grep -qx 'version=v0.1.0' "$inv/deployed-image"
	want_rc "откат после упавшего configure новой версии: возврат к раскатанной v0.1.0 без флага → 0" 0 FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.1.0 -- image-verify v0.1.0
	: >"$SHIM_LOG"
	drive_rc "configure: проверенный образ v0.1.0 раскатан → 0" 0 /tmp/sc.full FAKE_SSH_RC=0 -- configure
	check "configure: образ передан как -e albums_svc_image=<имя>@<дайджест> (проверенный)" grep -qF -- "-e albums_svc_image=ghcr.io/iliasa101/albums-svc@sha256:$d2 " "$SHIM_LOG"
	check "configure: deployed-image записан (версия и дайджест), 0600" bash -c '[ "$(cat "$1")" = "version=v0.1.0
digest=sha256:$2" ] && [ "$(stat -c %a "$1")" = 600 ]' _ "$inv/deployed-image" "$d2"
	want_rc "откат: v0.0.9 ниже раскатанной v0.1.0 без флага → 3" 3 FAKE_GH_COMMIT="$head0" FAKE_GH_REF=refs/tags/v0.0.9 -- image-verify v0.0.9

	# --- configure: образ проверен для этого HEAD и нигде больше не задан ---------------------------------------
	cp "$inv/verified-image" /tmp/vi.ok
	printf 'version=v0.2.0\ncommit=%s\ndigest=sha256:%s\n' 1111111111111111111111111111111111111111 "$d2" >"$inv/verified-image"
	: >"$SHIM_LOG"
	want_msg "configure: образ проверен для другого коммита, чем HEAD → 3" 3 "другого коммита" -- configure
	want_msg "up: образ проверен для другого коммита → 3 (до apply)" 3 "другого коммита" -- up
	check "configure/up при коммите образа ≠ HEAD: ни плеев, ни контейнеров" bash -c '! grep -q "^ANSIBLE \|^RUN \|^TOFU " "$1"' _ "$SHIM_LOG"
	rm -f "$inv/verified-image"
	want_msg "configure: образ не проверен вовсе → 3" 3 "provision.sh image-verify" -- configure
	cp /tmp/vi.ok "$inv/verified-image"
	mkdir -p "$inv/ansible/group_vars/all"
	printf 'albums_svc_image: "ghcr.io/example/albums-svc@sha256:0"\n' >"$inv/ansible/group_vars/all/vars.yml"
	want_msg "configure: albums_svc_image задан в group_vars → 3, «уберите»" 3 "уберите" -- configure
	rm -rf "$inv/ansible/group_vars"
	check "ни один отказ не оставил временных каталогов проверки" bash -c '! compgen -G "/run/user/1000/sa-iv.*" >/dev/null'

	# --- review_ctl_cases: невидимые символы в .github, go.mod, go.sum ----------------------------------------
	local br
	for br in "wf:.github/workflows/release.yml:a\xe2\x80\x8bb" "sum:go.sum:a\xe2\x80\xaeb" "mod:go.mod:a\x1bb"; do
		IFS=: read -r name path bytes <<<"$br"
		gitc -C "$HOME/origin" checkout -q -b "ctl-$name" "$head0"
		printf '%b\n' "$bytes" >>"$HOME/origin/$path"
		gitc -C "$HOME/origin" commit -q -a -m "ctl $name"
		gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle "ctl-$name" 2>/dev/null
		review "ctl-$name"
		check "review_ctl: невидимый или управляющий символ в $path — отказ до checkout" bash -c '[ "$1" = 3 ] && [ "$(git -C "$2" rev-parse HEAD)" = "$3" ]' _ "$?" "$clone" "$head0"
		printf '%b\n' "$bytes" >>"$clone/$path"
		gitc -C "$clone" commit -q -a -m "ctl $name"
		want_msg "review_ctl: тот же символ в $path в раскатываемом коммите — отказ guard_content" 3 ".github, go.mod, go.sum" -- configure
		git -C "$clone" reset -q --hard "$head0"
	done
	gitc -C "$HOME/origin" checkout -q -b full-diff "$head0"
	printf 'name: release\non:\n  push: {}\n' >"$HOME/origin/.github/workflows/release.yml"
	printf 'example.org/z v1.0.0 h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\n' >>"$HOME/origin/go.sum"
	gitc -C "$HOME/origin" commit -q -a -m "full diff"
	gitc -C "$HOME/origin" bundle create /srv/sa-inbox/code.bundle full-diff 2>/dev/null
	review full-diff
	check "review: .github/workflows, go.mod и go.sum — ПОЛНЫЙ diff в less, а не --stat" bash -c 'grep -q "^+  push: {}" /tmp/shim-out/less.txt && grep -q "^+example.org/z v1.0.0" /tmp/shim-out/less.txt && grep -q "diff --git a/.github/workflows/release.yml" /tmp/shim-out/less.txt'
	check "review: в --stat остальное, но не .github/go.sum" bash -c '! grep -E "^ (\.github|go\.sum)" /tmp/review.txt'
	git -C "$clone" checkout -q --detach "$head0"
	gitc -C "$HOME/origin" checkout -q main
}
