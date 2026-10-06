terraform {
  required_version = ">= 1.8"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # State holds the server's host private key and the backup key, so it is
  # encrypted and OpenTofu refuses to write it in the clear.
  encryption {
    key_provider "pbkdf2" "passphrase" {
      passphrase = var.state_passphrase
    }
    method "aes_gcm" "default" {
      keys = key_provider.pbkdf2.passphrase
    }
    state {
      method   = method.aes_gcm.default
      enforced = true
    }
    plan {
      method   = method.aes_gcm.default
      enforced = true
    }
  }
}

# Credentials come from Application Default Credentials
# (gcloud auth application-default login) or GOOGLE_APPLICATION_CREDENTIALS.
provider "google" {
  project = var.project
  region  = var.region
  zone    = var.zone
}
