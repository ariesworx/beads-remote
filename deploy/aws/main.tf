locals {
  bucket    = "${var.name}-backups-${random_id.bucket.hex}"
  subnet_id = var.subnet_id != "" ? var.subnet_id : sort(data.aws_subnets.default[0].ids)[0]
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

# ── Network ────────────────────────────────────────────────────────────────
data "aws_vpc" "default" {
  count   = var.subnet_id == "" ? 1 : 0
  default = true
}

data "aws_subnets" "default" {
  count = var.subnet_id == "" ? 1 : 0
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default[0].id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_subnet" "server" {
  id = local.subnet_id
}

resource "aws_security_group" "server" {
  name        = var.name
  description = "beads server: ssh in, anything out"
  vpc_id      = data.aws_subnet.server.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "ssh" {
  for_each          = toset(var.ssh_allowed_cidrs)
  security_group_id = aws_security_group.server.id
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_ipv4         = strcontains(each.value, ":") ? null : each.value
  cidr_ipv6         = strcontains(each.value, ":") ? each.value : null
}

resource "aws_vpc_security_group_egress_rule" "all_v4" {
  security_group_id = aws_security_group.server.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "all_v6" {
  security_group_id = aws_security_group.server.id
  ip_protocol       = "-1"
  cidr_ipv6         = "::/0"
}

# ── Backups ────────────────────────────────────────────────────────────────
resource "aws_s3_bucket" "backups" {
  bucket = local.bucket
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  dynamic "rule" {
    for_each = local.retention
    content {
      id     = trimsuffix(replace(rule.key, "/", "-"), "-")
      status = "Enabled"
      filter {
        prefix = rule.key
      }
      expiration {
        days = rule.value
      }
    }
  }

  rule {
    id     = "incomplete-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

resource "aws_s3_bucket_policy" "backups" {
  bucket = aws_s3_bucket.backups.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.backups.arn, "${aws_s3_bucket.backups.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
  depends_on = [aws_s3_bucket_public_access_block.backups]
}

# The instance's identity. It can write backups and nothing else: no
# reading, listing or deleting them.
resource "aws_iam_role" "server" {
  name = "${var.name}-server"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "backup_writer" {
  name = "backup-writer"
  role = aws_iam_role.server.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:PutObject", "s3:AbortMultipartUpload"]
      Resource = "${aws_s3_bucket.backups.arn}/*"
    }]
  })
}

resource "aws_iam_instance_profile" "server" {
  name = "${var.name}-server"
  role = aws_iam_role.server.name
}

# ── Server ─────────────────────────────────────────────────────────────────
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-${var.architecture}-server-*"]
  }
}

module "server" {
  source               = "../modules/server"
  admin_user           = var.admin_user
  admin_ssh_public_key = var.admin_ssh_public_key
  backup_remote        = "backup:${local.bucket}"
  backup_env = {
    RCLONE_CONFIG_BACKUP_TYPE     = "s3"
    RCLONE_CONFIG_BACKUP_PROVIDER = "AWS"
    RCLONE_CONFIG_BACKUP_ENV_AUTH = "true"
    RCLONE_CONFIG_BACKUP_REGION   = var.region
    RCLONE_CONFIG_BACKUP_ACL      = "bucket-owner-full-control"
  }
}

resource "aws_instance" "server" {
  ami                     = data.aws_ami.ubuntu.id
  instance_type           = var.instance_type
  subnet_id               = local.subnet_id
  vpc_security_group_ids  = [aws_security_group.server.id]
  iam_instance_profile    = aws_iam_instance_profile.server.name
  user_data               = module.server.cloud_init
  disable_api_termination = true

  metadata_options {
    http_tokens                 = "required" # IMDSv2 only
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.volume_size
    encrypted   = true
  }

  # The server holds the data: never replace it because a newer AMI appeared
  # or cloud-init changed. Rebuild deliberately (tofu apply -replace=...).
  lifecycle {
    ignore_changes = [ami, user_data]
  }
}

resource "aws_eip" "server" {
  instance = aws_instance.server.id
  domain   = "vpc"
}
