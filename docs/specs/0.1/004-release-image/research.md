# 004. Выпуск образа — факты и отвергнутое (редакция 3)

Спека — [../004-release-image.md](../004-release-image.md), техрешение — [design.md](design.md).

## 1. Замеры (07.11.2026)

| Что | Результат | Как |
|---|---|---|
| Бинарник `albums-svc` | 10 723 488 байт; gzip — 4 338 186 | `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`, как в `deploy/Containerfile` |
| База `distroless/static-debian13:nonroot`, amd64 | 13 слоёв, 858 693 байт сжатых | манифест платформы из индекса по дайджесту `Containerfile` |
| Образ целиком | ≈ 5,2 МБ сжатых, 14 слоёв (оценка: сумма двух строк выше) | — |
| Слой из `ghcr.io` | переадресация на `pkg-containers.githubusercontent.com` | анонимный токен, `HEAD` слоя чужого публичного образа |
| Раннер `ubuntu-24.04` | `skopeo` 1.13.3, Podman 4.9.3 | README образа раннера (`actions/runner-images`) |
| `actions/attest` | последний выпуск v4.2.2 (04.08.2026), коммит `1e69f48a…`; исполнитель сверяет заново | API GitHub |
| Репозиторий | публичный, числовой id 1440574753; `main` на GitHub ещё нет, ветка по умолчанию `ci/first`; меток и наборов правил нет | API GitHub, `git ls-remote` |

Проверено прототипами второго мнения (07.11, Podman 4.9.3, `gh` 2.103.3): `podman push … oci-archive:` даёт слои
`tar+gzip`, дайджест в `index.json` = sha256 манифеста; `gh attestation verify --bundle --custom-trusted-root` в
контейнере `--network=none --read-only` на публичной аттестации — код 0, чужие `--source-ref`/`--source-digest` —
код 1; в JSON есть числовые id, `buildTrigger`, `runnerEnvironment`, время подписи из Rekor (часы ops-ВМ не участвуют);
после загрузки OCI-архива `podman image exists имя@sha256:D` — 0. Слой GHCR — ответ 307.

## 2. Документация (факты)

