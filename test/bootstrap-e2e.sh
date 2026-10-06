#!/usr/bin/env bash
# End-to-end test of deploy/bootstrap.sh on this machine, then of
# beads-remote against the server it built, using every default path.
#
# Needs root on a disposable Ubuntu 24.04+ machine or container without
# systemd running (stubs stand in for systemctl and ufw): it installs packages,
# writes sshd drop-ins and creates accounts.
#
#   sudo test/bootstrap-e2e.sh ./beads-remote
set -euo pipefail

BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
HERE=$(cd "$(dirname "$0")/.." && pwd)
[ "$(id -u)" = 0 ] || { echo "run as root on a disposable machine" >&2; exit 2; }
for u in bsadmin bstest dolt; do ! id "$u" >/dev/null 2>&1 || { echo "account $u exists; use a fresh machine" >&2; exit 2; }; done
for p in 3306 2223; do ! (exec 3<>/dev/tcp/127.0.0.1/$p) 2>/dev/null || { echo "port $p is in use" >&2; exit 2; }; done

ADMIN=bsadmin DB=bstest SSHPORT=2223 LPORT=3398
WORK=$(mktemp -d)
pass=0 fail=0
ok() { echo "ok   $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }
expect() { local n=$1; shift; if "$@" >/dev/null 2>&1; then ok "$n"; else bad "$n"; fi; }
refuse() { local n=$1; shift; if "$@" >/dev/null 2>&1; then bad "$n"; else ok "$n"; fi; }

cleanup() {
  set +e
  [ -n "${SSH_AGENT_PID:-}" ] && kill "$SSH_AGENT_PID"
  [ -f "$WORK/sshd.pid" ] && kill "$(cat "$WORK/sshd.pid")"
  [ -f "$WORK/dolt.pid" ] && kill "$(cat "$WORK/dolt.pid")"
  for u in "$DB" "$ADMIN" dolt; do
    id "$u" >/dev/null 2>&1 || continue
    for p in $(pgrep -u "$u"); do kill "$p"; done
  done
  sleep 1
  userdel -r "$DB" 2>/dev/null; userdel -r "$ADMIN" 2>/dev/null; userdel dolt 2>/dev/null
  rm -rf /var/lib/dolt /etc/dolt /etc/beads /var/backups/dolt /usr/local/sbin/dolt-backup.sh \
    /etc/systemd/system/dolt*.service /etc/systemd/system/dolt-backup-*.timer \
    /etc/ssh/sshd_config.d/00-beads-hardening.conf /etc/ssh/sshd_config.d/99-beads-hardening.conf \
    /etc/sudoers.d/bootstrap-e2e /usr/local/sbin/systemctl /usr/local/sbin/ufw "$WORK"
}
trap cleanup EXIT

# systemctl and ufw stand-ins: no systemd or netfilter in a container. They
# go in /usr/local/sbin, first on sudo's secure_path, so the server side of
# beads-remote finds them too.
for s in systemctl ufw; do [ ! -e "/usr/local/sbin/$s" ] || { echo "/usr/local/sbin/$s exists" >&2; exit 2; }; done
cat > /usr/local/sbin/systemctl <<STUB
#!/bin/sh
# Test stand-in for systemctl; logs every call.
echo "\$*" >> $WORK/systemctl.log
start() { [ -f $WORK/dolt.pid ] && kill -0 "\$(cat $WORK/dolt.pid)" 2>/dev/null && return 0
  (cd /var/lib/dolt/data && exec setpriv --reuid=dolt --regid=dolt --init-groups env HOME=/var/lib/dolt /usr/local/bin/dolt sql-server --config /etc/dolt/config.yaml >> $WORK/dolt.log 2>&1) &
  echo \$! > $WORK/dolt.pid; }
stop() { [ -f $WORK/dolt.pid ] && kill "\$(cat $WORK/dolt.pid)" 2>/dev/null; while pgrep -x dolt >/dev/null; do sleep 0.2; done; rm -f $WORK/dolt.pid; }
case "\$*" in
  *"reload ssh"*|*"reload sshd"*|*"reload-or-restart ssh"*) [ -f $WORK/sshd.pid ] && kill -HUP "\$(cat $WORK/sshd.pid)"; exit 0 ;;
  *"stop dolt"*) stop ;;
  *"restart dolt"*) stop; start ;;
  *"start dolt"*|*"enable --now dolt"|*"enable --now dolt "*) start ;;
esac
exit 0
STUB
printf '#!/bin/sh\necho "$*" >> %s/ufw.log\n' "$WORK" > /usr/local/sbin/ufw
chmod 0755 /usr/local/sbin/systemctl /usr/local/sbin/ufw

# ── The machine as a cloud image leaves it: an admin with a key and sudo ──
useradd -m -s /bin/bash "$ADMIN"
usermod -p '*' "$ADMIN"
echo "$ADMIN ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/bootstrap-e2e
chmod 0440 /etc/sudoers.d/bootstrap-e2e
ssh-keygen -q -t ed25519 -N '' -f "$WORK/admin_key"
install -d -o "$ADMIN" -g "$ADMIN" -m 0700 "/home/$ADMIN/.ssh"
install -o "$ADMIN" -g "$ADMIN" -m 0600 "$WORK/admin_key.pub" "/home/$ADMIN/.ssh/authorized_keys"
mkdir -p "$WORK/backups"

