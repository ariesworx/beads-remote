#!/usr/bin/env bash
# shellcheck disable=SC1091  # sources files this script writes
# Turns a fresh Ubuntu (22.04+) or Debian (12+) machine into a beads server:
# Dolt on 127.0.0.1:3306, sshd hardened to keys only, ufw allowing 22 only,
# and nightly backups. Idempotent: re-running repairs drift and changes
# nothing that is already right.
#
# The OpenTofu in deploy/{digitalocean,gcp,aws} runs this from cloud-init. By
# hand, as root:
#
#   ADMIN_USER=alice BACKUP_REMOTE=backup:my-bucket/beads ./bootstrap.sh
#
# ADMIN_USER     the account you ssh in as to administer the server. It must
#                already exist with a key in ~/.ssh/authorized_keys, or this
#                script refuses (it is about to turn passwords off).
# BACKUP_REMOTE  rclone destination for backups, e.g. backup:bucket/prefix.
#                The remote is configured by RCLONE_CONFIG_* variables in
#                /etc/beads/backup.env. Empty: backups stay on this machine.
#
# Afterwards, from a repository: beads-remote server provision
set -euo pipefail

DOLT_VERSION=2.4.2
declare -A DOLT_SHA256=(
  [amd64]=5d95a271149dc6ea97f47157af7ba9a0df5e8d4e9bbac4d79c169f2faa3cfb9e
  [arm64]=c1c610e22c6bacd4c5b2fbf5697a0f453d759d075c4abff380c6e646553e22d8
)
DATA=/var/lib/dolt
ETC=/etc/beads
SSHD_FIRST=/etc/ssh/sshd_config.d/00-beads-hardening.conf
SSHD_USERS=/etc/ssh/sshd_config.d/99-beads-hardening.conf

ADMIN_USER=${ADMIN_USER:-}
BACKUP_REMOTE=${BACKUP_REMOTE:-}

