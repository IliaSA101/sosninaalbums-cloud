# 004. Выпуск образа — техрешение (редакция 3, правки короткого аудита)

Спека — [../004-release-image.md](../004-release-image.md); развилки — [../decisions.md](../decisions.md) (004-1…004-25);
факты и отвергнутое — [research.md](research.md). «Факт» — проверено (кодом, документацией, замером, прототипом),
«гипотеза» — проверяется на первом выпуске (§10) или пробной аренде (§11).

## 1. Файлы и граф заданий

| Файл | Запуск | Что |
|---|---|---|
| `.github/workflows/checks.yml` | только `workflow_call` (вход `version`, выход `digest`) | `check`, `build-image`, `stand-full` |
| `.github/workflows/ci.yml` | `push` в `ci/**`, `workflow_dispatch` | `probe`, `runner-check`, `uses: ./.github/workflows/checks.yml` |
| `.github/workflows/release.yml` | только `push` меток `v[0-9]+.[0-9]+.[0-9]+` | `release-gate`, `checks`, `release-publish`, `release-attest` |

```
release-gate ──────────────┐
checks: check ─────────────┤
        build-image ─ D ───┼─ stand-full (архив + D, может только «нет»)
                           └──────────────┴─► release-publish ─► release-attest (API раннеров → подпись)
```

- `release-publish`: `needs: [release-gate, checks]`; `release-attest`: `needs: [release-gate, checks, release-publish]`.
  `D` в обоих — только `needs.checks.outputs.digest`; `release-attest` не берёт ничего из выходов `release-publish`.
- `release.yml`: `permissions: {}`; `concurrency: {group: release-${{ github.ref }}, cancel-in-progress: false}`.
- Подпись — от `release.yml`; `checks.yml` подписи не касается.

## 2. Инварианты workflow — `deploy/release/workflow_guard.py`

Один скрипт (`python3 -I`) для CI (`workflow_static`, шаг `check`) и ops-ВМ (`image-verify`, в контейнере
контроллера `--network=none`). Вызов (004-30): `python3 -I workflow_guard.py --root <дерево> --paths <файл> [--pins <каталог>]`;
`--paths` обязателен — вывод `git ls-tree -r -z --name-only HEAD` (NUL), правда о дереве коммита: по нему проверяются
«пути только печатный ASCII» и состав `.github/` (ровно `checks.yml`, `ci.yml`, `release.yml`); файл есть в paths, но нет
в `--root` или не обычный файл (`lstat`) — отказ; обхода файловой системы нет. `deploy/ansible/requirements.txt`
обязателен (и в paths, и в `--root`; И6b кладёт его в `/src`): его строка PyYAML с хэшами равна строке
`deploy/release/requirements.txt`, а версия установленного PyYAML равна закреплённой. Коды: 0, 3 (отказ), 2 (вызов). Правила файлов — `wfrules.py`.
Файлы — все в `.github/workflows/` у `HEAD`; незнакомый файл — отказ. Разбор —
**двумя путями со сверкой** (004-25): (а) белый список строк; (б) настоящий YAML-парсер (PyYAML `safe_load`, без
своих тегов). Из обоих строится одно и то же представление (задания, их ключи, права, `needs`, `if`, шаги с `uses`
/`with`/`env`/`run`); расхождение — отказ. PyYAML: в ops-ВМ — из образа контроллера (зависимость ansible-core,
закреплена хэшем в `deploy/ansible/requirements.txt`); в CI — та же строка с хэшами в
`deploy/release/requirements.txt` (равенство строк проверяет сам скрипт), `pip --require-hashes` в venv в
`$RUNNER_TEMP`. Разночтения парсеров YAML (у PyYAML `on` — `True`, YAML 1.1) закрывает (а): допускается лишь
подмножество синтаксиса, где они не расходятся.

**(а) Белый список строк** — отказ на всё незнакомое:
1. Запрещены: `{`, `}` вне `${{ … }}`, `&`, `*` (якорь, ссылка) и `<<:`, `!` (тег), `---`/`...`, табуляция, ключи в
   кавычках, продолжение простого скаляра на следующей строке; многострочное значение — только `run: |`.
   Символы — только из белого списка `ctl_pattern` (`guard.sh`, `review.sh`): печатный ASCII, кириллица, `§ © « » ° — … № → ≠ ≤ ≥`; табуляция,
   NBSP, `–`, `✓`, типографские кавычки, невидимые и bidi-символы — нигде, даже в комментариях (004-31); пути дерева — только ASCII.
2. Ключи — из перечня на каждом уровне (workflow, задание, шаг, `with` — по действию); незнакомый (`shell`,
   `defaults`, `strategy`, `container`, `services`, `continue-on-error` …) — отказ. Единственное исключение —
   `continue-on-error: true` на первой попытке подписи в `release-attest` (§6, 004-46).
3. Слова `uses`, `runs-on`, `container`, `services`, `environment`, `secrets`, `vars`, `continue-on-error`,
   `shell` вне комментария встречаются только в канонической строке `^\s*(- )?<ключ>: <значение>$`; в тексте
   `run:` их нет вовсе.
