# Building a beads server

Pick one:

| | What it creates |
|---|---|
| [`digitalocean/`](digitalocean) | Droplet, cloud firewall (22 only), Spaces bucket with a bucket-scoped key |
| [`gcp/`](gcp) | Compute Engine VM (Shielded VM, static IP), firewall rule, GCS bucket, a service account that can only create backup objects |
| [`aws/`](aws) | EC2 instance (IMDSv2, encrypted gp3, Elastic IP), security group, S3 bucket (TLS-only, private, encrypted), an instance role that can only write backups |
| [`bootstrap.sh`](bootstrap.sh) | Any other Ubuntu 22.04+ or Debian 12+ machine, run by hand |

Every stack runs `bootstrap.sh` through cloud-init, so the result is the same
server everywhere: Dolt on 127.0.0.1:3306, sshd keys-only, a host firewall,
unattended security upgrades, and nightly backups to the bucket (a filesystem
archive at 03:15 UTC that keeps Dolt history, a SQL dump per database at 06:00
UTC). Bucket lifecycle rules keep archives 14 days daily, 8 weeks weekly and
6 months monthly, and dumps 30 days.

## The host key is generated before the server exists

OpenTofu creates the server's ED25519 host key and installs it through
cloud-init, so `host_key_fingerprint` is known from your own state, not
learned over the network on first connect. The cost is that the host private
key is in state. So state is encrypted, with `enforced = true`, and every
stack needs a passphrase:

```sh
export TF_VAR_state_passphrase='…'    # 16+ characters; keep it in a password manager
```

Lose the passphrase and you lose the ability to manage the stack (the server
keeps running). For a team, put state in a remote backend as well; encryption
applies there too.

State is not the only copy. The cloud-init document carries the host private
key, and on DigitalOcean the Spaces key too, and every cloud keeps user data
for the life of the machine:

| Who can read it | DigitalOcean | GCP | AWS |
|---|---|---|---|
| Processes on the server, via the metadata endpoint | any local process | any local process | any local process (IMDSv2) |
| Cloud users without a shell on it | team members | anyone with `compute.instances.get` (Compute Viewer) | anyone with `ec2:DescribeInstanceAttribute` |

On the server, Dolt is confined to loopback by its systemd unit, so a
database login cannot reach the endpoint; developer keys can forward only to
port 3306. Keep read access to the cloud project as narrow as access to state.

## Usage

```sh
cd deploy/digitalocean            # or gcp, aws
cp terraform.tfvars.example terraform.tfvars   # admin_user, admin key, region …
tofu init
tofu apply
tofu output remote_yaml           # paste into .beads/remote.yaml, then add database/port/prefix
```

Give cloud-init a few minutes; progress is in `/var/log/beads-bootstrap.log` on
the server. Then, from a repository:

```sh
beads-remote server check         # every step should be ok except "missing" for the new database
beads-remote server provision
```

Credentials for each cloud come from its usual environment: `DIGITALOCEAN_TOKEN`
plus `SPACES_ACCESS_KEY_ID`/`SPACES_SECRET_ACCESS_KEY` (a Spaces key allowed to
create buckets), Application Default Credentials for GCP (with the Compute and
Storage APIs enabled), and the standard AWS chain.

## Things that are deliberate

- **The server is never replaced by accident.** Image and cloud-init changes
  are ignored after creation, GCP has deletion protection and AWS termination
  protection. To rebuild, restore from backup onto a new server on purpose.
  To tear down, turn protection off first.
- **The server can write backups but not read or delete them** (GCP, AWS). A
  compromised server cannot erase its history: on GCP it cannot overwrite an
  object, and on AWS the bucket is versioned, so an overwritten backup keeps
  its original for the same retention. DigitalOcean's Spaces keys
  cannot be write-only, so there the key is limited to the one bucket.
- **Port 22 is open to the world by default** because developers move around.
  Keys are the only way in. If your team has fixed addresses, set
  `ssh_allowed_cidrs`.

## Restoring

```sh
# Latest SQL dump of one database (current rows; no Dolt history):
rclone copy backup:<bucket>/dumps/<db>/<file>.sql.gz . && gunzip <file>.sql.gz
dolt sql < <file>.sql                       # into a database of that name

# Whole server, with history, onto a freshly bootstrapped machine:
systemctl stop dolt
tar -C /var/lib -xzf dolt-fs-<stamp>.tar.gz
chown -R dolt:dolt /var/lib/dolt && systemctl start dolt
```

Reading backups needs credentials the server does not have, by design; use
your own cloud login.
