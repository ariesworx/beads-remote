# tofu test: renders cloud-init with a real (local-only) host key and checks
# that the document is valid YAML carrying what bootstrap needs.

variables {
  admin_user           = "alice"
  admin_ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl alice@laptop"
  backup_remote        = "backup:example-bucket"
  backup_env = {
    RCLONE_CONFIG_BACKUP_TYPE = "s3"
  }
}

run "renders" {
  command = apply

  assert {
    condition     = startswith(output.cloud_init, "#cloud-config\n")
    error_message = "cloud-init must start with #cloud-config"
  }
  assert {
    condition     = yamldecode(output.cloud_init).users[0].name == "alice"
    error_message = "admin user missing"
  }
  assert {
    condition     = yamldecode(output.cloud_init).users[0].ssh_authorized_keys[0] == var.admin_ssh_public_key
    error_message = "admin key missing"
  }
  assert {
    condition     = yamldecode(output.cloud_init).ssh_pwauth == false && yamldecode(output.cloud_init).disable_root == true
    error_message = "password login or root login left on"
  }
  assert {
    condition     = trimspace(yamldecode(output.cloud_init).ssh_keys.ed25519_private) == trimspace(tls_private_key.host.private_key_openssh)
    error_message = "host private key mangled by indentation"
  }
  assert {
    condition     = yamldecode(output.cloud_init).ssh_keys.ed25519_public == output.host_public_key
    error_message = "host public key missing"
  }
  assert {
    condition     = startswith(output.host_key_fingerprint, "SHA256:") && length(output.host_key_fingerprint) == 50
    error_message = "fingerprint is not SHA256:<43 base64 chars>"
  }
  assert {
    condition     = base64decode([for f in yamldecode(output.cloud_init).write_files : f.content if f.path == "/root/beads-bootstrap.sh"][0]) == file("${path.module}/../../bootstrap.sh")
    error_message = "bootstrap.sh not embedded verbatim"
  }
  assert {
    condition     = base64decode([for f in yamldecode(output.cloud_init).write_files : f.content if f.path == "/etc/beads/backup.env"][0]) == "RCLONE_CONFIG_BACKUP_TYPE=s3\n"
    error_message = "backup.env not rendered"
  }
  assert {
    condition     = [for f in yamldecode(output.cloud_init).write_files : f.permissions if f.path == "/etc/beads/backup.env"][0] == "0600"
    error_message = "backup.env must be 0600"
  }
}

run "rejects_a_non_ed25519_admin_key" {
  command = plan
  variables {
    admin_ssh_public_key = "ssh-rsa AAAAB3NzaC1yc2E alice@laptop"
  }
  expect_failures = [var.admin_ssh_public_key]
}

run "rejects_root_as_admin" {
  command = plan
  variables {
    admin_user = "root"
  }
  expect_failures = [var.admin_user]
}