4. Выражения `${{ … }}` — ровно из перечня, побайтово: `github.ref`, `github.token`, `runner.temp`, `inputs.version`,
   `needs.checks.outputs.digest`, `needs.build-image.outputs.digest` (стенд), `jobs.build-image.outputs.digest`,
   `steps.build.outputs.digest`, `steps.attest2.outputs.bundle-path || steps.attest1.outputs.bundle-path` (одна строка, §6),
   `secrets.GITHUB_TOKEN`, `secrets.RELEASE_PACKAGES_TOKEN`; любое другое (`vars['X']`, `toJSON(secrets)`,
   `format(…)`) — отказ. Внутри `run:` — ни одного.

**(б) Содержательные правила** (по сверенному представлению):
1. `uses:` — `./.github/workflows/checks.yml` или `actions/<имя>@<40 hex> # <тег>`; тройка есть в
   `deploy/release/actions.lock`; в ops-ВМ ещё sha256 lock есть в `~/inventory/actions-pins/` (§7).
2. `runs-on:` — ровно `ubuntu-24.04`; `secrets.` — только из перечня; `environment:` — только `release` у
   `release-publish`.
3. `release.yml`: `permissions: {}` наверху; `on` — только `push.tags` с шаблоном; права заданий ровно:
   `release-gate`, `checks` — `contents: read`; `release-publish` — `contents: read, packages: write`;
   `release-attest` — `contents: read, actions: read, id-token: write, attestations: write`.
4. **«Стоп» не становится зелёным** (Н3-3): `continue-on-error` нет нигде, кроме первой попытки подписи (`attest1`
   в `release-attest`); у заданий `checks`, `build-image`, `stand-full`, `release-*` и у их шагов нет `if:`, кроме
   шага выхода из реестра (`if: always()`) в `release-publish` и повтора подписи в `release-attest` (пауза и `attest2`
   с `if: steps.attest1.outcome == 'failure'`; `attest2` без `continue-on-error`, провал обеих попыток проваливает
   задание; §6 п. 2, 004-46); `needs` — как в §1; `timeout-minutes` — да, `fail-fast`/`strategy` — нет.
5. **Критичные задания закреплены целиком** (004-26 и его дополнение — уровень задания: ключи, `timeout-minutes`, без `name` и `environment`; `${{` в любом `name` — отказ): `release-gate`, `release-publish`, `release-attest`, `build-image`
   (`checks.yml`) и `runner-check` (`ci.yml`) — список шагов, их ключи, `env` и `with` равны эталону `SPECS` в `wfrules.py`
   (свободно только `name`). Следствие: у шагов `uses:` переменных окружения нет; `SHELLOPTS`, `PS4`, `BASH_ENV`, `NODE_OPTIONS`,
   `GH_HOST`, `LD_PRELOAD`, `PATH` и подобное не пройдут; `SA_DIGEST`/`SA_ARCHIVE` — только из `needs…digest` и пути загрузки;
   `release-gate` обязан звать `gate.sh`; `id: build` — у шага `build.sh`. Текст `run` шага сверки раннеров (§6) сравнивается
   по sha256-константе `RUNNER_STEP_SHA` в guard (правка проверки видна дважды), а не с копией в `ci.yml`. Остальные шаги — `env` только
   из `GITLEAKS_*`, `SA_IMAGE_*`. `run:` критичных заданий — ASCII. `secrets.*` — только в `release-publish`; загрузка
   артефактов — только в `build-image` (`checks.yml`) и в заданиях выпуска; нет `actions/checkout` и `download-artifact`
   в `release-attest`. SHA действия с хвостом нулей (заготовка) и lock с такой строкой — отказ (004-27). Неожиданная форма узла
   (`permissions: write-all`, скаляр вместо словаря) — отказ с кодом 3, а не исключение.
6. `checks.yml`: только `on: workflow_call`; `outputs.digest.value` ровно `${{ jobs.build-image.outputs.digest }}`.
7. `download-artifact`: `path` начинается с `${{ runner.temp }}/`; нет `run-id`, `github-token`, `pattern`,
   `merge-multiple`; `upload-artifact` — ровно файл, `if-no-files-found: error`; SHA `download-artifact` — не старше
   v4.1.3 (CVE-2024-42471).
8. Нигде нет `actions/cache`; у `setup-go` — `cache: false`, у `setup-node` — `package-manager-cache: false` (004-33); нет строки `latest`.

## 3. `build-image` и эталон состава

Задание: `contents: read`, checkout без сохранения учётных данных, `deploy/release/build.sh`, `image_check.py`,
загрузка. Кода стенда, Ansible и сторонних образов, кроме двух баз по дайджесту, в нём нет.

- `deploy/Containerfile`, итоговая стадия: `ARG SA_VERSION=""`, `ARG SA_REVISION=""`; `LABEL`
  `org.opencontainers.image.{source=https://github.com/IliaSA101/sosninaalbums-cloud, licenses=AGPL-3.0-only,
  title=albums-svc, version=$SA_VERSION, revision=$SA_REVISION}`. Без `created`.
