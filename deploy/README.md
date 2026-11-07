# deploy/ — развёртывание albums-svc

Артефакты сервиса (спека 001, design §1, §5.6, §7) и подъём сервера (спека 002: OpenTofu → Ansible, раздел «Раскатка»
ниже). Домена, адресов и ключей здесь нет: домен — `ALBUMS_DOMAIN` в окружении Caddy, образ — `albums_svc_image`
(строго с дайджестом) в приватном inventory учётной записи раскатки; пример значений — `deploy/inventory.example/`.

| Файл | Что |
|---|---|
| `Containerfile`, `Containerfile.containerignore` | сборка Go в `golang@sha256`, итог — `distroless/static:nonroot@sha256` и один бинарник; контекст — белый список (`cmd/`, `internal/`, `web/` с готовой листалкой, `go.mod`, `go.sum`) |
| `quadlet/albums-svc.container.j2` | Quadlet-юнит rootless-пользователя `albums`: `Network=none`, `ReadOnly` без tmpfs, `DropCapability=ALL`, `NoNewPrivileges`, обычная карта subuid, лимиты памяти и pids, `Secret` файлом `0400`, образ по дайджесту (иначе шаблон падает), тома `noexec,nosuid,nodev`, `HealthOnFailure=kill` |
| `caddy/Caddyfile` | TLS, `reverse_proxy` на сокет, тайм-ауты и пределы тела по маршрутам, заголовки, журнал с одним первым сегментом пути, единая страница 404/502 |
| `systemd/` | `tmpfiles.d` для `/run/albums-svc` с ACL для Caddy; drop-in Caddy (`EnvironmentFile`, одна способность `CAP_NET_BIND_SERVICE`, `NoNewPrivileges`, фильтр системных вызовов, `ExecStart` без `--environ`); `sa-backup.socket.j2` и `sa-backup@.service.j2` — забор копии (служба с одной способностью `CAP_DAC_READ_SEARCH` в пустом корне) |
| `backup/` | `sa-backup-stream.sh` (поток копии: tar данных и конфига, метка `SA-END`, zstd, age) и `sa-restore.sh` (разбор и распаковка при возврате от uid сервиса: только файлы и каталоги под `data/` и `conf/`, предел размера) — спека 002, design §11 |
| `caddy/caddy-stable-archive-keyring.asc` | открытый ключ apt-репозитория Caddy; роль `caddy` сверяет отпечаток основного ключа (ровно один ключ в файле) |
| `tests/` | `socket-probe.sh` (сокет, ACL, Caddy, журнал на заглушке), `caddy-validate.sh`, `quadlet-validate.sh` (+ `quadlet-render.py`), `stub/main.go.txt` (заглушка бинарника; не `.go`, иначе `go vet`/`go test` в `precheck` падают на пакете без файлов), `stand.sh` (стенд П1), `access/` (матрица доступа pytest + httpx), `conformance/` (Conformance контракта через Caddy) |

Сборка образа из корня репозитория:
`podman build --ignorefile deploy/Containerfile.containerignore -f deploy/Containerfile -t albums-svc .`

## Caddy

Только из официального apt-репозитория Caddy (подписанный ключ), версия закреплена: **2.11.7** (образ в
`tests/caddy-validate.sh` и `tests/socket-probe.sh` — тот же дайджест). Пакет Debian 13 (2.6.2) не годится: фильтры
журнала, `request_header -X-Sa-*`, `strict_sni_host`, `protocols` на нём не проверялись. Роль Ansible закрепляет
версию пакета и после установки сверяет `caddy version`; расхождение — ошибка. При смене версии прогнать оба скрипта
на соответствующем образе.

## Сокет и пользователи

Сервис создаёт сокет внутри контейнера (`Network=none`) в `/run/albums-svc`. Контейнер идёт по обычной карте
идентификаторов: процесс — uid 65532, на хосте это uid из диапазона subuid пользователя `albums`. Доступ Caddy —
ACL, а не группа: `tmpfiles.d` создаёт каталог с `u:caddy:x` и default `u:caddy:rw`, юнит перед стартом меняет
владельца каталога на uid сервиса (`podman unshare chown`), ACL при этом сохраняется. Caddy не входит в группу
`albums`, `config.json` ему не читается. Проверка на стенде (`socket-probe.sh`): Caddy-uid с ACL получает 200, без
ACL — 502, хост под обычным uid подключиться к сокету не может.

## Что делает роль Ansible `albums_svc` (спека 002, design §9)

- пользователь `albums`, linger, диапазон subuid/subgid; в группе `albums` нет людей и пользователя
  развёртывания (группа — граница для `/etc/albums-svc` и каталога данных);
- каталоги: `/var/lib/albums-svc/data` (0700; владелец — uid процесса сервиса на хосте, `начало subuid + 65531`:
  роль делает `chown` от root, то же, что `podman unshare chown 65532:65532`, без вложенного Podman; непустой каталог
  другого владельца — ошибка, не тихий `chown`), `/etc/albums-svc`
  (0750 root:albums, внутри `config.json` 0640 root:albums), `/run/albums-svc` (tmpfiles);
- **доступ процесса сервиса к конфигу — ACL на его uid в карте subuid.** Без `keep-id` процесс (65532) на хосте —
  `subuid_base + 65531`, где `subuid_base` — первое поле строки пользователя `albums` в `/etc/subuid` (роль читает
  его оттуда, например `getent`/`lookup('file', …)`). Роль ставит `u:<uid>:rx` и default `d:u:<uid>:r` на каталог и
  `u:<uid>:r` на файл (`ansible.posix.acl`) — **после каждой записи config.json** (замена файла ACL файла не
  переносит; default на каталоге нужен для файла, созданного копированием внутри него). Владелец остаётся
  root:albums; Caddy и остальные конфиг не читают. `socket-probe.sh` проверяет: с ACL сервис стартует, без — выходит
  с ошибкой до открытия сокета, Caddy-uid файл не читает;
- `podman secret create albums_master -` из vault. Драйвер file хранит значение в base64 в
  `~albums/.local/share/containers/storage/secrets`, то есть на диске оно есть: **бэкап (этап 4) обязан исключить
  `~albums/.local/share/containers`** (секрет не должен лежать рядом с данными, design §8);
- образ — `albums_svc_image` с дайджестом (его пишет `image-verify` и передаёт `configure` как `-e`, в inventory он не задаётся); копия `web/viewer/notfound.html` в `/usr/share/albums-svc/errors/`;
- Caddy — из apt-репозитория Caddy, версия по разделу выше; `/etc/caddy/albums.env` с `ALBUMS_DOMAIN`.

## Проверка локально

```
bash deploy/tests/caddy-validate.sh    # Caddyfile: validate с доменом и отказ без него
bash deploy/tests/quadlet-validate.sh  # рендер, quadlet -dryrun, systemd-analyze, отказ без дайджеста
bash deploy/tests/socket-probe.sh      # сокет и ACL, заглушка и Caddy в контейнерах, пределы, журнал, 502
```

Нужны rootless Podman, `curl`, `setfacl` (`acl`), python3 с jinja2; на сервер скрипты не ходят, образы тянутся
по дайджесту.

## Стенд П1 и матрица доступа

