#!/usr/bin/env bash
# End-to-end test: a real Dolt server, a real sshd and a real bd, on this
# machine. Provisions a database, authorizes a developer key, connects, and
# proves the security properties (no shell, no other forwards, isolation,
# revocation).
#
# Needs root on a disposable Linux machine or container (it creates Unix
# accounts and runs sshd on 127.0.0.1:2222 and Dolt on 127.0.0.1:3306):
#   dolt, bd, sshd, sudo, python3 with pymysql, git, and a built beads-remote.
#
#   sudo test/e2e.sh ./beads-remote
set -euo pipefail

BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
HERE=$(cd "$(dirname "$0")/.." && pwd)
[ "$(id -u)" = 0 ] || { echo "run as root on a disposable machine" >&2; exit 2; }
for t in dolt bd sshd sudo python3 git ssh-keygen; do command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 2; }; done
for u in beadse2e beadsadmin; do ! id "$u" >/dev/null 2>&1 || { echo "account $u exists from an earlier run; remove it first" >&2; exit 2; }; done
for p in 3306 2222; do ! (exec 3<>/dev/tcp/127.0.0.1/$p) 2>/dev/null || { echo "port $p is in use; stop the old server first" >&2; exit 2; }; done

DB=beadse2e ADMIN=beadsadmin SSHPORT=2222 LPORT=3399
ETC=/etc/beads-e2e
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
  [ -n "${DOLT_PID:-}" ] && kill "$DOLT_PID" && wait "$DOLT_PID" 2>/dev/null
  for u in "$DB" "$ADMIN"; do
    id "$u" >/dev/null 2>&1 || continue
    for p in $(pgrep -u "$u"); do kill "$p"; done
  done
  sleep 0.5
  userdel -r "$DB" 2>/dev/null; userdel -r "$ADMIN" 2>/dev/null
  rm -rf "$ETC" "/etc/${DB:?}" /etc/sudoers.d/beads-e2e "$WORK"
  [ -n "${STUB:-}" ] && rm -f "$STUB"
}
trap cleanup EXIT

# ── Server: Dolt with an admin login, sshd with an AllowUsers drop-in ──────
mkdir -p "$ETC/sshd.d" "$WORK/dolt/data"
umask 077
head -c 48 /dev/urandom | base64 | tr -d '\n=+/' | cut -c1-32 > "$ETC/admin-pw"
(cd "$WORK/dolt/data" && { dolt init --name e2e --email e2e@example.invalid >/dev/null 2>&1 || true; })
(cd "$WORK/dolt/data" && exec dolt sql-server --host 127.0.0.1 --port 3306 > "$WORK/dolt.log" 2>&1) &
DOLT_PID=$!
for _ in $(seq 50); do python3 -c "import pymysql; pymysql.connect(host='127.0.0.1', port=3306, user='root')" 2>/dev/null && break; sleep 0.2; done
python3 - "$ETC/admin-pw" <<'PY'
import pymysql, sys
pw = open(sys.argv[1]).read().strip()
c = pymysql.connect(host="127.0.0.1", port=3306, user="root", autocommit=True)
cur = c.cursor()
cur.execute("CREATE USER IF NOT EXISTS `beads`@`localhost` IDENTIFIED BY %s", (pw,))
cur.execute("CREATE USER IF NOT EXISTS `beads`@`%%` IDENTIFIED BY %s", (pw,))
cur.execute("GRANT ALL ON *.* TO `beads`@`localhost` WITH GRANT OPTION")
cur.execute("GRANT ALL ON *.* TO `beads`@`%` WITH GRANT OPTION")
cur.execute("CREATE DATABASE IF NOT EXISTS `otherproject`")
PY
# shellcheck disable=SC2016  # literal script text
printf '#!/bin/bash\nDATABASES=(otherproject)\necho backup "${DATABASES[@]}"\n' > "$ETC/dolt-backup.sh"
chmod 0755 "$ETC/dolt-backup.sh"