say() { printf '\033[32m✓\033[0m %s\n' "$*"; }
die() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
[[ $ADMIN_USER =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "set ADMIN_USER to the account you administer this server as"
[ -z "$BACKUP_REMOTE" ] || [[ $BACKUP_REMOTE =~ ^[A-Za-z0-9_-]+:[A-Za-z0-9._/-]*$ ]] || die "BACKUP_REMOTE must look like remote:bucket/prefix"
id "$ADMIN_USER" >/dev/null 2>&1 || die "account $ADMIN_USER does not exist"
admin_home=$(getent passwd "$ADMIN_USER" | cut -d: -f6)
grep -Eq '^(ssh-|sk-|ecdsa-)' "$admin_home/.ssh/authorized_keys" 2>/dev/null ||
  die "$ADMIN_USER has no key in $admin_home/.ssh/authorized_keys; add one before passwords are turned off"
# The admin runs `sudo -n bash -s` over ssh; without passwordless sudo every
# beads-remote server command fails.
sudo -n -u "$ADMIN_USER" sudo -n true 2>/dev/null || die "$ADMIN_USER needs passwordless sudo"
umask 022
# Dolt reads .dolt/ in the working directory, and the service account may not
# be able to enter the one this was started from.
cd /

# ── Packages ────────────────────────────────────────────────────────────────
export DEBIAN_FRONTEND=noninteractive
need=()
for p in python3-pymysql ufw rclone unattended-upgrades curl ca-certificates; do
  dpkg -s "$p" >/dev/null 2>&1 || need+=("$p")
done
if [ ${#need[@]} -gt 0 ]; then
  apt-get update -q
  apt-get install -yq --no-install-recommends "${need[@]}"
fi
say "packages"

# ── Dolt ────────────────────────────────────────────────────────────────────
arch=$(dpkg --print-architecture)
[ -n "${DOLT_SHA256[$arch]:-}" ] || die "no Dolt build pinned for $arch"
if [ "$(/usr/local/bin/dolt version 2>/dev/null | awk 'NR==1 { print $3 }')" != "$DOLT_VERSION" ]; then
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/dolt.tar.gz" "https://github.com/dolthub/dolt/releases/download/v$DOLT_VERSION/dolt-linux-$arch.tar.gz"
  echo "${DOLT_SHA256[$arch]}  $tmp/dolt.tar.gz" | sha256sum -c --quiet - || die "Dolt download does not match its pinned checksum"
  tar -xzf "$tmp/dolt.tar.gz" -C "$tmp"
  install -m 0755 "$tmp/dolt-linux-$arch/bin/dolt" /usr/local/bin/dolt
  rm -rf "$tmp"
fi
say "dolt $DOLT_VERSION"

id dolt >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin dolt
install -d -o dolt -g dolt -m 0700 "$DATA" "$DATA/data" "$DATA/cfg"
install -d -m 0755 /etc/dolt
cat > /etc/dolt/config.yaml <<CONF
# Written by beads-remote deploy/bootstrap.sh. Loopback only: developers
# reach it through the SSH tunnel, never directly.
log_level: info
behavior:
  autocommit: true
listener:
  host: 127.0.0.1
  port: 3306
data_dir: $DATA/data
cfg_dir: $DATA/cfg
privilege_file: $DATA/cfg/privileges.db
branch_control_file: $DATA/cfg/branch_control.db
CONF
# Dolt's metrics are opt-out. Run from its own home: Dolt looks for
# databases in the working directory.
(
  cd "$DATA"
  dc() { sudo -u dolt HOME="$DATA" dolt config --global "$@"; }
  dc --add metrics.disabled true >/dev/null 2>&1 || true
  dc --get user.name >/dev/null 2>&1 || dc --add user.name beads-server >/dev/null
  dc --get user.email >/dev/null 2>&1 || dc --add user.email beads-server@localhost >/dev/null
)

cat > /etc/systemd/system/dolt.service <<'UNIT'
[Unit]
Description=Dolt SQL server for beads
After=network.target

[Service]
User=dolt
Group=dolt
Environment=HOME=/var/lib/dolt
WorkingDirectory=/var/lib/dolt/data
ExecStart=/usr/local/bin/dolt sql-server --config /etc/dolt/config.yaml
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/dolt
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now dolt >/dev/null 2>&1 || systemctl enable --now dolt
systemctl restart dolt
for _ in $(seq 60); do
  python3 -c 'import socket; socket.create_connection(("127.0.0.1", 3306), 1)' 2>/dev/null && break
  sleep 1
done
say "dolt.service on 127.0.0.1:3306"

# ── Database logins ─────────────────────────────────────────────────────────
# A fresh Dolt has root@localhost with no password, and every tunnel arrives
# from localhost. Give root a password nobody uses, and create the admin
# login beads-remote uses (paths.admin_user / admin_password_file).
install -d -o root -g root -m 0700 "$ETC"
for f in root-password db-password; do
  [ -s "$ETC/$f" ] || (umask 077; head -c 48 /dev/urandom | base64 | tr -d '\n=+/' | cut -c1-32 > "$ETC/$f")
  chmod 0600 "$ETC/$f"
done
python3 - "$ETC/root-password" "$ETC/db-password" <<'PY'
import pymysql, sys
root_pw, admin_pw = (open(p).read().strip() for p in sys.argv[1:3])

def connect(pw):
    return pymysql.connect(host="127.0.0.1", port=3306, user="root", password=pw, autocommit=True)

try:
    c = connect(root_pw)
except pymysql.err.OperationalError:
    c = connect("")  # first run: Dolt's passwordless root
cur = c.cursor()
cur.execute("ALTER USER `root`@`localhost` IDENTIFIED BY %s", (root_pw,))
cur.execute("CREATE USER IF NOT EXISTS `beads`@`localhost` IDENTIFIED BY %s", (admin_pw,))
cur.execute("ALTER USER `beads`@`localhost` IDENTIFIED BY %s", (admin_pw,))
cur.execute("GRANT ALL ON *.* TO `beads`@`localhost` WITH GRANT OPTION")
try:
    connect("")
    sys.exit("root still logs in without a password")
except pymysql.err.OperationalError:
    pass
PY
say "root locked; admin login beads (password in $ETC/db-password)"

# ── sshd ────────────────────────────────────────────────────────────────────
# sshd keeps the first value it reads and reads drop-ins in name order, so the
# hardening goes in 00- to beat images that ship 50-cloud-init.conf with
# passwords on. AllowUsers sits alone in 99-, which beads-remote server
# provision appends each database account to.
cat > "$SSHD_FIRST" <<'CONF'
# Written by beads-remote deploy/bootstrap.sh.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AuthenticationMethods publickey
MaxAuthTries 3
LoginGraceTime 30
X11Forwarding no
AllowAgentForwarding no
AllowStreamLocalForwarding no
PermitTunnel no
GatewayPorts no
# Developers forward one port; each key's permitopen limits where to.
AllowTcpForwarding local
CONF
if [ -f "$SSHD_USERS" ] && grep -q '^AllowUsers' "$SSHD_USERS"; then
  grep -Eq "^AllowUsers(.*[[:space:]])?$ADMIN_USER([[:space:]]|\$)" "$SSHD_USERS" ||
    sed -i -E "s/^(AllowUsers)/\\1 $ADMIN_USER/" "$SSHD_USERS"
else
  printf '# Written by beads-remote; server provision adds database accounts.\nAllowUsers %s\n' "$ADMIN_USER" > "$SSHD_USERS"
fi
install -d -m 0755 /run/sshd   # sshd -t needs it; absent when sshd has not run yet
sshd -t || die "sshd rejected the configuration; it was not reloaded"
eff=$(sshd -T -C user="$ADMIN_USER",host=localhost,addr=127.0.0.1)
for want in "passwordauthentication no" "permitrootlogin no" "kbdinteractiveauthentication no"; do
  grep -qx "$want" <<<"$eff" || die "sshd would still use '$(grep "^${want% *} " <<<"$eff")'; another drop-in overrides $SSHD_FIRST"
done
systemctl reload ssh 2>/dev/null || systemctl reload sshd
say "sshd: keys only, AllowUsers $(sed -n 's/^AllowUsers //p' "$SSHD_USERS")"

# ── Firewall ────────────────────────────────────────────────────────────────
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw limit 22/tcp >/dev/null
ufw --force enable >/dev/null
say "ufw: 22/tcp only (rate limited)"

# ── Updates ─────────────────────────────────────────────────────────────────
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'CONF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
CONF
say "unattended security upgrades"

# ── Backups ─────────────────────────────────────────────────────────────────
# Two layers, as the server runbook describes: a filesystem archive that keeps
# Dolt's commit history, and a per-database SQL dump of the current rows. Both
# enumerate every database, so new projects are covered without edits.
[ -f "$ETC/backup.env" ] || install -m 0600 /dev/null "$ETC/backup.env"
printf 'BACKUP_REMOTE=%s\n' "$BACKUP_REMOTE" > "$ETC/backup.conf"
install -d -m 0700 /var/backups/dolt
cat > /usr/local/sbin/dolt-backup.sh <<'SCRIPT'
#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2012  # sources its own config; names are ours
# Backs up every Dolt database. Written by beads-remote deploy/bootstrap.sh.
#   dolt-backup.sh fs     archive of /var/lib/dolt (stops Dolt briefly)
#   dolt-backup.sh dump   gzipped SQL per database (Dolt keeps running)
# Uploads to $BACKUP_REMOTE with rclone; the bucket's lifecycle rules expire
# old copies. The upload credentials may only write (no list, read or
# delete), so rclone is told not to look at the destination first. Two local copies of each kind are kept in /var/backups/dolt.
set -euo pipefail
umask 077
. /etc/beads/backup.conf
set -a; . /etc/beads/backup.env; set +a
out=/var/backups/dolt
stamp=$(date -u +%Y%m%dT%H%M%SZ)

upload() { # file, remote dir
  [ -n "$BACKUP_REMOTE" ] || return 0
  rclone copyto --s3-no-check-bucket --no-check-dest "$1" "$BACKUP_REMOTE/$2/$(basename "$1")"
}
prune() { ls -1t "$out"/"$1"* 2>/dev/null | tail -n +3 | xargs -r rm -f; }

case "${1:-}" in
  fs)
    f="$out/dolt-fs-$stamp.tar.gz"
    systemctl stop dolt
    trap 'systemctl start dolt' EXIT
    tar -C /var/lib -czf "$f" dolt
    systemctl start dolt; trap - EXIT
    kind=daily
    [ "$(date -u +%u)" = 7 ] && kind=weekly
    [ "$(date -u +%d)" = 01 ] && kind=monthly
    upload "$f" "dolt-fs/$kind"
    prune dolt-fs-
    ;;
  dump)
    # dolt dump runs as the dolt user, so it writes into a directory of its
    # own; root then takes the file over.
    stage=$(mktemp -d); chown dolt:dolt "$stage"
    trap 'rm -rf "$stage"' EXIT
    for d in /var/lib/dolt/data/*/; do
      db=$(basename "$d")
      [ -d "$d/.dolt" ] || continue
      f="$out/$db-$stamp.sql"
      (cd "$d" && sudo -u dolt HOME=/var/lib/dolt dolt dump -r sql -fn "$stage/$db.sql" >/dev/null)
      mv "$stage/$db.sql" "$f"; chown root:root "$f"; chmod 0600 "$f"
      gzip -f "$f"
      upload "$f.gz" "dumps/$db"
      prune "$db-"
    done
    ;;
  *) echo "usage: dolt-backup.sh fs|dump" >&2; exit 2 ;;
esac
SCRIPT
chmod 0755 /usr/local/sbin/dolt-backup.sh
for kind in fs dump; do
  when='*-*-* 03:15:00 UTC'; [ "$kind" = dump ] && when='*-*-* 06:00:00 UTC'
  cat > "/etc/systemd/system/dolt-backup-$kind.service" <<UNIT
[Unit]
Description=Dolt backup ($kind)
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/dolt-backup.sh $kind
UNIT
  cat > "/etc/systemd/system/dolt-backup-$kind.timer" <<UNIT
[Unit]
Description=Dolt backup ($kind), daily
[Timer]
OnCalendar=$when
RandomizedDelaySec=300
Persistent=true
[Install]
WantedBy=timers.target
UNIT
done
systemctl daemon-reload
systemctl enable --now dolt-backup-fs.timer dolt-backup-dump.timer >/dev/null 2>&1
if [ -n "$BACKUP_REMOTE" ]; then
  set -a; . "$ETC/backup.env"; set +a
  probe=$(mktemp); echo "beads-remote bootstrap $(date -u +%FT%TZ)" > "$probe"
  # A new name each run: write-only credentials cannot overwrite. The
  # bucket's lifecycle rules expire checks/ after a week.
  if rclone copyto --s3-no-check-bucket --no-check-dest "$probe" "$BACKUP_REMOTE/checks/bootstrap-$(date -u +%Y%m%dT%H%M%SZ).txt" 2>/dev/null; then
    say "backups: daily to $BACKUP_REMOTE"
  else
    printf '\033[33m!\033[0m backups: cannot write to %s; check /etc/beads/backup.env\n' "$BACKUP_REMOTE"
  fi
  rm -f "$probe"
else
  printf '\033[33m!\033[0m backups: local only (/var/backups/dolt); set BACKUP_REMOTE to send them off the machine\n'
fi

fpr=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')
cat <<DONE

Server ready. Host key (put this in .beads/remote.yaml as server.host_key,
after checking it matches what your cloud reported):

  $fpr

Next, from a repository:  beads-remote server provision
DONE