Весь сервис локально: контейнер сервиса как в Quadlet (`Network=none`, `ReadOnly`, `cap-drop=ALL`,
`no-new-privileges`, сокет, конфиг и секрет) + Caddy с `deploy/caddy/Caddyfile` (домен стенда `albums.test`, `tls
internal`). Отличия от боя перечислены в шапке `tests/stand.sh`.

```
bash deploy/tests/stand.sh up            # режим matrix (public_base = https://albums.test, как в бою)
bash deploy/tests/stand.sh test          # ~25 с: стадии main → Conformance → damage → down → logs; сервис в конце запущен
bash deploy/tests/stand.sh down          # убрать контейнеры, секрет, рабочий каталог (--images — и образы)
bash deploy/tests/stand.sh up browser    # для листалки в браузере: http://127.0.0.1:18480 (STAND_PORT меняет порт)
bash deploy/tests/stand.sh seed          # опубликовать альбомы (без пароля, с паролем, «для правки») и напечатать ссылки
bash deploy/tests/stand.sh edit          # опубликовать правку альбома «для правки» (страница 3 станет белой)
bash deploy/tests/stand.sh env           # адреса, каталоги, где токены (значения не печатаются)
```

`tests/access/` — матрица на pytest + httpx; запускается в контейнере Python (образ по дайджесту, зависимости —
`requirements.txt` с точными версиями и хэшами, `pip --require-hashes`); на хосте ничего не ставится. Тесты ходят к
Caddy из сетевого пространства его контейнера, адрес источника — свой у каждого теста (127.x.y.z), поэтому лимит
попыток по IP и «альбом» проверяются честно. `tests/conformance/` — Go-тест `albumstest.Conformance` через Caddy с двумя
токенами (запускается в контейнере golang по дайджесту, модули — из кэша хоста только на чтение, без сети; в обычном
`go test ./...` пропускается). Токены стенда — в `$XDG_RUNTIME_DIR/sa-stand/private/env` (0600), в вывод и журналы не идут.
Один запуск pytest для отладки: `stand.sh pytest main -k login -x`.

## Раскатка: OpenTofu → Ansible (спека 002)

Все команды `provision.sh` и `review.sh` владелец запускает под `sa-ops` в ВМ раскатки без агентов (ops-ВМ, design §16;
подготовка — шаг 1 ниже); в рабочей ВМ они отказывают кодом 3 (метка машины).

| Файл | Что |
|---|---|
| `provision.sh` | единственная точка входа владельца: `prepare` (окно Б, без секретов; единственная, которой нужна открытая сеть: GitHub — архив провайдера, выпуски `cli/cli` (архив `gh` и `checksums.txt`) и `raw.githubusercontent.com` (`action.yml` закреплённых действий), TUF Sigstore (`tuf-repo-cdn.sigstore.dev`) — корень, `ghcr.io`, `mirror.gcr.io` — база контроллера по дайджесту, apt Debian и PyPI — контроллер), `image-verify vX.Y.Z [--allow-downgrade]` (проверка выпущенного образа без сети), `up`, `apply [--replace-server]`, `configure`, `backup`, `forget-host`; tofu, ansible, age и gh идут в контейнерах `sa-prov-*` без журнала |
| `ops/review.sh` | просмотр нового кода **до** checkout (единственный рубеж против агента рабочей ВМ): метка машины, учётная запись, копия bundle через `dd` с `nofollow` и пределом, `verify`, `fetch`, `fsck --strict`, полный diff в `less`, «yes», затем checkout; копия живёт в `~/.local/libexec/sa/` |
| `ops/guard.sh`, `secrets.sh`, `backup-lib.sh`, `mirror.sh`, `images.env` | отказы до действий (код 3: метка машины раскатки, учётная запись, сессия, окружение, клон), расшифровка файла секретов, агент ключей, чистка и свежесть копий, провайдер tofu архивом с GitHub (оба хэша, зеркало), образ tofu по дайджесту |
| `ops/ghcr_probe.py`, `ops/probe.sh` | проба сети сервера до реестра образов (спека 004, 004-17): первое действие `configure`, до `bootstrap.yml`; скрипт уходит на сервер по ssh через stdin (на сервере под `timeout`, под root — `runuser -u nobody`), скачивает образ целиком по дайджесту со сверкой размера и sha256 каждого слоя, на диск не пишет; не вышло — код 3, «сервер не изменён», текст по коду (255 — ssh, 127 — нет python3, 2 — вызов, 124/137 — срок, 1 — сеть/план Б) |
| `ops/image.sh`, `ops/tools.sh`, `ops/image-consts.env`, `ops/tools.env` | выпуск образа (спека 004): `image-verify` (улики из входящих, `gh attestation verify` в контейнере без сети, решение по JSON сертификата), `verified-image`/`deployed-image`, порог отката; окно Б: `gh` из архива выпуска с закреплённым sha256, корень Sigstore, сверка закреплённых действий; константы личности подписанта и версия `gh` |
| `ops/ctl-run.sh` | запуск контроллера Ansible (argv `podman run`, тома, окружение, контракт `-e`, канал vault): один код для `provision.sh` и локального стенда (`tests/provision/converge.sh`); подключается только из просмотренного клона, в libexec не копируется; шаг `run.sh static ctl_shared` проверяет argv и что других мест сборки запуска нет |
| `ops/phrase-fd.sh`, `sa-backup-prune.*` | подача фразы в `ssh-add`; суточная чистка копий (пользовательский таймер, скрипты — копии из `~/.local/libexec/sa/`) |
| `tofu/clo/` | ресурсы провайдера `clo`, `.terraform.lock.hcl`, `tests/` (`mock_provider`), `tofu-entry.sh`; другие файлы в каталоге запрещены |
| `inventory.example/` | образец приватного inventory (домен, адреса и ключи — выдуманные); настоящий лежит только в `~/inventory` учётной записи раскатки в ops-ВМ |

Проверка без сервера и сети провайдера: `bash deploy/tests/provision/run.sh static` (шаги `tofu_*`, `provision_cli`).
Весь `provision.sh` запускается по полному пути из клона: `~/src/sosninaalbums-cloud/deploy/provision.sh <команда>`.
Ключ в ssh-агенте раскатки живёт час (`ssh-agent -t 3600`, `ssh-add -t 3600`): прогон `configure` короче; если затянулся
или убит, ключ снова добавляется при следующем запуске, фраза спрашивается заново.

### 1. Подготовка ВМ раскатки (один раз, администратор ops-ВМ)

Граница доступа — отдельная ВМ раскатки (ops-ВМ, design §16): в ней нет агентов, Claude и Node, нет SSH-сервера, общего буфера
и перетаскивания. Секреты, inventory и копии лежат только в доме `sa-ops` на ней. Против агента рабочей ВМ рубеж
один — полный diff в `review.sh` до checkout (шаг 2); метка машины, проверка сессии и `fsck` — помощь в чтении и защита от
ошибки, а не вторая граница. Имя учётной записи по умолчанию — `sa-ops` (`SA_OPS_USER`), каталог входящих —
`/srv/sa-inbox` (`SA_INBOX`).

