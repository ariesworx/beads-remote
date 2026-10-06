terraform {
  required_version = ">= 1.8"
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.60"
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

# DIGITALOCEAN_TOKEN, plus SPACES_ACCESS_KEY_ID and SPACES_SECRET_ACCESS_KEY
# (a Spaces key that can create buckets), come from the environment.
provider "digitalocean" {}