- `build.sh <архив>`: `podman build` (как сейчас) → `podman push <образ> oci-archive:<архив>` → `D`. Дайджест
  архива везде (здесь, стенд, `publish.sh`) — sha256 байтов блоба манифеста, на который указывает `index.json` с
  ровно одним манифестом; поле `index.json` само по себе не доверяется.
- `image_check.py`: первые слои = слои базы (diff_id); сверху один слой с одним обычным файлом `albums-svc`, 0555,
  0:0; `User` `65532:65532`; `Entrypoint`/`Cmd` как в `Containerfile`; `Env` = базы; метки — ровно пять наших
  (+ `io.buildah.version`); в `history[].created_by` нет `/home/runner`, `RUNNER_TEMP`, путей стенда.
- `upload-artifact` (`albums-svc-oci`, один файл, `retention-days: 7`); `D` — в `GITHUB_OUTPUT` после регулярного
  выражения.

## 4. Стенд в режиме архива

`stand-full` скачивает `albums-svc-oci` в `runner.temp`; `converge_image` получает `SA_IMAGE_ARCHIVE`,
`SA_IMAGE_DIGEST` (`env:`): дайджест архива = `D` → `skopeo copy --preserve-digests` в реестр стенда → чтение по `D`
= `D` → ссылка `<адрес>:5000/albums-svc@D`, как сейчас. Стенд ничего не выгружает. Локально архив делает `build.sh`. Стенд кладёт архив в свой реестр тем же `publish.sh`
(`SA_DEST_TLS_VERIFY=false`, репозиторий `127.0.0.1:<порт>/albums-svc`, тег `stand`). В заданиях проверок `if:` нет:
диагностику цели при провале шага печатает `ci.sh run` (один раз за задание), шага сноса нет — раннер одноразовый (004-28).

## 5. `release-publish` и `publish.sh`

Шаги: `download-artifact` → `$RUNNER_TEMP/release-oci/` → sparse-checkout `deploy/release` в `path: src`, `persist-credentials: false` → вход (`registry.sh login`) → `publish.sh` → выход (`registry.sh logout`)
(`skopeo logout`, `rm -f` файла входа) → загрузка `release-manifest`. Выход — до загрузки публичного артефакта.

Вход: `printf '%s' "$TOKEN" | skopeo login ghcr.io -u "$GITHUB_ACTOR" --password-stdin --authfile "$RUNNER_TEMP/auth.json"`
под `umask 077`; `TOKEN` — `secrets.GITHUB_TOKEN` (запасной путь — `secrets.RELEASE_PACKAGES_TOKEN`) в `env:` шага.

«Ровно один обычный файл `albums-svc.oci.tar`» проверяет сам `publish.sh` по каталогу архива (в `run:` выпуска встроенных шагов нет, 004-29).
`publish.sh <архив> <D> <репозиторий> <тег>` (`set -x` запрещён; `SA_DEST_TLS_VERIFY=false` — только стенд, в `env` шагов выпуска запрещён §2.б.5):
1. Аргументы — регулярными выражениями; репозиторий ровно `ghcr.io/iliasa101/albums-svc` (стенд — свой).
2. Дайджест архива (§3) = `D`.
3. Чтение тега: манифест **прочитан успешно** и sha256 его байтов = `D` → шаг 4 пропускается; прочитан и ≠ `D` →
   отказ «тег занят — новая версия». Любая ошибка чтения (нет пакета, `denied`, сеть) — текст ошибки не
   разбирается → шаг 4: до создания пакета GHCR может ответить не «нет такого» (§10), а метка дорогая. Перевес
   тега и так принят (004-5), подпись ставится на `D` из `build-image`, а не на прочитанное из тега.
4. `skopeo copy --preserve-digests --digestfile <ф> oci-archive:<архив> docker://<репозиторий>:<тег>`; `<ф>` = `D`.
5. По `D` — sha256 сырого манифеста = `D` → `$RUNNER_TEMP/release-manifest/release.manifest.json`.
6. Обязательно и решает: тег → sha256 сырого манифеста = `D`, иначе провал задания, и `release-attest` не идёт.
   Итог в `publish.sh`: `D`, тег, коммит (в `release-publish` истории нет). «Верхний `main` = метка» печатает `release-gate`
   (`fetch-depth: 0`): да/нет, не отказ (004-32); там же проверяется, что метка лёгкая (`git cat-file -t` = `commit`).

## 6. `release-attest`