Исходная точка: ops-ВМ — полный клон чистого снимка Ubuntu 24.04 (снимок сделан до установки агентов; клон обновлён),
учётная запись `sa-ops` создана (`adduser`), без `sudo`. В настройках гипервизора: у ops-ВМ буфер обмена и перетаскивание
выключены (галочки Guest Isolation; меняются только при выключенной ВМ; токен CLO — ВР-17), общая папка `sa-inbox` —
«Always enabled» и Read-only; у рабочей ВМ — та же папка хоста без Read-only, других общих папок нет. Команды ниже — под
администратором ops-ВМ (`sudo`), кроме проверок под `sa-ops`.

```
# 1. Свой machine-id, имя и пароль — до метки (клон несёт значения снимка)
sudo rm -f /etc/machine-id /var/lib/dbus/machine-id && sudo systemd-machine-id-setup && sudo ln -sf /etc/machine-id /var/lib/dbus/machine-id
sudo hostnamectl set-hostname <имя-ops-ВМ>      # и то же имя в строке 127.0.1.1 файла /etc/hosts (sudoedit /etc/hosts)
passwd                                          # пароль администратора — свой, не как в рабочей ВМ
cut -c1-6 /etc/machine-id                       # сверить с рабочей ВМ: первые 6 знаков различаются
# 2. Брандмауэр: слушающие службы — только на loopback, остальное закрыто (avahi, UDP 5353, закрывает ufw).
#    Исходящая сеть: сейчас allow (окно подготовки); в раскатке reject — раздел «Исходящая сеть» ниже
sudo ufw default deny incoming && sudo ufw default allow outgoing && sudo ufw enable
ss -tulpn                                       # слушают только 127.0.0.x и ::1 (исключение — avahi, UDP 5353: закрыт ufw)
dpkg -s openssh-server 2>&1 | head -n 1         # «package ... is not installed»: SSH-сервера нет
# 3. Учётная запись раскатки
id sa-ops                                       # группы: sa-ops и users (100: adduser в Ubuntu 24.04 добавляет её всем — норма);
                                                # опасны sudo, adm, lxd
sudo chmod 0700 ~sa-ops                         # HOME_MODE в Ubuntu 24.04 — 0750, provision.sh требует 0700
sudo stat -c '%a %U %n' ~sa-ops                 # ожидаем «700 sa-ops …»; не 700 — отказ (код 3)
grep '^sa-ops:' /etc/subuid /etc/subgid         # диапазон есть и не пересекается; нет — usermod --add-subuids/--add-subgids
                                                # enable-linger (таймер чистки копий) — в §5, после перехода в раскатку
# 4. Пакеты — только из архива Ubuntu
ls /etc/apt/sources.list.d/                     # только ubuntu.sources: без PPA и сторонних репозиториев
sudo apt update && sudo apt install podman uidmap slirp4netns git curl
for t in ps less timeout flock install ssh ssh-agent ssh-add ssh-keygen ssh-keyscan curl sha256sum awk; do command -v "$t" >/dev/null || echo "нет $t"; done   # нужны provision.sh; пусто — всё есть
# 5. Метка машины раскатки: без неё provision.sh и review.sh отказывают (код 3)
sudo sh -c 'umask 022; printf "sa-ops-machine %s\n" "$(cat /etc/machine-id)" >/etc/sa-ops-machine'
# 6. Канал: общая папка только на чтение
command -v vmhgfs-fuse || sudo apt install open-vm-tools
vmware-hgfsclient                               # ровно одна папка: sa-inbox
sudo install -d -m 0755 /srv/sa-inbox
echo '.host:/sa-inbox /srv/sa-inbox fuse.vmhgfs-fuse ro,nosuid,nodev,noexec,allow_other,umask=022,x-gvfs-hide,nofail 0 0' | sudo tee -a /etc/fstab
sudo systemctl daemon-reload && sudo mount /srv/sa-inbox
findmnt -no OPTIONS /srv/sa-inbox               # в списке есть ro (и nosuid, nodev, noexec); нет ro — стоп
touch /srv/sa-inbox/x && echo ЗАПИСЬ-ПРОШЛА     # ждём ошибку «только для чтения»
sudo -u sa-ops test -r /srv/sa-inbox/code.bundle && echo ЧИТАЕТСЯ   # после первого bundle (шаг 2)
```

#### Исходящая сеть ops-ВМ (шаг 2; design §16.10)

Код большой, построчно его с первого раза не прочесть, поэтому в раскатке ops-ВМ не выпускает наружу ничего, кроме API провайдера,
своего сервера и продления аренды: испорченный код не отправит секреты в другое место (чтение diff в `review.sh` это не заменяет).
Правила ставит администратор (`sudo`). Переключатель — политика `ufw` для исходящих: **раскатка** (обычно) — `reject`, **окно** (сеть
открыта) — `allow`; разрешающие правила постоянные. В раскатке DNS закрыт (имя API — строка в `/etc/hosts`), IPv6 выключен. В окне
исполняется весь код клона, а не только diff, поэтому окон два вида:
- **окно А** — `apt`, адреса, правила; администратор, **без процессов `sa-ops`**; раз в месяц или если сменился адрес API;
- **окно Б** — сверка lock (§4), затем `provision.sh prepare`; под `sa-ops`, поэтому редкое: только если `git diff --stat` из §5
  («Дальше») непуст. Сообщение `provision.sh` «окно подготовки» поводом не служит: его напечатает и испорченный код.

В окне не вводятся фразы файла секретов, ключей ssh и vault; пароль `sudo` администратора и вход в GDM допустимы.

**Один раз, администратор** (cron обошёл бы окно А: задание `sa-ops` идёт без сессии; IPv6 — `ufw` пропускает ICMPv6, а NetworkManager
возвращает link-local):

```
echo root | sudo tee /etc/cron.allow                  # crontab — только root
sudo ls -A /var/spool/cron/crontabs/                  # ожидаем: пусто (нет sa-ops; `crontab -l -u sa-ops` не годится: после cron.allow он отказывает)
printf '%s\n' 'net.ipv6.conf.all.disable_ipv6 = 1' 'net.ipv6.conf.default.disable_ipv6 = 1' | sudo tee /etc/sysctl.d/90-sa-noipv6.conf
nmcli -g NAME connection show --active                # ожидаем: имя соединения <соединение>
sudo nmcli connection modify "<соединение>" ipv6.method disabled   # действует после перезагрузки в раскатку
```

**Окно А: открыть.** Под `sa-ops` в окне не входить.

```
sudo loginctl disable-linger sa-ops && sudo loginctl terminate-user sa-ops
ps -eo uid=,user= | awk '$2=="sa-ops"||$1>=100000'    # ожидаем: пусто (uid от 100000 — контейнеры sa-ops)
sudo ls -A /var/spool/cron/crontabs/                  # ожидаем: нет sa-ops
dpkg -s at 2>&1 | head -n 1                           # ожидаем: «package 'at' is not installed» (стоит — только root в /etc/at.allow)
sudo ufw default allow outgoing
```

**Адреса и правила — один раз, в окне А.** `<хост API>` — из документации провайдера или панели; `clo.tfvars` в окне не создаётся (§3),
там в `api_url` пишется тот же хост. Порт 443 (если в `api_url` другой — он).