# ── Bootstrap ──────────────────────────────────────────────────────────────
refuse "bootstrap refuses an admin without a key" env ADMIN_USER=nobody "$HERE/deploy/bootstrap.sh"
printf 'RCLONE_CONFIG_BACKUP_TYPE=local\n' > "$WORK/backup.env"
install -d -m 0700 /etc/beads && install -m 0600 "$WORK/backup.env" /etc/beads/backup.env
if ADMIN_USER=$ADMIN BACKUP_REMOTE="backup:$WORK/backups" "$HERE/deploy/bootstrap.sh" > "$WORK/bootstrap.out" 2>&1; then ok "bootstrap"; else bad "bootstrap"; cat "$WORK/bootstrap.out"; fi
expect "bootstrap again is a no-op that succeeds" env ADMIN_USER=$ADMIN BACKUP_REMOTE="backup:$WORK/backups" "$HERE/deploy/bootstrap.sh"
expect "dolt is the pinned version" sh -c "/usr/local/bin/dolt version | grep -q 2.4.2"
expect "dolt answers on loopback" python3 -c "import socket; socket.create_connection(('127.0.0.1', 3306), 2)"
IP=$(hostname -I 2>/dev/null | awk '{ print $1 }')
if [ -n "$IP" ]; then refuse "dolt does not answer on $IP" python3 -c "import socket; socket.create_connection(('$IP', 3306), 2)"; fi
refuse "root has no empty password" python3 -c "import pymysql; pymysql.connect(host='127.0.0.1', port=3306, user='root')"
expect "logins cannot read server files" python3 -c "import pymysql; c = pymysql.connect(host='127.0.0.1', port=3306, user='beads', password=open('/etc/beads/db-password').read().strip()); cur = c.cursor(); cur.execute(\"SELECT LOAD_FILE('/etc/passwd')\"); assert cur.fetchone()[0] is None"
expect "admin login works" python3 -c "import pymysql; pymysql.connect(host='127.0.0.1', port=3306, user='beads', password=open('/etc/beads/db-password').read().strip())"
expect "secrets are root 0600" sh -c "[ \"\$(stat -c '%U %a' /etc/beads/db-password /etc/beads/root-password | sort -u)\" = 'root 600' ]"
expect "data dir is dolt 0700" test "$(stat -c '%U %a' /var/lib/dolt)" = "dolt 700"
expect "sshd: no passwords" sh -c "sshd -T -C user=$ADMIN,host=x,addr=127.0.0.1 | grep -qx 'passwordauthentication no'"
expect "sshd: AllowUsers is the admin" grep -qx "AllowUsers $ADMIN" /etc/ssh/sshd_config.d/99-beads-hardening.conf
expect "ufw allows 22 only" sh -c "grep -qx 'default deny incoming' '$WORK/ufw.log' && grep -qx 'limit 22/tcp' '$WORK/ufw.log'"
expect "backup reached the remote at bootstrap" sh -c "ls '$WORK/backups/checks/'bootstrap-*.txt"

# ── The real sshd, with the configuration bootstrap wrote ───────────────────
mkdir -p /run/sshd
"$(command -v sshd)" -p "$SSHPORT" -o PidFile="$WORK/sshd.pid" -o ListenAddress=127.0.0.1 -E "$WORK/sshd.log"
sleep 0.5
FPR=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $2 }')

export HOME="$WORK/home" GIT_CONFIG_GLOBAL="$WORK/gitconfig" GIT_CONFIG_NOSYSTEM=1
mkdir -p "$HOME/.ssh" "$WORK/repo/.beads"
git -C "$WORK/repo" init -q
ssh-keygen -q -t ed25519 -N '' -C dev@bootstrap -f "$HOME/.ssh/id_ed25519"
eval "$(ssh-agent -s)" >/dev/null
ssh-add -q "$WORK/admin_key"
# Only what a real repository would carry: no path overrides.
cat > "$WORK/repo/.beads/remote.yaml" <<YAML
server:
  host: 127.0.0.1
  host_key: $FPR
  admin: $ADMIN@127.0.0.1
  ssh_port: $SSHPORT
database: $DB
port: $LPORT
YAML
br() { "$BIN" -C "$WORK/repo" "$@"; }

refuse "password login is refused" ssh -o BatchMode=yes -o PreferredAuthentications=password -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p "$SSHPORT" "$ADMIN@127.0.0.1" true
refuse "root login is refused" ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p "$SSHPORT" root@127.0.0.1 true
expect "server provision (default paths)" br server provision
expect "server check" br server check
expect "AllowUsers gained the database account" grep -qx "AllowUsers $ADMIN $DB" /etc/ssh/sshd_config.d/99-beads-hardening.conf
expect "server add-key" br server add-key "$HOME/.ssh/id_ed25519.pub"
expect "setup" br setup --yes
expect "check" br check
expect "bd round trip" sh -c "cd '$WORK/repo' && bd create 'bootstrap round trip' -t task -p 4 --json | grep -q '\"id\": *\"$DB-'"

# ── Backups of what was just written ───────────────────────────────────────
expect "dump backup" /usr/local/sbin/dolt-backup.sh dump
expect "dump uploaded" sh -c "ls '$WORK/backups/dumps/$DB/'*.sql.gz"
expect "dump holds the issue" sh -c "zcat '$WORK/backups/dumps/$DB/'*.sql.gz | grep -q 'bootstrap round trip'"
expect "filesystem backup" /usr/local/sbin/dolt-backup.sh fs
expect "local copies are root-only" sh -c "[ \"\$(stat -c '%U %a' /var/backups/dolt/* | sort -u)\" = 'root 600' ]"
expect "archive uploaded" sh -c "ls '$WORK/backups/dolt-fs/'*/dolt-fs-*.tar.gz"
expect "dolt is back up after the archive" sh -c "cd '$WORK/repo' && bd list --json | grep -q 'bootstrap round trip'"
expect "server check sees the backup script covers every database" sh -c "'$BIN' -C '$WORK/repo' server check -v | grep -q 'covers every database'"

echo
echo "bootstrap-e2e: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