- Пакет, ставший публичным, обратно приватным не сделать; новый пакет личного пространства — приватный; связь с
  репозиторием до первой публикации — меткой `org.opencontainers.image.source` (GitHub Docs, «Configuring a
  package's access control and visibility»).
- `actions/attest-build-provenance` с v4 — обёртка над `actions/attest`, новые работы — на `actions/attest`. Права:
  `id-token: write`, `attestations: write`; `artifact-metadata: write` — только для записи хранения, которой нужен
  `push-to-registry: true` и репозиторий организации. `subject-name` — без тега. Публичный репозиторий — публичный
  Sigstore.
- `gh attestation verify`: `--repo`, `--signer-workflow`, `--signer-digest`, `--source-ref`, `--source-digest`,
  `--cert-oidc-issuer`, `--deny-self-hosted-runners`, `--bundle`, `--custom-trusted-root`; предикат по умолчанию —
  `https://slsa.dev/provenance/v1`; с `--bundle` владелец и репозиторий сверяются по именам.
- Блок `permissions` задания заменяет блок workflow целиком; явный блок повышает умолчание репозитория — так ветка
  `ci/*` получает любые права, которые сама попросит.
- Артефакт `download-artifact` v4+ по имени распаковывается прямо в `path`; CVE-2024-42471 (выход за каталог при
  распаковке) исправлена в v4.1.3. Артефакты публичного репозитория скачивает любой вошедший пользователь.
- Метки `vX.Y.Z` корня — версии модуля `github.com/IliaSA101/sosninaalbums-cloud`; после первого запроса к
  `proxy.golang.org` хэш навсегда в `sum.golang.org`.

## 2a. Проверено по документации (07.11.2026, редакция 3; только чтение страниц)

| Вопрос | Ответ | Источник |
|---|---|---|
| Работают ли импостор-коммиты (SHA из форка по имени родителя) | описаны в 2023, отказа GitHub не найдено; аудит `impostor-commit` есть в актуальном zizmor — **не подтверждено**, считаем рабочими | [Chainguard](https://www.chainguard.dev/unchained/what-the-fork-imposter-commits-in-github-actions-and-ci-cd), [zizmor](https://docs.rs/crate/zizmor/latest/source/tests/integration/audit/impostor_commit.rs) |
| Возьмёт ли свой раннер с меткой `ubuntu-24.04` задание `runs-on: ubuntu-24.04` | документация: подбор — по всем меткам, метку `self-hosted` «настоятельно рекомендуют», чтобы задание не попало на размещённые; метки не проверяются (`--no-default-labels` снимает `self-hosted`). Прямого ответа нет — **не подтверждено**, считаем возможным | [Choosing the runner](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job), [Apply labels](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/apply-labels) |
| Ставит ли интерфейс `repo` вместе с `write:packages` | да: «the `repo` scope will also be selected»; обход — ссылка `…/settings/tokens/new?scopes=write:packages` | [Working with the Container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry) |
| В какой версии `gh` закрыта CVE-2025-25204 | 2.67.0 (затронуты 2.49.0–2.66.x: код 0 без подходящих аттестаций) | [GitLab Advisory](https://advisories.gitlab.com/golang/github.com/cli/cli/v2/CVE-2025-25204/), [Debian](https://security-tracker.debian.org/tracker/CVE-2025-25204) |
| Видимость пакета при первой публикации | «the default visibility is private»; связанный пакет наследует права доступа репозитория (о видимости — ни слова) | там же, Container registry |
| `GITHUB_TOKEN` публикует и связывает пакет | «publish … using `GITHUB_TOKEN` … the repository that contains the workflow is linked automatically» | там же |
| Ответ GHCR до создания пакета (`denied` или «нет») | прямого ответа нет; на форумах `denied` — про права — **не подтверждено** | [docker/build-push-action#1178](https://github.com/docker/build-push-action/issues/1178) |
| Поля раннера в API заданий | `runner_name`, `runner_group_id`, `runner_group_name`, `labels`; читать может любой с доступом на чтение | [REST: workflow jobs](https://docs.github.com/en/rest/actions/workflow-jobs) |
| Имя группы размещённых раннеров | «GitHub Actions» — **по памяти**, уточнить на `ci/*` | — |

Прототипы второго мнения (повторный круг, `gh` 2.103.3, без сети): `--signer-workflow` — регулярное выражение с
якорями, регистрозависимо, а `-R` — нет; `--source-ref`/`--source-digest` — точное равенство; разбор bundle по
расширению (`.jsonl` построчно, `.json` — один объект, без расширения — отказ); две одинаковые строки — два
результата; корень без `\n` в конце — последняя строка молча пропускается; хватает строки public good; нет ключа
Rekor, нет CT, истёк срок УЦ — одно и то же сообщение; `trusted-root` идёт в TUF без входа в GitHub; `--jq` даёт TSV;
`FROM scratch` + бинарник от uid 65532 только для чтения — код 0.

## 3. Отвергнутое

**Редакция 1 (до аудита) — что в ней было не так:**
- Задания выпуска в `ci.yml` с условием на метку: ошибка в `if:` пускает публикацию с `ci/*`; файл больше, чем
  нужно читать в полном diff.
- Дайджест и архив из `stand-full`: стенд под root со сторонними образами задавал, что подпишут (Б2).
- Граница — глаза владельца на хосте («коммит = верхний `main`»): агент сам ставит верхний коммит (Б1).
- Дайджест в `deploy/release/albums-svc.ref` через `dev`: значение переносится, а не выводится из подписи, и
  раскатывался `dev`, а не коммит выпуска.

**Предложения аудита и второго мнения, не взятые в этом виде:**
- `tree=` в файле ссылки и сверка дерева на хосте (аудит, Б1): взято сильнее — раскатка ровно коммита выпуска и
  `--source-digest` = `HEAD` в коде, без сверки глазами.
- Публикация и аттестация одним заданием без скриптов репозитория (второе мнение): разделены — `id-token` не
  видит ни `skopeo`, ни токен записи; `publish.sh` проверяется случаями на стенде, встроенный в YAML — нет.
- `attest-build-provenance` и `push-to-registry: true` (второе мнение): обёртку GitHub не советует; улика
  едет артефактом, лишние объекты в пакете не нужны.
- Окружение с обязательным одобрением: одобрение снимается тем же доступом `repo`, в сертификате его нет —
  в ops-ВМ не проверить.

**Пути проверки аттестации:**
- Глазами на хосте (страница аттестаций): нет числовых id, триггера и раннера в виде проверки; доверие к
  отрисовке GitHub; ошибка чтения на каждом выпуске.
- Онлайн в окне Б (`oci://…:vX.Y.Z --bundle-from-oci`): окно Б на каждый выпуск, а оно под `sa-ops` и редкое.
- Улика в репозитории (коммит после выпуска): тогда `HEAD` ≠ коммит выпуска, привязка рвётся.
- `sigstore` (Python) в образе контроллера — без нового канала (PyPI с хэшами уже есть), но большое дерево
  зависимостей, а проверка DSSE-улик GitHub с его CLI — гипотеза; `gh` проверен прототипом.
- cosign — ещё один бинарник того же класса, не проверен.
- `gh` из apt Ubuntu 24.04 — гипотеза второго мнения: там 2.45, без `attestation`.

**Редакция 3:**
- Lock действий только в репозитории (аудит) — его пишет агент; только набор в inventory (второе мнение) — нечего
  читать в diff и сверять в CI. Взяты оба: lock в репозитории, сверенная копия — в inventory (004-19).
- Только ASCII во всём workflow (аудит): имена шагов и комментарии по-русски — правило проекта; запрещены
  невидимые, управляющие и bidi-символы, ASCII — в значимых строках (004-23).
- «Образ проверенного предка» (второе мнение): `image-verify` принимает коммит аттестации — предка `HEAD`, если
  разница только в путях раскатки. Не сейчас: при первой нужде (004-9).
- Выпуск без стенда на метке (стенд уже прошёл на `ci/*` с тем же деревом): теряется «опубликован ровно
  испытанный `D`».
- Отказ `publish.sh` на любой ошибке чтения тега (ред. 2): `denied` до создания пакета сжёг бы метку (004-21).

**Прочее:**
- Пересборка в выпуске; сборка → GHCR → стенд по GHCR: публикуется непроверенное или не то, что проверено.
- `docker/build-push-action`, `docker/login-action`: лишние сторонние действия рядом с токеном.
- cosign с ключом: ключ — секрет на хранение; бесключевая аттестация даёт происхождение без секрета.
- Тег `latest`/`main`, `podman auto-update`: подвижный тег двигает любой прогон Actions — раскатка мимо ops-ВМ.
- Приватный пакет и токен чтения на сервере: против 002-17.
- Перенос метки при провале: хэш модуля уже в `sum.golang.org`, аттестация — в Rekor; новая версия дешевле.
- Числовой id владельца в коде: страж публичных файлов считает его личным идентификатором; id репозитория
  достаточно — репозиторий, пересозданный под тем же именем, получит другой id.