1. **Сверка раннеров** — первый шаг, без `if:` и `continue-on-error` (§2(б).4); `run:` ASCII, `env: GH_TOKEN:
   ${{ github.token }}`: `gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/jobs?filter=all&per_page=100"`
   — задания **всех попыток** прогона: «перезапуск упавших» оставляет `D` из `build-image` первой попытки, и её
   задания тоже проверяются. Решает `runner_group_id`/`runner_group_name` (группа размещённых раннеров GitHub —
   имя и id §10), а не `labels`: метки — это запрошенный `runs-on`, их своя машина покажет те же. Правила: у
   каждого задания с назначенным раннером — группа GitHub; задание без раннера допустимо только со статусом
   `skipped`/`cancelled`; обязательны хотя бы по разу и с `conclusion: success` имена `checks / build-image`,
   `checks / stand-full`, `release-publish` (точный вид имён заданий повторно используемого workflow — §10);
   пустой ответ, ошибка `gh`, нет обязательного имени → стоп. Текущее задание (`release-attest`) не проверяется —
   его удостоверяет сертификат (`runnerEnvironment`), поэтому сверка живёт в нём. Тот же шаг — задание
   `runner-check` в `ci.yml` (`actions: read`) на `ci/*`, где обязательны `checks / build-image` и
   `checks / stand-full`.
   **Копии при перезапуске (004-46).** `gh run rerun --failed` копирует в новую попытку завершённые задания: те же `name`,
   `runner_id`, `runner_name`, но `runner_group_id = null` (`runner_group_name` при этом верное; так вышло на v0.1.0).
   Задание с `null` группой допустимо, только если в **более ранней** попытке (`run_attempt` — целое, строго меньше) есть
   задание с тем же `name` и тем же `runner_id`, у которого группа верная (0 / «GitHub Actions») и `conclusion: success`
   (GitHub копирует только успешные): иначе копию не отличить
   от задания на чужом раннере, который группы не показал. Копия без оригинала, с подменённым `runner_id`, с оригиналом на
   чужой группе, без `run_attempt` и без имени группы — отказ, как раньше. Оригинал при этом проверяется сам.
2. `actions/attest@<SHA> # v4.x` (`subject-name: ghcr.io/iliasa101/albums-svc`, `subject-digest` из `checks`,
   `push-to-registry: false`); до метки сверить `action.yml` закреплённого SHA: без `predicate-*` — SLSA provenance
   v1, `bundle-path` — `.jsonl`, строка на субъект. **Один повтор (004-46):** Rekor отвечает 500 на единичный запрос
   (v0.1.0). Шаги по порядку: `attest1` (`continue-on-error: true`), пауза `sleep 90` и `attest2` — оба с
   `if: steps.attest1.outcome == 'failure'`, `attest2` без `continue-on-error`: провал обеих попыток проваливает задание,
   успех первой пропускает вторую. Обе попытки — один и тот же `actions/attest@<SHA>` (сверяет guard: `shape` sha не видит).
   Если первая попытка успела создать запись в Rekor и упала позже, в журнале будет вторая запись на тот же `D`: лишняя,
   но безвредная (улику для `image-verify` даёт бандл удавшейся попытки).
3. `upload-artifact` `release-attestation`: `bundle-path` удавшейся попытки (`attest2 || attest1`; у пропущенного шага
   выход пуст).

## 7. Пины действий

- `deploy/release/actions.lock`: строки `actions/<имя> <тег> <40 hex>` (имя `[a-z0-9][a-z0-9._-]*`, тег `v[0-9][A-Za-z0-9._-]*`, комментарии
  `#` — строкой целиком, хотя бы одна строка, SHA без хвоста нулей); владельцы — только `actions` (константа).
- **Окно Б** (`prepare`, GitHub открыт): для каждой строки `git ls-remote https://github.com/actions/<имя>
  refs/tags/<тег> 'refs/tags/<тег>^{}'`; берётся строка с именем **ровно** `refs/tags/<тег>^{}` (аннотированная),
  иначе ровно `refs/tags/<тег>` (лёгкая) — хвост имени вроде `refs/heads/x/refs/tags/<тег>` не принимается;
  коммит = sha, иначе отказ. `ls-remote`
  показывает только ссылки самого репозитория — коммит из форка не пройдёт. Сверенный lock копируется в
  `~/inventory/actions-pins/<sha256 lock>.lock`. Пути `deploy/release/actions.lock` и `deploy/ops/tools.env`
  входят в проверку «нужно ли окно Б». Там же (004-38): `action.yml` закреплённого SHA берётся с `raw.githubusercontent.com`
  (`action.yaml` — только при 404 на `action.yml`) и разбирается PyYAML (`safe_load`) в контейнере контроллера без сети:
  `runs.using` — строка `node<N>`, нет `docker://`; composite (плавающие `uses:` внутри) и docker-образ по тегу — отказ.
  Построчный разбор не годится: приманка `using: node20` в описании. Та же проверка при закреплении нового пина — в CI.
- **`image-verify`** (офлайн): правило §2.1 + sha256 lock `HEAD` есть в `~/inventory/actions-pins/`; иначе код 3
  «окно Б: сверка действий». Возврат к старому выпуску со старым lock — тоже через окно Б (тихого отката на старую
  версию действия нет).

## 8. ops-ВМ: доставка, проверка, раскатка

**Код.** bundle несёт `main`; `review.sh main`: полный diff `deploy/`, `.github/`, `go.mod`, `go.sum` от
`deployed-commit`, остальное — `--stat`; `ctl_pattern` (управляющие и невидимые символы) — на те же пути, в
`review.sh` и `guard_content`. **Первый раз** `main` смотрит старый `review.sh` (его ставит `configure` из
раскатанного коммита), поэтому шаг README: после `review.sh`, до любого скрипта репозитория —
`git -C ~/src/sosninaalbums-cloud diff --text <deployed-commit или пустое дерево> HEAD -- .github go.mod go.sum | less`
(системный git, код репозитория не исполняется).

