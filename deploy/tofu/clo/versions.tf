# Версии закреплены: провайдер — точно, хэши — в .terraform.lock.hcl (init только с -lockfile=readonly).
terraform {
  required_version = ">= 1.11" # ephemeral-переменные (design 002 §3)

  # Путь к файлу state задаёт provision.sh (-backend-config=path=…): он лежит в приватном inventory.
  backend "local" {}

  required_providers {
    clo = {
      source  = "clo-ru/clo"
      version = "= 2.8.0"
    }
  }
}
