# Цель стенда раскатки (спека 002, design §12): Debian 13 с systemd, на которую «встают» роли Ansible.
# Всё, чего нет в облачном образе провайдера или что делают роли, сюда не кладётся: только базовый набор
# (systemd, sshd, python3 для Ansible, sudo, cloud-init) и то, что даёт cloud-init при первой загрузке.
#
# Запуск — только run.sh up (флаги важны: --systemd=always и
# --cap-add NET_ADMIN,SYS_ADMIN,DAC_READ_SEARCH,SYS_PTRACE; без них nft даёт EPERM, юниты с песочницей — 226/NAMESPACE,
# а ss -p не видит процессы чужих пользователей).
# Сборка: podman build -f deploy/tests/provision/target.Containerfile deploy/tests/provision/target
#
# База — по дайджесту индекса (public.ecr.aws, Debian 13 trixie); обновляется осознанно.
FROM public.ecr.aws/docker/library/debian@sha256:2dc419c6d7fe3dad0a2f9fd469c34cc90b0f9ba642d8fde837fe3d7d34a0e7f1

ENV DEBIAN_FRONTEND=noninteractive

# iproute2 — ради `ss` в verify_listeners: в облачном образе он есть, в базовом debian — нет.
# systemd-resolved — как на сервере провайдера: по умолчанию слушает LLMNR на 5355 на всех адресах, и стенд обязан это видеть
# (verify_listeners, verify_resolved; спека 005, 005-11). Что пакет не ломает DNS цели (у podman resolv.conf монтируется
# рантаймом, resolved его не трогает) — гипотеза до первого прогона стенда в CI.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        systemd systemd-sysv dbus openssh-server openssh-client python3 sudo ca-certificates \
        cloud-init iproute2 systemd-resolved \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/ssh/ssh_host_*

# Сеть цели — забота рантайма, как сеть ВМ — забота провайдера: адрес и маршрут выдаёт Podman (IPAM сети стенда), DHCP-сервера
# там нет. На раннере CI (виртуалка Azure; в rootless DMI не виден) ds-identify видит платформу и включает cloud-init, тот
# без метаданных берёт запасной конфиг сети «DHCP на eth0» и отдаёт его systemd-networkd, а networkd снимает с eth0 выданный
# Podman IPv4 (прогон CI: у eth0 остаётся один fe80::, вход «No route to host»). Поэтому cloud-init не трогает сеть, а networkd
# замаскирован (маска — на случай других источников конфигов). Роли Ansible networkd не используют и от него не зависят.
# Имя файла не 99-sa.cfg: роль cloud_init удаляет старый файл с таким именем.
RUN printf '%s\n' '# Только стенд: сеть цели даёт рантайм контейнера, не cloud-init' 'network: {config: disabled}' \
        > /etc/cloud/cloud.cfg.d/98-sa-stand-network.cfg \
    && systemctl mask systemd-networkd.service systemd-networkd.socket systemd-networkd-wait-online.service

# Docker-образ Debian приносит policy-rc.d с exit 101 (службы не стартуют при установке пакетов): в облачном образе
# провайдера его нет, а роль caddy чужой policy-rc.d отвергает (design 002 §8), поэтому цель — как облачный образ.
RUN rm -f /usr/sbin/policy-rc.d

# Пользователь образа и правило sudo — так, как их оставляет cloud-init облачного образа Debian: группы те же,
# пароль заблокирован (вход только по ключу; ключ кладёт run.sh up), sudo без пароля. Именно с него идёт
# converge_bootstrap; после него роль блокирует и удаляет пользователя образа (verify_users).
RUN useradd --create-home --shell /bin/bash \
        --groups adm,audio,cdrom,dialout,dip,floppy,plugdev,sudo,video debian \
    && printf '%s\n' '# Created by cloud-init (стенд)' '# User rules for debian' \
        'debian ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-cloud-init-users \
    && chmod 0440 /etc/sudoers.d/90-cloud-init-users \
    && visudo -c -q

COPY sa-tgt-hostkeys.service sa-tgt-listener.service /etc/systemd/system/
RUN systemctl enable ssh.service sa-tgt-hostkeys.service

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
