# Ресурсы CLO (design 002 §3). Только ресурсы и источники провайдера clo, без выполнения команд и чужих
# провайдеров: проверяет шаг tofu_static.

provider "clo" {
  auth_url = var.api_url
  token    = var.clo_api_token
}

data "clo_project_image" "debian" {
  project_id = var.project_id
  name       = "Debian 13"
}

# Только импорт открытого ключа: без public_key API сгенерирует пару и закрытый ключ ляжет в state.
resource "clo_compute_keypair" "admin" {
  project_id = var.project_id
  name       = var.keypair_name
  public_key = var.ssh_public_key
}

# Адрес живёт отдельно от сервера: пересоздание сервера не должно его удалять.
resource "clo_network_ip" "main" {
  project_id = var.project_id
  ptr        = var.ptr

  lifecycle {
    prevent_destroy = true
  }
}

# Без addresses и password: первый удалял бы адрес вместе с сервером, второй клал бы пароль в state.
resource "clo_compute_instance" "srv" {
  project_id   = var.project_id
  name         = var.server_name
  image_id     = data.clo_project_image.debian.image_id
  flavor_vcpus = 2
  flavor_ram   = 4
  keypairs     = [clo_compute_keypair.admin.id]

  block_device {
    bootable     = true
    storage_type = "volume"
    size         = 20
  }

  # image_id и block_device — ForceNew, а чтение сервера их из API не обновляет: без ignore_changes новый ID
  # образа или правка размера молча предложили бы пересоздание. Пересоздание — только provision.sh apply
  # --replace-server; диск растёт в панели провайдера. keypairs — тоже, на всякий случай: ForceNew у него и у
  # name по схеме провайдера не проверен (схему без `providers schema` не получить), а ключ администратора после
  # bootstrap меняет Ansible (authorized_keys), поэтому смена ключа в провайдере не должна пересоздавать сервер.
  lifecycle {
    ignore_changes = [image_id, block_device, keypairs]
  }
}

# Привязка зависит от сервера: при --replace-server tofu сначала снимает её (адрес остаётся), потом удаляет сервер.
resource "clo_network_ip_attach" "main" {
  address_id  = clo_network_ip.main.id
  entity_name = "server"
  entity_id   = clo_compute_instance.srv.id
  is_primary  = true
}
