# 002. Подъём сервера — техрешение

Основной файл — [../002-provisioning.md](../002-provisioning.md). Пометки: **факт** — проверено прототипом,
кодом или документацией (research §1); **гипотеза** — проверить при реализации или на пробной аренде.

## 1. Раскладка

Публичный репозиторий (новое — в `deploy/`, рядом с артефактами И1):

```
deploy/
  provision.sh                    точка входа владельца (§2); tofu, ansible и age — в контейнерах
  ops/review.sh                   просмотр нового кода до checkout (§2); копия — в ~/.local/libexec/sa/
  ops/guard.sh, secrets.sh        отказы до действий (код 3); контейнеры sa-prov-*, файл секретов, токен, ssh-agent
  ops/ctl-run.sh                  запуск контроллера Ansible — один код для provision.sh и стенда (§2, §12)
  ops/backup-lib.sh, sa-backup-prune.sh, sa-backup-prune.service, .timer   чистка и свежесть копий (§11)
  ops/phrase-fd.sh, images.env    подача фразы в ssh-add (§2); образ tofu по дайджесту
  tofu/clo/                       versions.tf, variables.tf, main.tf, outputs.tf, .terraform.lock.hcl,
                                  tests/plan.tftest.hcl, tofu-entry.sh (обёртка: токен из файла → exec tofu);
                                  других файлов нет (§3)
  ansible/
    Containerfile, requirements.in/.txt   контроллер: python по дайджесту, pip --require-hashes, age (§4)
    ansible.cfg, bootstrap.yml, site.yml, verify.yml
    roles/bootstrap, nftables, upgrades, journald, guest_agent, cloud_init         (И3)
    roles/podman, caddy, albums_svc, backup_access                                 (И4)
  backup/sa-backup-stream.sh, sa-restore.sh   (И4) поток копии и разбор/распаковка при возврате (§11)
  systemd/sa-backup.socket.j2, sa-backup@.service.j2   (И4) шаблоны: группа бэкапа и RuntimeMaxSec — из inventory
  systemd/caddy-albums.conf, albums-svc.tmpfiles.conf  drop-in юнита Caddy (§8); каталог сокета сервиса (§9)
  quadlet/albums-svc.container.j2             юнит сервиса (§9); образ — по дайджесту из inventory
  caddy/Caddyfile, caddy-stable-archive-keyring.asc   Caddyfile (И1); (И4) открытый ключ apt-репозитория Caddy (§8)
  inventory.example/              provision.env, tofu/clo.tfvars.example, ansible/hosts.yml.example,
                                  ansible/group_vars/all/{vars.yml, vault.yml.example}
  tests/provision/                стенд и проверки (§12); с И4 — service.sh (роли И4 и мутации), stream.sh (поток копии)
  README.md                       + runbook учётной записи раскатки (§2)
```

Роли берут файлы из `deploy/` по пути от `playbook_dir` (`../caddy/Caddyfile`, `../quadlet/…`, `../systemd/…`,
`../../web/viewer/notfound.html`): одна копия артефакта. Юниты со значениями inventory (`sa-backup.socket` —
группа, `sa-backup@.service` — `RuntimeMaxSec`, Quadlet — образ) — шаблоны `.j2` (модуль `template`), остальное —
`copy` (И4-9). Поэтому в контроллер из **клона учётной записи
раскатки** монтируются только `deploy/` и `web/viewer/notfound.html` — на чтение и по каноническому пути
(`realpath`; симлинк на месте `deploy`, `web` или `web/viewer` — отказ, Р-2); inventory — отдельным томом.
Рабочее дерево разработки не монтируется никогда.

Дом учётной записи раскатки (0700):

```
~/src/sosninaalbums-cloud/   клон, HEAD отсоединён на просмотренном коммите (§2)
~/.local/libexec/sa/         review.sh, sa-backup-prune.sh, backup-lib.sh — копии из развёрнутого коммита (0700)
~/.config/systemd/user/      sa-backup-prune.service, .timer — копии оттуда же
~/secrets.age                файл секретов, age по фразе, 0600 (SA_SECRETS, §2)
~/inventory/                 приватный inventory (свой git): provision.env, deployed-commit, known_hosts,
                             tofu/clo.tfstate (в git), tofu/.terraform-clo/ и .lock (не в git), ansible/hosts.yml,
                             ansible/group_vars/all/vault.yml (ansible-vault)
~/backups/                   копии <UTC-дата>.tar.zst.age (SA_BACKUP_DIR)
~/.ssh/                      id_server и id_backup — ed25519 с фразой
```

Каталог входящих (`/srv/sa-inbox`, `SA_INBOX`) — для bundle с кодом (§2): вне дома владельца и вне любого
git-дерева; пишет владелец, читает раскатка. *(Поправка 03.11, §16.2: это точка монтирования общей папки VMware — в рабочей ВМ на запись, в ops-ВМ только на
чтение.)* Файлов плана нет (§2). Где лежит удалённый git inventory — решение
владельца (приватные заметки).

## 2. `deploy/provision.sh` и учётная запись раскатки

> **Поправка 03.11.2026 (§16).** Машина раскатки — отдельная ВМ (ops-ВМ). Пункты «Создаёт владелец… recovery»,
> «Вход — только отдельной сессией на текстовой консоли» и «Проверка сессии» ниже заменены §16.1 и §16.4, `head -c`
> в «Просмотре до checkout» — §16.4 (у каждого места пометка); граница — ВМ, а не uid и сессия. Остальное в силе.

Команды: `up` (= `apply` + `configure`), `apply [--replace-server]`, `configure`, `backup`, `forget-host`.
Отдельной команды `plan` нет: интерактивный `apply` показывает план, «no» отменяет.

### Граница доступа — uid и сессия

Процессы Claude Code работают под учётной записью владельца: у них его `SSH_AUTH_SOCK`, история оболочки,
хранилище Podman, файлы. Любой секрет или файл кода под этим uid им доступен: токен — из `podman inspect` или
`/proc/<pid>/environ`; роль в рабочем дереве можно подменить (её задача выполнится от root на сервере с открытым
vault); в HCL можно добавить провижинер `local-exec`; образ контроллера можно переписать (аудит, прототипы).
Графическая сессия владельца — тоже его: в X11 его процесс вводит нажатия в чужие окна (XTEST, `xdotool type`) и
читает клавиатуру (XInput) — пароль, фразу, «yes» (аудит). Поэтому граница — **отдельная учётная запись ОС**
(по умолчанию `sa-ops`, переменная `SA_OPS_USER`) **и отдельная сессия**:

- *Заменено §16.1, §16.7.* **Создаёт владелец, один раз, в recovery-режиме GRUB** (runbook в `deploy/README.md`, Р-6, Р-15, Р-21):
  меню GRUB (Shift или Esc при загрузке) → «Advanced options» → «recovery mode» → пункт «root»;
  `systemd.unit=rescue.target` не годится — при заблокированном root `sulogin` не пускает. Перед перезагрузкой
  владелец сам завершает свои сеансы (`loginctl terminate-user "$USER"`, в том числе графический), в recovery —
  `ps -u <владелец>` пуст, linger владельца нет. Это root-оболочка без файлов запуска владельца, его агентов и
  графики. Под учётной записью владельца root-оболочку не открывать:
  её файлы запуска (`~/.bashrc`, `~/.profile`, `environment.d`) пишут агенты, а перехватчик на pty видит пароль
  `sudo`; в обычном режиме root-оболочка не нужна совсем. Там: пользователь с домом 0700 и своим паролем
  (задаётся на этой консоли); свой диапазон subuid/subgid (не пересекается с диапазоном владельца); linger —
  файлом `/var/lib/systemd/linger/<имя>` (logind в recovery не запущен) — для `XDG_RUNTIME_DIR` и пользовательского
  Podman; без прав `sudo`; не в группах владельца, `sudo`, `adm`, `systemd-journal`, `plugdev`, `video`, `input`;
  владелец не в `input` и `tty` и без ACL на `/dev/input/event*` (иначе читает и подделывает нажатия на консоли);
  `pam_access` — учётная запись раскатки входит только на `tty1`–`tty6` (`login`, `su`, `sshd`, PAM экранного
  менеджера, И2-6); `mesg n`. Каталог входящих создаёт там же root: 0755, владелец и группа — владелец, вне любого
  git-дерева; дом владельца раскатке не открывается.
- *Заменено §16.1.* **Вход — только отдельной сессией на текстовой консоли** (VT, getty) с паролем раскатки. Перед входом сеансов
  владельца быть не должно: его графический сеанс может переключить VT (`chvt` через polkit) и подменить ввод.
  Порядок (Р-15): выйти из графического сеанса (не «сменить пользователя»), автовход выключен,
  `loginctl list-sessions` без сеансов владельца. `sudo -iu`, `su`, `machinectl shell` из сессии владельца runbook
  запрещает: `~/.bashrc` или `PATH` владельца подменяют `sudo` и крадут пароль, нажатия в терминале
  перехватываются, а `XDG_RUNTIME_DIR` и D-Bus под `sudo -i` не настраиваются (Podman уходит в
  `/tmp/storage-run-<uid>`, нет `systemctl --user` — аудит).
- *Заменено §16.4.* **Проверка сессии** (до всего остального): `loginctl show-session "$XDG_SESSION_ID" -p Type -p User -p Remote`
  → `Type=tty`, `User=$(id -u)`, `Remote=no`; не заданы `DISPLAY`, `WAYLAND_DISPLAY`; `XDG_RUNTIME_DIR` равен
  `/run/user/$(id -u)`, принадлежит учётной записи, режим 0700. Поля `loginctl` — **факт** по документации; что
  под `sudo -iu` сессия остаётся сессией владельца (`User` ≠ uid) — **гипотеза**. Отказы на не-tty, чужой uid,
  удалённую сессию и графику проверяет `provision_cli` (поддельный `loginctl`); на машине негативные проверки —
  только из recovery-оболочки (`runuser -l`).
- **Код и клон.** Источник — `git bundle` из каталога входящих (`SA_SRC_BUNDLE`, по умолчанию `code.bundle`); с
  ним работает только `review.sh`, `provision.sh` bundle не трогает. Клон по локальному пути запрещён: он
  переносит `objects/info/alternates` источника, и объекты можно подменить (аудит). В клоне
  `transfer.fsckObjects=true` и `fetch.fsckObjects=true`, но при `fetch` из bundle на git 2.43 они битый объект
  пропускают (**факт**, И2-1) — поэтому отдельно `git fsck --strict`. `provision.sh` работает, только если клон и
  его родители принадлежат раскатке или root и закрыты на запись группе и прочим, `git status --porcelain` пуст,
  HEAD отсоединён, `objects/info/alternates` нет и обе fsck-настройки включены.
- **Просмотр до checkout** (`ops/review.sh <ветка>`, И2-1): иначе уже подменённый `provision.sh` нарисовал бы
  пустой diff и спросил фразу. Bundle — обычный файл (не симлинк), `realpath` внутри каталога входящих → копия в
  дом раскатки не больше 200 МиБ (`head -c` предел+1, затем размер; *заменено §16.4: `dd iflag=nofollow,count_bytes`*) → во временном пустом репозитории
  `bundle verify`, `fetch` ветки, `fsck --strict` → `fetch` в клон из копии, `fsck --strict` клона → отказы
  (ниже) → `git diff --text --no-color --no-ext-diff --no-textconv <deployed-commit | пустое дерево> FETCH_HEAD --
  deploy/ web/viewer/notfound.html ':(glob)**/.gitattributes'` в `less`: `LESS=-X` без `R` (управляющие
  последовательности показываются как текст, а не исполняются терминалом), `LESSSECURE=1` (без выхода в
  оболочку), `LESSOPEN`/`LESSCLOSE` пустые, без истории → хэш коммита и `diff --stat` остального дерева → «yes» →
  `checkout --detach` с `--attr-source=<пустое дерево>` (git ≥ 2.40: без фильтров и `eol` из `.gitattributes`).
  Первый раз — те же команды руками по runbook, от пустого дерева, весь текст; дальше — копия `review.sh` в
  `~/.local/libexec/sa/`, её ставит успешный `configure` из уже просмотренного коммита. Доверие — не источнику
  bundle, а прочитанному diff. Подписанный тег и `git verify-tag` — позже (нужен ключ подписи вне досягаемости
  процессов владельца).
- **Отказы просмотра** (код 3; одни правила в `review.sh` и `provision.sh`): изменился любой `.gitattributes`
  после `deployed-commit` (меняет показ и выгрузку файлов); в дереве коммита есть режим, кроме 100644/100755
  (симлинк, gitlink), — по **всему** дереву, а не по pathspec: симлинк на месте самого `deploy` или `web`
  pathspec не видит (Р-2); `deploy` — не каталог, страница 404 — не файл; в `deploy/` или странице 404 есть
  символ вне **белого списка** (Р-8, Р-12): `\t`, печатный ASCII, кириллица U+0400–04FF, `§ © « » ° № → ≠ ≤ ≥ — …`.
  Всё прочее — управляющие, нулевой ширины, теги, селекторы вариантов, смена направления, разделители строк —
  прячет строки от глаз, а перечнем запретов его не исчерпать. Двойников ASCII (en dash, типографские кавычки,
  знак умножения) в списке нет: в `less` они читаются как `-`, `'`, `"`, `x` и молча выключили бы проверку в
  скрипте. Шаблон — в трёх копиях (`guard.sh`, `review.sh`, runbook), совпадение проверяет шаг `ctl_shared`.
  Только у `review.sh`: bundle — симлинк, вне каталога входящих или больше предела; клон или inventory чужие
  или с записью группе и прочим.
- **Второй взгляд в `provision.sh`.** Тот же diff от `deployed-commit` до HEAD клона и те же отказы — до сборки
  контроллера, затем «yes». После успешного `configure` хэш HEAD пишется в `inventory/deployed-commit`, а
  `review.sh`, `sa-backup-prune.sh`, `backup-lib.sh` (0700) и юниты таймера ставятся копиями в
  `~/.local/libexec/sa/` и `~/.config/systemd/user/` (И2-1, И2-2): то, что идёт без консоли или до checkout, —
  только из прочитанного коммита.
- **Контроллер и tofu.** Образ контроллера собирается при каждом запуске из просмотренного клона
  (`podman build -q`) в хранилище раскатки, запуск — по полученному ID; образ tofu — по дайджесту из
  `ops/images.env`. Порядок «diff → yes → сборка → фраза» (И2-7): секреты получает только прочитанный
  контроллер. `age` — в образе контроллера, в систему ничего не ставится. **Каждый** `sa-prov-*` (age, tofu,
  ansible, проверка и возврат копии) запускается с `--log-driver=none`: иначе conmon пишет его вывод в журнал
  (драйвер журнала Podman на машине — journald), а журнал учётной записи раскатки читают члены `adm` и
  `systemd-journal`, в том числе владелец (аудит); и с `--read-only`, `--cap-drop=ALL`,
  `--security-opt=no-new-privileges`, `--pids-limit=512`, `--tmpfs /tmp`.
- **Запуск Ansible — один код** (`ops/ctl-run.sh`, Р-1): argv `podman run … ansible-playbook`, тома, окружение,
  контракт `-e` (§4) и канал vault собираются только там. Его подключают `provision.sh` и стенд, поэтому зелёный
  стенд доказывает тот же запуск. Файл подключается только из просмотренного клона, проверяется вместе с ним и в
  libexec не копируется. Контроллеру монтируются `deploy/` и страница 404 (`:ro`, по каноническому пути),
  inventory Ansible (`:ro`), сокет агента, `known_hosts` и открытая часть ключа (`:ro`); окружение — `HOME` и
  `ANSIBLE_HOME` в tmpfs, `ANSIBLE_CONFIG`, `ANSIBLE_NOCOLOR`.
- **Один прогон за раз:** `flock` на `inventory/.lock`. Демоны (`ssh-agent`, conmon) запускаются без дескриптора
  блокировки (Р-7): иначе пережили бы `SIGKILL` скрипта и держали бы её. Остатки убитого прогона (файл токена,
  сокет агента с мёртвым PID) удаляются при старте.
- **Отказы до любых действий** (код 3, сообщение без значений): `id -un` ≠ `SA_OPS_USER` или дом не 0700;
  сессия не прошла проверку выше; задан `CLAUDECODE`; клон не прошёл проверку выше; отказы просмотра; уже заданы
  `TF_VAR_clo_api_token` или `CLO_API_AUTH_TOKEN`; задан любой `TF_LOG*`; заданы `ANSIBLE_KEEP_REMOTE_FILES` или
  `ANSIBLE_*WORLD_READABLE*`; `SA_INVENTORY` внутри чужого git-дерева (своим git-деревом быть может),
  `SA_BACKUP_DIR` или `SA_SECRETS` внутри любого git-дерева; `SA_INVENTORY`, `SA_BACKUP_DIR`, каталог `SA_SECRETS`
  или любой их родитель — не раскатки и не root либо с записью группе и прочим (И2-5); файл секретов чужой или
  не 0600; `*.tfvars` содержит `token`; `SA_DOMAIN` или имена пользователей не по шаблону; неизвестная команда
  или лишний аргумент. Перед `apply` — состав `deploy/tofu/clo` (§3). `SA_START`/`SA_PROVIDER` не
  `bootstrap`/`clo` → код 2 «не реализовано». Нет сети до сервера (tcp/22 закрыт) → код 4.

### Секреты

Один файл `SA_SECRETS` (по умолчанию `~/secrets.age`, 0600): строки `ключ=значение` — `clo_token`,
`vault_pass`, `ssh_key_phrase`, `backup_key_phrase`, `age_identity` (закрытый ключ age копий). Зашифрован
`age -p` (scrypt) по фразе; фраза — сгенерированная `age -p` или не слабее. Создание и правка — runbook: в
контейнере контроллера, через его tmpfs, без открытого файла на диске раскатки.

Каждый запуск: `exec {fd}< <(podman run --rm -it --log-driver=none --network=none --preserve-fds=1 -v
"$SA_SECRETS":/run/sa/secrets.age:ro <ID> age -d -o /dev/fd/3 /run/sa/secrets.age 3>&1 </dev/tty >/dev/tty)`,
затем `read -r -u "$fd"` построчно в основной оболочке `provision.sh` (не в подоболочке: там переменные
пропали бы), без файла и argv; дескриптор закрывается после разбора. Терминал (`-t`) — только для фразы: `age`
спрашивает её с `/dev/tty` контейнера, то есть с терминала раскатки (§16: сессия `sa-ops` ops-ВМ); кэша нет. Расшифрованное идёт отдельным
каналом — дескриптор 3, его передаёт `--preserve-fds=1`, — а не в stdout: в одном псевдотерминале подсказка
смешалась бы с выводом, а `\n` стали бы `\r\n` (аудит). Что `age` в `podman run -it` читает фразу с терминала и
пишет в `/dev/fd/3`, а канал доходит до основной оболочки, — проверяет шаг `provision_cli` на настоящем Podman
(П2a-3).

