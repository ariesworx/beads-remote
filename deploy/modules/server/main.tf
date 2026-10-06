# Everything the three clouds share: the server's SSH host key and the
# cloud-init document that installs it and runs bootstrap.sh.
#
# The host key is generated here so its fingerprint is known before the
# machine exists and never has to be learned over the network. Its private
# half is therefore in state: the stacks that use this module enforce state
# encryption.

terraform {
  required_version = ">= 1.8"
  required_providers {
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

variable "admin_user" {
  description = "Account to create for administering the server (ssh + passwordless sudo)."
  type        = string
  validation {
    condition     = can(regex("^[a-z_][a-z0-9_-]{0,31}$", var.admin_user)) && !contains(["root", "dolt", "beads", "ubuntu", "admin"], var.admin_user)
    error_message = "admin_user must be a lowercase Unix account name, and not root, dolt, beads, ubuntu or admin."
  }
}

variable "admin_ssh_public_key" {
  description = "The admin's public key, one line (ssh-ed25519 …)."
  type        = string
  validation {
    condition     = can(regex("^(ssh-ed25519|sk-ssh-ed25519@openssh\\.com) [A-Za-z0-9+/]+=* ?[^\\n]*$", trimspace(var.admin_ssh_public_key)))
    error_message = "admin_ssh_public_key must be one ED25519 public key line."
  }
}

variable "backup_remote" {
  description = "rclone destination, e.g. backup:my-bucket. Empty keeps backups on the server."
  type        = string
  default     = ""
  validation {
    condition     = var.backup_remote == "" || can(regex("^[A-Za-z0-9_-]+:[A-Za-z0-9._/-]*$", var.backup_remote))
    error_message = "backup_remote must look like remote:bucket/prefix."
  }
}

variable "backup_env" {
  description = "RCLONE_CONFIG_BACKUP_* settings for the backup remote, written to /etc/beads/backup.env (0600)."
  type        = map(string)
  default     = {}
  sensitive   = true
}

resource "tls_private_key" "host" {
  algorithm = "ED25519"
}

locals {
  backup_env = join("", [for k in sort(keys(var.backup_env)) : "${k}=${var.backup_env[k]}\n"])
  cloud_init = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    admin_user       = var.admin_user
    admin_key        = trimspace(var.admin_ssh_public_key)
    host_private_key = tls_private_key.host.private_key_openssh
    host_public_key  = trimspace(tls_private_key.host.public_key_openssh)
    backup_remote    = var.backup_remote
    backup_env_b64   = base64encode(local.backup_env)
    bootstrap_gz_b64 = base64gzip(file("${path.module}/../../bootstrap.sh"))
  })
}

output "cloud_init" {
  description = "cloud-init user data for the server."
  value       = local.cloud_init
  sensitive   = true
}

output "host_key_fingerprint" {
  description = "SHA256 fingerprint of the server's ED25519 host key, for server.host_key."
  value       = tls_private_key.host.public_key_fingerprint_sha256
}

output "host_public_key" {
  description = "The server's ED25519 host public key."
  value       = trimspace(tls_private_key.host.public_key_openssh)
}
