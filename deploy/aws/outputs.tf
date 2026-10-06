output "ip" {
  description = "The server's Elastic IP."
  value       = aws_eip.server.public_ip
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
      host: ${aws_eip.server.public_ip}
      host_key: ${module.server.host_key_fingerprint}
      admin: ${var.admin_user}@${aws_eip.server.public_ip}
  YAML
}
