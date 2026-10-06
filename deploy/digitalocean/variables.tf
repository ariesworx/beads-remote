variable "state_passphrase" {
  description = "Encrypts state and plans (16+ characters). Set TF_VAR_state_passphrase; keep it in a password manager."
  type        = string
  sensitive   = true
  validation {
    condition     = length(var.state_passphrase) >= 16
    error_message = "state_passphrase must be at least 16 characters."
  }
}

variable "name" {
  description = "Name for the droplet, firewall and bucket prefix."
  type        = string
  default     = "beads"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,40}$", var.name))
    error_message = "name: lowercase letters, digits and hyphens."
  }
}

variable "region" {
  description = "Droplet and Spaces region (one with Spaces, e.g. nyc3, sfo3, ams3, fra1, sgp1, syd1)."
  type        = string
  default     = "nyc3"
}

variable "size" {
  description = "Droplet size. 2 GB is comfortable for a handful of projects."
  type        = string
  default     = "s-1vcpu-2gb"
}

variable "image" {
  description = "Ubuntu LTS image slug."
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "admin_user" {
  description = "Admin account created on the server (ssh + passwordless sudo); server.admin in remote.yaml."
  type        = string
}

variable "admin_ssh_public_key" {
  description = "The admin's ED25519 public key, one line."
  type        = string
}

variable "ssh_allowed_cidrs" {
  description = "Addresses allowed to reach port 22. Narrow this if your team has fixed egress addresses."
  type        = list(string)
  default     = ["0.0.0.0/0", "::/0"]
}

variable "droplet_backups" {
  description = "Also enable DigitalOcean's weekly droplet backups (extra cost)."
  type        = bool
  default     = false
}