| Секрет | Как идёт дальше | Остаток |
|---|---|---|
| токен API | файл 0600 в `XDG_RUNTIME_DIR` раскатки → том `:ro` в `/run/sa/token` → `tofu-entry.sh`: `TF_VAR_clo_api_token=$(cat /run/sa/token) exec tofu "$@"`. Файл удаляет `provision.sh` на хосте по `trap` (в контейнере том только на чтение). Без `-e`: нет в `podman inspect`, `config.json` контейнера, argv | память `provision.sh`, файл и `/proc/<pid>/environ` tofu — для процессов **той же** учётной записи во время прогона |
| пароль vault | у каждого `podman run` ansible свой канал — `bootstrap.yml` и `site.yml` отдельно (канал читается один раз): `--preserve-fds=1 … 3< <(printf '%s' "$vault")` → `--vault-password-file=/dev/fd/3` (канал в контейнере читается — **факт**; что ansible читает `/dev/fd/3` как файл, проверяет стенд: его плеи идут тем же `ops/ctl-run.sh`, И3-5) | память `provision.sh` и ansible |
| ключ сервера, ключ забора | файлы ed25519 в `~/.ssh` раскатки (`id_server`, `id_backup`), фраза ≥ 128 бит — из файла секретов. `provision.sh` поднимает свой `ssh-agent` в `XDG_RUNTIME_DIR` и грузит **только ключ команды** (сервера — `up`/`apply`/`configure`, забора — `backup`) отдельным вызовом `ssh-add` с `SSH_ASKPASS_REQUIRE=force` и `SSH_ASKPASS=<абсолютный путь к deploy/ops/phrase-fd.sh в клоне>` — подача фразы, не подтверждение (переменная — **факт** по `ssh-add(1)`, OpenSSH ≥ 8.4). Помощник печатает фразу из унаследованного дескриптора 3; что `ssh-add` его не закрывает, проверяет `provision_cli` (П2a-3), запасной путь с именованным каналом не понадобился. Срок ключа в агенте и самого агента — 3600 с (`ssh-agent -t`, `ssh-add -t`, Р-18): агент-сирота убитого прогона не держит ключ дольше часа. В контроллер монтируется только сокет агента; агент завершается по `trap` (свой PID) | агент — на время прогона, не дольше часа |
| ключ age копий | канал в контейнер контроллера при проверке и возврате копии (§11) | память `age` |

FIDO2 `ed25519-sk` — необязательное усиление позже (не resident или `verify-required`); импорт такого ключа
в провайдер — необязательный пункт аренды (§13). Короткоживущий токен провайдера на время `apply` и его отзыв
— в runbook, если провайдер это умеет (§13).

«yes» с `/dev/tty` — после просмотра diff (все команды, кроме `forget-host`), у `forget-host` — своё, у нового ключа
хоста — после сверки отпечатка; защита от случайности.

### Ход команд

- Окружение контейнеров чистое (`podman run` без `--env-host`).
- Любая команда после проверок отказа начинает с проверки свежести копий (§11): предупреждение, не отказ.
- `SA_DOMAIN` в `provision.env` — **один** источник домена: `TF_VAR_ptr` и `-e albums_domain` (`public_base`,
  `ALBUMS_DOMAIN`). Смена домена при выданных ссылках — переезд с переадресацией (001-22).
- `apply`/`up`: `tofu init -lockfile=readonly -backend-config=path=…/clo.tfstate` (`TF_DATA_DIR` — в inventory;
  сеть — только у `init`) → интерактивный `tofu apply` в `podman run -it`, токен — томом. `--replace-server`
  добавляет ровно `-replace=clo_compute_instance.srv`, других аргументов tofu скрипт не принимает. После `apply` —
  проверка state (`tofu show -json` без сети, §3); `hosts.yml` из `tofu output -raw ipv4`; ожидание tcp/22 (до
  10 мин, `SA_WAIT_SSH_SEC`).