```
getent ahostsv4 <хост API> | awk '{print $1}' | sort -u   # ожидаем: адреса API <адрес API>
whois <адрес API>                                         # ожидаем: сеть самого провайдера, не CDN и не общий фронт; иначе стоп, записать в research §4
nmcli -g DHCP4.OPTION device show | tr '|' '\n' | grep dhcp_server_identifier   # ожидаем: один адрес <сервер DHCP>
sudo ufw allow out to <адрес API> port 443 proto tcp      # на каждый адрес API
echo '<адрес API> <хост API>' | sudo tee -a /etc/hosts    # по строке на каждый адрес API
sudo ufw allow out to <сервер DHCP> port 67 proto udp     # продление аренды; без него ВМ потеряет сеть
```

**Правило на сервер — после `apply`, один раз** (`--replace-server` адрес сохраняет). Адрес — **из панели провайдера**, а не из вывода
`apply` (его печатает код, против которого закрыта сеть); не совпал с панелью — стоп.

```
sudo ufw allow out to <адрес сервера> port 22 proto tcp   # ожидаем: в ufw status адрес тот же, что в панели
```

**Переключение.**
- В раскатку: `sudo ufw default reject outgoing && sudo reboot`, потом `sudo loginctl enable-linger sa-ops` (таймер чистки копий). Перезагрузка
  нужна: `ufw` не обрывает уже открытые соединения, процесс из окна сохранил бы своё.
- В окно: `sudo ufw default allow outgoing`; окно А — только после «Окно А: открыть».

**Проверка после перезагрузки (раскатка).** Администратор: `sudo ufw status verbose` — ожидаем `deny (incoming), reject (outgoing)` и
`ALLOW OUT` трёх видов: tcp 443 на адреса API, tcp 22 на адрес сервера (как в панели), udp 67 на сервер DHCP;
`ip -6 addr show scope global` и `ip -6 addr show scope link` — ожидаем пусто, иначе стоп, к §3 не переходить. Под `sa-ops`:

```
getent ahostsv4 example.org                                           # ожидаем: пусто (DNS закрыт)
curl -q -m 5 -sS -o /dev/null https://<внешний IPv4>                  # ожидаем: ошибка (любой публичный адрес, найденный в окне)
curl -q -m 10 -sS -o /dev/null -w '%{http_code}\n' https://<хост API>/   # ожидаем: любой код HTTP
IMG=$(sed -n 's/^SA_TOFU_IMAGE=\(ghcr\.io\/opentofu\/opentofu@sha256:[0-9a-f]\{64\}\)$/\1/p' ~/src/sosninaalbums-cloud/deploy/ops/images.env); echo "образ: $IMG"; test -n "$IMG"   # ожидаем: образ по дайджесту
podman run --rm --entrypoint wget "$IMG" -T 5 -q -O /dev/null https://<внешний IPv4>   # ожидаем: ошибка (из контейнера тоже закрыто)
```

**Сменился адрес API** — `provision.sh apply` падает с ошибкой соединения; само сообщение ничего не доказывает. В окне А, администратор:
1. `sudo cp /etc/hosts /etc/hosts.sa-keep && sudoedit /etc/hosts` — убрать строки с `<хост API>` (иначе `getent` вернёт старый адрес из файла).
2. `getent ahostsv4 <хост API> | awk '{print $1}' | sort -u` — ожидаем: актуальные адреса; сравнить с правилами `ufw status`.
3. Те же — окно не нужно: `sudo mv /etc/hosts.sa-keep /etc/hosts`, причину искать в панели и балансе. Иные — `whois`, новые правила и
   строки `/etc/hosts` по образцу выше, старые правила убрать (`sudo ufw status numbered`, `sudo ufw delete <номер>`).
4. `sudo grep api_url ~sa-ops/inventory/tofu/clo.tfvars | cat -v` — ожидаем тот же хост, что в строках `/etc/hosts`. Файл пишет
   `sa-ops`, ему не доверять: источник хоста — `/etc/hosts` (пишет root) или документация провайдера; расхождение — стоп.

**Обновления** — в окне А, не реже раза в месяц: `sudo apt update && sudo apt full-upgrade` (в раскатке `unattended-upgrades` до архива не
дотянется, часы без NTP уходят на секунды — это нормально).

Метка — защита от ошибки владельца (раскатка не в той машине), а не от агента: `provision.sh` и `review.sh` отказывают
(код 3, «не машина раскатки»), если нет `/etc/sa-ops-machine` (root, без записи группе и прочим, содержимое — ровно
`sa-ops-machine <machine-id>` и перевод строки), если `/etc/machine-id` — не 32 строчных hex, и если в `ps -e` есть процесс
`claude` или `node` (или `ps` не выполнился). Путь метки постоянный. Клон ops-ВМ после смены machine-id метку теряет:
шаги 1 и 5 повторяются осознанно.

Вход `sa-ops` — обычным входом в графический менеджер; правило сессии — только терминал: без браузера,
почты и мессенджеров. Веб-консоль провайдера (сверка отпечатка ключа хоста, шаг 5 порядка работ) — в браузере хоста или в
сессии администратора. Проверка под `sa-ops`:

```
cat /etc/sa-ops-machine /etc/machine-id                             # machine-id в обеих строках совпадает
loginctl show-session "${XDG_SESSION_ID:-$(loginctl show-user "$(id -u)" -p Display --value)}" -p Type -p User -p Remote -p State
                                                                    # Type=wayland|x11|tty, User=<id -u>, Remote=no, State=active
echo "$XDG_RUNTIME_DIR"; stat -c '%a %U' "$XDG_RUNTIME_DIR"         # /run/user/<uid>, 700, sa-ops
podman info --format '{{.Host.Security.Rootless}}'                  # true
```

Один раз под `sa-ops`, в его графической сессии: индексатор файлов GNOME разбирает видимые файлы в доме и в смонтированных
папках, а там лежат чужие данные (bundle), поэтому его отключают:

```
systemctl --user mask tracker-miner-fs-3.service tracker-extract-3.service
systemctl --user is-enabled tracker-miner-fs-3.service tracker-extract-3.service   # masked (оба)
```

Ошибка «нет такого юнита» (`Unit … does not exist`) — пакета индексатора в системе нет, это не провал: идти дальше. Правило:
папку bundle открывать только через `review.sh` (шаг 2), не файловым менеджером; `x-gvfs-hide` в `fstab` скрывает точку
монтирования из его боковой панели.

Форма с `${XDG_SESSION_ID:-…}` нужна из-за терминала GNOME: `XDG_SESSION_ID` в нём не задан (gnome-terminal-server — потомок
`systemd --user`, а не сессии), и `loginctl show-session ""` отвечает «Caller does not belong to any known session». Тогда
берётся графическая сессия учётной записи (`Display`); `provision.sh` делает то же.

Не сошлось — стоп. Проверка сессии в `provision.sh` — окружение для файла токена, сокета агента и rootless Podman
(`XDG_SESSION_ID` или, если он пуст, `Display` учётной записи; тип `tty`, `x11` или `wayland`, свой uid, не удалённая,
`State` — `active` или `online`; `XDG_RUNTIME_DIR` — `/run/user/<uid>`, 0700). Что
`sudo -iu sa-ops` и `su - sa-ops` из сессии администратора дают отказ (нет сессии или чужой `User`), — гипотеза: проверить
`provision.sh forget-host` под ними, ждём код 3. Что `age` в `podman run -it` читает фразу с терминала, а `ssh-add` с
`SSH_ASKPASS_REQUIRE=force` передаёт дескриптор помощнику, проверяет шаг `provision_cli` на тестовых данных.

