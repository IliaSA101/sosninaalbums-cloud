# Собрано вручную по SHA256SUMS релиза clo-ru/terraform-provider-clo v2.8.0. СВЕРЕНО ВЛАДЕЛЬЦЕМ 05.11.2026
# (решение П2a-8, deploy/README.md §4): sha256 архива = строка SHA256SUMS = zh, h1 linux_amd64 посчитан без сети из
# архива; совпало. Каждое обновление провайдера — та же сверка до prepare, шапку после неё правят заново.
# zh — архивы всех платформ из SHA256SUMS (без manifest.json); h1 — только linux_amd64, посчитан по содержимому
# архива, чей sha256 совпал с SHA256SUMS (без опоры на реестр; совпал и с h1 реестра). Подпись SHA256SUMS не
# проверена: реестр ключей провайдера не публикует. Одна платформа — как у `providers lock -platform=linux_amd64`.

provider "registry.opentofu.org/clo-ru/clo" {
  version     = "2.8.0"
  constraints = "2.8.0"
  hashes = [
    "h1:Yzesb2ilJp7p0Kf6Lc2gsA9dLtU3Mr2T3/RtQVJJeoU=",
    "zh:0853d31d25725aaeee55d7bd44302f20d0163fedd5354ea0c96dcd87b96d6d29",
    "zh:0d093f741e2b589ac73d04bec48e880804453b0395c2d24c4a580a198d4e35f8",
    "zh:14a16f1c2230108f9d229f3a5c22fd60d0ae8114df9f1f11063af2d104673ec9",
    "zh:189625ede311f4bafc4ea5592749d323317c02eab7702c41e806267820c4c537",
    "zh:29db4abf73fcebfcfcdf82a5e846b748540a873cce2b3d8f4c07a457302d3ea9",
    "zh:397b11ffd1e392a666f85c5eac8347146e97438342b3b88236670e6606d7c2ab",
    "zh:5394164f7954257f30770667254166e589255a35f7e431b424c7476fbe88b413",
    "zh:64137c63b50d780c7d6596f3f5255c2c05052fb57733c43bfcf9a4fab41728c4",
    "zh:97905f691300fc842ce9136598f1bb050e9831c324a4676982337e58b5d41e6d",
    "zh:9b112721ef00d0a2995c0b9bc4dad6b0d2aa3d7968a8fdd8eb7c39e1b5dcf54a",
    "zh:b4d642a25890ff2ced5bdc35852c9d3037b79f275ca6cf0282246cb661a47017",
    "zh:bf163884f5e44564ecb313c89e0d3da08c5e3351aa413eb4465b056656a15f86",
    "zh:ee1ab148599a332bc3c27b13c9e2fbeff9d5752eee209de74b3ecb5541cb3d3f",
    "zh:fe514eddf8151736f57837bad01960c7e6889df27202e9076dc5270f6cde8f09",
  ]
}
