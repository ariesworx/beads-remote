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

variable "region" {
  description = "AWS region."
  type        = string
  default     = "us-east-1"
}

variable "instance_type" {
  description = "Instance size. t4g.small (Graviton, 2 GB) is comfortable for a handful of projects."
  type        = string
  default     = "t4g.small"
}

variable "architecture" {
  description = "arm64 for Graviton (t4g, m7g …) or amd64 for x86 (t3, m7i …). Must match instance_type."
  type        = string
  default     = "arm64"
  validation {
    condition     = contains(["arm64", "amd64"], var.architecture)
    error_message = "architecture is arm64 or amd64."
  }
}

variable "subnet_id" {
  description = "Public subnet for the instance. Empty uses a default-VPC subnet."
  type        = string
  default     = ""
}

variable "volume_size" {
  description = "Root volume size in GB."
  type        = number
  default     = 20
}