**Улики.** Рабочая ВМ кладёт во входящие `release.attestation.jsonl` и `release.manifest.json` (временное имя →
`mv`). `image-verify` берёт ровно эти два имени тем же `dd`, что bundle (`nofollow`, предел 1 МиБ), во временный
каталог 0700, сохраняя расширение `.jsonl` (`gh` выбирает разбор по расширению — факт, прототип). Прочие файлы
входящих не читаются.

**`gh` и корень (окно Б).** В коде `image-verify`: адрес `https://github.com/cli/cli/releases/download/v<v>/gh_<v>_linux_amd64.tar.gz`
литералом, `v` — регулярным выражением и не ниже `2.103.3` (проверен прототипом; CVE-2025-25204 закрыта в 2.67.0).
`deploy/ops/tools.env` — версия и sha256; `prepare` сверяет sha256 и с ним, и с `gh_<v>_checksums.txt` выпуска;
образ `FROM scratch` + бинарник, его ID — в `~/inventory`; `image-verify` запускает по ID с `--pull=never` и
сверяет `gh --version` с `tools.env`. Корень — в каждом окне Б: `gh attestation trusted-root > файл` (ровно вывод,
с `\n` в конце; в образе `FROM scratch` нет корневых сертификатов — к контейнеру монтируется `/etc/ssl/certs/ca-certificates.crt:ro`,
подлинность корня даёт подпись TUF, TLS — только транспорт) → `~/inventory/sigstore/trusted_root.jsonl`, печать даты. При проверке: файл кончается `\n`, есть
строка с УЦ `O=sigstore.dev`, иначе код 3. Код 3 от `gh` → одно обновление корня в окне Б → повтор → снова 3 —
стоп и разбор («незнакомый ключ» от подделки не отличить — факт, прототип).

**`provision.sh image-verify vX.Y.Z [--allow-downgrade]`:**
1. `guard`; `HEAD` отсоединён, дерево чистое; версия — регулярным выражением.
2. `workflow_guard.py` по `HEAD` (§2, §7) → иначе код 3. Вызов (004-36): `python3 -I workflow_guard.py --root <дерево> --paths <файл>
   [--pins <каталог>]`, коды 0 / 3 (отказ) / 2 (ошибка вызова); в ops-ВМ — в контейнере контроллера без сети, `--root` = `/src`,
   `--pins` = `/pins`, `--paths` = `/in/paths` (вывод `git ls-tree -r -z --name-only HEAD`). `/src` строится из blob'ов `ls-tree` +
   `cat-file` (не `git archive`: `export-ignore`/`export-subst` из `.gitattributes` дерева исказили бы то, что видит guard) и
   содержит `.github`, `deploy/release` и `deploy/ansible/requirements.txt` (по нему guard сверяет строку PyYAML).
3. Улики → временный каталог; `D` = sha256 манифеста.
4. `podman run --rm --pull=never --network=none --read-only --user 65532:65532 --cap-drop=ALL
   --security-opt=no-new-privileges --tmpfs /tmp -e HOME=/tmp -v <улики>:/in:ro
   -v ~/inventory/sigstore/trusted_root.jsonl:/root/trusted_root.jsonl:ro <ID> attestation verify
   /in/release.manifest.json --bundle /in/release.attestation.jsonl --custom-trusted-root /root/trusted_root.jsonl
   -R IliaSA101/sosninaalbums-cloud --signer-workflow IliaSA101/sosninaalbums-cloud/.github/workflows/release.yml
   --source-ref refs/tags/vX.Y.Z --source-digest <HEAD> --signer-digest <HEAD> --deny-self-hosted-runners
   --cert-oidc-issuer https://token.actions.githubusercontent.com --format json`. `--signer-workflow` —
   путь workflow без экранирования (`IliaSA101/sosninaalbums-cloud/.github/workflows/release.yml`), регистрозависим:
   gh 2.103.3 сам экранирует значение и сравнивает буквально по всей длине (факт, опыт: с `\.`, с префиксом и с `.`
   как любым символом проверка отказывает; тесты `image_verify_cases` закрепляют это при смене версии gh, 004-35);
   `--cert-identity` не использовать. Контейнер gh — ещё и `--userns=keep-id:uid=65532,gid=65532` (каталог улик 0700
   принадлежит учётной записи раскатки, без отображения uid он недоступен процессу 65532; 004-37).
5. Решение — только по JSON: код `gh` ≠ 0, пустой массив → 3. Для **каждого** результата из
   `verificationResult.signature.certificate` (то, что выписал Fulcio, а не предикат): `sourceRepositoryIdentifier`
   = 1440574753, `buildTrigger` = `push`, `runnerEnvironment` = `github-hosted`,
   `sourceRepositoryVisibilityAtSigning` = `public`, `buildConfigURI` и `buildSignerURI` — точно
   `https://github.com/<workflow>@refs/tags/vX.Y.Z` (SAN, который проверяет gh, не говорит, какой файл запустил подпись:
   reusable-вызов `release.yml` из другого файла дал бы тот же SAN; 004-39); из утверждения — ровно один субъект
   `ghcr.io/iliasa101/albums-svc` с `D`. Поля — `--jq` самого `gh` (TSV), без своего разбора JSON.