- `configure`: tcp/22 закрыт → «нет сети», код 4. Адреса нет в `known_hosts` → `ssh-keyscan -t ed25519` →
  отпечаток на экран, владелец сверяет его в веб-консоли (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`) →
  «yes» → запись; дальше `StrictHostKeyChecking=yes`. Пробный вход администратором: код 0 → только `site.yml`
  (роли `bootstrap` и `nftables` в нём есть). Код 255 — это и «входа ещё нет», и «ключ хоста сменился», поэтому
  ключ от `ssh-keyscan` сверяется с `known_hosts`: совпал → `bootstrap.yml` под `bootstrap_user` (`root` или
  `image_user`), затем `site.yml`; не совпал → отказ «сервер пересоздан или подмена» до `forget-host`. Иной код —
  ошибка. После успешного `site.yml` — `deployed-commit` и копии в libexec (выше).
- SSH-аргументы — одни для Ansible и пробных входов: `-F /dev/null` (ни настроек, ни прокси-команд из
  окружения), `IdentitiesOnly=yes`, `IdentityFile=<открытый ключ>`, `IdentityAgent=<сокет скрипта>`,
  `UserKnownHostsFile=…/known_hosts`, `StrictHostKeyChecking=yes`, `ConnectTimeout=10`; в контроллер — списком
  `sa_ssh_args` с путями внутри контейнера.
- `backup` — §11. `forget-host` (без просмотра и секретов, со своим «yes») удаляет запись из `known_hosts` (после
  пересоздания сервера).
- Ansible — без `--diff`; итог — `PLAY RECAP`.

## 3. OpenTofu, провайдер `clo`

- `required_providers { clo = { source = "clo-ru/clo", version = "= 2.8.0" } }`, OpenTofu ≥ 1.11 (`ephemeral`).
  Образ `ghcr.io/opentofu/opentofu` 1.13.x по дайджесту, не `-minimal` (нет корневых сертификатов — **факт**).
  `.terraform.lock.hcl` в репозитории (`tofu providers lock -platform=linux_amd64`; первый раз хэши сверяются с
  подписанным `SHA256SUMS` релиза провайдера), дальше `init -lockfile=readonly`.
- `variable "clo_api_token" { type = string, sensitive = true, ephemeral = true }`, без значения по умолчанию;
  `provider "clo" { auth_url = var.api_url, token = var.clo_api_token }`; у `api_url` — `validation`: только
  `https://хост[:порт][/путь]`, без учётных данных (токен уходит на этот адрес, И2-4). Эфемерное значение в блоке provider
  принимают `validate` и `tofu test` (**факт**, прототип). Эфемерные значения разрешены и в провижинерах — отсюда
  запрет провижинеров в `tofu_static` и просмотр diff (§2).
- Ресурсы (имена и поля — **факт** по документации и коду провайдера 2.8.0):
  - `data "clo_project_image" "debian"` — `project_id`, `name = "Debian 13"` → `image_id`;
  - `clo_compute_keypair` — **только** с `public_key` (без него API генерирует пару, закрытый ключ — в state);
  - `clo_network_ip` — `project_id`, `ptr = var.ptr` (может быть пустым до DNS), `prevent_destroy = true`;
  - `clo_compute_instance` — `project_id`, `name`, `image_id`, `flavor_vcpus = 2`, `flavor_ram = 4`,
    `block_device { bootable = true, storage_type = "volume", size = 20 }`, `keypairs = [ключ]`, **без**
    `addresses` и без `password` (попал бы в state);
  - `clo_network_ip_attach` — `address_id = clo_network_ip.main.id`, `entity_name = "server"`,
    `entity_id = clo_compute_instance.srv.id`, `is_primary = true`.
- **Почему привязка отдельно (факт по коду).** Удаление сервера берёт `Addresses` из ответа API о сервере и
  передаёт их в запрос удаления как «удалить вместе с сервером»; `prevent_destroy` у `clo_network_ip` от этого не
  спасает. Привязка зависит от сервера (`entity_id` — `ForceNew`), поэтому при `--replace-server` tofu сначала
  удаляет её (отвязка с ожиданием «отвязан»), потом сервер. Что API после отвязки не числит адрес за сервером —
  **гипотеза**, первый пункт аренды (§13).
- `lifecycle { ignore_changes = [image_id, block_device, keypairs] }` у сервера: `image_id` и `block_device` —
  `ForceNew`, а чтение сервера из API их не обновляет (**факт**: читаются только id, имя, статусы, дата). Без
  `ignore_changes` новый ID образа «Debian 13» или правка `size` молча предложили бы пересоздание; пересоздание —
  только `--replace-server`. `keypairs` — на всякий случай: `ForceNew` у него по схеме провайдера не проверен, а
  ключ администратора после `bootstrap` ведёт Ansible. Увеличение диска — в панели провайдера.
- Выход: `ipv4`, `server_id`.
- `plan` без настоящего токена невозможен (401 — **факт**). Локально и в CI — только `tofu test` с
  `mock_provider "clo"` и `mock_data` для образа (офлайн, **факт**); привязка — `command = plan` с
  `override_resource`. `command = apply` не годится: уборка теста упирается в `prevent_destroy` (**факт**).
  Сеть у контейнера tofu — только у `init` (реестр OpenTofu, без токена); `validate`, `test`, `show`, `output` —
  `--network=none` (П2a-1). С 04.11 (ВР-15) реестр не используется и `init` тоже идёт без сети: §16.10.1.
- `tofu_static` (по HCL, И2-4, Р-9, Р-11): в `deploy/tofu/clo` только `*.tf`, `tofu-entry.sh`,
  `.terraform.lock.hcl`, `tests/*.tftest.hcl` (без симлинков и подкаталогов; `*.tofu` и `*.tf.json` tofu тоже
  читает); у токена `sensitive` и `ephemeral`, без значения по умолчанию; `validation` у `api_url`; у
  `clo_network_ip` — `prevent_destroy`; у сервера — `ignore_changes` и нет `addresses`; нет `password`, нет
  `clo_compute_keypair` без `public_key`; ресурсы, источники и `ephemeral`-блоки — только провайдера `clo`; нет
  `provisioner`, `terraform_data`, `null_resource`, `data "external"`/`"http"`; `backend` — только `local`, нет
  `cloud`; нет блока `encryption` и `key_provider`/`method "external"` (запустили бы команду в контейнере
  `apply`, где токен и сеть); модули — только с локальным `source`. Блоки — на любой глубине, запретные слова —
  до вырезания комментариев (heredoc с `/* */`), метки — и без кавычек; на каждое правило — мутация в
  самопроверке (`selftest`). Тот же состав каталога `provision.sh` проверяет перед `apply`. Опора — просмотр
  diff владельцем (§14), проверка — от случайности.
- `tfstate` без шифрования, в git приватного inventory: секретов в нём нет — это доказывают тесты и проверка
  после `apply` (`tofu show -json` без сети: непустые `password`, `secret`, `token`, `private_key` — ошибка с
  указанием перевыпуска). Шифрование состояния средствами tofu запрещено (Р-11).

## 4. Ansible

- **Контроллер** — `deploy/ansible/Containerfile`: `python:3.13-slim` с `mirror.gcr.io/library/…` по
  дайджесту (ВР-16: слои `public.ecr.aws` из РФ не скачиваются); пакеты Debian `age`, `zstd`, `openssh-client`, `whois` (`mkpasswd` — только в контроллере) без
  закрепления версий (подписаны Debian, база по дайджесту — И3-6); pip-пакеты `ansible` (с коллекциями
  `ansible.posix`, `community.general`), `ansible-lint`, `yamllint` — точные версии и хэши
  (`pip install --require-hashes --only-binary=:all: --no-deps`). Тот же файл с хэшами строит образ проверок
  стенда (П2a-4). Коллекции — из пакета pip, не из Galaxy. `ansible-lint` — профиль `production`,
  `offline: true`. Флаги запуска — §2 (`ops/ctl-run.sh`).
- **`ansible.cfg`** (путь — `ANSIBLE_CONFIG`: из смонтированного каталога Ansible конфиг сам не читает):
  `interpreter_python = /usr/bin/python3` — путь, а не поиск по `PATH` администратора (Р-3);
  `pipelining = True` в `[connection]` и `[ssh_connection]` (модули через stdin, без временных файлов с
  аргументами-секретами; пробный вход идёт через `delegate_to: localhost`, соединение `local`);
  `allow_world_readable_tmpfiles = False`, `become_exe = /usr/bin/sudo`, `host_key_checking = True`;
  `ControlMaster=auto`, `ControlPersist=60s` — открытая сессия переживает `reload` sshd с новым `00-sa.conf`.
  Пакет `acl` ставит `bootstrap`: `become_user: albums` без него упал бы.
- **Контракт `provision.sh` ↔ плеи** (И3-5): извне — `albums_domain`, `admin_user`, `backup_user`,
  `bootstrap_user`, `image_user`, `sa_ssh_args` (список аргументов ssh), `sa_controller_pubkey` (открытая часть
  ключа входа; обязана быть среди `admin_ssh_keys`) и переменные vault; `ansible_ssh_common_args` (из
  `sa_ssh_args`) и `ansible_become_password` (из vault) задают сами плеи; пароль vault —
  `--vault-password-file=/dev/fd/3`.
- **Плеи.** `bootstrap.yml`: первый плей — под `bootstrap_user`, роли `bootstrap`, `nftables`, `guest_agent`
  (агент с настройками образа иначе весь прогон открыт для команд из панели), затем `apt full-upgrade`; второй
  плей — под администратором, блокировка пользователя образа (§7). `site.yml` — под администратором с `become`:
  `bootstrap`, `nftables`, `upgrades`, `journald`, `guest_agent`, `cloud_init`, с И4 — `podman`, `caddy`,
  `albums_svc`, `backup_access` (Caddy — до `albums_svc`: tmpfiles ссылается на пользователя `caddy`).
  `post_tasks` `site.yml` — проверка входа администратора (`include_role` `bootstrap`, `tasks_from:
  verify_admin.yml`, Р-22), то есть в каждом `configure` (§7). `verify.yml` — проверки на хосте под тегами
  `verify_*`, ничего не меняет (§12); его запускает только стенд. Тегов у `configure` нет; если появятся —
  `include_role` проверки обязан стоять с `tags: [always]` и `apply: {tags: [always]}`, иначе прогон с тегами
  молча её пропустит (Р-23).
- **Секреты** — `group_vars/all/vault.yml`: `vault_albums_master` (base64url ≥ 32 байт, `albums-svc -gen-secret`),
  `vault_admin_password` (для `become` и консоли, не короче 16 знаков), `vault_admin_password_hash` (yescrypt или
  SHA-512, `mkpasswd` в контейнере; готовый хэш — `password_hash` на Python 3.13 без `crypt` требует passlib, а
  случайная соль ломает идемпотентность). Длины и формат — `assert` до изменений. Пароль vault — случайный,
  ≥ 128 бит (ansible-vault — PBKDF2 10000 итераций).
- **Без утечек:** `no_log: true` у задач с vault-значениями; `diff: false` у шаблона `config.json`; секреты — в
  `stdin` модулей, не argv (хэш пароля — `chpasswd -e`, а не `usermod -p`).
- **Чужой код под root:** скрипты проверок — через stdin, `python3 -I` и `chdir: /` (И3-7): иначе python под
  `become` возьмёт `json.py` из дома администратора.
- **Идемпотентность:** `command`/`shell` — с `creates`/`changed_when`; `apt` — `cache_valid_time`. Задачи,
  которым нужен rootless Podman или uid процесса сервиса (секрет, образ, юнит, `chown`/ACL на этот uid), — тег
  `albums-runtime` (§12).

## 5. Раскладка на хосте

| Путь | Владелец, права | Откуда |
|---|---|---|
| `/etc/ssh/sshd_config.d/00-sa.conf` | root 0644 | `bootstrap`, `validate: sshd -t -f %s`, затем `sshd -t` целиком (§7) |
| `/home/<admin>/.ssh/authorized_keys` | администратор 0600 | `bootstrap`: ровно `admin_ssh_keys` (`exclusive`) |
| `/root/.ssh/authorized_keys`; `/etc/sudoers.d/90-cloud-init-users`, `authorized_keys` пользователя образа | удаляются | `bootstrap` (§7) |
| `/etc/nftables.conf` | root 0644 | `nftables`, `validate: nft -c -f %s` |
| `/etc/systemd/system/nftables.service.d/sa.conf` | root 0644 | `nftables`: пустой `ExecStop=` (§6) |
| `/etc/apt/apt.conf.d/20auto-upgrades`, `52sa-unattended` | root 0644 | `upgrades` |
| `/etc/systemd/journald.conf.d/sa.conf`; `/var/log/journal` | root 0644; root:systemd-journal 2755 | `journald` |
| `/etc/qemu/sa-qga.conf` | root 0644 | `guest_agent`: белый список команд (§10) |
| `/etc/systemd/system/qemu-guest-agent.service.d/sa.conf` | root 0644 | `guest_agent`: `ExecStart` с `-c` своего файла (§10) |
| `/etc/cloud/cloud.cfg.d/zz-sa.cfg` (прежний `99-sa.cfg` удаляется) | root 0644 | `cloud_init` (§10) |
| `/etc/cloud/cloud-init.disabled` | root 0644, только при `cloud_init_disable: true` | `cloud_init` (§10) |
| `/etc/apt/keyrings/caddy-stable.asc`, `sources.list.d/caddy-stable.sources`, `preferences.d/caddy` | root 0644 | И4: `caddy` |
| `/etc/caddy/Caddyfile`, `/etc/caddy/albums.env` | root 0644; root:caddy 0640 | И4: `deploy/caddy/Caddyfile`; `ALBUMS_DOMAIN` |
| `/etc/systemd/system/caddy.service.d/albums.conf` | root 0644 | И4: `deploy/systemd/caddy-albums.conf` (§8) |
| `/var/lib/caddy` — дом пользователя `caddy`, ACME и сертификаты | caddy:caddy 0700 | И4: `caddy` (§8, И4-2) |
| `/etc/systemd/system/caddy-api.service` | маска (→ `/dev/null`) | И4: `caddy` (§8, И4-16) |
| `/usr/sbin/policy-rc.d` | нет; root 0755 с меткой `# sa-caddy-install` — только на время установки Caddy | И4: `caddy` (§8, И4-4) |
| `/usr/share/albums-svc/errors/notfound.html` | root 0644 | И4: `web/viewer/notfound.html` |
| `podman.socket`, `podman-auto-update.timer` — системные и `--global` | маски | И4: `podman` (§10) |
| `/home/albums`, `~albums/.config/containers/systemd/albums-svc.container` | albums 0700; 0644 | И4: `albums_svc`, шаблон `deploy/quadlet/albums-svc.container.j2` |
| `/etc/albums-svc/`, `config.json` | root:albums 0750 / 0640 + ACL | И4: §9 |
| `/var/lib/albums-svc/data` | uid процесса сервиса 0700 | И4: §9 |
| `/etc/tmpfiles.d/albums-svc.conf` → `/run/albums-svc` | root 0644 | И4: `deploy/systemd/albums-svc.tmpfiles.conf` |
| `/etc/systemd/system/sa-backup.socket`, `sa-backup@.service` | root 0644 | И4: шаблоны `deploy/systemd/*.j2` (И4-9) |
| `/usr/local/libexec/sa-backup-stream`, `sa-restore`, `/etc/sa-backup/recipients` | root 0755 / 0644 | И4: §11 |
| `/home/<backup>`, `/home/<backup>/.ssh`; `.ssh/authorized_keys` | root 0755; root 0644 | И4: `backup_access` — ключ и `command=` пользователь бэкапа не правит (§11, И4-9) |
| `/var/lib/sa-restore/` | администратор 0700 | И4: §11, возврат |

## 6. nftables

Своя таблица, без `flush ruleset` (чужие таблицы не трогаем); файл грузится одной транзакцией:

```
table inet sa_filter
delete table inet sa_filter
table inet sa_filter {
  chain input {
    type filter hook input priority filter; policy drop;
    ct state invalid drop
    ct state established,related accept
    iif "lo" accept
    icmp type { destination-unreachable, time-exceeded, parameter-problem } accept
    icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem,
                  nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept
    icmp type echo-request limit rate 5/second accept
    icmpv6 type echo-request limit rate 5/second accept
    ip6 saddr fe80::/10 udp dport 546 accept        # DHCPv6, если провайдер выдаст IPv6
    tcp dport { 22, 80, 443 } accept
  }
  chain forward { type filter hook forward priority filter; policy drop; }
}
```

- Подбор пароля к SSH невозможен (только ключ); от шума и исчерпания слотов — sshd: `PerSourcePenalties yes`
  (OpenSSH 10.0 в Debian 13 — **факт**) и `MaxStartups 10:30:60`. Счётчиков источников в nft нет: их множество
  без срока жизни заполняется и закрывает SSH новым адресам (research §3).
- Исключений по `iifname` нет: внутренний интерфейс локальной сети проекта закрыт так же. Исходящие открыты (apt,
  ACME, реестр, NTP). DHCPv4 идёт через packet-сокет мимо фильтра — **гипотеза**, проверка на аренде. UDP 443
  закрыт: в Caddyfile `protocols h1 h2`. Rootless Podman правил не ставит, у сервиса `Network=none`.
- `nftables.service` включён и запущен; правила применяются сразу (`flush_handlers`), до следующих ролей.
  Обработчик в ролях — **только** `reload`. Drop-in `nftables.service.d/sa.conf` с пустым `ExecStop=` (И3-2):
  `ExecStop` юнита в Debian 13 — `nft flush ruleset` (**факт**), а postinst пакета при обновлении делает
  `restart`; без `ExecStop` `restart` — повторная загрузка файла, которая заменяет таблицу одной транзакцией, без
  окна без правил. `verify_nft_reload_only` (стенд) ищет в ролях `restarted`/`stopped` для `nftables`;
  `verify_nft_ports` — политика `drop`, ровно tcp 22/80/443, юнит `enabled`, `ExecStop` пуст.
- Второй рубеж — `verify_listeners`: `ss -Htulpn` — вне `127.0.0.0/8` и `::1` слушают только `sshd` на 22,
  `caddy` на 80 и 443 и DHCP-клиент (udp 68/546: `dhclient`, `systemd-networkd`, `dhcpcd`, `NetworkManager`);
  любой другой слушатель — ошибка.

## 7. SSH, пользователи, вход

- **Администратор** — `admin_user`: `useradd -K SUB_UID_COUNT=0 -K SUB_GID_COUNT=0` (существующие строки
  `/etc/subuid`/`/etc/subgid` роль удаляет), оболочка `/bin/bash`, группа `sudo` (по паролю), не в группе
  `albums`. Ключи `admin_ssh_keys` (`authorized_key`, `exclusive`) — только ed25519 (в том числе `sk-`), одной
  строкой, без опций. Пароль — `vault_admin_password_hash` через `chpasswd -e` из stdin (модуль `user` передал
  бы хэш аргументом `usermod -p`), меняется только при расхождении. `assert` до первого изменения: имена —
  строчные латинские; администратор ≠ root, ≠ пользователь образа, ≠ пользователь бэкапа;
  `bootstrap_connect_user` (кем идёт прогон) — root, администратор или пользователь образа (И3-3);
  `sa_controller_pubkey` есть среди `admin_ssh_keys`.
- **`00-sa.conf`:** `PermitRootLogin no`, `PasswordAuthentication no`, `KbdInteractiveAuthentication no`,
  `AuthenticationMethods publickey`; другие дороги к входу закрыты явно (Р-4): `AuthorizedKeysFile
  .ssh/authorized_keys`, `AuthorizedKeysCommand none`, `AuthorizedPrincipalsCommand none`,
  `AuthorizedPrincipalsFile none`, `TrustedUserCAKeys none`, `HostbasedAuthentication no`,
  `GSSAPIAuthentication no`, `PermitUserEnvironment no`; `AllowUsers <admin> <backup>`, `DisableForwarding yes`,
  `AllowTcpForwarding no`, `AllowAgentForwarding no`, `X11Forwarding no`, `PermitTunnel no`, `MaxAuthTries 3`,
  `LoginGraceTime 30`, `MaxStartups 10:30:60`, `PerSourcePenalties yes`. Без `Match`: пользователь бэкапа
  ограничен в `authorized_keys` (§11). Debian берёт первое значение, и `00-` читается раньше
  `50-cloud-init.conf`, но порядок — не гарантия (drop-in «000-…», директивы основного файла до `Include`):
  поэтому итог проверяет `sshd -T` (И3-1).
- **Итоговая конфигурация sshd** (`sshd_effective.yml`, она же `verify_sshd_effective`): `sshd -T -C` для
  администратора и для root — все значения выше (`PerSourcePenalties` — не `no`, ровно две строки
  `AllowUsers`); поиск без учёта регистра по `/etc/ssh/sshd_config` и всем файлам `sshd_config.d` — нет `Match`
  и нет `Include`, кроме `Include /etc/ssh/sshd_config.d/*.conf`. Провал внутри блока — откат.
- **Защита от самоблокировки — на каждом прогоне, в обоих плеях** (И3-4; роль `bootstrap` входит в оба), а не
  только при изменении: прошлый прогон мог оборваться после создания администратора.
  1. Администратор, ключи, пароль → пробный вход с контроллера (`delegate_to: localhost`, модуль `command` с
     `argv: [ssh, <sa_ssh_args>, -o BatchMode=yes, -o ControlMaster=no, -o ControlPath=none, <admin>@<хост>,
     sudo, -S, -v]` и `stdin: "{{ vault_admin_password }}"`, без оболочки и `echo`, `no_log`) — новое
     соединение, не мультиплексированное. Провал → прежние ключи и пароль возвращаются, ошибка; root и sshd не
     тронуты.
  2. `block`: `00-sa.conf` (`validate`) → `sshd -t` целиком → **безусловный** `reload` (И3-8: `sshd -T` читает
     диск, а не демон, и оборванный прогон иначе остался бы незамеченным) → итоговая конфигурация → повторный
     пробный вход → вход по паролю отвергнут живым демоном: `ssh` с `PubkeyAuthentication=no`,
     `PreferredAuthentications=password,keyboard-interactive` и `BatchMode` — код 255 и
     `Permission denied (publickey)` (после положительной проверки — из-за штрафов). `rescue`: прежний
     `00-sa.conf` возвращается (или новый удаляется), `reload`, ошибка. Текущая сессия жива (`ControlPersist`).
  3. Только после этого: ключи root удалены, `passwd -l root`.
- **Порядок `bootstrap.yml`:** первый плей под `bootstrap_user` — `assert` → пакеты `acl`, `sudo`,
  `openssh-server`, `systemd-timesyncd` → шаги 1–3 → `nftables` → `guest_agent` → `apt full-upgrade`
  (`DEBIAN_FRONTEND=noninteractive`, `force-confold,force-confdef`; после закрытия root — долгий шаг не держит
  окно; в `site.yml` его нет). Второй плей — **под администратором** (И3-3: `nologin` и снятый sudoers оборвали
  бы прогон, идущий от имени пользователя образа): если задан `image_user` (через него шёл первый вход при
  `disable_root`) — `passwd -l`, оболочка `nologin`, `authorized_keys` удалён,
  `/etc/sudoers.d/90-cloud-init-users` удалён.
- **`site.yml`** (под администратором): те же шаги 1–3 (обычно без изменений). Если `authorized_keys`
  администратора пришлось менять — `site.yml` падает (файл уже возвращён к `admin_ssh_keys`): его правили вне
  раскатки (Р-14). Поэтому менять `admin_ssh_keys` на поднятом сервере — сначала `bootstrap.yml` (ставит ключи
  под пробным входом), потом `site.yml` (README, Р-21); иначе `site.yml` упадёт один раз. Если `image_user` есть — `loginctl terminate-user` (его мультиплексированная сессия из
  `bootstrap` может быть жива), затем `userdel -r` (модуль `user`, `remove`). Повторно `bootstrap.yml` не
  запускается: есть вход администратора — только `site.yml`.
- **Файлы запуска администратора** (Р-3, Р-14). Debian-овский bash читает `~/.bashrc` и для команд по ssh:
  подмена `PATH` подсунула бы прогону поддельный `python3` уже от root. Меры: `interpreter_python =
  /usr/bin/python3` (§4) и проверка `bootstrap/tasks/verify_admin.yml` (тег `verify_users`):
  - `.bashrc`, `.profile`, `.bash_logout` — копии `/etc/skel` (или пусты, если их там нет), не симлинки;
    `.bash_profile`, `.bash_login`, `.bash_aliases`, `.bash_completion` нет или пусты; нет `~/.ssh/rc`,
    `~/.ssh/environment`, `~/.pam_environment`, `~/bin`, `~/.local/bin`, `/etc/ssh/sshrc`;
  - **разрешающий список** содержимого дома (Р-21): `.bashrc`, `.profile`, `.bash_logout`,
    `.sudo_as_admin_successful`, `.bash_history`, `.lesshst`, `.ssh`, `.ssh/authorized_keys`, `.ansible`,
    `.ansible/tmp` и `.ansible/tmp/ansible-tmp-*`; всё прочее (`.config/bash_completion`, `.local`, чужой файл) —
    провал: запретным списком не охватить то, что оболочка и программы читают при входе. Имя с переводом
    строки — провал. Р-23: симлинк в доме (кроме содержимого `.ansible/tmp/`) — провал (`find -P` по ссылке не
    идёт, и сверка только по имени пропустила бы `.ssh` → наружу); `.ansible/tmp/*` сужен до
    `ansible-tmp-*`; ошибка `find` (нет дома и т. п.) — провал, а не тихий успех;
  - в `authorized_keys` — ровно ключи `admin_ssh_keys`, без опций (`command=`, `environment=`, `from=`): тип и
    ключ каждой строки берутся `regex_search('^[^ ]+ [^ ]+')`, без обратных ссылок (Р-19: `\1 \2` в
    folded-скаляре Jinja раскодируются в управляющие символы, и сверка вырождается в подсчёт строк; так же —
    в `assert.yml` и в разборе `ExecStart` агента, §10). Мутация стенда: опция перед чужим ключом при том же
    числе строк — проверка красная.

  Где идёт (Р-22): в конце `site.yml`, то есть в каждом `configure` на настоящем сервере, и в `verify.yml`
  стенда — один файл на оба места.
- **`verify_users`** ещё: администратор в `sudo`, не в `albums`, без subuid; у пользователя бэкапа subuid нет;
  root заблокирован; `NOPASSWD` нет ни в `/etc/sudoers`, ни в `sudoers.d` (И3-3); пользователь образа после
  `bootstrap.yml` заблокирован (пароль, `nologin`, ни ключей, ни sudoers), после `site.yml` удалён.
- **Консоль и rescue** — вход администратором по паролю в веб-консоли; потерян ключ — rescue провайдера.
- Часы: `systemd-timesyncd` включён (сторож часов сервиса, 001 §3).

## 8. Caddy

- Ключ apt-репозитория — файл в репозитории; отпечаток основного ключа `65760C51EDEA2017CEA2CA15155B6D79CA56EA34`
  (**факт**). Файл сверяется на контроллере до записи в `/etc/apt` (`key_fingerprints.py` через stdin,
  `python3 -I`) и ещё раз на хосте (`verify_caddy`, `caddy_key_fingerprint`): ровно один armor-блок (после `END`
  — только пробельные, один `BEGIN`), заголовки `^[A-Za-z][A-Za-z0-9-]*: `, обязательная пустая строка, тело —
  только base64, сверка CRC24 (И4-6, И4-17: CRC24 линейна, одной суммы мало); основной ключ в файле ровно один и с
  этим отпечатком — apt доверил бы каждому ключу файла. Источник — deb822 с `Signed-By:`.
- Версия — ровно 2.11.7 (**факт**: есть в репозитории, она же в образе проверок `deploy/tests/`):
  `preferences.d/caddy` — `Package: caddy` / `Pin: version 2.11.7*` / `Pin-Priority: 1001`, плюс
  `Package: *` / `Pin: origin "dl.cloudsmith.io"` / `Pin-Priority: -1`. После установки `caddy version`
  начинается с `v2.11.7`, иначе ошибка. unattended-upgrades Caddy не трогает.
- **Порядок установки** (И4-2, И4-4, И4-5, И4-16; код — `roles/caddy/tasks/main.yml`):
  1. группа и пользователь `caddy` заранее, как их создаёт пакет (системные, `nologin`); дом —
     `/var/lib/caddy` caddy:caddy 0700, отдельной задачей: без дома ACME негде хранить аккаунт и сертификаты, TLS
     на аренде не поднялся бы, а стенд (TCP 443 без сертификата) этого не видит;
  2. ключ (выше), источник, закрепление; `apt update`, если источник изменился или у `caddy` нет
     `Candidate: [0-9]` (для пакета не из списков `apt-cache policy` печатает пустоту с кодом 0);
  3. `albums.env`, drop-in юнита и **`Caddyfile` — до пакета**: служба не стартует с чужими настройками, а обрыв
     между пакетом и Caddyfile оставил бы после перезагрузки пакетную витрину на `:80`. Пакета ещё нет —
     Caddyfile кладётся без `validate:` (нечем, проверка — в п. 5); уже стоит (повторный прогон, правка) —
     `validate:` на временном файле до подмены;
  4. `policy-rc.d` **с меткой**: файл ровно с нашим содержимым (`#!/bin/sh`, `# sa-caddy-install`, `exit 101`) —
     остаток оборванного прогона, снимается; любой другой — `fail`, а не тихий пропуск (чужой запрет запуска
     нельзя ни снять, ни оставить вслепую). Нужна установка или смена версии — `block`: наш `policy-rc.d` (пакет
     не стартует со своим `:80`) → `apt` `caddy=<версия>*` с `force-confold,force-confdef` (свой Caddyfile не
     затирается) → `always`: файл удалён. `always` при обрыве SSH не выполняется, отсюда снятие по метке в
     следующем прогоне и `verify_caddy`: `/usr/sbin/policy-rc.d` нет (`exit 101` иначе блокировал бы перезапуски
     служб и после обновлений безопасности). Смена версии под запретом службу не перезапускает — перезапуск за
     обработчиком;
  5. `caddy version` начинается с `v<caddy_version>` → страница 404 → **`caddy validate` отдельной задачей до
     запуска** (домен — как в окружении юнита): плохой Caddyfile роняет прогон, пока служба не перезапущена;
  6. `caddy-api.service` **замаскирован**: пакетный юнит поднимает Caddy с admin API, а в Caddyfile `admin off`;
  7. `daemon-reload`, `enable`, `start`, затем `flush_handlers` — отложенный перезапуск.
- Проверка — `env ALBUMS_DOMAIN=<домен> caddy validate --adapter caddyfile --config <файл>`. Обработчик —
  `daemon-reload` и `restart` (не `reload`: в Caddyfile `admin off`); его зовут Caddyfile, `albums.env`, drop-in и
  пакет. ACME — после A-записи.
- **Drop-in `caddy.service.d/albums.conf`** (И4-7, И4-16): Caddy — единственный процесс, разбирающий интернет.
  `EnvironmentFile=/etc/caddy/albums.env`; `ExecStart=/usr/bin/caddy run --config /etc/caddy/Caddyfile` —
  **без `--environ`** (окружение с доменом не печатается в журнал); `CapabilityBoundingSet=` и
  `AmbientCapabilities=` — **одна** способность `CAP_NET_BIND_SERVICE`; `NoNewPrivileges`, `ProtectHome`,
  `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`, `RestrictNamespaces`,
  `SystemCallFilter=@system-service`, `SystemCallArchitectures=native`, `PrivateDevices`,
  `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, `LockPersonality`; `ProtectSystem=full` — из
  пакетного юнита. `verify_caddy` сверяет действующие значения через `systemctl show` (в том числе `User=caddy`
  и `ExecStart`); фильтр systemd печатает раскрытым списком — нужные вызовы (`openat`, `read`, `write`,
  `socket`, `bind`, `accept4`) в нём есть, опасных (`mount`, `ptrace`, `bpf`, `kexec_load` и т. п.) нет.
  Проверку доказывает мутация «чужой drop-in» `zz-mut.conf` (И4-14): правку своего файла раньше ловит sha256.
- **sysctl нет, пока выключен HTTP/3** (И4-16). `CAP_NET_ADMIN` пакетного юнита Caddy нужен лишь для буферов
  UDP HTTP/3, а в Caddyfile `protocols h1 h2`. Глобальная правка ядра с обработчиком — лишняя движущаяся часть;
  включение HTTP/3 — вместе с `net.core.rmem_max`/`wmem_max` и UDP 443 в nftables (развилка).
- **Путь исправлений.** Закрепление нужно: заголовки Caddy менялись между версиями (стенд П1). До этапа 7 —
  пункт runbook раз в неделю: советы безопасности репозитория Caddy (GHSA). Есть исправление → правка
  `caddy_version` и образа проверок → `caddy-validate.sh` и `stand.sh test` → `configure`. Этап 7 — Renovate на
  `caddy_version` и дайджест образа с теми же проверками.

## 9. `albums_svc`

- Пользователь `albums`: дом 0700, `/usr/sbin/nologin`, пароль `*`, без доп. групп; группа `albums` пуста
  (`assert` по `getent group albums`).
- **subuid/subgid** — из inventory `albums_subuid: "<начало>:<число>"`, число ≥ 65536 (процессу 65532 нужна карта
  ≥ 65533, research §2). `assert`: начало ≥ 100000, конец ≤ 524287 (ниже — системные uid и `DynamicUser`
  61184–65519, выше — диапазоны `systemd-nspawn`); ни одна другая строка `/etc/subuid`/`/etc/subgid` не
  пересекается. В примере inventory — `200000:65536`. Строка `albums:` ставится ровно по inventory; изменилась —
  `podman system migrate` и `chown` данных (дорого: тег `albums-runtime`, runbook). Uid процесса на хосте —
  `начало + 65531`.
- linger: `loginctl enable-linger albums` (`creates: /var/lib/systemd/linger/albums`).
- `/etc/albums-svc` и `config.json` (`public_base: https://<домен>`, `owners` и `sha256` токенов, пределы 001 §6).
  ACL после каждой записи — каталог `u:<uid>:rx`, `d:u:<uid>:r`, файл `u:<uid>:r` (`ansible.posix.acl`) — под
  тегом `albums-runtime`.
- `/var/lib/albums-svc` root 0755; `data` — `file: owner=<uid> group=<gid> mode=0700` от root (то же, что
  `podman unshare chown 65532:65532`, без вложенного Podman) — тег `albums-runtime`. Каталог непуст и чужой —
  ошибка, не тихий `chown`. `deploy/README.md` и шапка Quadlet приведены к этому в И4.
- tmpfiles — из `deploy/systemd/`, при изменении `systemd-tmpfiles --create` этого файла. Quadlet — шаблон из
  `deploy/quadlet/` (`albums_svc_image` с дайджестом; без него шаблон падает).
- `albums-runtime`: `podman secret exists` → нет — `create` из vault через stdin; смена — только
  `-e albums_secret_rotate=true` (001 §6). `podman image exists` → нет — `pull`. `daemon-reload`, `start`;
  перезапуск — по изменению Quadlet, конфига, образа; готовность — `podman healthcheck run`. `become_user: albums`
  с `XDG_RUNTIME_DIR` и `DBUS_SESSION_BUS_ADDRESS` пользователя.

## 10. Podman, обновления, журнал, агент провайдера, cloud-init

- **`podman`** (И4): пакеты `podman`, `uidmap`, `dbus-user-session`, `acl` из Debian; `podman.socket` и
  `podman-auto-update.timer` — системные и пользовательские (`systemctl --global`) — `masked`: API-сокет никому
  не нужен, обновление — этап 7. `verify_podman`: версия из Debian, оба юнита замаскированы.
- **unattended:** `20auto-upgrades` — раз в сутки; таймеры `apt-daily` и `apt-daily-upgrade` включены.
  `52sa-unattended`: `#clear Unattended-Upgrade::Origins-Pattern;` и `#clear Unattended-Upgrade::Allowed-Origins;`
  (иначе остались бы шаблоны `50unattended-upgrades`, а старый список складывается с новым) и только
  `origin=Debian,codename=${distro_codename}-security,label=Debian-Security`; `Automatic-Reboot "true"`,
  `Automatic-Reboot-Time "04:00"` — по часам сервера, он в UTC (≈ 07:00 МСК); `Remove-Unused-Kernel-Packages`.
  `verify_upgrades` смотрит итог `apt-config dump`, а не файлы. Источники apt образа провайдера — §13.5.
- После перезагрузки: Caddy — `enabled`; сервис — linger и `WantedBy=default.target`; `/run/albums-svc` — tmpfiles.
- **journald:** `Storage=persistent` (каталог `/var/log/journal` root:systemd-journal 2755),
  `MaxRetentionSec=14day`, `SystemMaxUse=1G` — для разбора инцидента. `rsyslog` не ставится, `verify_journald`
  проверяет, что его нет (иначе копия журнала без срока).
- **Агент провайдера** (`guest_agent`). Панель провайдера умеет сброс пароля и снимки; через агент QEMU это
  значит exec и запись файлов от root мимо SSH. Белый список
  `allow-rpcs=guest-ping,guest-sync,guest-sync-delimited,guest-info,guest-fsfreeze-status,guest-fsfreeze-freeze,guest-fsfreeze-thaw,guest-shutdown`
  лежит в **своём** файле `/etc/qemu/sa-qga.conf`, drop-in юнита — `ExecStart=` и
  `ExecStart=-/usr/sbin/qemu-ga -c /etc/qemu/sa-qga.conf` (И3-1): `-c` указывает единственный файл, настройки
  образа (`/etc/qemu/qemu-ga.conf`, `QGA_CONF`) не подмешиваются. Ключ `allow-rpcs` в разделе `[general]` есть в
  qemu-ga 10.0 Debian 13 (**факт**); что `-c` отменяет и `QGA_CONF` из окружения юнита — **гипотеза**, §13.3.
  Файл и drop-in ложатся **до** установки пакета; запущенный агент перезапускается (иначе работает со старым
  списком), не запущенный (нет виртуального порта) — нет. Роль стоит уже в первом плее `bootstrap.yml`. Снимки с
  заморозкой ФС — страховка перед обновлениями; сброс пароля не нужен (пароль администратора в vault, консоль,
  rescue). Белый список, а не `--block-rpcs`: новые команды в обновлениях агента закрыты по умолчанию. Проверка —
  в самой роли (прогон падает) и в `verify_guest_agent`: `ExecStart`, как его видит systemd, — ровно
  `qemu-ga -c <наш файл>`, и `argv[]=` в нём ровно один (Р-23: `regex_search` без обратных ссылок, Р-19, берёт
  первое вхождение, и вторая команда drop-in иначе осталась бы незамеченной); тот же бинарник с теми же аргументами и `--dump-conf` показывает `allow-rpcs` — ровно
  список, `block-rpcs` пуст. Принятый риск: кабинет или токен провайдера = диск через rescue (2FA и хранение
  токена — владелец).
- **cloud-init** (`cloud_init`, И3). Вторая дорога провайдера к паролям и ключам мимо агента: при смене
  метаданных cloud-init при загрузке снова выполняет модули пользователей, паролей и ключей. Списки модулей в
  `cloud.cfg.d` заменяют исходные целиком, а файлы читаются по алфавиту (поздний перекрывает ранний), поэтому
  роль пишет **полные копии** `cloud_init_modules`, `cloud_config_modules`, `cloud_final_modules` без
  `users_groups`, `set_passwords`, `ssh`, `ssh_import_id` в `/etc/cloud/cloud.cfg.d/zz-sa.cfg` — он читается
  последним (прежний `99-sa.cfg` перекрывали файлы вроде `99_provider.cfg`, роль его удаляет — И3-1); сеть не
  трогает. Списки считает сам cloud-init — скрипт `effective_modules.py` (через stdin, `python3 -I`, `chdir: /`):
  - `base` — исходные списки образа без `zz-sa.cfg` (`util.read_conf_with_confd`: `cloud.cfg` и `cloud.cfg.d` по
    алфавиту); из них строится наш файл;
  - `full` — как соберёт cloud-init при загрузке: `helpers.ConfigMerger` с `stages.fetch_base_config`
    (встроенная конфигурация, `/etc/cloud`, `/run/cloud-init/cloud.cfg`, параметры ядра) и cloud-config
    экземпляра (`cloud-config`, `vendor-cloud-config`, `vendor2-cloud-config` в `/var/lib/cloud/instance`,
    И3-9); каждый из этих файлов ещё проверяется отдельно — запрещённый модуль в нём даёт провал с именем файла.

  Запрещённое ищется по **каноническому имени** (`form_module_name` cloud-init: `cc_set_passwords`,
  `set-passwords`, `set_passwords.py` — один модуль, Р-5), как это делает cloud-init: сначала `strip()`, потом
  `form_module_name` (`"ssh.py "` иначе обходил запрет, Р-20); запись — строка, список `[имя, частота]` или
  словарь `{name: …}` (Р-10). Пустые записи (`""`, `[]`, `{}`, `None`, словарь без `name`) cloud-init
  пропускает — пропускает и скрипт (иначе ложный провал на образе провайдера); иная форма — провал, а не пропуск. Проверка — сразу после записи в роли (поздний чужой файл
  вернул бы модули) и в `verify_cloud_init`: итоговые списки (`full`) равны спискам образа без запрещённых
  (`base`), запрещённых нет, `99-sa.cfg` нет, `cloud-init.disabled` — как задано. Не покрыто: datasource без кэша
  на диске и динамические vendor-данные, которых ещё нет в `/var/lib/cloud/instance` (§13.3). Полное выключение
  (`/etc/cloud/cloud-init.disabled`) — переменная `cloud_init_disable`, по умолчанию `false`, пока аренда не
  покажет, что конфигурация сети записана в файл и без cloud-init поднимается (§13.3). Принято, как rescue: код
  через `bootcmd`/`runcmd`/`write_files` в метаданных из кабинета выполняется от root (002-13, И3-9).

## 11. Бэкап на старте

Файлы сервиса — 0600 uid процесса; ACL не поможет: маска ACL нового файла берётся из режима (0600 → `---`).
Чтение — отдельной службой с одной способностью:

- `sa-backup.socket` (шаблон `.j2`): `ListenStream=/run/sa-backup/stream.sock`, `SocketGroup=<группа бэкапа>`,
  `SocketMode=0660`, `Accept=yes`, `MaxConnections=1` (DoS при краже ключа бэкапа — принят).
- `sa-backup@.service` (шаблон `.j2`): `DynamicUser=yes`, `AmbientCapabilities=` и
  `CapabilityBoundingSet=CAP_DAC_READ_SEARCH`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `TemporaryFileSystem=/:ro` +
  `BindReadOnlyPaths=/usr /usr/bin:/bin /usr/lib:/lib /usr/lib64:/lib64 /etc/ld.so.cache /etc/sa-backup
  /etc/albums-svc /var/lib/albums-svc/data`, `RuntimeDirectory=sa-backup-stream` (метка), `PrivateDevices=yes`,
  `ProtectProc=invisible`, `ProcSubset=pid`, `PrivateNetwork=yes`, `RestrictAddressFamilies=AF_UNIX`,
  `SystemCallFilter=@system-service`, `StandardOutput=socket`, **`StandardError=journal`** (И4-1: без него stderr
  дублируется в сокет, строка `sa-backup: tar code=…` и сообщения zstd и age встают внутрь потока, и ни одна копия
  не расшифруется), ужесточение — `RestrictNamespaces=yes`, `SystemCallArchitectures=native`,
  `LockPersonality=yes`, `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`; `RuntimeMaxSec=` из
  переменной (`backup_runtime_max`, старт 6 ч; уточняется замером §13.4), `IOSchedulingClass=idle`. В Debian 13
  `/bin`, `/lib`, `/lib64` — симлинки в `/usr` (**факт**); директивы юнита симлинков в пустом корне не создают,
  поэтому те же каталоги привязываются по путям симлинков (интерпретатор ELF — `/lib64/…`, `#!/bin/bash`). С
  `CAP_DAC_READ_SEARCH` служба читает всё, что видит, поэтому видит она только данные, конфиг и программы: ни
  `/etc/shadow`, ни ключей хоста, ни `/var/lib/caddy`, ни дома `albums`. Этот довод держится на
  `BindReadOnlyPaths`, `ProtectHome`, `SystemCallFilter`, `StandardError` — `verify_backup` сверяет их действующие
  значения через `systemctl show` (`BindReadOnlyPaths` — ровно список выше), мутация — чужой drop-in. Пустой корень
  с `RuntimeDirectory` и `DynamicUser` проверяет на стенде шаг `verify_backup_stream` (§12); `PrivateNetwork=` в
  цели стенда молча не действует — проверка на аренде (§13.6).
- `ExecStart=/usr/local/libexec/sa-backup-stream` — без аргументов, `set -o pipefail`:
  `{ tar --numeric-owner -c data conf 2>/dev/null; код=$?; при коде ≤ 1 — второй архив с одним файлом SA-END
  (дата UTC, число файлов, код tar); } | zstd | age -R /etc/sa-backup/recipients`. `/etc/albums-svc` идёт в архив
  как `conf/` (`--transform`); список файлов (`--index-file`) и метка — в `RuntimeDirectory` (tmpfs, исчезает с
  экземпляром). Код tar 1 («файл изменился»)
  допустим (файлы пишутся атомарно; после возврата — `albums-svc verify`); при коде ≥ 2 метки нет и служба
  завершается с ошибкой. Свои сообщения tar в журнал не попадают: в них полные пути, а id альбома — секрет
  ссылки (001 §7); в журнал скрипт пишет одну строку — код tar и число файлов.
- Пользователь `backup_user` (`sa-backup`): `useradd --no-create-home -K SUB_UID_COUNT=0 -K SUB_GID_COUNT=0`
  (строки subuid/subgid, если есть, удаляются), оболочка `/bin/sh` (её запускает `command=`), пароль `*`, только
  своя группа. **Дом и `.ssh` — root 0755, `authorized_keys` — root 0644** (И4-9): сам пользователь свой ключ и
  команду не поменяет, а `StrictModes` sshd файлы root принимает. Ключ `backup_ssh_key` (открытый — в inventory,
  ed25519 или `sk-`; закрытый — в `~/.ssh` учётной записи раскатки) — роль берёт из него только тип и ключ и
  пишет `restrict,command="socat -u UNIX-CONNECT:/run/sa-backup/stream.sock STDOUT"`.
- `recipients` — открытый ключ age владельца из inventory; закрытый — в файле секретов раскатки (§2). Сервер
  копию расшифровать не может.
- Состав — 001-20: `data/` целиком и конфиг; секрет Podman, дом `albums`, TLS-ключи Caddy — нет.
- **`provision.sh backup`:** `ssh sa-backup@… > $SA_BACKUP_DIR/<UTC-дата>.tar.zst.age.part` (ключ забора — из
  агента скрипта) → проверка в контейнере контроллера `--network=none --log-driver=none`, копия — томом `:ro`,
  ключ age — каналом: `age -d -i /dev/fd/3 | zstd -dc | tar -t --ignore-zeros | tail -n 1` (имена идут в канал,
  на диск ничего) — последний член `SA-END` → переименование без `.part`; иначе — ошибка и удаление `.part`.
  Метка и так пишется только при коде tar ≤ 1. `SA_BACKUP_DIR` — по умолчанию `~/backups` раскатки, 0700, вне
  любого git-дерева.
- **Чистка** — пользовательский таймер раскатки `sa-backup-prune.timer` (раз в сутки; скрипты — копии в
  `~/.local/libexec/sa/` из развёрнутого коммита, И2-2) и конец успешного `backup`: возраст — по дате в имени, не по mtime; копии старше 30 дней удаляются **без исключений**, даже
  единственная (срок хранения — 001-20: фото не живут в копиях дольше срока, в том числе альбомы, удалённые по
  сроку); `.part` старше суток (по mtime) удаляются; файл с именем не по формату чистка не трогает, но
  предупреждает о нём.
- **Свежесть.** Проверенная копия — файл без `.part` (их создаёт только `backup` после проверки метки). Если
  самая новая из них старше 7 дней по дате в имени или копий нет — предупреждение без значений путей: при каждом
  запуске `provision.sh` (любая команда, до «yes») и в выводе `sa-backup-prune` (журнал пользователя раскатки).
  Не отказ: `configure` при остановленном заборе должен работать. Остановку забора видно за три недели до того,
  как чистка удалит последнюю копию.
- **Возврат** (плей — этап 7; `sa-restore` и `verify_restore_rejects` — И4, на стенде). Поток из контейнера
  контроллера (`--log-driver=none`) `age -d -i /dev/fd/3 < копия` → `ssh <admin> 'umask 077; cat >
  /var/lib/sa-restore/incoming.tar.zst'` — каталог администратора 0700, вне `data/`. Ansible с `become` (пароль
  из vault; `NOPASSWD` нет) запускает `sa-restore <файл> <uid> <gid> <предел_байт> <каталог рядом с data/>
  [запас_байт]` (предел — `restore_max_bytes` плея). От root он только проверяет аргументы, создаёт временный
  каталог `.restore.XXXXXX` рядом с `data/` (`umask 077`, `chown` на uid сервиса, 0700), открывает файл и отдаёт
  его работнику **дескриптором 3**, а не через stdin: файл в каталоге администратора uid сервиса не прочтёт,
  открывает его root. Всё, что разбирает недоверенный поток, идёт от uid сервиса — `setpriv --reuid=<uid>
  --regid=<gid> --clear-groups --no-new-privs --inh-caps=-all`: age не удостоверяет отправителя, копию может
  собрать любой с открытым ключом владельца (аудит). Правила — `deploy/backup/sa-restore.sh`, у каждого метка
  `# rule:<имя>` для мутаций (§12).
  1. **Работник** (И4-3, И4-13): `uid > 0` и `gid > 0`; uid — внутри диапазона subuid пользователя `albums`, gid
     — внутри subgid (защита в глубину: не root и не чужой пользователь). Числа — только десятичные (`10#`:
     «059000» иначе читалось бы как восьмеричное).
  2. **Одна копия потока:** сжатый поток ложится одним файлом во временный каталог (`head -c`, предел сжатого —
     `предел + предел/16 + 1 МиБ`), разбор и распаковка читают один и тот же файл — без гонки с подменой.
     Распакованный поток длиннее предела — отказ.
  3. **Разбор** целиком до первой записи на диск: `tar --list --verbose --numeric-owner --ignore-zeros
     --quoting-style=escape` (перевод строки в имени не подделывает строку) → `awk`:
     - только обычные файлы и каталоги (ссылки, каналы, устройства — отказ);
     - строка листинга — **ровно 6 полей** (`NF == 6`), дата `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`, время
       `^[0-9]{2}:[0-9]{2}$`: огромный `mtime` в pax-заголовке печатается одним числом и сдвинул бы первое слово
       имени в «дату», обходя правила имени;
     - **белый список имён** `^[A-Za-z0-9._/-]+$` (пробел, `;`, управляющие — отказ); без абсолютных путей и
       `..`; **глубина ≤ 8** компонентов, **длина имени ≤ 255**; только под `data/` и `conf/`; последний член —
       `SA-END` (обычный файл), после него — ничего;
     - **сумма:** каждый член — `4096 + размер`, округлённый вверх до 4096 (пустой каталог весит 0, а занимает
       inode и блок), плюс 4096 и +1 к числу членов за каждый новый **неявный каталог** (член `data/a/…/f` —
       одна строка, а tar создаёт все промежуточные каталоги); сумма ≤ предела; **число членов** ≤
       `SA_RESTORE_MAX_MEMBERS` (по умолчанию 500 000);
     - место: сумма + запас (по умолчанию 1 ГиБ) ≤ свободного на разделе каталога.
  4. **Распаковка** — тем же uid: `tar --extract --ignore-zeros --no-same-owner --no-same-permissions
     --no-overwrite-dir` в `new/` временного каталога; `SA-END` и сжатая копия удаляются; последняя строка
     вывода — путь `new/`. Отказ — код 1, причина в stderr, временный каталог удалён, `data/` не тронут.
  5. Плей возврата (этап 7): `albums-svc verify` → подмена `data/` при остановленном сервисе →
     `albums-svc after-restore`; входящий файл и временный каталог удаляются.
- **`conf/` из копии в `/etc/albums-svc` не ставится — требование к этапу 7** (И4-11). age не удостоверяет
  отправителя: подложенный `conf/config.json` с чужим `sha256` дал бы токен загрузки. Конфиг — только из inventory
  (`configure`); `sa-restore` пишет только во временный каталог, плей возврата берёт оттуда лишь `data/`.
- **Место на диске при возврате — принятый остаток** (И4-12). Сжатая копия ложится на раздел `data/` до проверки
  места: входящий файл, его копия у работника и распакованное — в худшем случае ≈ 3× объёма. Объём распаковки
  заранее не известен, предел уже стоит, возврат — ручное действие администратора. Пересмотр — этап 7 (restic).
- Пакеты `age`, `zstd`, `socat` есть в Debian 13 (**факт**).

## 12. Проверки (`deploy/tests/provision/run.sh`)

Стенд, `run.sh`, `target.Containerfile` и шаг `static` — задача П2a (первой); задание CI — П2. Шаги отдельные
(каждый короче 4 мин), стенд живёт между шагами: `static` без цели (`tofu_fmt`, `tofu_validate`, `tofu_test`,
`tofu_static`, `ansible_lint`, `yamllint`, `syntax_check`, `shellcheck`, `selftest`, `provision_cli`,
`ctl_shared`), `up`, на цели по порядку `converge_bootstrap`, `converge_site`, `idempotence`, `verify_ssh`,
`verify_nft`, `verify_host`, с И4 — `verify_service` (`verify_podman`, `verify_caddy` с `caddy_key_fingerprint`,
`verify_albums`, `verify_backup`, порт 80 → 308, `verify_restore_rejects`; `service.sh`), `verify_backup_stream`
(`stream.sh`) и мутации `mutate_*` (ниже), затем `down`.

- **Плеи стенда идут тем же кодом, что у `provision.sh`** — `ops/ctl-run.sh` (Р-1): argv `podman run`, тома,
  окружение, контракт `-e`, канал vault. Своё у стенда — адрес цели, сеть, свой `ssh-agent` с ключом стенда и
  снимок рабочей копии (без приватного inventory) в собственном каталоге с файлом-меткой: удаляется только он
  (Р-13). `ctl_shared` сравнивает argv контроллера с эталоном целиком (Р-16), проверяет, что запуск не
  собирается где-то ещё и не уезжает в libexec или тестовое дерево, что `provision.sh` не задаёт
  `ctl_podman_extra` (добавочные флаги podman — только у стенда, Р-21), и что три копии белого списка символов
  совпадают побайтово (Р-12).
- **Цель** — один `target.Containerfile`: `debian:trixie` с `public.ecr.aws/docker/library/` по дайджесту +
  `systemd`, `openssh-server`, `python3`, `sudo`, `ca-certificates`, `cloud-init` (остальное ставят роли).
  `podman run --systemd=always --cap-add NET_ADMIN,SYS_ADMIN,DAC_READ_SEARCH,SYS_PTRACE`: без `NET_ADMIN` `nft` —
  EPERM (**факт**); без `SYS_ADMIN` юниты с песочницей (`sa-backup@`, вероятно `caddy`) падают с 226/NAMESPACE, а
  `CAP_DAC_READ_SEARCH` нужен службе бэкапа (**факт**, прототип ревью). **`SYS_PTRACE` — только у цели стенда**
  (И4-9): без него `ss -p` в `verify_listeners` не видит процессы чужих пользователей (Caddy — «?»); роли и
  сервер его не требуют. `PrivateNetwork=` в такой цели молча не применяется (**факт**) — проверяется только на
  аренде. `policy-rc.d` Docker-образа Debian (`exit 101`) цель удаляет: в облачном образе его нет, а роль `caddy`
  чужой файл отвергает (§8). Это стенд, не роли.
- **Только стенд:** пользователь образа `debian` с ключом стенда и `/etc/sudoers.d/90-cloud-init-users`
  (`NOPASSWD`, как от cloud-init) — `converge_bootstrap` идёт через него (`bootstrap_user: debian`,
  `image_user: debian`), затем повторяет `bootstrap.yml` от администратора (`changed=0`); `verify_users` после
  `bootstrap` видит пользователя образа заблокированным, после `site.yml` — удалённым; путь через root
  проверяется на аренде, если образ его даст. `/etc/hosts` цели с ACME-серверами на 127.0.0.1; слушатель на 8080
  для `verify_nft_ports`; в CI — `registries.conf.d` для локального реестра. Ключи, пароль vault (случайный),
  ключ age, файл секретов и его фраза — на каждый запуск в `XDG_RUNTIME_DIR` (0700), в вывод не идут. Ни токена,
  ни сети провайдера.
- **Порядок из-за `PerSourcePenalties`:** `verify_ssh` — сначала положительные (`verify_ssh_admin_key`,
  `verify_admin_sudo`, `verify_sshd_effective`), потом отрицательные (`verify_ssh_root_denied`,
  `verify_ssh_password_denied`, `verify_ssh_foreign_key_denied`, `verify_ssh_image_user_denied`) — из отдельного
  контейнера проверок со своим адресом, чтобы штраф не задел адрес контроллера.
- **Локально** — rootless-цель, `converge_site` и `idempotence` идут с **`--skip-tags albums-runtime`**
  (И4-9): вложенному rootless Podman и `chown`/ACL на uid `начало + 65531` не хватает карты (**факт**, research
  §2). «Стендового» диапазона subuid нет. Задачи-факты, нужные `albums-runtime`, несут его тег: `--tags
  albums-runtime` отдельно тоже работает (И4-8). `vault.yml` стенда содержит `vault_albums_master` (`assert` роли
  идёт и без `albums-runtime`).
- **Полный прогон** — `SA_STAND_FULL=1` (без `--skip-tags`), в CI на rootful-цели (задача П2): `sudo podman`
  (**гипотеза**: проверить первым прогоном), образ сервиса из `deploy/Containerfile` в контейнере `registry` по
  дайджесту. **В П2 остаются:** весь `albums-runtime` (linger, ACL, данные, секрет, образ, юнит, healthcheck),
  `verify_acl`, `/v1/health` через сокет от имени `caddy`, `idempotence_full`, `verify_after_restart`; там же —
  `verify_backup_stream` в полном режиме (данные роли; локально его логика та же, но на rootful-цели шаг ещё не
  запускался). Задание `stand-full` (`.github/workflows/ci.yml`) — push в `ci/**` и `workflow_dispatch`, без
  `pull_request` и `pull_request_target` (решение П2-2), `permissions: contents: read`, без секретов; SKIP любого
  шага в CI — провал (`deploy/tests/provision/ci.sh`). Образ сервиса собирается из `deploy/Containerfile` и тянется
  целью из одноразового реестра стенда по дайджесту (П2-1).
- `idempotence` — по `PLAY RECAP`: повторный `site.yml` — `changed=0`, `failed=0`.
- `verify_*` — задачи `verify.yml` на цели (`sshd -T`, `nft -j list table inet sa_filter`, `ss -Htulpn`,
  `getent`, `/etc/subuid`, sudoers, файлы запуска администратора, `apt-config dump`, `systemctl`, `qemu-ga
  --dump-conf`, итог cloud-init; с И4 — `apt-cache policy caddy`, `caddy version`, `systemctl show` юнитов Caddy и
  службы копии, маски Podman и `caddy-api`, владельцы и sha256 файлов ролей) и проверки из соседнего
  контейнера: порты 22/80/443/8080 (`verify_nft_ports`; с И4 — `curl -I -H 'Host: <домен стенда>' http://<цель>/`
  → 308). `verify_nft_reload_only` — в ролях нет `restarted`/`stopped` для `nftables` (`nft_reload_only.py`).
- `verify_backup_stream` (**стенд**, И4-10, И4-15; `stream.sh`): забор от root через сокет, как у ключа
  забора (`socat -u UNIX-CONNECT:… STDOUT`), без ssh; получатель age на время шага подменяется тестовым ключом.
  Нет `data/` (локально) — шаг создаёт его с выдуманными файлами (uid 59000, 0600) и удаляет; есть
  (`SA_STAND_FULL=1`) — кладёт файлы во временный подкаталог. Проверки: поток расшифровывается (stdout и stderr не
  смешаны, И4-1), в нём `data/…` и `conf/config.json`, `SA-END` последний и один; приманка в
  `~albums/.local/share/containers/storage/secrets` отсутствует и по имени, и по содержимому; в журнале службы
  от начала шага — ровно одна строка `sa-backup: tar code=[01] files=N`, путей данных нет. Отказ: drop-in
  `SystemCallFilter=~getdents64` + `SystemCallErrorNumber=EIO` — tar не читает каталоги, в журнале ровно одна
  `tar code=2`, метки в потоке нет (переименовать `/etc/albums-svc` нельзя: путь в `BindReadOnlyPaths` без «-»,
  служба упала бы с 226/NAMESPACE до tar). Всё подменённое возвращается при любом выходе.
- `verify_restore_rejects` (**стенд**, в `verify_service`): положительный контроль раньше отрицательных —
  хороший архив распакован, файлы принадлежат uid работника (так видно, что разбор и распаковка шли не от root),
  `SA-END` удалён. Отказ (код 1 и **своя** причина в stderr, И4-14) — на архивы `symlink`, `hardlink`, `dotdot`,
  `absolute`, `foreign` (вне `data/`/`conf/`), `nomarker`, `toolong`, `mtime` (pax с `mtime=1e17`), `charset`,
  `dirs` (1500 пустых каталогов), `many` (предел членов), `fifo`, `deep`, `namelen`, `fanout` (неявные каталоги),
  на нехватку места, uid/gid вне subuid/subgid и uid 0; после отказов `data/` не тронут, временных каталогов нет.
  Тест подставляет свои файлы subuid/subgid (`SA_RESTORE_SUBUID_FILE`, `SA_RESTORE_SUBGID_FILE`) и uid 59000.
- **Мутации** (`mutate_*`, `service.sh`): каждая защитная проверка ролей И4 обязана покраснеть на своей порче —
  с ожидаемым текстом (у циклов — `failed: [server] (item=X)`, а не просто `item=X`: эта строка есть и у
  прошедших элементов), затем возврат и зелёный прогон.
  `mutate_restore_a…d` — `sa-restore` ослабляется снятием строк с меткой `# rule:<имя>`, соответствующий архив
  должен пройти; `mutate_caddy`, `mutate_caddy_b` — ключ, Caddyfile, закрепление, служба, дом, остаток
  `policy-rc.d`, чужой drop-in `zz-mut.conf`; `mutate_policy` — чужой `policy-rc.d` роняет `site.yml` и остаётся,
  свой остаток снимается; `mutate_host` — маски Podman, режим `config.json`, оболочка и subuid `albums`, ACL
  каталога сокета; `mutate_backup` — `authorized_keys`, subuid пользователя бэкапа, получатель, чужой drop-in
  службы копии.
- `provision_cli` — без сети и секретов, в контейнере `--network=none`: пользователь = `SA_OPS_USER`, поддельный
  `loginctl` (`Type=tty`, `User`, `Remote` задаются тестом), фраза и «yes» — через псевдотерминал (свой драйвер).
  *Заменено §16.8: случаи `DISPLAY`, `Type=x11` и требование `Type=tty` сняты, добавлены метка машины и сессии.*
  Код 3 на каждый отказ §2 (в том числе `DISPLAY`, `Type=x11`, чужой `User`, чужой `XDG_RUNTIME_DIR`, грязное
  дерево, `alternates`, изменённый `.gitattributes`, символ вне белого списка в `deploy/`, режим не 100644/100755,
  файл секретов 0644; у `review.sh` — bundle-симлинк, bundle больше предела, режимы и белый список до checkout),
  код 2 на B/C. «Клон» в тесте — копия только файлов, видимых git (`git ls-files --cached --others
  --exclude-standard` по `deploy/` и странице 404, без `deploy/inventory`, `__pycache__`, `*.pyc`, Р-21):
  неотслеживаемый мусор рабочей копии не меняет состав и не роняет проверки. Тестовый файл секретов с меткой
  вместо токена — метки нет в снимке argv
  процессов, в `podman inspect` контейнера, в файлах inventory, в журнале (`journalctl --user`) и в выводе; у
  каждого `sa-prov-*` `podman inspect --format '{{.HostConfig.LogConfig.Type}}'` = `none`. `backup` на поддельном
  потоке без метки — ошибка, `.part` удалён. Чистка и свежесть на поддельных копиях (даты в имени задаются
  относительно текущей): единственная копия старше 30 дней удалена; новейшая старше 7 дней или копий нет →
  предупреждение и в выводе любой команды, и в выводе `sa-backup-prune`; новейшая моложе 7 дней →
  предупреждения нет.
- **Гипотезы П2a** проверены в И2 шагом `provision_cli` (П2a-3): `age -d -o /dev/fd/3` в `podman run -it` — на
  настоящем Podman, фраза с терминала, вывод каналом до основной оболочки; `ssh-add` с
  `SSH_ASKPASS_REQUIRE=force` передаёт помощнику унаследованный дескриптор. Запасной путь с именованным каналом не
  нужен.

## 13. Только на пробной аренде

То, что видно только на настоящем образе провайдера и в его панели. Итог каждого пункта — в research; если он
меняет роли — развилка в decisions.

1. **Ресурсы.** `apply` с настоящим токеном; `clo_project_image "Debian 13"`; PTR без A-записи; у сервера нет
   других внешних адресов, кроме привязанного; `apply --replace-server` — IP остаётся и снова привязан.
   Короткоживущий токен и отзыв после `apply` — умеет ли провайдер.
2. **Первый вход, sshd, администратор.**
   - Первый вход: root или пользователь образа (его имя → `image_user`); задан ли пароль root. Веб-консоль
     паролем администратора; rescue. Необязательно: импорт открытого ключа `sk-ssh-ed25519` в провайдер (для
     будущего FIDO2).
   - `sshd_config` образа (И3-1): директивы до `Include`, свои drop-in раньше `00-sa.conf` (`00-cloud…`,
     `000-…`), `Match`; `sshd -T` после `bootstrap`.
   - Новые поля `sshd -T` OpenSSH 10 в Debian 13 (Р-4): как печатаются `AuthorizedKeysCommand`,
     `AuthorizedPrincipalsCommand`, `AuthorizedPrincipalsFile`, `TrustedUserCAKeys` со значением `none`,
     `DisableForwarding`, `PerSourcePenalties` — `sshd_effective.yml` сравнивает строки буквально.
   - `user_readenv` у `pam_env` в `/etc/pam.d/sshd` образа (Р-14): при `user_readenv=1` `~/.pam_environment`
     задаёт окружение входа мимо `PermitUserEnvironment no`; проверка файла в `verify_users` — запасная.
   - Файлы запуска администратора (Р-3, Р-14): `.bashrc`, `.profile`, `.bash_logout` в `/etc/skel` образа (образ
     мог положить свои) и `verify_users` на настоящем образе; разрешающий список дома (Р-21): что ещё кладут
     образ и первый вход (`.cache`, `.cloud-locale-test.skip` и т. п.) — иначе `configure` падает на чистом сервере; интерпретатор, который нашёл Ansible
     (`ansible_facts.python.executable`), — ровно `/usr/bin/python3`, а не найденный по `PATH`.
3. **Сеть, cloud-init, агенты.**
   - Скан с машины владельца: открыты только 22/80/443; интерфейс локальной сети закрыт; DHCPv4 продлевается
     (packet-сокет мимо фильтра — гипотеза §6); IPv6.
   - cloud-init 25.x: канонические имена модулей образа — `form_module_name` функцией модуля
     `cloudinit.config.modules` или методом класса (скрипт берёт оба); словарная форма записей
     (`{name: …, frequency: …}`) в `cloud.cfg` образа (Р-5, Р-10).
   - `*_modules` в метаданных экземпляра CLO (user-data, vendor-data): задаёт ли провайдер списки стадий в
     cloud-config экземпляра — тогда они выше `cloud.cfg.d`, и роль падает с именем файла (И3-9). Командная
     строка ядра и datasource без кэша на диске — что cloud-init получит только при следующей загрузке.
   - Как CLO делает **сброс пароля и смену ключа** из панели (И3-9): через агент QEMU
     (`guest-set-user-password`, `guest-exec`, `guest-file-*` — закрыты белым списком), через метаданные и
     cloud-init (модули убраны) или иначе — после нашей настройки ни то, ни другое не должно срабатывать.
   - Откуда сеть (файл или генерация при каждой загрузке); после перезагрузки не возвращаются ключи root,
     пользователь образа, `50-cloud-init.conf`; можно ли `cloud_init_disable: true`.
   - Агент QEMU: `qemu-ga.conf` и `QGA_CONF` в образе (`Environment=` юнита, `/etc/default/…`) — `-c` своего
     файла их отменяет (`verify_guest_agent` запускает `--dump-conf` вне окружения юнита); снимок с заморозкой ФС
     есть, сброса пароля нет.
   - Другие агенты провайдера в образе (мониторинг, свой агент панели и т. п.): службы и слушатели
     (`systemctl list-units`, `ss -Htulpn`), что они умеют от root; лишнее — выключить ролью (развилка по итогу).
   - `growpart` после увеличения диска.
4. **Эксплуатация.** A-запись (владелец) → сертификат (§13.6); доступ к реестру образов; ночная перезагрузка;
   повторный `up` — `changed=0`; `provision.sh backup` — время и размер потока → `backup_runtime_max`.
5. **Образ: ядро, apt, sudoers** (И3-10).
   - `kernel.yama.ptrace_scope` (ожидается ≥ 1): иначе процесс администратора, запущенный до проверки файлов
     запуска, читает память сессии Ansible той же учётной записи (пароль `become`, §14). Если 0 — sysctl в роли
     `bootstrap` (развилка).
   - Источники apt образа (зеркало провайдера, свои репозитории и ключи): `52sa-unattended` пропускает только
     безопасность Debian, но `apt full-upgrade` в `bootstrap.yml` и установка пакетов берут все источники образа.
   - `sudoers.d` образа, кроме `90-cloud-init-users`: чужой `NOPASSWD` ловит `verify_users`, но роль его не
     удаляет.
6. **Сервис, Caddy, бэкап** (И4).
   - Образ Debian — пункт 2 в силе и для ролей И4: пользователь образа, `sshd_config` до `Include`, `/etc/skel`,
     разрешающий список дома администратора; плюс `/usr/sbin/policy-rc.d` в образе — роль `caddy` чужой файл
     отвергает (§8), тогда развилка.
   - Источник `podman`: `verify_podman` требует пакет и бинарник из Debian; зеркало или свои репозитории образа
     (§13.5) могут дать другую версию — смотреть `apt-cache policy podman`.
   - Caddy 2.11.x: закреплённая версия есть в репозитории на дату аренды (иначе — путь исправлений §8); **ACME на
     настоящем домене** после A-записи: сертификат ложится в `/var/lib/caddy` и продлевается при суженном drop-in
     (одна способность, `SystemCallFilter`, `RestrictAddressFamilies` с `AF_NETLINK`, `PrivateDevices`) — на стенде
     TLS без сертификата этого не видно.
   - `PrivateNetwork` у `sa-backup@`: на стенде молча не действует (**факт**); на сервере — у экземпляра службы
     нет сетевых интерфейсов, кроме `lo`, а поток через сокет при этом идёт.
   - **Rootful-прогон `albums-runtime`:** если CI (П2) ещё не дал зелёного полного прогона, первый `configure` на
     аренде — первая проверка linger, ACL на uid процесса, `chown` данных, секрета Podman, образа по дайджесту,
     юнита и healthcheck; затем `/v1/health` через сокет от `caddy` и `verify_after_restart` после перезагрузки.
   - **Переменные inventory**, без которых `site.yml` падает на `assert` до изменений: `vault_albums_master`
     (vault, `albums-svc -gen-secret`), `albums_subuid`, `albums_svc_image` — с дайджестом `@sha256:`,
     `albums_owners` (slug, студия, квоты, `sha256` токенов), `backup_ssh_key`, `backup_age_recipient`,
     `backup_runtime_max`; образец — `inventory.example`, значения — приватный inventory.

## 14. Модель угроз (полная)

> **Поправка 03.11.2026 (§16.6).** Затронуто пять строк, у каждой пометка: «Процесс Claude Code читает секреты
> раскатки» — заменена; «Root на машине владельца при создании учётной записи раскатки» и «Перехват ввода в
> графической сессии владельца» — отменены; «Подмена `~/.bashrc`/`PATH` владельца» (часть о recovery) и «Inventory
> как канал кода» (консоль → сессия `sa-ops` ops-ВМ) — уточнены.

| Угроза | Мера | Остаток |
|---|---|---|
| Процесс Claude Code читает секреты раскатки | отдельная учётная запись, вход только на консоли; дом и `XDG_RUNTIME_DIR` 0700; файл секретов по фразе с консоли; свой Podman и агент | ошибка владельца (секрет под своим uid); страж — от случайности. *Заменена §16.6* |
| Root на машине владельца при создании учётной записи раскатки | один раз в recovery-режиме GRUB, без файлов запуска и агентов владельца; в обычном режиме root-оболочки нет; `pam_access` — только `tty1`–`tty6`; перед входом раскатки — ни одного сеанса владельца, автовход выключен (Р-6, Р-15, И2-6) | ошибка владельца в порядке входа; физический доступ к машине. *Отменена §16.1* |
| Вывод контейнеров с секретами в журнале (conmon → journald), журнал раскатки читает владелец через `adm` | `--log-driver=none` у каждого `sa-prov-*`, проверка в `provision_cli`; раскатка не в `adm`/`systemd-journal` | — |
| Перехват ввода в графической сессии владельца (XTEST, XInput, `chvt`) | пароль и вход раскатки — только на текстовой консоли без сеансов владельца; владелец не в `input`/`tty`; отказ при `DISPLAY`/`WAYLAND_DISPLAY`, не-tty или чужой сессии | — *Отменена §16.1, §16.4* |
| Подмена `~/.bashrc`/`PATH` владельца: его пароль `sudo` крадётся в его же терминалах | вне границы этой спеки; совет runbook: `sudo` владелец набирает не в терминале с агентами (на консоли); root для раскатки — только в recovery (Р-6); *recovery заменён §16.1: раскатка — в ops-ВМ* | root машины разработки = всё |
| Процесс Claude Code подменяет код раскатки (роль, HCL, образ контроллера, скрипт просмотра) | просмотр до checkout (`review.sh`: копия bundle с пределом, verify и `fsck --strict` во временном репозитории, полный diff как текст в `less` без `R`, `checkout` после «yes» и без атрибутов); второй взгляд в `provision.sh`; режимы по всему дереву и типы `deploy`/404 (Р-2); белый список символов (Р-8, Р-12); отказ при изменённом `.gitattributes`; контроллер из просмотренного клона по ID, argv — один код `ctl-run.sh`; скрипты без консоли — копии из развёрнутого коммита (И2-2); `tofu_static` | владелец не прочёл diff; подписанный тег — позже |
| Кириллический двойник ASCII в коде (`а` вместо `a` в имени или ключе) | белый список без латинских двойников, `shellcheck`, тесты | принят (Р-12): рубеж — просмотр diff владельцем |
| Подмена HCL, которую `tofu_static` не ловит | `tofu_static` (вложенные блоки, `backend`, `ephemeral`, `encryption`, мутации), состав каталога перед `apply` | принят (Р-9): против агента рубеж — просмотр diff владельцем (`tofu_static` — код того же дерева); перенос критичных проверок HCL в `review.sh` из прошлой просмотренной версии — открытый вопрос И4/П2 |
| Токен API во время `apply` | файл 0600 и том `:ro`, без `-e`; `ephemeral`; запрет `encryption` (Р-11); отказ при токене в окружении, `TF_LOG*`, `CLAUDECODE` | процессы той же учётной записи раскатки во время прогона |
| Пароль vault | канал `--preserve-fds` на каждый плей, без файла | память ansible |
| Ключ SSH сервера, ключ забора | ed25519 с фразой из файла секретов, свой агент на время прогона, только ключ команды, срок 1 ч, `trap` | агент во время прогона (сирота — не дольше часа); FIDO2 — позже |
| Два прогона сразу, блокировка у сироты | `flock` на `inventory/.lock`; демоны без дескриптора блокировки (Р-7) | — |
| Файл секретов | age по фразе (scrypt), фраза с консоли каждый запуск, без кэша; вывод — каналом, не в терминал; 0600 | слабая или потерянная фраза — runbook |
| Inventory как канал кода (`group_vars` — Jinja, `deployed-commit`, `hosts.yml`, `known_hosts`) | только `~/inventory` раскатки, правка на её консоли; владелец и права inventory, копий, каталога секретов и их родителей (И2-5) | изменения из сессии владельца — просматривать как код. *§16.1: правка — в сессии `sa-ops` ops-ВМ; пришедшее из рабочей ВМ — как код* |
| Кабинет/токен провайдера | 2FA; агент — белый список; модули cloud-init для пользователей и ключей убраны | диск через rescue, код через `bootcmd`/`runcmd` — приняты; сброс пароля и смена ключа из панели — аренда (§13.3) |
| Настройки образа перекрывают наши (`cloud.cfg.d`, `qemu-ga.conf`, `sshd_config` до `Include`) | роли проверяют итоговую конфигурацию (И3-1): `ConfigMerger` cloud-init с cloud-config экземпляра, `--dump-conf` агента с `ExecStart` юнита, `sshd -T -C`; провал — откат или падение прогона | метаданные без кэша, окружение юнита агента — аренда (§13.3) |
| Самоблокировка | `assert` ключей и ключа контроллера; пробный вход + `sudo -S -v` на каждом прогоне до закрытия root; безусловный `reload` и отказ в пароле живым демоном (И3-8); `block/rescue` в обоих плеях | консоль, rescue |
| Подмена файлов запуска администратора (`~/.bashrc`, `PATH`, `~/.ssh/rc`, опции в `authorized_keys`) — root при следующем прогоне | `interpreter_python` путём; `verify_admin.yml` в конце каждого `configure` и в стенде — файлы как в `/etc/skel`, разрешающий список дома, симлинки и ошибки `find` — провал, `authorized_keys` без опций; `site.yml` падает, если ключи администратора пришлось менять (Р-3, Р-14, Р-19, Р-21…Р-23) | нужна кража ключа администратора; `.bashrc`, выполненный в прогоне до проверки, видит stdin с паролем `become` того же прогона — принят (Р-3), подмену находит конец этого же прогона или следующий |
| Чужой код в проверках под root (`json.py` в доме администратора) | скрипт через stdin, `python3 -I`, `chdir: /` (И3-7) | — |
| Пользователь образа с `NOPASSWD` | блокировка вторым плеем под администратором, `terminate-user` и `userdel` в `site.yml`, `assert` пользователя подключения и ≠ администратор, поиск `NOPASSWD` во всех sudoers (И3-3) | возврат cloud-init, чужие `sudoers.d` образа — аренда |
| Пересечение subuid | у админа и бэкапа нет subuid; `assert` диапазона `albums` | — |
| IP удалён вместе с сервером | отдельная привязка | гипотеза API — аренда |
| Открытый порт, внутренняя сеть, сброс правил | своя таблица, drop везде, только `reload`, пустой `ExecStop` (И3-2), `verify_listeners` | — |
| Подмена провайдера, пакетов, образов | lock + `SHA256SUMS` (сверка — владелец), ключ Caddy по отпечатку, дайджесты, pip с хэшами | пакеты контроллера из apt без версий (И3-6); источники apt образа — аренда |
| Секрет во временных файлах Ansible | `pipelining`, `acl`, запрет world-readable и `KEEP_REMOTE_FILES`, `no_log`, `stdin` | — |
| Служба бэкапа видит лишнее | пустой корень и привязка только нужного, `ProtectProc`, `ProtectHome`, фильтр вызовов; действующие значения — `systemctl show` в `verify_backup` | `PrivateNetwork` — аренда (§13.6) |
| Служебный вывод внутри потока копии (копия не расшифруется) | `StandardError=journal`, проверка расшифровкой в `verify_backup_stream` (И4-1) | — |
| Пути с id альбомов в журнале | stderr tar — в `/dev/null`, в журнал код и число | — |
| Кража ключа бэкапа | `restrict,command=`, поток шифрован ключом владельца; дом и `authorized_keys` — root (ключ и команду пользователь не правит) | DoS `MaxConnections=1` — принят |
| Неполная копия | метка вторым архивом только при tar ≤ 1, проверка последнего члена | — |
| Вредный или подложенный архив при возврате | root только создаёт каталог и отдаёт поток дескриптором 3; работник — uid/gid > 0 из subuid/subgid `albums` (`setpriv`); строгий листинг (`NF == 6`, белый список имён, глубина, длина), только файлы и каталоги, сумма с неявными каталогами, предел членов, `--no-same-owner` | место на диске при возврате (И4-12) — принят до этапа 7 |
| Подложенный `conf/config.json` в копии (чужой `sha256` токена) | `conf/` из копии не ставится, конфиг — только из inventory (И4-11) | требование к плею возврата этапа 7 |
| Caddy разбирает интернет | одна способность `CAP_NET_BIND_SERVICE`, `NoNewPrivileges`, фильтр вызовов, `PrivateDevices`, семейства адресов, без `--environ`; `caddy-api` замаскирован (§8) | — |
| `policy-rc.d` остаётся после обрыва и блокирует перезапуски служб | файл с меткой, снятие в следующем прогоне, чужой — `fail`, `verify_caddy` (И4-4) | — |
| Открытые фото на машине владельца | расшифровка только в поток | — |
| Потеря всех копий при остановке забора | предупреждение, если новейшая проверенная копия старше 7 дней: каждый запуск `provision.sh` и таймер чистки | забор остановлен больше 3 недель без реакции — копий нет (принято: срок 001-20) |
| Caddy без исправлений | недельная проверка GHSA, этап 7 — Renovate | окно до недели |
| Вредный PR в CI | чужой PR задание не запускает (push `ci/**` и `workflow_dispatch` — только с правом записи), `contents: read`, без секретов | — |

## 15. Требования к стражу (правка правил — «да» владельца)

> **Поправка 03.11.2026.** Граница — ВМ раскатки (§16.1); в рабочей ВМ `sa-ops` нет, правила ниже остаются защитой
> от случайности; каталог входящих — `/srv/sa-inbox` и `/mnt/hgfs` (§16.5).

Граница — учётная запись раскатки и её сессия на консоли (§2). Страж — **дополнительная** защита от
случайности: текстовые правила обходятся, полагаться на них нельзя.

- Для всех процессов Claude Code, включая оркестратора, в любом виде (путь, `sh -c`, интерпретатор, `script`):
  - `provision.sh` с любой командой (`up`, `apply`, `configure`, `backup`, `forget-host`);
  - `sudo`, `su`, `runuser`, `machinectl`, `systemd-run --uid`, `ssh` к учётной записи раскатки; чтение и
    листинг её дома, `XDG_RUNTIME_DIR` и каталога входящих (запись bundle — только владелец);
  - строки `TF_VAR_clo_api_token`, `CLO_API_AUTH_TOKEN`, `SA_SECRETS`, `secrets.age`;
  - менеджеры секретов и расшифровка: `pass`, `gopass`, `secret-tool`, `keepassxc-cli`, `op`, `bw`, `gpg -d`,
    `age -d`, `ansible-vault view|decrypt|edit`;
  - журнал раскатки: `journalctl` с `_UID=`/`--user-unit`/`-u` для её uid и юнитов, `podman logs` контейнеров
    `sa-prov-*`;
  - ввод и перехват в графической сессии: `xdotool`, `xinput`, `xprop`, `xev`, `ydotool`, `chvt`;
  - правка `~/.bashrc`, `~/.profile`, `~/.bash_profile`, `~/.config/environment.d/`, каталогов из `PATH`
    владельца, `PATH=`/`alias sudo`/функции `sudo` в файлах запуска оболочки;
  - `podman inspect|exec|cp|export|commit|mount` для контейнеров `sa-prov-*`; `/proc/*/environ`, `/proc/*/root`;
  - `ssh-add`, `SSH_AUTH_SOCK=` на чужой сокет, `socat`/`nc` на unix-сокеты агентов;
  - `ansible`, `ansible-console`, `ansible-pull`; `ansible-playbook` — только с inventory стенда из
    `deploy/tests/provision/`, без `-e ansible_host=` и `-e @…`;
  - `SA_INVENTORY=` и чтение inventory — только внутри `.gotmp/`; запись в `deploy/` — только в своём worktree.
- tofu/terraform в любом виде (в том числе в `podman run`) — только `fmt`, `validate`, `test`,
  `init -lockfile=readonly`; `plan`, `apply` и `providers lock` — владелец.
- При старте сессии — предупреждение, если в окружении токен, `ANSIBLE_VAULT_PASSWORD*` или агент с ключом сервера.
- Проверочные случаи стража — на каждый пункт выше и на обходы из аудита.

## 16. Поправка 03.11.2026: машина раскатки — отдельная ВМ (ops-ВМ)

Решение владельца — decisions ВР-1…ВР-9; факты и отвергнутое — research §4; устройство хоста — см. приватные
заметки. Поправка **заменяет**: в §1 — строку о каталоге входящих; в §2 — пункты «Создаёт владелец… recovery»,
«Вход — только отдельной сессией на текстовой консоли», «Проверка сессии» и `head -c` в «Просмотре до checkout»; в
§12 — случаи сессии в `provision_cli`; в §14 — пять строк (список и пометки — в шапке §14); в §15 — вводный абзац
и `INCOMING`. У каждого заменённого места — пометка со ссылкой сюда. Остальное в §2–§15 в силе: учётная запись
`sa-ops`, файл секретов, просмотр до checkout, контроллер по ID, `flock`, ключи в своём агенте.

### 16.1. Граница — ВМ, а не учётная запись

Обе ВМ — гости одного настольного гипервизора (VMware); владелец работает с хостом через удалённый доступ.

| Машина | Что в ней | Чего нет |
|---|---|---|
| рабочая ВМ | учётная запись владельца, Claude Code и его агенты, Node, рабочие копии | секретов раскатки, inventory, копий, `sa-ops` |
| ops-ВМ | полный клон чистого снимка Ubuntu 24.04; администратор владельца (`sudo`), `sa-ops` без `sudo`; графика; NAT; Podman, git | агентов, Claude, Node; SSH-сервера; общего буфера и перетаскивания (ВР-17: токен — одной загрузкой с буфером до появления секретов); общих папок, кроме канала на чтение |

Агент в рабочей ВМ может писать что угодно под учётной записью владельца, а через подменённый `sudo` — и под
root рабочей ВМ. Поэтому всё, что приходит из рабочей ВМ, — данные противника, и против агента код в ops-ВМ
держит ровно один рубеж: **владелец читает полный diff в `review.sh` до checkout** (ВР-2). fsck, белый список
символов, режимы, второй взгляд в `provision.sh`, метка машины — помощь в чтении и защита от ошибок, а не вторая
граница. Секреты (токен, vault, фразы, ключи, ключ age копий), inventory и копии живут только в доме `sa-ops`
ops-ВМ (раскладка §1 не меняется). Внутри ops-ВМ администратор и `sa-ops` — оба владелец; `sa-ops` отделён,
чтобы браузер и прочие программы администратора не жили рядом с секретами, а `provision.sh` не шёл от root.

Recovery-режим, `pam_access`, проверка групп `input`/`tty` и запрет графики больше не нужны: в ops-ВМ некому
перехватывать ввод (ВР-1). Правило сессии `sa-ops`: только терминал — без браузера, почты, мессенджеров;
веб-консоль провайдера (сверка отпечатка ключа хоста, §2) — в браузере хоста или в сессии администратора.

### 16.2. Канал: общая папка VMware (ВР-3, ВР-4)

Одна пустая папка на хосте, только под bundle (её место — на усмотрение владельца, в репозиторий не пишется).
В настройках обеих ВМ — общая папка с именем `sa-inbox`, «Always enabled»; у ops-ВМ — флаг **Read-only**
(вторая линия; соблюдает ли его хост — **гипотеза**, не проверяется). Главное — гостевое монтирование `ro`: запрет
записи на точке монтирования с `ro` проверяет VFS гостя независимо от хоста (**факт**, research §4). В обеих ВМ —
точка `/srv/sa-inbox` (`SA_INBOX` по умолчанию), запись `fstab` с `fuse.vmhgfs-fuse`: в рабочей ВМ
`rw,nosuid,nodev,noexec,uid=<владелец>,gid=<владелец>,umask=022`, в ops-ВМ
`ro,nosuid,nodev,noexec,allow_other,umask=022` (без `umask` права файлов с хоста могут не дать `sa-ops` читать —
**гипотеза**, runbook проверяет чтение от `sa-ops`). Владелец кладёт bundle своей командой во временное имя и `mv`
на `code.bundle`; `review.sh` читает только это имя.

| Что может агент через канал | Чем закрыто | Остаток |
|---|---|---|
| подменить bundle в любой момент, в том числе между проверкой и копией | доверие — прочитанному diff копии, а не файлу; копия в дом один раз, дальше только она; verify и `fsck --strict` во временном репозитории | владелец не прочёл diff |
| огромный или растущий файл, разреженный файл | `dd … iflag=count_bytes count=<предел+1>`, отказ сверх 200 МиБ; копия — в дом `sa-ops`, не дальше | — |
| симлинк (в том числе подложенный после `test -L`) на файл ops-ВМ (`~/secrets.age`, `/dev/zero`) | `iflag=nofollow` — ELOOP (**факт**, прототип); `realpath` внутри каталога входящих; прочитанное не уходит назад (`ro`) | — |
| странные имена, много файлов | читается одно постоянное имя; `review.sh` каталог не перебирает | — |
| FIFO, устройство, файл с `suid` | `test -f`; монтирование `nodev,nosuid,noexec`; ФС хоста FIFO и устройств не хранит (**гипотеза**) | — |
| неполный bundle (читают во время записи) | запись во временное имя и `mv` (атомарность на HGFS — **гипотеза**); неполный — отказ verify, повтор | — |
| ошибка в клиенте HGFS ops-ВМ (`vmhgfs-fuse` от root) на вредных метаданных | данные проходят через ФС хоста, имена и атрибуты — в её рамках | принят (**гипотеза** о малой поверхности) |
| атака рабочей ВМ на хост через HGFS-сервер, заполнение диска хоста | только одна папка, обновления гипервизора | принят: добавочная поверхность; диск рабочей ВМ растёт так же |
| утечка из ops-ВМ в рабочую | у ops-ВМ `ro` (и флаг хоста); буфер и перетаскивание выключены (ВР-17: токен — одной загрузкой с буфером до появления секретов); других общих папок нет | — |

**Сеть.** Обе ВМ — в NAT гипервизора и, вероятно, в одной сети (**гипотеза**). Рабочая ВМ может стучаться в
ops-ВМ независимо от канала, поэтому в ops-ВМ: `ufw default deny incoming`, SSH-сервера нет, слушающие службы —
только на loopback (в Ubuntu 24.04 на всех интерфейсах слушает avahi, UDP 5353, — **факт** на рабочей ВМ; `ufw`
его закрывает). ops-ВМ к рабочей ВМ не ходит.

### 16.3. Что ставится в ops-ВМ из сети (ВР-8)

| Что | Проверка подлинности |
|---|---|
| Ubuntu (клон снимка) | ISO не скачивался; доверие — снимку «до агентов» (со слов владельца) |
| `podman`, `uidmap`, `slirp4netns`, `git`, `curl`, при нужде `open-vm-tools` | apt — подпись `InRelease` архива Ubuntu; только `ubuntu.sources`, без PPA и сторонних репозиториев |
| образ tofu | дайджест (`ops/images.env`) |
| контроллер | собирается из просмотренного клона: база по дайджесту, pip `--require-hashes`, apt Debian внутри — подписан (без версий, И3-6) |
| провайдер tofu | `.terraform.lock.hcl`, `-lockfile=readonly`, первая сверка (README §4) |
| `SHA256SUMS` провайдера для сверки | TLS GitHub; подписи нет (П2a-8) — как раньше |

Новых сверок не нужно. Что нужно `provision.sh` на хосте (по коду): в списке `command -v` — `podman`, `git`,
`ssh`, `ssh-agent`, `ssh-add`, `ssh-keygen`, `ssh-keyscan` (openssh-client — в базе Ubuntu, **гипотеза** для
клона), `less`, `timeout`, `install`, `flock` и новый `ps` (procps); плюс `realpath`, `stat`, `awk`, `sed`,
`loginctl`, `systemctl --user` — в базе. Rootless Podman — `uidmap` (newuidmap), сеть `slirp4netns` (по
умолчанию в Podman 4.9; `passt` приходит рекомендацией), `dbus-user-session` (рекомендация) для `systemctl --user`.
`curl` — только для сверки lock-файла (README §4).

### 16.4. `guard.sh` и `review.sh` (ВР-5, ВР-6, ВР-9)

Порядок отказов в `provision.sh`: `guard_machine` → `guard_user` → `guard_session` → прочие (без изменений).

- **`guard_machine` (новое, код 3, текст «не машина раскатки»).**
  - `/etc/machine-id` — ровно 32 строчных hex и `\n`, ничего больше; иначе отказ.
  - `/etc/sa-ops-machine` — файл (`stat -L`: проверяется то, что читается), владелец root, без записи группе и
    прочим; содержимое побайтово равно `sa-ops-machine <machine-id>\n` — ровно одна строка с завершающим `\n`.
    Нет файла, не тот владелец или режим, другое содержимое — отказ. Симлинк отдельно не проверяется (ВР-9): в
    `/etc` пишет только root, а цель ссылки проходит те же проверки.
  - `/etc` не проверяется: на машине он root и 0755, а машина с чужим `/etc` уже не наша — проверка ничего не
    добавила бы.
  - Путь постоянный, без переменной окружения (research §4). Метку ставит администратор ops-ВМ (runbook §1); клон
    ops-ВМ после смены machine-id метку теряет — повторная установка осознанная.
  - Отказ, если в `ps -e -o comm=` есть `claude` или `node` (признак рабочей ВМ; в ops-ВМ их нет по правилу);
    `ps` не выполнился — тоже отказ.
  - Назначение — ошибка владельца (секреты в рабочей ВМ), а не агент: в рабочей ВМ агент перепишет и саму проверку.
- **`guard_session` (изменено).** Убрать отказ при `DISPLAY`/`WAYLAND_DISPLAY` и требование `Type=tty`. Оставить:
  `XDG_SESSION_ID` задан (пуст — сессия из `loginctl show-user <uid> -p Display`, пусто и там — отказ; ВР-13); `State` ∈
  {`active`, `online`}; `Type` ∈ {`tty`, `x11`, `wayland`}; `User` = свой uid; `Remote=no` (SSH-сервера нет;
  будущий удалённый вход — отказ); `XDG_RUNTIME_DIR` = `/run/user/<uid>`, свой, 0700. Смысл — правильное окружение
  для файла токена, сокета агента и rootless Podman. Что `sudo -iu sa-ops`/`su - sa-ops` из сессии администратора
  дают отказ (нет `XDG_SESSION_ID` или `Type=unspecified`, чужой `User`) — **гипотеза**, проверка — И5f.
- **`guard_user`, `guard_env`, `guard_repo`, `guard_content`, `guard_paths`, `guard_files`** — без изменений.
- **`review.sh`.** Та же проверка метки и `ps` (своя копия — скрипт самодостаточен, ставится в libexec; совпадение
  копий проверяет `ctl_shared`, как у белого списка) и `id -un` = `SA_OPS_USER`; копия bundle — `dd if=… of=…
  iflag=nofollow,count_bytes count=$((max_bytes + 1)) bs=1M status=none` под `umask 077` вместо `head -c`.
- Шапки, комментарии и сообщения `provision.sh`, `guard.sh`, `review.sh` — без «текстовой консоли» (например,
  `provision.sh`: «остальное не имеет смысла вне консоли раскатки»).

### 16.5. Страж Claude Code (ВР-7)

В рабочей ВМ `sa-ops` нет, остальные правила §15 безвредны и остаются защитой от случайности. `INCOMING =
['/srv/sa-inbox', '/mnt/hgfs']`: агенты не читают и не пишут каталог входящих (точка монтирования и стандартная
точка VMware), bundle кладёт владелец. По желанию владельца — правило на `vmhgfs-fuse` (монтирование папки в другой
каталог в обход `INCOMING`). Шапка стража — без «текстовой консоли». Правка — оркестратор, с «да» владельца.

### 16.6. Модель угроз (замена строк §14)

| Угроза | Мера | Остаток |
|---|---|---|
| Агент рабочей ВМ читает секреты раскатки | секретов в рабочей ВМ нет; ops-ВМ без агентов, буфера, перетаскивания, SSH-сервера; папка у ops-ВМ `ro`; `ufw` | побег из ВМ в гипервизор — всё; обновления гипервизора |
| Агент подменяет код раскатки | `review.sh` до checkout — единственный рубеж (ВР-2); остальное — §14 без изменений | владелец не прочёл diff |
| Канал bundle | §16.2 | см. таблицу §16.2 |
| Запуск `provision.sh` в рабочей ВМ по ошибке (секреты к агентам) | метка машины, `sa-ops` нет, отказ при `claude`/`node`, `CLAUDECODE`; страж | ошибка владельца в обход всех отказов |
| Программы в сессии `sa-ops` (браузер) | runbook: только терминал; веб-консоль провайдера — на хосте | ошибка владельца |
| Ввод фразы и пароля через удалённый доступ к хосту | см. приватные заметки | компрометация хоста или средства удалённого доступа — всё |
| Клон не чистый | снимок до агентов (со слов владельца), новый machine-id | — |
| Потеря ops-ВМ (диск хоста) | — | потеря state, ключей, копий и файла секретов: открытый вопрос владельцу |
| Код раскатки уносит секреты по сети (ВР-14) | исходящая сеть ops-ВМ закрыта, кроме API провайдера и сервера (§16.10) | выгрузка через сервер, аккаунт или в окне подготовки — §16.10 |

### 16.7. Набросок `deploy/README.md` §1–3 (для исполнителя)

Исходная точка: ops-ВМ есть (полный клон, обновления), `adduser sa-ops` сделан. Шаги 1–2 владелец уже выполнил
03.11 (machine-id отличается от рабочей ВМ, `ufw` включён) — в runbook они остаются для повторной сборки.

**§1. Подготовка ops-ВМ (один раз, администратор).** В VMware: у ops-ВМ буфер и перетаскивание выключены (ВР-17: токен —
одной загрузкой с буфером до появления секретов), общая папка `sa-inbox` — «Always enabled», Read-only; у рабочей ВМ —
та же папка хоста без Read-only, других папок нет.

```
# 1. Свой machine-id, имя и пароль — до метки (клон несёт значения снимка)
sudo rm /etc/machine-id /var/lib/dbus/machine-id && sudo systemd-machine-id-setup && sudo ln -sf /etc/machine-id /var/lib/dbus/machine-id
sudo hostnamectl set-hostname <имя-ops-ВМ>      # и то же имя в строке 127.0.1.1 файла /etc/hosts (sudoedit /etc/hosts)
passwd                                          # пароль администратора — свой, не как в рабочей ВМ
cut -c1-6 /etc/machine-id                       # сверить с рабочей ВМ: первые 6 знаков различаются
# 2. Брандмауэр
sudo ufw default deny incoming && sudo ufw default allow outgoing && sudo ufw enable
# 3. Учётная запись раскатки
id sa-ops                                       # группы: только sa-ops (не sudo, adm, lxd)
sudo chmod 0700 ~sa-ops                         # HOME_MODE в Ubuntu 24.04 — 0750
grep '^sa-ops:' /etc/subuid /etc/subgid         # диапазон есть и не пересекается; нет — usermod --add-subuids/--add-subgids
                                                # enable-linger (таймер чистки копий) — после перехода в раскатку (README §5)
# 4. Пакеты — только из архива Ubuntu
ls /etc/apt/sources.list.d/                     # только ubuntu.sources: без PPA
sudo apt update && sudo apt install podman uidmap slirp4netns git curl
# 5. Метка машины раскатки
sudo sh -c 'umask 022; printf "sa-ops-machine %s\n" "$(cat /etc/machine-id)" >/etc/sa-ops-machine'
# 6. Канал
command -v vmhgfs-fuse || sudo apt install open-vm-tools
vmware-hgfsclient                               # ровно одна папка: sa-inbox
sudo install -d -m 0755 /srv/sa-inbox
echo '.host:/sa-inbox /srv/sa-inbox fuse.vmhgfs-fuse ro,nosuid,nodev,noexec,allow_other,umask=022,nofail 0 0' | sudo tee -a /etc/fstab
sudo systemctl daemon-reload && sudo mount /srv/sa-inbox
findmnt -no OPTIONS /srv/sa-inbox               # в списке есть ro (и nosuid, nodev, noexec); нет ro — стоп
touch /srv/sa-inbox/x && echo ЗАПИСЬ-ПРОШЛА     # ждём ошибку «только для чтения»; результат — в research §4
sudo -u sa-ops test -r /srv/sa-inbox/code.bundle && echo ЧИТАЕТСЯ   # после первого bundle (§2)
```

Вход `sa-ops` — обычным входом в GDM; в сессии только терминал. Проверка под `sa-ops`:

```
cat /etc/sa-ops-machine /etc/machine-id                             # machine-id совпадает
loginctl show-session "$XDG_SESSION_ID" -p Type -p User -p Remote   # Type=wayland|x11|tty, User=<id -u>, Remote=no
echo "$XDG_RUNTIME_DIR"; stat -c '%a %U' "$XDG_RUNTIME_DIR"         # /run/user/<uid>, 700, sa-ops
podman info --format '{{.Host.Security.Rootless}}'                  # true
```

**§2. Канал, первый просмотр, клон.** Рабочая ВМ, один раз (владелец): `sudo install -d -m 0755 /srv/sa-inbox`;
в `fstab` — `.host:/sa-inbox /srv/sa-inbox fuse.vmhgfs-fuse rw,nosuid,nodev,noexec,uid=<id -u>,gid=<id -g>,umask=022,nofail 0 0`;
`sudo mount /srv/sa-inbox`. Каждый раз (владелец, рабочая ВМ):
`git bundle create /srv/sa-inbox/.code.bundle.new <ветка> && mv -f /srv/sa-inbox/.code.bundle.new /srv/sa-inbox/code.bundle`.
В ops-ВМ под `sa-ops` первый раз — команды нынешнего §2 с двумя отличиями: первой строкой `cat /etc/sa-ops-machine
/etc/machine-id` (нет или не совпало — стоп: это не ops-ВМ) и копия
`test -f "$B" && test ! -L "$B" && (umask 077; dd if="$B" of=~/in.bundle iflag=nofollow,count_bytes count=209715201 bs=1M status=none)`.
Дальше — `~/.local/libexec/sa/review.sh <ветка>`, как сейчас.

**§3. Inventory, ключи, файл секретов** — команды без изменений; «на консоли» → «в сессии `sa-ops` ops-ВМ»;
«пришло из сессии владельца» → «пришло из рабочей ВМ — просматривать как код». **§4** — без изменений (`curl`
поставлен в §1). **§5** — отпечаток ключа хоста сверять в веб-консоли провайдера в браузере хоста; в списке
кодов 3 вместо «сессия, `DISPLAY`» — «не машина раскатки (метка), не та сессия».

### 16.8. Проверки

`provision_cli` (шаг `static`, он же в CI через `scripts/check.sh`). Подстановка файлов: в rootless Podman файл
хоста, смонтированный `-v …:ro`, виден в контейнере как uid 0 (владелец на хосте — сам пользователь), а `-v`
разрешает симлинк источника — поэтому симлинк метки этим способом не воспроизвести (ВР-9).

- **Основной прогон `inner.sh`** обязан монтировать верную метку и `machine-id` (`/etc/sa-ops-machine`,
  `/etc/machine-id`, `:ro`) — без них все прочие случаи упали бы на метке. Случаи `отказ: DISPLAY`,
  `отказ: WAYLAND_DISPLAY`, `отказ: сессия не tty` снимаются — требование отменено решением владельца (ВР-6), это
  не ослабление; вместо них `сессия: wayland разрешена` (`FAKE_TYPE=wayland`, `WAYLAND_DISPLAY`), `сессия: x11
  разрешена` (`FAKE_TYPE=x11`, `DISPLAY`) — проверка сессии пройдена, и `отказ: сессия unspecified`. Там же
  `отказ: процесс node` и `отказ: процесс claude` (копия `sleep` под этим именем, запуск в фоне, остановка по своему
  PID), `review: отказ: не та учётная запись`; `inner.sh::review()` передаёт `SA_OPS_USER`.
- **Отдельные прогоны контейнера** (по одному на случай, сверяется код 3 **и** текст «не машина раскатки»):
  `метка: верная` (контрольный — проходит `guard_machine`, отказ дальше или успех, но не с этим текстом);
  `отказ: нет метки машины` (метка не смонтирована); `отказ: метка чужой машины` (другой id);
  `отказ: machine-id не 32 hex`; `отказ: метка не одна строка` (лишняя строка или без `\n`);
  `отказ: метка не root` (источник — `podman unshare chown 1000:1000`); `отказ: метка с записью группе`
  (источник 0664); `review: отказ: нет метки машины`.
- **`ctl_shared`** (статически): копии проверки метки в `guard.sh` и `review.sh` совпадают; в `review.sh` и в README
  есть `dd … iflag=nofollow,count_bytes` (случай симлинка bundle и так ловит `[ -L ]`, гонку тестом не поймать).
- CI-стенд (`stand-full`) `guard.sh` и `review.sh` не запускает — не затронут.

### 16.9. Задачи

| № | Зона | Что | Готово, когда |
|---|---|---|---|
| И5a | исполнитель инфраструктуры | `deploy/ops/guard.sh`: `guard_machine`, `guard_session` по §16.4; порядок и `ps` в `provision.sh`; шапки, комментарии и сообщения | `provision_cli` зелёный с новыми случаями §16.8 |
| И5b | исполнитель инфраструктуры | `deploy/ops/review.sh`: метка, `ps`, учётная запись, `dd nofollow`; шапка | случаи `review:` §16.8 |
| И5c | исполнитель инфраструктуры | `deploy/tests/provision/`: `cli/inner.sh` (метка в основном прогоне, `review()` с `SA_OPS_USER`), `provision_cli.sh` (отдельные прогоны), `ctl_static.sh` (`ctl_shared`) | `run.sh static` зелёный локально и в CI |
| И5d | исполнитель инфраструктуры | `deploy/README.md` §1–3, §5 и таблица раскатки по §16.7; убрать recovery, `pam_access`, группы | текст сверен с кодом; три копии белого списка совпадают |
| И5e | оркестратор | страж раскатки в приватной оснастке: `INCOMING`, шапка, по желанию правило `vmhgfs-fuse`; его наборы случаев | **только с «да» владельца** |
| И5f | владелец | runbook §1 в ops-ВМ; гипотезы research §4; `sudo -iu sa-ops` и `su - sa-ops` из сессии администратора → `provision.sh forget-host` даёт код 3 | runbook пройден, итог — в research §4 |

Порядок: И5a ‖ И5b → И5c → И5d; И5e — независимо; И5f — после И5a. Аудит безопасности — после И5a–И5c.
**Второе мнение (независимый критик):** копии, ключ age, inventory со state и файл секретов теперь живут только в ops-ВМ —
это место хранения бэкапа; потеря ВМ или диска хоста — потеря всего (открытый вопрос владельцу).

### 16.10. Поправка 04.11.2026: исходящая сеть ops-ВМ (ВР-14)

Решение владельца: первый просмотр (≈ 13,6 тыс. строк) построчно не читается; вместо этого ops-ВМ закрыта
исходящая сеть, кроме нужного раскатке, — чтобы испорченный код не унёс секреты (токен, ключи сервера и копий,
фразы, ключ age, inventory) никуда, кроме API провайдера и своего сервера. Правила ставит администратор ops-ВМ
(`sa-ops` без `sudo` их не меняет); в репозитории — инструкция (README §1) с заполнителями вместо адресов.
ВР-2 уточняется: мера сужает, куда уйдёт украденное, но не заменяет чтения (остаток — ниже). Отвергнутое и
прототипы — research §4.

**Два режима.** Переключатель — политика `ufw` по умолчанию для исходящих; разрешающие правила постоянные (в
подготовке они ничего не меняют).

| | подготовка (окно) | раскатка (обычное состояние) |
|---|---|---|
| `ufw default … outgoing` | `allow` | `reject` |
| что делается | окно А — `apt` (пакеты, обновления), адреса и правила: администратор, **без процессов `sa-ops`**. Окно Б — сверка lock (README §4, `curl` GitHub), затем `provision.sh prepare`: образ tofu по дайджесту, архив провайдера с GitHub (оба хэша) и `tofu init`/`validate` из зеркала без сети (§16.10.1, ВР-15), сборка контроллера (база по дайджесту, apt Debian, PyPI с хэшами): под `sa-ops` | `review.sh`; `provision.sh up/apply/configure/backup/forget-host`; ключи, файл секретов, vault (контейнеры `--network=none`) |
| фразы и пароли | не вводятся фразы файла секретов, ключей ssh и vault (в сессии `sa-ops`; просит — стоп); пароль `sudo` администратора и вход в GDM допустимы | вводятся |
| DNS | шлюз NAT | закрыт; имя API — строкой в `/etc/hosts` |
| IPv6 | выключается при первом переходе в раскатку и остаётся выключенным (sysctl-файл `/etc/sysctl.d/90-sa-noipv6.conf` и `nmcli connection modify … ipv6.method disabled`) | выключен |

**Окно А — без процессов `sa-ops`** (аудит А3): в окне исполняется весь код клона, а не только прочитанный diff, поэтому перед
`ufw default allow outgoing` — `sudo loginctl disable-linger sa-ops && sudo loginctl terminate-user sa-ops`, проверка
`ps -eo uid=,user= | awk '$2=="sa-ops"||$1>=100000'` пуста (uid от 100000 — контейнеры `sa-ops`), под `sa-ops` в окне не входить; после
возврата в `reject` и перезагрузки — `enable-linger`. **cron и at** (аудит Н1) обошли бы это: `crontab` разрешён всем, задание `sa-ops`
выполнится без сессии и linger. Закрыто один раз `echo root > /etc/cron.allow` (проверка — `sudo ls -A /var/spool/cron/crontabs/`: нет `sa-ops`; `crontab -l -u sa-ops` после этого отвечает «The user sa-ops cannot use this
program (crontab)», ожидание «no crontab» неверно — находка И5h-1); в окне
перед `allow` — та же `ls -A` (нет `sa-ops`) и `dpkg -s at` (не установлен; стоит — `/etc/at.allow` только с root). Окно Б требует `sa-ops` и остаётся в остатке (ниже). **Когда открывать:** окно Б — только
по детерминированной проверке (`git diff --stat` от развёрнутого коммита по путям из «Порядка первого раза») или первый раз; окно А — для `apt`
раз в месяц или если адрес API действительно сменился (администратор проверяет сам: копирует `/etc/hosts`, убирает старые строки, затем
`getent`, сравнивает с правилами; адреса те же — возвращает файл). Сообщение `provision.sh` («окно подготовки, prepare») поводом не служит: его напечатает и
испорченный код.

**IPv6 выключен** (аудит А2): `before6.rules` у `ufw` пропускает исходящий ICMPv6 echo-request до политики по умолчанию, а `ping` с
`cap_net_raw` у `sa-ops` есть. Делаются оба способа сразу (аудит Н3): sysctl-файл и `nmcli connection modify "<соединение>" ipv6.method
disabled` — NetworkManager иначе возвращает link-local поверх sysctl. Проверка: `ip -6 addr show scope global` и `scope link` пусты, иначе
стоп, к ключам и секретам (README §3) не переходить.

**Что ходит в сеть (по коду, 04.11).**
- `tofu apply`: провайдер — на хост из `api_url` (`auth_url = var.api_url`), TCP 443; других хостов в конфигурации
  нет. Что провайдер не уходит дальше (переадресации) — **гипотеза**, проверяет первый `apply`.
- `tofu init` при установленном провайдере всё равно опрашивает реестр — без сети отказ (**факт**, прототип).
  С `-plugin-dir=/state/.terraform-clo/mirror` (зеркало, ВР-15, §16.10.1) init без сети проходит; пакет, не совпавший ни с одним
  хэшем lock, init отвергает, но при верном `zh:` неверный `h1` init пропускает — `h1` сверяют `validate` (в `prepare`) и сам `apply`:
  подменённый `h1` — отказ (**факт**, образ tofu 1.13.5). Ставит и обновляет зеркало и провайдер только `prepare`.
- Контроллер: `podman build` по умолчанию `--pull=missing`, слои из кэша — без реестра и PyPI (**факт**: сборка с
  прокси «в никуда» дала тот же ID). Кэш стёрт или изменились `Containerfile`/`requirements.txt` — в раскатке сборка
  падает (так и надо): окно подготовки. Образ tofu по дайджесту — локальный, без pull.
- TCP 22 на адрес сервера: `ssh`, `ssh-keyscan`, `tcp22`, Ansible (`delegate_to: localhost` — тот же ssh и
  локальный скрипт). Apt, образ сервиса, ACME — сеть сервера, не ops-ВМ.
- Без сети: `age`, `show`, `output`, проверка копии, vault, `review.sh`, git.
- Rootless Podman 4.9 (slirp4netns): соединения контейнеров открывает процесс хоста под `sa-ops` — `ufw`
  действует и на них (проверка — из контейнера, ниже).
- Имена в контейнере: Podman переносит строки `/etc/hosts` хоста в контейнер (**факт**: строка хоста видна в
  контейнере tofu), в образе tofu `hosts: files dns` (**факт**) — строка API работает и для провайдера (**гипотеза**:
  провайдер на Go резолвит сам и читает `nsswitch.conf`; проверяет первый `apply`).

**Правила раскатки.** Наружу разрешено только:

| Куда | Порт | Зачем | Откуда значение |
|---|---|---|---|
| адреса хоста API | tcp 443 | tofu | хост — из документации провайдера или панели (`clo.tfvars` в окне не создаётся: файлы inventory — только в раскатке); `getent ahostsv4 <хост API> \| awk '{print $1}' \| sort -u` в окне А (после удаления старых строк `/etc/hosts`); `whois <адрес API>` — сеть самого провайдера, не CDN и не общий фронт, иначе стоп и запись в research §4; адреса же — в `/etc/hosts` |
| адрес сервера | tcp 22 | ssh, Ansible, копия | **из панели провайдера** (публичный адрес сервера); вывод `provision.sh apply` — только подсказка (это вывод кода, против которого закрыта сеть), не совпал с панелью — стоп; постоянный (`--replace-server` его сохраняет) |
| сервер DHCP сети NAT | udp 67 | продление аренды адреса ВМ | `nmcli -g DHCP4.OPTION device show \| tr '\|' '\n'` → `dhcp_server_identifier` |

Прочее — `reject` (быстрый отказ, не ожидание): DNS, NTP, ICMP (IPv4; IPv6 выключен), multicast (mDNS дошёл бы до рабочей ВМ в той
же сети NAT), реестры, PyPI, GitHub, рабочая ВМ. Loopback и ответы по уже открытым соединениям `ufw` пропускает
сам (`before.rules`). Широковещательный udp 67 не открывается — его слышит рабочая ВМ. Без правила DHCP продление
аренды уходит в `reject`, и ВМ теряет адрес (аренда NAT VMware короткая, 30 мин по умолчанию — **гипотеза**).

**Почему точные адреса, а не диапазоны провайдера.** АС юрлица провайдера (см. приватные заметки) по открытым
базам BGP — около 170 префиксов IPv4 общего хостинга, больше 110 тыс. адресов (что адреса API и серверов — в
ней, **гипотеза**): разрешить их — разрешить ВМ любого его клиента, то есть дешёвую чужую аренду как
приёмник. Адрес сервера меняется только с ресурсом адреса (`prevent_destroy`); сменился адрес API — `apply`
падает, администратор в окне А: сохраняет копию `/etc/hosts`, убирает старые строки (иначе `getent` вернёт старый адрес из `files`), потом
`getent`, потом правила; адреса те же — файл возвращается, окно было не нужно (обход `files` через `resolvectl` не выбран: `resolved`
читает `/etc/hosts` сам). Хост при этом — не из файла `sa-ops`: источник — строки `/etc/hosts` (пишет root) или документация/панель
провайдера; его сверяют с `api_url` (`sudo grep api_url … | cat -v`), расхождение — стоп (аудит Н2).

**Переключение.**
- В раскатку: (первый раз — sysctl-файл IPv6) `sudo ufw default reject outgoing && sudo reboot`, затем `enable-linger sa-ops`. Перезагрузка — потому что `ufw` пропускает уже
  установленные соединения (`ESTABLISHED`): процесс, открывший соединение или поток UDP в подготовке, сохранил
  бы его после переключения.
- В окно: `sudo ufw default allow outgoing` (окно А — после `disable-linger` и проверки `ps`); в окне — только перечисленное в таблице;
  закончить — в раскатку.
- Скрипт не нужен: переключатель — одна команда, правила постоянные.

**Проверка (после перезагрузки, в раскатке).**
- Администратор: `sudo ufw status verbose` — `deny (incoming), reject (outgoing)`, `ALLOW OUT` — только три вида
  строк таблицы; адрес в строке tcp 22 равен адресу в панели провайдера (аудит А1); `ip -6 addr show scope global` и `scope link` пусты.
- Под `sa-ops`: `getent ahostsv4 example.org` — пусто (DNS закрыт); `curl -m 5 -sS -o /dev/null https://<внешний IPv4>` (любой публичный адрес, взятый в окне подготовки)
  — ошибка (прямой адрес закрыт); `curl -m 10 -sS -o /dev/null -w '%{http_code}\n' https://<хост API>/` — любой
  код HTTP (строка и правило работают); из контейнера: `podman run --rm --entrypoint wget <образ tofu> -T 5 -q -O
  /dev/null https://<внешний IPv4>` — ошибка (в образе tofu есть `wget`, **факт**).

**Обновления и время.** В раскатке unattended-upgrades не дотягивается до архива и ничего не делает — допустимо:
обновления — в каждом окне и не реже раза в месяц (`sudo apt update && sudo apt full-upgrade`). NTP молчит: дрейф
часов ВМ за недели — секунды, TLS и SSH не задевает (**гипотеза**); по желанию — синхронизация времени гостя с
хостом в настройках VMware (без сети).

**Порядок первого раза.** (1) Окно А (нынешнее состояние ops-ВМ; администратор, без процессов `sa-ops`): пакеты,
`apt full-upgrade`, адреса API (хост — из документации провайдера или панели), `whois`, правила API и DHCP, строки
`/etc/hosts`. (2) Канал, первый просмотр, клон (сети не нужно). (3) Окно Б (`sa-ops`): сверка lock (README §4) **до**
`provision.sh prepare` (prepare ставит провайдер по lock, несверенный ставить нельзя). (4) Раскатка: (раз) `cron.allow`, sysctl-файл и `nmcli … ipv6.method disabled`,
перезагрузка, `enable-linger`, проверка (IPv6 пуст, иначе стоп). (5) Только теперь `clo.tfvars`, ключи, файл секретов, vault (README §3): файлы
inventory — только в раскатке, в окне есть лишь каталог, который создаёт `prepare`. (6) `provision.sh apply` → адрес сервера
**из панели провайдера** (печать `apply` — подсказка; не совпали — стоп) → правило tcp 22 → `provision.sh configure` (`up`
тоже годится: `wait_ssh` ждёт до 600 с, правило добавляется из сессии администратора). **Дальше:** bundle → `review.sh` → если
`git diff --stat` от развёрнутого коммита по `ops/images.env`, `ansible/Containerfile*`, `ansible/requirements*`,
`tofu/clo/versions.tf` или lock непуст — окно Б (`prepare`) → раскатка, перезагрузка → `provision.sh`.

**Остаток (мера не закрывает).**

| Путь | Почему открыт | Чем уменьшен |
|---|---|---|
| Через свой сервер: код кладёт секрет на сервер и открывает наружу (файл в корне Caddy, свой ключ в `authorized_keys`, порт в nftables) | tcp 22 на сервер нужен раскатке | после первого `configure` — проверка результата (README §5): `sshd -T` (строки `authorizedkeys*`), ключи во всех `authorized_keys*` (`find / -xdev`), `ss -tulnp`, `nft list ruleset` (tcp 22, 80, 443, ICMP, DHCPv6 udp 546), файлы в корне Caddy — с ожидаемым; дальше — небольшие diff в `review.sh` |
| Через аккаунт провайдера: токен создаёт сервер с чужим ключом, адрес; трата денег | API нужен tofu | после `apply` в панели — один сервер, один ключ, один адрес; деньги — предоплата небольшим балансом, без автоплатежа или с лимитом (см. приватные заметки) |
| Отложенная выгрузка: код в раскатке прячет секрет на диск или ставит свой таймер, а в следующем окне отправляет | окно открывает сеть всей ВМ; в окне исполняется весь код клона, не только diff; разделение по uid не держит DNS (research §4) | окна для `apt` — без процессов `sa-ops`, cron закрыт `cron.allow`, at — проверкой пакета (остаток — только ядро/root, строка ниже); для `prepare` (под `sa-ops`) — редкость окон: открываются только по детерминированной проверке путей, diff по ним мал и читается |
| Порча сервера и данных | раскатке нужна запись | копии в ops-ВМ (§11) |
| `sa-ops` → root в ops-ВМ (уязвимость ядра) и снятие правил | вне меры | обновления в окнах, не реже раза в месяц |

Против целевой атаки (через сервер, аккаунт, отложенно) рубеж прежний — чтение diff; мера снимает общий случай
(`curl`/DNS на любой адрес, реестры, PyPI, GitHub). **Владелец читает целиком** инструкцию сети в README (она
пришла из рабочей ВМ) и дальше — каждый diff в `review.sh`.

**Изменения кода (И5g, исполнитель инфраструктуры).**
- `provision.sh prepare` — новая команда: те же `guard_*` до `guard_repo`, `guard_tofu_files`, `flock`; без
  `review_code` (просмотр — `review.sh`; секретов и сервера prepare не трогает), `secrets_load`, агента; не требует
  файла секретов, `SA_DOMAIN`, `SA_ADMIN_USER` (первый раз — до них; проверка `token` в `*.tfvars` остаётся).
  Делает (поправка ВР-15, §16.10.1): архив провайдера с GitHub в зеркало, `tf --network=none -- init -lockfile=readonly -input=false
  -plugin-dir=/state/.terraform-clo/mirror -backend-config=path=/state/clo.tfstate`, `validate` и `ctl_prepare`.
- `cmd_apply`: init — `tf --network=none -- init -lockfile=readonly -input=false
  -plugin-dir=/state/.terraform-clo/mirror -backend-config=path=/state/clo.tfstate`; отказ — «провайдер не
  установлен или не совпал с lock: окно подготовки, provision.sh prepare»; у `ctl_prepare` — та же подсказка.
- `cmd_apply` печатает адрес сервера и строку «сверить с панелью провайдера; в раскатке — правило tcp/22 (README §1)»
  (адрес для правила — из панели, а не из этого вывода, аудит А1).
- README: §1 шаг 2 — режимы, правила (заполнители), проверка; §3 — в раскатке; §4 — в окне Б, до `prepare`; §5 — порядок,
  окно, проверка результата первого `configure`; `prepare` — в списке команд.

**Проверки (`provision_cli`, шаг `static`).** `prepare: без фразы, агента и токена` (нет контейнера age и
`ssh-agent`; файла секретов нет — не отказ), `prepare: init с сетью по lock и сборка контроллера`, `apply: init
без сети из установленного провайдера` (`-plugin-dir`, `--network=none`), `apply: адрес сервера напечатан`,
`prepare: отказ при лишних аргументах`; `ctl_shared` — в README есть `ufw default reject outgoing`.

**Задачи.** И5g — исполнитель инфраструктуры: код, тесты, README (готово — `run.sh static` зелёный локально и в
CI); аудит безопасности — после. И5h — владелец: правила и проверка в ops-ВМ; первый `apply` в раскатке
подтверждает гипотезы (один хост API, строка `/etc/hosts`), итог — research §4. **Второе мнение (критик):**
порядок «секреты — только в раскатке» и остаток «отложенная выгрузка».

#### 16.10.1. Провайдер без реестра OpenTofu (ВР-15, 04.11.2026)

**Факт из ops-ВМ.** Из России `registry.opentofu.org` (Cloudflare) отвечает 403 (`loc=RU`): `tofu providers lock` (README §4) и
`tofu init` в `prepare` не работают. GitHub (архив выпуска провайдера, `SHA256SUMS`) и `ghcr.io` (образ tofu) доступны; хэши `zh:`
lock-файла с `SHA256SUMS` релиза совпали. VPN для ops-ВМ отвергнут: лишняя сторона и сложнее сеть.

**Решение.** Провайдер берётся архивом выпуска с GitHub и ставится как локальное зеркало (packed layout), реестр не нужен. Версия,
источник и платформа — **из одного места**, `.terraform.lock.hcl` (адрес `registry.opentofu.org/<ns>/<тип>`, `version`; платформа
`linux_amd64` — константа рядом, как у `providers lock -platform`); литералов, которые могли бы разойтись с lock, нет. Ссылка:
`https://github.com/<ns>/terraform-provider-<тип>/releases/download/v<версия>/terraform-provider-<тип>_<версия>_linux_amd64.zip`
и `…_<версия>_SHA256SUMS` рядом.

**`provision.sh prepare`** (окно Б; код — `deploy/ops/mirror.sh`, `mirror_fetch`):
1. Разбор lock: ровно один `provider` вида `registry.opentofu.org/<ns>/<тип>`, `version` вида `X.Y.Z`; иначе отказ до скачивания.
2. Скачивание `curl` хоста под `sa-ops` (проще проверить глазами, чем контейнер; секретов и токена не касается): `-q` (без
   `~/.curlrc`), `--proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL`, `--connect-timeout 20 --max-time 300`, предел размера
   (архив 128 МиБ, `SHA256SUMS` 1 МиБ) — заголовком `--max-filesize` и жёстко `ulimit -f` (запись сверх предела убивает curl);
   `nolock` — как у всех дочерних процессов. Файлы — в черновик `…/.terraform-clo/.mirror-stage`.
3. Сверка sha256 архива: со строкой `SHA256SUMS` с именем архива **и** с `zh:` в lock; оба обязательны, расхождение с любым —
   отказ, черновик убирается, прежнее зеркало не тронуто. Только после обеих сверок черновик заменяет зеркало
   (`~/inventory/tofu/.terraform-clo/mirror/registry.opentofu.org/<ns>/<тип>/<архив>`; каталог внутри `.terraform-clo/`, уже
   в `.gitignore` inventory).
4. `tofu init -lockfile=readonly -input=false -plugin-dir=/state/.terraform-clo/mirror -backend-config=…` в контейнере
   `--network=none`; затем `tofu validate` там же (`--entrypoint tofu`: обёртка `tofu-entry.sh` без файла токена `validate` не
   пускает — это проверяет `tofu_static`, ослаблять нельзя; сам `validate` токена не требует).
5. `ctl_prepare` — без изменений.

**Что проверено на настоящем образе tofu по дайджесту из `ops/images.env`** (OpenTofu 1.13.5, без сети, прототип в `.gotmp/`):
- init из каталога (`-plugin-dir`, а также `filesystem_mirror` в CLI-конфиге) проходит; провайдер ставится как «unauthenticated».
- init принимает пакет, совпавший с **любым** из хэшей lock: при верном `zh:` и подменённом `h1:` init проходит (только
  предупреждение «lock file not updated»); архив с изменённым содержимым (совпал бы ни с одним `zh:`/`h1:`) init отвергает
  («doesn't match any of the checksums previously recorded in the dependency lock file»). **Поправка к §16.10:** фраза «подменённый
  `h1` — отказ при init» верна только для пакета, не совпавшего ни с одним хэшем.
- `tofu validate` (как и `providers schema`, `apply`) сверяет **установленный** пакет (`TF_DATA_DIR/providers`) с lock: при
  подменённом `h1:` — «the cached package … does not match any of the checksums recorded in the dependency lock file». Поэтому
  prepare после init запускает `validate`: именно он проверяет `h1`; а apply в раскатке сверяет `h1` сам при каждом запуске
  провайдера (подмена зеркала между prepare и apply заметна).
- `tofu providers lock -fs-mirror=<каталог> -platform=linux_amd64` в контейнере `--network=none` собирает lock из архива в packed layout
  (`<каталог>/registry.opentofu.org/<ns>/<тип>/<архив>.zip`); `h1` совпал с lock (`h1:Yzes…`). На этом построена сверка README §4.

**`cmd_apply`.** `tf --network=none -- init -lockfile=readonly -input=false -plugin-dir=/state/.terraform-clo/mirror
-backend-config=path=/state/clo.tfstate`; отказ — прежнее «провайдер не установлен или не совпал с lock: окно подготовки,
provision.sh prepare». Прежний каталог `-plugin-dir=/state/.terraform-clo/providers` — кэш установки tofu (TF_DATA_DIR), не
источник; §16.10 («Изменения кода», факты об `init`) исправлен на месте. Хвост пути зеркала (`.terraform-clo/mirror`) — одна
переменная `provision.sh`, из неё и путь в контейнере, и путь на хосте. Точка входа tofu (`tf_entry`) в начале обнуляется:
окружение её не меняет.

**README.** §4 — сверка без реестра: `sha256sum` архива = строка `SHA256SUMS` = `zh:` в lock; все `zh:` lock = `SHA256SUMS`; `h1` —
`providers lock -fs-mirror` в `--network=none` против lock; один блок в чистой оболочке (`env -i … bash --noprofile --norc <<'EOF'`, внутри `set -euo pipefail` и `trap` уборки каталога; без
`~/.bashrc` и `~/.curlrc`), у каждой строки «ожидаем …», итог «код 0 = совпало». `ops/images.env` — файл из bundle: в README он не
исполняется (`.`), образ берётся `sed` по строгому образцу (аудит Н1); у каждого `curl` первым `-q` (Н2). Замена зеркала в
`mirror_fetch` — через `.old` с возвратом при неудаче; у `curl` в `mirror_get` ещё `ulimit -c 0`. §1 «Один раз»: проверка cron — `sudo ls -A /var/spool/cron/crontabs/` (пусто, нет `sa-ops`),
`crontab -l` убран (И5h-1).

**Проверки (`provision_cli`).** Поддельный `curl` (cli/shim/curl) отдаёт архив и `SHA256SUMS` из файлов теста; lock теста (версия
9.9.9, `zh:` фикстурного архива) коммитится в клоне и возвращается. `prepare`: URL выведены из lock, только https, таймаут и предел;
архив в зеркале, черновика нет; `init` из `-plugin-dir=/state/.terraform-clo/mirror` без сети после появления архива;
`validate` без сети и мимо обёртки; контейнеров ровно два, оба без сети; отказы (SHA256SUMS не совпал, нет строки архива, нет в `zh:`,
curl не скачал, два провайдера в lock, init, validate) — до сборки контроллера, зеркало прежнее, curl не вызывается там, где разбор
lock отказал; `apply`: init без сети из зеркала, curl не вызывается. Мутация проверки `zh:` в `mirror.sh` краснит тест. `ctl_shared`: в README
есть `sudo ls -A /var/spool/cron/crontabs/`, блок сверки (`-fs-mirror`, `test "$A" = "$S"`, `grep … zh:`), нет `crontab -l` и
`providers lock` без `-fs-mirror`.

**Остаток.** Подпись `SHA256SUMS` не проверяется (провайдер ключа не публикует): доверие — GitHub и владельцу, сверившему lock
(§4); архив и `SHA256SUMS` лежат в одном выпуске, поэтому `SHA256SUMS` защищает от повреждения, а не от подмены выпуска, — от неё
защищает сверка lock владельцем один раз (README §4) и `zh:` в репозитории.
