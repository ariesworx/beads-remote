locals {
  bucket     = "${var.name}-backups-${random_id.bucket.hex}"
  ipv4_cidrs = [for c in var.ssh_allowed_cidrs : c if !strcontains(c, ":")]
  # prefix => days to keep
  retention = {
    "dolt-fs/daily/"   = 14
    "dolt-fs/weekly/"  = 56
    "dolt-fs/monthly/" = 186
    "dumps/"           = 30
    "checks/"          = 7
  }
}

resource "random_id" "bucket" {
  byte_length = 4
}

resource "google_storage_bucket" "backups" {
  name                        = local.bucket
  location                    = var.bucket_location
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  dynamic "lifecycle_rule" {
    for_each = local.retention
    content {
      condition {
        age            = lifecycle_rule.value
        matches_prefix = [lifecycle_rule.key]
      }
      action {
        type = "Delete"
      }
    }
  }
}

# The VM's identity. It can create objects in the backup bucket and nothing
# else: no reading, listing, overwriting or deleting backups.
resource "google_service_account" "server" {
  account_id   = "${var.name}-server"
  display_name = "beads server (${var.name})"
}

resource "google_storage_bucket_iam_member" "backup_writer" {
  bucket = google_storage_bucket.backups.name
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.server.email}"
}

module "server" {
  source               = "../modules/server"
  admin_user           = var.admin_user
  admin_ssh_public_key = var.admin_ssh_public_key
  backup_remote        = "backup:${local.bucket}"
  backup_env = {
    RCLONE_CONFIG_BACKUP_TYPE               = "google cloud storage"
    RCLONE_CONFIG_BACKUP_ENV_AUTH           = "true"
    RCLONE_CONFIG_BACKUP_BUCKET_POLICY_ONLY = "true"
  }
}

resource "google_compute_address" "server" {
  name = var.name
}

resource "google_compute_instance" "server" {
  name                      = var.name
  machine_type              = var.machine_type
  tags                      = ["${var.name}-ssh"]
  deletion_protection       = true
  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = var.image
      size  = 20
      type  = "pd-balanced"
    }
  }

  network_interface {
    network = var.network
    access_config {
      nat_ip = google_compute_address.server.address
    }
  }

  metadata = {
    user-data = module.server.cloud_init
    # Keys come from cloud-init only: no project-wide keys, no OS Login.
    block-project-ssh-keys = "TRUE"
    enable-oslogin         = "FALSE"
    serial-port-enable     = "FALSE"
  }

  service_account {
    email  = google_service_account.server.email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  # The server holds the data: never replace it because the image moved on
  # or cloud-init changed. Rebuild deliberately.
  lifecycle {
    ignore_changes = [boot_disk[0].initialize_params[0].image, metadata["user-data"]]
  }
}

resource "google_compute_firewall" "ssh" {
  name          = "${var.name}-ssh"
  network       = var.network
  direction     = "INGRESS"
  source_ranges = local.ipv4_cidrs
  target_tags   = ["${var.name}-ssh"]
  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}