6. Откат: версия ниже `version` в `~/inventory/deployed-image` → 3 без `--allow-downgrade`.
7. Запись (`umask 077`, временный файл + `mv`): `~/inventory/verified-image` (`version`, `commit`, `digest`);
   улики → `~/inventory/releases/vX.Y.Z/` (артефакты прогона живут 90 дней; откат позже — без сети).

**`configure`, `up`** (строже, чем «при `albums-runtime`»: образ проверяется всегда, 004-40): `verified-image.commit` ≠ `HEAD` → 3. `albums_svc_image` передаётся
в Ansible из `verified-image` как `-e` (высший приоритет; другой источник его не перебьёт); `grep` по `group_vars`
и `host_vars` — отказ кодом 3 с подсказкой «уберите» (004-37). После успешного `site.yml` — `~/inventory/deployed-image` (версия и `D`).
`inventory.example`: `albums_svc_image` убран из `vars.yml`, пояснение «пишет image-verify».

**README для третьих лиц:** проверять образ с `--signer-workflow …/release.yml --source-ref refs/tags/vX.Y.Z`
(с одним `-R` проходит и аттестация из прогона `ci/*`); брать только по дайджесту. То же — в описании пакета.

## 9. Настройки GitHub, токены, вопросы владельцу

Всё — из браузера хоста, каждое — «да» владельца, по одному:

1. **До первой метки:** токен рабочей ВМ — fine-grained на один репозиторий: Contents RW, Workflows RW, Actions R,
   без Administration, Packages, Secrets, Variables, Environments; со сроком. Пока не заменён — перед каждым
   выпуском список раннеров репозитория (настройки → Actions → Runners) пуст, смотреть с хоста.
2. Первый push `main`; ветка по умолчанию `main` (сейчас `ci/first`).
3. Наборы правил: `main` — Restrict updates/deletions, Block force pushes; `refs/tags/v*` — Restrict
   creations/updates/deletions; обход — администратор. Защита от `GITHUB_TOKEN` и ошибки, не от агента.
4. Окно Б в ops-ВМ: `gh`, корень Sigstore, пины действий (новые зависимости; часы ops-ВМ должны быть верны — TUF).
5. `main` + лёгкая метка `v0.1.0` — это и публикация; первая метка создаёт пакет (по документации — приватным, §10).
6. Пакет: доступ Actions — только этот репозиторий, роль Write; публичность (необратимо) — до первого `configure`.
7. Подтвердить `licenses=AGPL-3.0-only`. Пробная аренда — трата.
8. По желанию — пробная аттестация на `ci/*` с субъектом-пустышкой до v0.1.0 (запись в Rekor вечная); без неё
   ошибка в положительном пути `image-verify` стоит версии (v0.1.1). Пропущена (004-47): так и вышло, v0.1.0 сгорела
   на сбое Rekor и сверке раннеров при перезапуске.
9. **Перезапуск прогона Release** (`gh run rerun`, `gh api …/rerun*`) — это выпуск: только с «да» владельца, как
   постановка метки (004-48). `image-verify` видит коммит и метку, но не номер попытки и не актора перезапуска; копии
   заданий в новой попытке сверка раннеров принимает только при верном оригинале (§6 п. 1), но кто и зачем перезапустил,
   не показывает ни она, ни улика.

**Запасной токен** (только если `GITHUB_TOKEN` не создаст пакет — по документации создаст, §10): классический,
выпуск на хосте по ссылке `https://github.com/settings/tokens/new?scopes=write:packages`; галочка `write:packages`
в интерфейсе сама ставит `repo` (факт, документация GHCR) — снять, до сохранения и в списке токенов после —
только `write:packages`/`read:packages`; срок 1 день; секрет `RELEASE_PACKAGES_TOKEN` окружения `release`
(развёртывание — метки `v*`). **Агент прочтёт токен**, пока секрет существует: любой workflow на любой метке `v*`
с `environment: release` его выведет, а метку агент ставит в обход правил. Ущерб — запись во все пакеты владельца
на срок токена. Поэтому секрет удаляется сразу после зелёного `release-publish`, токен отзывается в тот же час;
в рабочую ВМ и ops-ВМ не вводится.

## 10. Гипотезы и то, что проверено по документации