### 2. Канал, первый просмотр и клон

Код попадает к раскатке только git bundle через общую папку (design §16.2). Рабочая ВМ, один раз (владелец):

```
sudo install -d -m 0755 /srv/sa-inbox
echo ".host:/sa-inbox /srv/sa-inbox fuse.vmhgfs-fuse rw,allow_other,nosuid,nodev,noexec,uid=$(id -u),gid=$(id -g),umask=022,nofail 0 0" | sudo tee -a /etc/fstab
sudo systemctl daemon-reload && sudo mount /srv/sa-inbox
```

Без `allow_other` монтирование (его делает root) недоступно обычной учётной записи: `Permission denied`.

Каждый раз (владелец, рабочая ВМ) — во временное имя и `mv`, чтобы ops-ВМ не прочла недописанный файл:

```
git bundle create /srv/sa-inbox/.code.bundle.new <ветка> && mv -f /srv/sa-inbox/.code.bundle.new /srv/sa-inbox/code.bundle
```

Всё, что пришло из рабочей ВМ, — данные противника: агент меняет файл в любой момент. Поэтому копия в дом делается один раз
(`dd` с `nofollow`, `nonblock`, `fullblock`, пределом и таймаутом), дальше работают только с копией, а доверие — прочитанному diff. Просмотр — **до** checkout и до
любой сборки; первый раз команды выполняются руками под `sa-ops` в ops-ВМ (дальше их заменяет
`~/.local/libexec/sa/review.sh <ветка>`):

```
cat /etc/sa-ops-machine /etc/machine-id                                       # machine-id в обеих строках совпадает; нет файла или не совпало — стоп: это не ops-ВМ
B=/srv/sa-inbox/code.bundle; BR=<ветка>
T=$(umask 077; mktemp -d ~/.review.XXXXXX)                                     # скрытый рабочий каталог (копия и временный репозиторий) — до копии
test -f "$B" && test ! -L "$B" && (umask 077; timeout 120 dd if="$B" of="$T/in.bundle" iflag=nofollow,nonblock,count_bytes,fullblock count=209715201 bs=1M status=none)   # не больше предела+1; nofollow — ELOOP на симлинке, nonblock — FIFO не повесит, fullblock — короткие чтения не режут копию
test "$(stat -c %s "$T/in.bundle")" -le 209715200 || { rm -rf "$T"; echo "bundle больше 200 МиБ или не скопирован: стоп"; }   # после копии, как в review.sh
git init -q "$T/v"                                                             # сначала проверка во временном пустом репозитории
git -C "$T/v" bundle verify "$T/in.bundle" && git -C "$T/v" fetch "$T/in.bundle" "refs/heads/$BR" && git -C "$T/v" fsck --strict --no-dangling
(umask 077; mkdir -p ~/src && git init -q ~/src/sosninaalbums-cloud) && cd ~/src/sosninaalbums-cloud   # umask 077: при 002 (Ubuntu) каталоги вышли бы 775, а запись группе на клоне или его предке — отказ (код 3)
git config transfer.fsckObjects true; git config fetch.fsckObjects true
git fetch "$T/in.bundle" "refs/heads/$BR"                                      # только из копии
git fsck --strict --no-dangling                                                # на git 2.43 fsckObjects при fetch из bundle битый коммит пропускает
git ls-tree -r FETCH_HEAD | awk '$1 != "100644" && $1 != "100755"'            # должно быть пусто — по ВСЕМУ дереву: симлинк на месте самого deploy или web pathspec deploy/ не видит
git cat-file -t FETCH_HEAD:deploy; git cat-file -t FETCH_HEAD:web/viewer/notfound.html   # tree и blob, иначе стоп
LC_ALL=C git grep -a -l -P '^(?!(?:[\t\x20-\x7e]|[\xd0-\xd3][\x80-\xbf]|\xc2[\xa7\xa9\xab\xb0\xbb]|\xe2\x80[\x94\xa6]|\xe2\x84\x96|\xe2\x86\x92|\xe2\x89[\xa0\xa4\xa5])*+$)' FETCH_HEAD -- deploy/ web/viewer/notfound.html .github go.mod go.sum   # белый список символов; код 1 — чисто; 0 — стоп
git diff --text --no-color --no-ext-diff --no-textconv 4b825dc642cb6eb9a060e54bf8d69288fbee4904 FETCH_HEAD -- \
  deploy/ web/viewer/notfound.html .github go.mod go.sum ':(glob)**/.gitattributes' | LESSSECURE=1 LESSOPEN= LESSCLOSE= LESS=-X less   # первый раз — от пустого дерева, весь текст
git hash-object -t tree -w /dev/null
git --attr-source=4b825dc642cb6eb9a060e54bf8d69288fbee4904 checkout --detach FETCH_HEAD   # только после «да»; --attr-source — git ≥ 2.40
rm -rf "$T"
```

`owned_safe` (guard.sh, review.sh) проверяет только клон, `~/inventory`, `~/backups`, каталог файла секретов и их предков: каталоги,
которые `fetch` и `checkout` создадут внутри клона при umask 002, живут под клоном с правами 700 и снаружи недоступны. Те же
каталоги в этом runbook создаются с 700 (`install -d -m 700 ~/inventory ~/backups`; `prepare` и `backup` — под `umask 077`;
каталог файла секретов — `~`, §1: 700). Отказ «есть запись группе или прочим» называет путь и лечится `chmod go-w <путь>`.
Клон, созданный по прежней инструкции (при umask 002 — 775), чинится сразу целиком: `chmod go-w ~/src ~/src/sosninaalbums-cloud`;
иначе `owned_safe` отказывает по одному пути за раз. Для каталога root подсказка другая: права не менять, выбрать путь в доме `sa-ops`.

Дальше — `~/.local/libexec/sa/review.sh <ветка>`: то же, но diff — от `inventory/deployed-commit`, и отказ при изменённом
`.gitattributes`. Скрипт, копии `sa-backup-prune.sh` и `backup-lib.sh` и юниты таймера ставит в `~/.local/libexec/sa/` и
`~/.config/systemd/user/` успешный `configure` из уже прочитанного коммита. Клон по локальному пути запрещён (переносит
`alternates`).

### 3. Inventory, ключи, файл секретов (под `sa-ops`, в сессии ops-ВМ; только в раскатке)

Этот шаг — **только в раскатке** (сеть закрыта, раздел «Исходящая сеть» выше): файлы inventory (`provision.env`, `clo.tfvars`,
`vars.yml`, vault) и файл секретов создаются здесь, а не в окне; в окне есть только каталог `~/inventory`, который создаёт
`provision.sh prepare`. В `api_url` файла `clo.tfvars` пишется тот же хост API, что в правилах и `/etc/hosts` (расхождение —
`apply` не соединится). Образ контроллера ниже собирается из кэша, который наполнил `provision.sh prepare`.
Команды этого шага копировать из этого README внутри ops-ВМ (проверенный клон, буфер гостевой системы). Токен CLO — по
ВР-17: одна загрузка ВМ с включённым буфером до появления секретов, файл `~/sa-new/clo_token` (0600); в вопрос
`clo_token:` вставить из него, затем `shred -u ~/sa-new/clo_token`.