useradd -m -s /bin/bash "$ADMIN"
usermod -p "*" "$ADMIN"   # unlocked, no password: sshd refuses locked accounts when UsePAM is off
echo "$ADMIN ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/beads-e2e
chmod 0440 /etc/sudoers.d/beads-e2e
ssh-keygen -q -t ed25519 -N '' -f "$WORK/admin_key"
install -d -o "$ADMIN" -g "$ADMIN" -m 0700 "/home/$ADMIN/.ssh"
install -o "$ADMIN" -g "$ADMIN" -m 0600 "$WORK/admin_key.pub" "/home/$ADMIN/.ssh/authorized_keys"

ssh-keygen -q -t ed25519 -N '' -f "$ETC/host_key"
echo "AllowUsers $ADMIN" > "$ETC/sshd.d/99-beads.conf"
cat > "$ETC/sshd_config" <<CONF
Port $SSHPORT
ListenAddress 127.0.0.1
HostKey $ETC/host_key
PidFile $WORK/sshd.pid
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowTcpForwarding local
UsePAM no
Include $ETC/sshd.d/*.conf
CONF
mkdir -p /run/sshd
# beads-remote reloads sshd with systemctl; this sshd is not a service, so a
# stub systemctl sends it SIGHUP instead.
# It goes in /usr/local/sbin, first on sudo's secure_path; cleanup removes it.
STUB=/usr/local/sbin/systemctl
[ ! -e "$STUB" ] || { echo "$STUB exists; refusing to shadow it" >&2; exit 2; }
# shellcheck disable=SC2016  # literal script text
printf '#!/bin/sh\nkill -HUP "$(cat %s)"\n' "$WORK/sshd.pid" > "$STUB"
chmod 0755 "$STUB"
"$(command -v sshd)" -f "$ETC/sshd_config" -E "$WORK/sshd.log"
sleep 0.5
FPR=$(ssh-keygen -lf "$ETC/host_key.pub" | awk '{ print $2 }')

# ── Repository and developer ───────────────────────────────────────────────
export HOME="$WORK/home" GIT_CONFIG_GLOBAL="$WORK/gitconfig" GIT_CONFIG_NOSYSTEM=1
mkdir -p "$HOME/.ssh" "$WORK/repo/.beads"
git -C "$WORK/repo" init -q
ssh-keygen -q -t ed25519 -N '' -C dev@e2e -f "$HOME/.ssh/id_ed25519"
cat > "$WORK/repo/.beads/remote.yaml" <<YAML
server:
  host: 127.0.0.1
  host_key: $FPR
  admin: $ADMIN@127.0.0.1
  ssh_port: $SSHPORT
database: $DB
port: $LPORT
prefix: e2e
paths:
  admin_password_file: $ETC/admin-pw
  sshd_config: $ETC/sshd.d/99-beads.conf
  backup_script: $ETC/dolt-backup.sh
YAML
eval "$(ssh-agent -s)" >/dev/null
ssh-add -q "$WORK/admin_key"
br() { "$BIN" -C "$WORK/repo" "$@"; }
# Owner, mode and content of every file the server commands manage, to show
# a repeated command changes nothing.
server_state() {
  for f in "/home/$DB/.ssh/authorized_keys" "/etc/$DB/db-password" "$ETC/sshd.d/99-beads.conf" "$ETC/dolt-backup.sh"; do
    [ -e "$f" ] && printf '%s %s %s\n' "$f" "$(stat -c '%U:%G %a' "$f")" "$(sha256sum < "$f")"
  done
}

# ── Server side ────────────────────────────────────────────────────────────
refuse "server check fails before provisioning" br server check
# Dolt's default lets any login read server files; provision must refuse it.
refuse "provision refuses a Dolt that leaks files" br server provision
expect "and says why" sh -c "'$BIN' -C '$WORK/repo' server provision | grep -q 'file access: .*can read and write any file'"
kill "$DOLT_PID"; wait "$DOLT_PID" 2>/dev/null || true
printf 'listener:\n  host: 127.0.0.1\n  port: 3306\nsystem_variables:\n  secure_file_priv: /nonexistent-beads\n' > "$WORK/dolt.yaml"
(cd "$WORK/dolt/data" && exec dolt sql-server --config "$WORK/dolt.yaml" >> "$WORK/dolt.log" 2>&1) &
DOLT_PID=$!
for _ in $(seq 50); do python3 -c "import socket; socket.create_connection(('127.0.0.1', 3306), 1)" 2>/dev/null && break; sleep 0.2; done
expect "server provision" br server provision
before=$(server_state)
expect "server provision again succeeds" br server provision
expect "and changes nothing" test "$before" = "$(server_state)"
expect "server check passes" br server check
expect "AllowUsers gained the account" grep -Eq "^AllowUsers $ADMIN $DB\$" "$ETC/sshd.d/99-beads.conf"
expect "backup list gained the database" grep -q "DATABASES=($DB otherproject" "$ETC/dolt-backup.sh"
expect "password file root:$DB 0640" test "$(stat -c '%U:%G %a' "/etc/$DB/db-password")" = "root:$DB 640"

# ── Developer side ─────────────────────────────────────────────────────────
refuse "setup refused before the key is added" br setup --yes --key "$HOME/.ssh/id_ed25519"
expect "refusal names the add-key step" sh -c "'$BIN' -C '$WORK/repo' setup --yes 2>&1 | grep -q 'server add-key id_ed25519.pub'"
refuse "a private key is refused by add-key" br server add-key "$HOME/.ssh/id_ed25519"
expect "server add-key" br server add-key "$HOME/.ssh/id_ed25519.pub"
before=$(server_state)
expect "server add-key again replaces, not duplicates" br server add-key "$HOME/.ssh/id_ed25519.pub"
expect "and changes nothing" test "$before" = "$(server_state)"
expect "one key on the server" test "$(grep -c ssh-ed25519 "/home/$DB/.ssh/authorized_keys")" = 1
expect "setup" br setup --yes
expect "check" br check
expect "check --json says ok" sh -c "'$BIN' -C '$WORK/repo' check --json | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin)[\"ok\"] else 1)'"
# The MCP server over real stdio, from a closed tunnel: the first tool call
# must open it.
(cd "$HERE" && go build -o "$WORK/mcpe2e" ./test/mcpe2e)
expect "mcp: create, ready, claim, close, comment with the tunnel closed" sh -c "'$BIN' -C '$WORK/repo' down && '$WORK/mcpe2e' '$BIN' '$WORK/repo'"
expect "and the tunnel is up after it" br status
expect "bd creates an e2e- issue on the server" sh -c "cd '$WORK/repo' && bd create 'e2e round trip' -t task -p 4 --json | grep -q '\"id\": *\"e2e-'"
expect "the issue is in the server database" python3 - "/etc/$DB/db-password" <<PY
import pymysql, sys
c = pymysql.connect(host="127.0.0.1", port=3306, user="$DB", password=open(sys.argv[1]).read().strip(), database="$DB")
cur = c.cursor(); cur.execute("SELECT COUNT(*) FROM issues WHERE title = 'e2e round trip'")
sys.exit(0 if cur.fetchone()[0] == 1 else 1)
PY
expect "bd init left the repository alone" sh -c "cd '$WORK/repo' && [ -z \"\$(git config core.hooksPath)\" ] && [ ! -e AGENTS.md ] && [ ! -e .beads/README.md ]"
expect "a second up does not re-initialize" sh -c "'$BIN' -C '$WORK/repo' up -v | grep -q 'server database initialized: prefix e2e'"
# hq's case: bd's tables exist on the server but the prefix was never stored.
python3 - "/etc/$DB/db-password" <<PY2
import pymysql, sys
c = pymysql.connect(host="127.0.0.1", port=3306, user="$DB", password=open(sys.argv[1]).read().strip(), database="$DB", autocommit=True)
cur = c.cursor()
cur.execute("DELETE FROM config WHERE \`key\` = 'issue_prefix'")
PY2
expect "up stores the missing prefix" sh -c "'$BIN' -C '$WORK/repo' up -v | grep -q 'server database initialized: created'"
expect "tables without a prefix are repaired" sh -c "cd '$WORK/repo' && bd create 'after repair' -t task -p 4 --json | grep -q '\"id\": *\"e2e-'"
expect "the repair kept existing issues" sh -c "cd '$WORK/repo' && bd list --json | grep -q 'e2e round trip'"
expect "no local database was created" test ! -e "$WORK/repo/.beads/embeddeddolt"

# ── Security properties ────────────────────────────────────────────────────
TK=(-o BatchMode=yes -o IdentitiesOnly=yes -i "$HOME/.ssh/id_ed25519" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p "$SSHPORT")
refuse "the tunnel key gets no shell" sh -c "ssh $(printf '%q ' "${TK[@]}") $DB@127.0.0.1 'echo SHELL_UNEXPECTED' 2>/dev/null | grep -q SHELL_UNEXPECTED"
# permitopen lets the local listener start but refuses each channel to any
# other target, so a scan through it gets no sshd banner.
ssh "${TK[@]}" -N -L 3401:127.0.0.1:"$SSHPORT" "$DB@127.0.0.1" 2>/dev/null &
FWD_PID=$!
sleep 1
refuse "the tunnel key cannot forward to sshd" sh -c "ssh-keyscan -T 3 -p 3401 127.0.0.1 2>/dev/null | grep -q ssh-"
kill "$FWD_PID" 2>/dev/null; wait "$FWD_PID" 2>/dev/null || true
expect "the database user cannot see other projects" python3 - "/etc/$DB/db-password" <<PY
import pymysql, sys
c = pymysql.connect(host="127.0.0.1", port=3306, user="$DB", password=open(sys.argv[1]).read().strip())
cur = c.cursor(); cur.execute("SHOW DATABASES")
sys.exit(1 if "otherproject" in {r[0] for r in cur.fetchall()} else 0)
PY
expect "the database user cannot read server files" python3 - "/etc/$DB/db-password" <<PY
import pymysql, sys
c = pymysql.connect(host="127.0.0.1", port=3306, user="$DB", password=open(sys.argv[1]).read().strip())
cur = c.cursor(); cur.execute("SELECT LOAD_FILE('/etc/passwd')")
sys.exit(0 if cur.fetchone()[0] is None else 1)
PY
expect "password never printed" sh -c "! '$BIN' -C '$WORK/repo' check -v --json | grep -qF \"\$(cat /etc/$DB/db-password)\""

# An older, looser key line is tightened by provision.
sed -i -E 's/^command="[^"]*",//' "/home/$DB/.ssh/authorized_keys"
refuse "server check flags a loose key" br server check
expect "provision restricts it again" br server provision
expect "key line forced again" grep -q '^command="cat /etc/beadse2e/db-password",restrict,port-forwarding,permitopen="127.0.0.1:3306" ssh-ed25519' "/home/$DB/.ssh/authorized_keys"

# Revocation.
expect "down" br down
expect "revoke by a shared substring matches nothing" sh -c "'$BIN' -C '$WORK/repo' server revoke port-forwarding | grep -q 'nothing removed'"
expect "and removed nothing" test "$(grep -c ssh-ed25519 "/home/$DB/.ssh/authorized_keys")" = 1
# The same key under a second comment must not survive revoking the first.
dup=$(sed -n '/ dev@e2e$/{s/ dev@e2e$/ dev@other/;p}' "/home/$DB/.ssh/authorized_keys")
printf '%s\n' "$dup" >> "/home/$DB/.ssh/authorized_keys"
expect "server revoke" br server revoke dev@e2e
expect "and every line with that key went" test "$(grep -c ssh-ed25519 "/home/$DB/.ssh/authorized_keys")" = 0
before=$(server_state)
expect "server revoke again succeeds" br server revoke dev@e2e
expect "and changes nothing" test "$before" = "$(server_state)"
rm -f "$HOME/.config/beads-remote/$DB@127.0.0.1.pw"
refuse "a revoked key cannot connect" br up

# A changed server key is refused.
br server add-key "$HOME/.ssh/id_ed25519.pub" >/dev/null
sed -i "s|host_key: .*|host_key: SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA|" "$WORK/repo/.beads/remote.yaml"
rm -f "$HOME/.config/beads-remote/known_hosts"
refuse "a different host key is refused" br up

echo
echo "e2e: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
