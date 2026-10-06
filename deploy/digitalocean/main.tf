locals {
  bucket = "${var.name}-backups-${random_id.bucket.hex}"
}

resource "random_id" "bucket" {
  byte_length = 4
}

resource "digitalocean_spaces_bucket" "backups" {
  name   = local.bucket
  region = var.region
  acl    = "private"

  lifecycle_rule {
    id      = "fs-daily"
    enabled = true
    prefix  = "dolt-fs/daily/"
    expiration { days = 14 }
  }
  lifecycle_rule {
    id      = "fs-weekly"
    enabled = true
    prefix  = "dolt-fs/weekly/"
    expiration { days = 56 }
  }
  lifecycle_rule {
    id      = "fs-monthly"
    enabled = true
    prefix  = "dolt-fs/monthly/"
    expiration { days = 186 }
  }
  lifecycle_rule {
    id      = "dumps"
    enabled = true
    prefix  = "dumps/"
    expiration { days = 30 }
  }
  lifecycle_rule {
    id      = "checks"
    enabled = true
    prefix  = "checks/"
    expiration { days = 7 }
  }
  lifecycle_rule {
    id                                     = "incomplete-uploads"
    enabled                                = true
    abort_incomplete_multipart_upload_days = 7
  }
}

# A key scoped to this one bucket. It lives on the server, so a compromised
# server can reach its own backups but no other bucket.
resource "digitalocean_spaces_key" "backups" {
  name = "${var.name}-backups"
  grant {
    bucket     = digitalocean_spaces_bucket.backups.name
    permission = "readwrite"
  }
}

module "server" {
  source               = "../modules/server"
  admin_user           = var.admin_user
  admin_ssh_public_key = var.admin_ssh_public_key
  backup_remote        = "backup:${local.bucket}"
  backup_env = {
    RCLONE_CONFIG_BACKUP_TYPE              = "s3"
    RCLONE_CONFIG_BACKUP_PROVIDER          = "DigitalOcean"
    RCLONE_CONFIG_BACKUP_ENDPOINT          = "${var.region}.digitaloceanspaces.com"
    RCLONE_CONFIG_BACKUP_ACL               = "private"
    RCLONE_CONFIG_BACKUP_ACCESS_KEY_ID     = digitalocean_spaces_key.backups.access_key
    RCLONE_CONFIG_BACKUP_SECRET_ACCESS_KEY = digitalocean_spaces_key.backups.secret_key
  }
}

resource "digitalocean_droplet" "server" {
  name       = var.name
  region     = var.region
  size       = var.size
  image      = var.image
  ipv6       = true
  monitoring = true
  backups    = var.droplet_backups
  user_data  = module.server.cloud_init
  tags       = [var.name, "beads"]

  # The server holds the data: never replace it because the image moved on
  # or cloud-init changed. Rebuild deliberately (tofu apply -replace=...).
  lifecycle {
    ignore_changes = [image, user_data]
  }
}

resource "digitalocean_firewall" "server" {
  name        = var.name
  droplet_ids = [digitalocean_droplet.server.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.ssh_allowed_cidrs
  }
  inbound_rule {
    protocol         = "icmp"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}
