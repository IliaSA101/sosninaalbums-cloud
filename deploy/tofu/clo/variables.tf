# Значения — из приватного inventory (clo.tfvars) и provision.sh; в публичном репозитории их нет.

# Токен приходит только через TF_VAR_clo_api_token из tofu-entry.sh. sensitive + ephemeral: его нет ни в state,
# ни в плане (design 002 §3). Значения по умолчанию нет: без токена tofu не запустится.
variable "clo_api_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "api_url" {
  type        = string
  description = "Адрес API провайдера."

  # Токен уходит на этот адрес: только https и без учётных данных в адресе (подмена адреса в tfvars не должна
  # отправить токен по открытому каналу или на адрес с логином). Какой именно хост — решает владелец.
  validation {
    condition     = can(regex("^https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]*)?$", var.api_url))
    error_message = "api_url: только https://хост[:порт][/путь], без учётных данных и параметров."
  }
}

variable "project_id" {
  type        = string
  description = "Идентификатор проекта у провайдера."
}

variable "server_name" {
  type        = string
  description = "Имя сервера у провайдера."
}

variable "keypair_name" {
  type        = string
  description = "Имя импортированного открытого ключа у провайдера."
}

# Открытая часть ключа сервера. Закрытый ключ создаёт владелец; пара, созданная провайдером, положила бы
# закрытый ключ в state (поэтому у keypair всегда задан public_key).
variable "ssh_public_key" {
  type        = string
  description = "Открытый ключ ed25519 для первого входа."
}

# Обратная запись (PTR) адреса. Пустая до готовности DNS; provision.sh подставляет домен из provision.env.
variable "ptr" {
  type        = string
  default     = null
  description = "Домен для PTR адреса или null."
}
