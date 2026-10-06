output "ip" {
  description = "The server's static public IPv4 address."
  value       = google_compute_address.server.address
}

output "host_key_fingerprint" {
  description = "Pinned in remote.yaml as server.host_key."
  value       = module.server.host_key_fingerprint
}

output "backup_bucket" {
  value = local.bucket
}

output "remote_yaml" {
  description = "The server block for .beads/remote.yaml (add database, port and prefix per repository)."
  value       = <<-YAML
    server:
      host: ${google_compute_address.server.address}
      host_key: ${module.server.host_key_fingerprint}
      admin: ${var.admin_user}@${google_compute_address.server.address}
  YAML
}