Inventory — **только** `~/inventory` (`SA_INVENTORY`), владелец и права проверяет `provision.sh`; правится только в
сессии `sa-ops` ops-ВМ, а всё, что пришло в него из рабочей ВМ, просматривается как код (`group_vars` — это Jinja,
`deployed-commit` управляет diff, `hosts.yml` и `known_hosts` решают, куда уйдёт пароль).

```
install -d -m 700 ~/.ssh ~/inventory ~/backups ~/sa-new     # ~/inventory уже создан prepare
ssh-keygen -t ed25519 -f ~/.ssh/id_server       # фразы — не слабее 128 бит, например `openssl rand -base64 18`
ssh-keygen -t ed25519 -f ~/.ssh/id_backup
cd ~/inventory && git init -q && printf '%s\n' 'tofu/.terraform-clo/' '.lock' >.gitignore
mkdir -p tofu ansible/group_vars/all
cp ~/src/sosninaalbums-cloud/deploy/inventory.example/provision.env .
cp ~/src/sosninaalbums-cloud/deploy/inventory.example/tofu/clo.tfvars.example tofu/clo.tfvars
cp ~/src/sosninaalbums-cloud/deploy/inventory.example/ansible/group_vars/all/vars.yml ansible/group_vars/all/
# отредактировать provision.env, clo.tfvars, vars.yml; в clo.tfvars ssh_public_key — содержимое ~/.ssh/id_server.pub,
# а то же значение (ключ и комментарий) обязано быть среди admin_ssh_keys в vars.yml;
# backup_ssh_key в vars.yml — содержимое ~/.ssh/id_backup.pub;
# backup_age_recipient (открытый ключ age) появится ниже, на шаге файла секретов: после него вернуться в vars.yml
```

Менять `admin_ssh_keys` на уже поднятом сервере — сначала плеем `bootstrap.yml` (он ставит ключи под проверкой входа
с контроллера), и только потом `site.yml`: `site.yml` при каждом прогоне проверяет, что ключи администратора на сервере
менять не пришлось, иначе падает (файл при этом уже возвращён к заданному, повторный прогон проходит). Это же
сработает, если `authorized_keys` администратора кто-то правил вне раскатки: выяснить, кто и зачем.

Куда уходит копия git-репозитория inventory (в нём `tofu/clo.tfstate` и `deployed-commit`) — **открытый вопрос владельцу**:
удалённый приватный репозиторий или резервная копия вне сервера; потеря state без копии означает потерю связи с созданными
ресурсами. Пока копии нет — inventory хранится только в доме учётной записи раскатки.

Контроллер собирается из **просмотренного** клона (`deploy/ansible/Containerfile`, задача И3). Создание файла секретов
и vault — только после первого полного просмотра (шаг 2), в контейнере контроллера, через его tmpfs, в пустой каталог
`~/sa-new`, а не в весь дом; затем `mv` на место. Файл секретов `~/secrets.age` (0600): пять строк `ключ=значение` —
`clo_token`, `vault_pass`, `ssh_key_phrase`, `backup_key_phrase`, `age_identity` (закрытый ключ age копий; `age-keygen`
печатает открытый — он идёт в `backup_age_recipient`). Фраза файла — сгенерированная `age -p` или не слабее; потеряна —
потеряны копии: записать на бумагу и хранить отдельно.

```
ID=$(podman build -q -f ~/src/sosninaalbums-cloud/deploy/ansible/Containerfile ~/src/sosninaalbums-cloud/deploy/ansible)
RUN="podman run --rm -it --log-driver=none --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --tmpfs /tmp:rw,mode=0700 -v $HOME/sa-new:/out"
$RUN "$ID" bash -c '
    umask 077
    for k in clo_token vault_pass ssh_key_phrase backup_key_phrase; do
      read -rsp "$k: " v; echo; printf "%s=%s\n" "$k" "$v" >>/tmp/s
    done
    age-keygen -o /tmp/k && printf "age_identity=%s\n" "$(grep ^AGE-SECRET-KEY /tmp/k)" >>/tmp/s
    age -p -o /out/secrets.age /tmp/s'
mv ~/sa-new/secrets.age ~/secrets.age && chmod 600 ~/secrets.age
```

Vault (`ansible/group_vars/all/vault.yml`): создаётся и шифруется в том же контейнере; пароль vault — значение `vault_pass`
из файла секретов, идёт каналом, не файлом. Хэш пароля администратора — `mkpasswd` (yescrypt; пакет есть в контроллере,
если нет — сверить с И3):

```
$RUN "$ID" bash -c '
    umask 077; set -e
    gen() { head -c "$1" /dev/urandom | base64 | tr "+/" "-_" | tr -d "=\n"; }
    read -rsp "vault_pass: " vp; echo
    admin=$(gen 24); master=$(gen 32)
    hash=$(printf "%s" "$admin" | mkpasswd --method=yescrypt --stdin)
    printf "%s\n" "---" "vault_albums_master: \"$master\"" "vault_admin_password: \"$admin\"" "vault_admin_password_hash: \"$hash\"" >/tmp/v.yml
    echo "пароль администратора (запишите для консоли провайдера): $admin"
    exec 3< <(printf "%s" "$vp")
    ansible-vault encrypt --vault-password-file=/dev/fd/3 --output /out/vault.yml /tmp/v.yml'
mv ~/sa-new/vault.yml ~/inventory/ansible/group_vars/all/vault.yml
```

**Правка vault и смена секретов** — так же, только в контейнере контроллера в сессии `sa-ops`: `ansible-vault decrypt` (или
`age -d`) в `/tmp` контейнера, правка там же, `ansible-vault encrypt` (или `age -p`) в `/out`, `mv`. Открытого файла на
диске раскатки не остаётся. Смена `vault_pass`, `age_identity` или фраз — перешифровать файл и vault, перевыпустить ключи.

### 4. Сверка lock-файла провайдера (до `prepare` и первого `apply`; при каждом обновлении провайдера; в окне Б)

`.terraform.lock.hcl` в репозитории собран по `SHA256SUMS` релиза провайдера; **первая сверка выполнена владельцем 05.11.2026**
(версия 2.8.0, совпало; решение П2a-8). Каждое обновление провайдера — снова эта же сверка до `prepare`. Подпись `SHA256SUMS`
проверить нечем — реестр ключей провайдера не публикует. Реестр OpenTofu из России закрыт (403), поэтому
провайдер берётся **архивом выпуска с GitHub**, без реестра (ВР-15). Сверка под `sa-ops`, **в окне Б, до
`provision.sh prepare`** (нужны GitHub и `ghcr.io`; фразы не вводятся): `prepare` ставит провайдер по этому lock, и несверенный
lock нельзя ставить (после смены версии он снова несверенный). Расхождение — стоп. Блок вставляется целиком и идёт в чистой оболочке (`env -i … bash --noprofile --norc`: ни
`~/.bashrc`, ни `~/.curlrc`); `images.env` из bundle **не исполняется** (`.` выполнил бы любую его строку), образ извлекается `sed` по
строгому образцу; `h1` считается без сети (`--network=none`) из того же архива:

