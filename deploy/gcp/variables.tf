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
  description = "Name for the VM, firewall, service account and bucket prefix."
  type        = string
  default     = "beads"
  validation {
    # "<name>-server" is the service account ID, which GCP caps at 30.
    condition     = can(regex("^[a-z][a-z0-9-]{1,21}$", var.name))
    error_message = "name: 2 to 22 lowercase letters, digits and hyphens."
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

variable "project" {
  description = "Google Cloud project ID."
  type        = string
}

variable "region" {
  description = "Region for the address and bucket."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "Zone for the VM."
  type        = string
  default     = "us-central1-a"
}

variable "machine_type" {
  description = "VM size. e2-small (2 GB) is comfortable for a handful of projects."
  type        = string
  default     = "e2-small"
}

variable "image" {
  description = "Boot image (Ubuntu LTS)."
  type        = string
  default     = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
}

variable "network" {
  description = "VPC network for the VM."
  type        = string
  default     = "default"
}

variable "bucket_location" {
  description = "Backup bucket location; a different region from the VM survives a regional outage."
  type        = string
  default     = "US"
}