| Что | Статус (источник — research §2) | Если нет |
|---|---|---|
| Импостор-коммиты из форка исполняются | гипотеза: опровержения нет, аудит `impostor-commit` в zizmor жив | меры §7 нужны всё равно |
| Свой раннер с меткой `ubuntu-24.04` возьмёт задание | не подтверждено: документация лишь советует метку `self-hosted`, метки не проверяет | меры §6, §9.1 |
| Новый пакет приватен | факт (документация GHCR: «default visibility is private») | — |
| `GITHUB_TOKEN` создаёт пакет и связывает с репозиторием | факт по документации; проверить на выпуске | запасной токен §9 |
| GHCR до создания пакета отвечает `denied`, а не «нет» | не подтверждено (форумы: `denied` — права) | §5 п. 3 и так не сжигает метку |
| Имя и id группы размещённых раннеров в API; имена заданий повторно используемого workflow | по памяти: «GitHub Actions», id 0; «checks / build-image»; поля `runner_group_*` — факт | уточнить на `ci/*` до кода сверки |
| `skopeo` 1.13 держит дайджест | гипотеза | `podman push --digestfile` со сверкой |
| `gh` проверяет манифест как файл | гипотеза | улика — `oci-archive` |
| перезапуск упавших заданий сохраняет вывод `checks` | гипотеза | новая версия |
| копии заданий при перезапуске приходят с `runner_group_id = null` | факт (v0.1.0, прогон 42380708848); правило §6 п. 1 | — |
| сбой Rekor временный | факт на v0.1.0 (500 на создании записи журнала); один повтор §6 п. 2 | v0.1.x заново |

## 11. Сеть ЦОД CLO → GHCR

Образ ≈ 5,2 МБ, 14 слоёв; слои — с `pkg-containers.githubusercontent.com`; из ops-ВМ `ghcr.io` скачивается
целиком, ECR — нет (ВР-16). Сеть ЦОД неизвестна.

- **Проба** `ghcr_probe` (004-41) — первое, что `configure` делает на сервере, до `bootstrap.yml` и любой правки: функция `ghcr_probe` в
  `ops/probe.sh` отправляет `deploy/ops/ghcr_probe.py` по ssh через stdin (`python3 -I -`; stdlib; вход администратором, а при первом
  подъёме — пользователем bootstrap, под root с `runuser -u nobody`): анонимный токен по 401 (адрес выдачи — на узле реестра), манифест по
  `D` (sha256 = `D`), все блобы целиком по `Location` без токена на адрес хранилища слоёв, размер и sha256 каждого по манифесту, ничего не
  пишет; пределы: общее время (600 с), нет байта 30 с, средняя скорость (20 КиБ/с после 15 с), манифест 1 МиБ, блоб 64 МиБ, всего 128 МиБ,
  слоёв 64. Не вышло — код 3, «сервер не изменён», подсказка на план Б. Ловит именно неполное скачивание (ответ 200, обрезка на 16 КиБ, ВР-16).
- **Пробная аренда:** проба и `pull`, время — в research; предел пробы и `timeout` у `podman pull` — по замеру
  (до замера — 600 с как страховка от вечного зависания, гипотеза). Предел `pull` — `albums_svc_pull_timeout_sec` роли `albums_svc`
  (`timeout --kill-after=15`, коды 124 и 137 называются в отказе; границы 60-3600 проверяются и в `assert.yml`, и в `runtime.yml`); предел
  пробы — `ghcr_probe_sec` в `ops/probe.sh`; на сервере проба под `timeout -k 5 <срок + 15>`, чтобы не пережить оборванный ssh (004-45).
  **Чек-лист пробной аренды:** (1) настоящий прогон `configure` до `bootstrap.yml`: цепочка `ssh → timeout → runuser -u nobody → python3 -I -`
  под root (первый подъём) и под администратором (повтор); на стенде и в CI она не исполняется; (2) замер времени пробы и `podman pull`,
  по нему предел 600 с; (3) сверка порога средней скорости 20 КиБ/с после 15 с с фактом (на каждый блоб два TLS-соединения: реестр и
  хранилище слоёв, при высокой задержке и мелких первых слоях порог может сработать зря).
- **План Б** (отдельная спека): архив через ops-ВМ (`skopeo copy …@D oci-archive:` в окне Б; на сервере дайджест
  архива = `D`, `skopeo copy oci-archive: containers-storage:` от `albums`, проверка по `D`). Проверенные улики уже
  хранятся по версиям (§8).

## 12. Случаи тестов

- `workflow_guard_bypass` (мутации `workflow_guard.py` по каждому обходу Н3-1, на копии настоящих файлов):
  `- {uses: actions/attest@<чужой sha>}` (поток), `"uses": …` и `'runs-on': …` (ключ в кавычках), якорь `&a`/`*a`
  и `<<: *a`, тег `!!str`, простой скаляр с продолжением на следующей строке, `${{ vars['X'] }}`,
  `${{ toJSON(secrets) }}`, `${{ format('{0}', secrets.X) }}`, второй документ `---`, табуляция, `shell: bash {0}`,
  `defaults:`, `continue-on-error: true` на шаге сверки раннеров и на `build-image`, `if: always()`/`if: false` на
  шаге сверки и на задании `build-image`, `uses:` в тексте `run:`, кириллический двойник в адресе внутри `run:`
  задания выпуска, вызов не из `deploy/release/` в `run:` `release-publish`, путь дерева не ASCII; расхождение
  представлений (а) и (б) на синтетическом файле → отказ; строка PyYAML в `deploy/release/requirements.txt` ≠
  `deploy/ansible/requirements.txt` → отказ.
