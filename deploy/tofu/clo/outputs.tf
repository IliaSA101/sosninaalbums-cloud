output "ipv4" {
  value = clo_network_ip.main.address
}

output "server_id" {
  value = clo_compute_instance.srv.id
}
