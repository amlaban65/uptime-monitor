output "ipv4_address" {
  value = hcloud_server.app.ipv4_address
}

output "ipv6_address" {
  value = hcloud_server.app.ipv6_address
}

output "status_page_url" {
  value = var.domain != "" ? "https://${var.domain}" : "http://${hcloud_server.app.ipv4_address}"
}

output "ssh_command" {
  value = "ssh root@${hcloud_server.app.ipv4_address}"
}

output "admin_token_hint" {
  description = "The admin token is generated on the server and never stored in Terraform state."
  value       = "ssh root@${hcloud_server.app.ipv4_address} grep ADMIN_TOKEN /opt/uptime/secrets.env"
}