- `workflow_static`: каждое правило §2(б) — мутация на копии → провал (`vars.`, `secrets.X`, `secrets: inherit`,
  `environment:`, `container:`, `services:`, чужой `runs-on`, `${{` в `run:`, `uses:` вне lock, невидимый символ, не-ASCII в `uses:`).
- `release_gate_cases`: не `main`, `v1.2`, `v1.2.3-rc1`, `v01.2.3`, не push → отказ; на `main` — да.
- `image_composition`: лишний файл, чужой `Env`, лишняя метка, путь раннера в истории → провал.
- `stand_archive_mode`: архив с `D` — в реестре ровно `D`; другой `D` → отказ.
- `release_publish_cases`: первый push; повтор тем же `D`; тег прочитан с другим `D` → отказ; ошибка чтения тега
  (`denied`, обрыв) → запись и решающая сверка шага 6; после записи тег перевешен другим образом → провал, без
  аттестации; архив не тот, `index.json` с двумя манифестами, артефакт не один файл,
  `mutate_release_artifact` → отказ; токен-приманка не в выводе; файл входа удалён до загрузки.
- `workflow_static` / `workflow_guard_bypass`, повтор подписи (004-46): `continue-on-error` на `attest2`, на `upload-artifact`
  в `release-attest` и на шаге `checks.yml` → отказ; `continue-on-error` снят с `attest1` или `false` → отказ; `if` на
  `attest2` или на паузе пропущен / `always()` → отказ; `sleep 9000`; пауза или `attest2` выброшены; выход загрузки
  только из `attest1`; две попытки с разными `sha` (оба в lock) → отказ.
- `runner_check_cases` (синтетические ответы API, тот же текст шага): чужая группа при верной метке → стоп;
  чужая группа на второй странице → стоп; нет `checks / build-image` в ответе → стоп; пустой ответ, ошибка `gh` →
  стоп; вторая попытка: `build-image` первой попытки на чужой группе → стоп; задание без раннера со статусом
  `skipped` — допустимо, с `success` — стоп; верный ответ → да. Копии при перезапуске (004-46; фикстура — настоящий ответ
  API): снимок v0.1.0 → да; подменён `runner_id` копии, `runner_id` копии равен `runner_id` оригинала другого задания,
  нет оригинала, оригинал на чужой группе, оригинал не `success`, оригинал в той же или более поздней попытке, нет
  `run_attempt` (или bool), чужое имя группы у копии → стоп.
- `actions_pins_cases`: пин вне lock, lock не сверен (нет в `actions-pins`), `ls-remote` дал другой коммит →
  отказ/код 3; в ответе `ls-remote` есть только `refs/heads/x/refs/tags/<тег>` → отказ; аннотированная метка —
  берётся `^{}`, а не объект метки; `action.yml` с `using: composite` или `docker://…:<тег>` → отказ.
- `image_verify_cases` (`--network=none`, фикстура публичной аттестации `cli/cli`; константы — копией файла
  констант в копии дерева, не флагом и не переменной боевого пути): верный → 0; неверна любая константа-флаг → 3;
  испорчен бит подписи → 3; `.json` из двух строк, файл без расширения → 3; корень без `\n`, без public good, без
  ключа Rekor → 3 с подсказкой про окно Б; `trusted_root.jsonl` во входящих — не используется; `buildTrigger` ≠
  push (фикстура — `workflow_dispatch`) → 3; две одинаковые строки → 0, проверены оба результата; пустой массив →
  3; `gh` ниже минимума или версия ≠ `tools.env` → 3; доп. проверки — модульно на синтетическом JSON.
- `review_ctl_cases`: невидимый символ в `.github/workflows/*.yml`, в `go.sum` → отказ.
- `configure_image_cases` (`provision_cli`): коммит образа ≠ `HEAD` → 3; возврат к раскатанной версии после
  упавшего `configure` новой — без флага; ниже раскатанной — только с флагом.
- `ghcr_probe_cases` (`test_ghcr_probe.py`, реестр-заглушка на петлевом адресе; `provision_cli`, `cli/inner_probe.sh`): успех; токен только на
  реестр, не на хранилище слоёв; обрыв на 16 КиБ при полном и при честном `Content-Length`; тот же размер, другие байты; манифест не равен `D`; нет
  ответа и пустое тело → отказ по времени; медленно; байт за байтом без закрытия соединения (краснеет и на мутации `tick`); адрес выдачи токена на другом узле, переход на другую схему, токен с лишними знаками; индекс
  вместо манифеста, слишком много слоёв, огромный блоб, плохой дайджест; переход по http на непетлевой узел, порт `:abc`, `Content-Length` не из ASCII-цифр, глубокий JSON токена → отказ с текстом, без трейсбека; ошибки вызова → код 2; `pull` под `timeout` с переменной и границами.
  В `configure`: проба раньше любого плея и раньше `bootstrap.yml`; отказ → код 3, плеев нет, `deployed-*` прежние; тексты по кодам 1, 2, 124, 137, 127, 255 (для 255 и 127 — без «плана Б», строки ssh без метки «сервер:»); ESC и BEL
  из вывода сервера заменены; при смене ключа хоста скрипт на сервер не уходит.
- После v0.1.0: своя аттестация — фикстура с боевыми константами.