```
env -i HOME="$HOME" PATH=/usr/bin:/bin XDG_RUNTIME_DIR="$XDG_RUNTIME_DIR" bash --noprofile --norc <<'EOF'
set -euo pipefail
IMG=$(sed -n 's/^SA_TOFU_IMAGE=\(ghcr\.io\/opentofu\/opentofu@sha256:[0-9a-f]\{64\}\)$/\1/p' ~/src/sosninaalbums-cloud/deploy/ops/images.env)
echo "образ: $IMG"; test -n "$IMG"                                                              # ожидаем: образ по дайджесту (файл из bundle не исполняется)
L=~/src/sosninaalbums-cloud/deploy/tofu/clo/.terraform.lock.hcl
V=$(sed -n 's/^  version *= *"\(.*\)"$/\1/p' "$L"); echo "версия по lock: $V"                    # ожидаем: одна строка вида X.Y.Z (сейчас 2.8.0)
[[ $V =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]                                                          # ожидаем: без вывода
d=$(mktemp -d); trap 'rm -rf "$d"' EXIT; m=$d/m/registry.opentofu.org/clo-ru/clo; mkdir -p "$m" "$d/w"
U=https://github.com/clo-ru/terraform-provider-clo/releases/download/v$V; N=terraform-provider-clo_${V}_linux_amd64.zip
curl -q --proto '=https' --tlsv1.2 -fsSL -o "$m/$N" "$U/$N"                                   # ожидаем: без вывода (скачано около 7 МБ)
curl -q --proto '=https' --tlsv1.2 -fsSL -o "$d/SHA256SUMS" "$U/terraform-provider-clo_${V}_SHA256SUMS"   # ожидаем: без вывода
A=$(sha256sum "$m/$N" | cut -d' ' -f1); S=$(awk -v n="$N" '$2==n {print $1}' "$d/SHA256SUMS")
echo "sha256 архива:     $A"; echo "строка SHA256SUMS: $S"                                   # ожидаем: два одинаковых 64-значных хэша
test "$A" = "$S"                                                                              # ожидаем: без вывода (архив = SHA256SUMS)
grep -qF "\"zh:$A\"" "$L"                                                                     # ожидаем: без вывода (архив = zh: в lock)
diff <(grep -o 'zh:[0-9a-f]*' "$L" | sort) <(grep -v manifest "$d/SHA256SUMS" | awk '{print "zh:"$1}' | sort)   # ожидаем: без вывода (все zh = SHA256SUMS)
cp ~/src/sosninaalbums-cloud/deploy/tofu/clo/*.tf "$d/w"
podman run --rm --network=none --log-driver=none -v "$d/w:/work" -v "$d/m:/mirror:ro" -w /work -e HOME=/tmp -e TF_DATA_DIR=/tmp/d \
  "$IMG" providers lock -fs-mirror=/mirror -platform=linux_amd64 >/dev/null </dev/null        # ожидаем: без вывода; lock собран из архива, без сети
diff <(grep -o 'h1:[^"]*' "$d/w/.terraform.lock.hcl") <(grep -o 'h1:[^"]*' "$L")               # ожидаем: без вывода (h1 linux_amd64 совпал)
echo "сверено: lock совпал с архивом выпуска $V"                                              # ожидаем: эта строка
EOF
echo "код $?"                                                                                 # ожидаем: код 0 = совпало; иначе стоп
```

Итог — «сверено» и **код 0**; любой другой исход — стоп (расхождение хэша, нет сети или другая версия в lock). `provision.sh
prepare` ставит в `~/inventory/tofu/.terraform-clo/mirror` архив провайдера ровно версии lock: скачивает его и `SHA256SUMS` с
GitHub, сверяет sha256 и со строкой `SHA256SUMS`, и с `zh:` в lock (оба обязательны), затем `tofu init` из этого каталога и
`validate` (h1 установленного провайдера с lock) — оба без сети; расхождение — отказ. `provision.sh apply` в раскатке берёт
провайдер оттуда же без сети. Сверка 05.11.2026 отмечена в шапке lock-файла; при обновлении провайдера после новой сверки
шапку правят заново и отправляют новый bundle. `init` везде идёт с `-lockfile=readonly`.

### 5. Порядок работ

**Первый раз** (ссылки — на блок «Исходящая сеть ops-ВМ» в §1; сейчас сеть открыта):
1. Администратор: «Один раз» (cron, IPv6) и «Окно А» — `apt full-upgrade`, адреса и правила API и DHCP, строки `/etc/hosts`.
   Хост API — из документации провайдера или панели; `clo.tfvars` ещё нет.
2. Канал, первый просмотр и клон (§2; под `sa-ops`, сети не нужно).
3. Окно Б (под `sa-ops`): сверка lock (§4), **затем** `~/src/sosninaalbums-cloud/deploy/provision.sh prepare`.
4. Раскатка: «Переключение» и «Проверка» (§1; IPv6 пуст, иначе стоп); `sudo loginctl enable-linger sa-ops`.
5. Только теперь `clo.tfvars`, ключи, файл секретов и vault (§3).
6. `provision.sh apply`; адрес сервера сверить **с панелью провайдера** (вывод `apply` — подсказка), правило tcp/22 (§1), затем
   `provision.sh configure` (`up` тоже годится: ждёт tcp/22 до 600 с, правило добавляется из сессии администратора).
7. **Проверка результата первого `configure`** — на сервере (веб-консоль или вход администратора) ожидаем только своё:
   - `sudo sshd -T | grep -iE '^authorizedkeys'` — какие файлы ключей читает sshd;
   - `sudo find / -xdev -name 'authorized_keys*' -exec tail -n +1 {} +` — только ключи из `admin_ssh_keys` и ключ копий (`backup_ssh_key`);
   - `ss -tulnp` — снаружи только `sshd` (tcp 22), Caddy (tcp 80, 443; udp 443) и DHCP-клиент (udp 68, 546), остальное на `127.0.0.1`;
   - `sudo nft list ruleset` — открыты только tcp 22, 80, 443, ICMP (служебные типы и echo-request с лимитом), DHCPv6 udp 546 от `fe80::/10`;
   - `ls -la /usr/share/albums-svc/errors` — один файл `notfound.html` (корень Caddy).

   В панели провайдера: один сервер, один ключ, один адрес.

**Дальше.** Новый bundle → `~/.local/libexec/sa/review.sh <ветка>`. Окно Б — только если вывод команды непуст (сообщение `provision.sh`
поводом не служит); если пуст, `apply` и `configure` идут в раскатке. Сменился адрес API — окно А (§1, «Сменился адрес API»).

```
git -C ~/src/sosninaalbums-cloud diff --stat "$(cat ~/inventory/deployed-commit)" HEAD -- deploy/ops/images.env 'deploy/ansible/Containerfile*' \
  'deploy/ansible/requirements*' deploy/tofu/clo/versions.tf deploy/tofu/clo/.terraform.lock.hcl \
  deploy/ops/tools.env deploy/release/actions.lock   # ожидаем: пусто
```

Окно Б: сверка lock (§4, если менялся lock или `versions.tf`) → `provision.sh prepare` → в раскатку (§1, «Переключение»). Новая версия
`gh` (`tools.env`) или набор действий (`actions.lock`) — тоже окно Б; корень Sigstore обновляется в каждом окне.

