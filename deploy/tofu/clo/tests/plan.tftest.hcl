# tofu test без сети и токена: mock_provider, command = plan (design 002 §3). command = apply не годится:
# уборка теста упирается в prevent_destroy у адреса.
mock_provider "clo" {
  mock_data "clo_project_image" {
    defaults = {
      image_id = "image-mock"
    }
  }
}

variables {
  clo_api_token  = "mock-value"
  api_url        = "https://api.invalid"
  project_id     = "00000000-0000-0000-0000-000000000000"
  server_name    = "example-server"
  keypair_name   = "example-key"
  ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAImock mock"
  ptr            = "host.example.org"
}

run "keypair_import_only" {
  command = plan

  assert {
    condition     = clo_compute_keypair.admin.public_key == var.ssh_public_key
    error_message = "ключ только импортом открытой части"
  }
}

run "no_server_password" {
  command = plan

  assert {
    condition     = clo_compute_instance.srv.password == null
    error_message = "пароль сервера не задаётся (иначе попал бы в state)"
  }
}

run "ip_attached_separately" {
  command = plan

  override_resource {
    target = clo_network_ip.main
    values = {
      id = "ip-mock"
    }
  }

  override_resource {
    target = clo_compute_instance.srv
    values = {
      id = "server-mock"
    }
  }

  assert {
    condition     = clo_network_ip_attach.main.address_id == clo_network_ip.main.id
    error_message = "адрес привязан отдельным ресурсом"
  }

  assert {
    condition     = clo_network_ip_attach.main.entity_id == clo_compute_instance.srv.id
    error_message = "привязка к этому серверу"
  }

  assert {
    condition     = length(clo_compute_instance.srv.addresses) == 0
    error_message = "у сервера нет addresses: иначе удаление сервера удалит и адрес"
  }
}