**Образ сервиса: проверка и раскатка (спека 004).** `configure` и `up` берут образ только из `~/inventory/verified-image`, который пишет
`image-verify`; значение `albums_svc_image` в `vars.yml` и `host_vars` теперь отказ (код 3, «уберите»). Порядок для каждого выпуска `vX.Y.Z`:
1. Рабочая ВМ кладёт во входящие bundle с `main` и ровно два файла улик: `release.attestation.jsonl` и `release.manifest.json`
   (сначала под временным именем, затем `mv`). Других имён `image-verify` не читает; предел размера — 1 МиБ.
2. `~/.local/libexec/sa/review.sh main`: полный diff по `deploy/`, `.github/`, `go.mod`, `go.sum`; остальное — `--stat`.
   **Первый раз** `main` смотрит старый `review.sh` (его ставит `configure` из раскатанного коммита), и `.github`, `go.mod`, `go.sum`
   он не покажет. Поэтому после `review.sh`, до запуска любого скрипта репозитория, руками (системный git, код репозитория не исполняется):
   ```
   git -C ~/src/sosninaalbums-cloud diff --text --no-color --no-ext-diff --no-textconv "$(cat ~/inventory/deployed-commit 2>/dev/null || echo 4b825dc642cb6eb9a060e54bf8d69288fbee4904)" HEAD -- .github go.mod go.sum | LESSSECURE=1 LESSOPEN= LESSCLOSE= LESSHISTFILE=- LESS=-X less
   ```
   Читать целиком; нашлось незнакомое — стоп. Невидимые и управляющие символы старый `review.sh` в этих путях не ищет: выполнить ту же
   команду `git grep` из §2 (проверка белого списка символов, с теми же флагами и шаблоном), подставив `HEAD` вместо `FETCH_HEAD` и пути `.github go.mod go.sum`;
   код 1 — чисто, 0 — стоп. Дальше `review.sh` покажет и проверит эти пути сам.
3. `provision.sh image-verify vX.Y.Z` (без сети, без секретов): `workflow_guard.py` по `HEAD`, сверка `uses:` с набором, сверенным в окне Б
   (`~/inventory/actions-pins/<sha256 lock>.lock`), затем `gh attestation verify` в контейнере без сети по ID образа из
   `~/inventory/gh-image-id` и корню `~/inventory/sigstore/trusted_root.jsonl`. Решение — только по JSON сертификата каждого результата
   (id репозитория, событие `push`, раннер GitHub, публичность) и субъекту (ровно образ с дайджестом улики). Итог: `~/inventory/verified-image`
   (версия, коммит = `HEAD`, дайджест) и улики в `~/inventory/releases/vX.Y.Z/` (артефакты прогона живут 90 дней; откат позже — без сети).
   Сообщение про окно Б значит: устарел корень или набор действий не сверен — окно Б, затем повторить; код 3 от `gh` дважды подряд — стоп и разбор.
4. `provision.sh configure` (или `up`): код 3, если образ проверен не для текущего `HEAD`; **первым делом на сервере, до `bootstrap.yml` и любой правки**,
   проба сети (004-17): `ops/ghcr_probe.py` уходит на сервер по ssh через stdin (под root права опускаются до `nobody`), скачивает образ из `ghcr.io`
   целиком по дайджесту — манифест и все слои, размер и sha256 каждого сверяются, на диск ничего не пишется — и укладывается в 600 с. Не вышло
   (обрыв, обрезка слоёв на 16 КиБ, медленно, нет ответа) — код 3, «сервер не изменён», вывод сервера построчно; повторить позже, а если стабильно — план Б
   (архив через ops-ВМ, отдельная спека). Образ идёт в Ansible как `-e albums_svc_image=<имя>@<дайджест>`; `podman pull` в роли идёт под `timeout`
   (`albums_svc_pull_timeout_sec`, по умолчанию 600, границы 60-3600); после успешного `site.yml` пишется `~/inventory/deployed-image` (версия и дайджест) — порог отката.
5. Откат: версия ниже раскатанной (`deployed-image`) — только `image-verify vX.Y.Z --allow-downgrade`. Упавший `configure` новой версии порог не двигает:
   вернуться к раскатанной можно без флага.
Окно Б для образа (`provision.sh prepare`, сеть открыта): получает `gh` из архива выпуска `cli/cli` (sha256 архива сверяется с `ops/tools.env` и с `gh_<версия>_checksums.txt`
выпуска; **значение в `tools.env` сверить с выпуском в первом окне Б** — оно взято из прототипа), собирает образ `FROM scratch` с одним бинарником; обновляет корень
Sigstore (`gh attestation trusted-root`, часы машины должны быть верны) и сверяет каждую строку `deploy/release/actions.lock` командой `git ls-remote` с меткой исходного
репозитория (строка ровно `refs/tags/<тег>^{}` или `refs/tags/<тег>`) и `runs.using` в `action.yml` закреплённого коммита (`node*`; composite и docker — отказ).
Файлы `sigstore/`, `actions-pins/`, `gh-image-id`, `verified-image`, `deployed-image`, `releases/` лежат в `~/inventory` (приватный git, коммит после прогона).

**Команды.**
1. `~/src/sosninaalbums-cloud/deploy/provision.sh up` — второй просмотр diff в `less`, «yes»; фраза файла секретов;
   интерактивный `tofu apply` (план показывает он, «no» отменяет); ожидание ssh; `bootstrap.yml` (если входа
   администратора ещё нет), `site.yml`; в конце хэш HEAD пишется в `inventory/deployed-commit`, ставятся скрипты в
   `~/.local/libexec/sa/`. При первом входе показывается отпечаток ключа хоста — сверить в веб-консоли провайдера
   (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`), открытой в браузере хоста: в сессии `sa-ops` браузера нет
   по правилу. A-запись домена — вручную у владельца DNS. **После каждого прогона** —
   коммит inventory (`cd ~/inventory && git add -A && git commit`): state и `deployed-commit` не должны потеряться.
2. Правка (токен фотографа в vault): новый bundle → `~/.local/libexec/sa/review.sh <ветка>` → `provision.sh configure`. Новый образ — выпуск и `image-verify` (выше).
3. Восстановление: `provision.sh apply --replace-server` (адрес остаётся) → `provision.sh forget-host` → `provision.sh configure`
   → возврат копии (этап 7). Смена ключа хоста без `forget-host` — отказ: сверить отпечаток в веб-консоли.
4. `provision.sh backup` забирает копию, проверяет метку конца и чистит старые. Таймер чистки: скрипты и юниты ставит успешный
   `configure` (юниты при каждом прогоне заново); включить один раз, после первого `configure`:
   `systemctl --user enable --now sa-backup-prune.timer`. Копии старше 30 дней
   удаляются без исключений; новейшая старше 7 дней — предупреждение при каждом запуске и в выводе таймера; файл не по
   формату имени чистка не трогает, но предупреждает о нём.
5. Раз в неделю до этапа 7 — советы безопасности репозитория Caddy; есть исправление → правка версии Caddy и образа проверок.

Коды выхода (`image-verify`: 0 — проверено и записано; 3 — улика, корень, набор действий, версия или откат не сошлись, `inventory` не тронут): 2 — способ B/C или другой провайдер (не реализовано); 3 — отказ до действий (не машина раскатки: нет метки
или идёт `claude`/`node`; не та учётная запись или сессия, `CLAUDECODE`, грязное дерево, `alternates`, токен или `TF_LOG*` в
окружении, чужие права на inventory и т. д. — сообщение не содержит значений); 4 — нет сети до сервера.
