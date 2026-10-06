# shellcheck shell=bash
# Server side of beads-remote. Sent over ssh to `sudo -n bash -s` by
# `beads-remote server …`; never installed on the server. The Go side prepends
# the variables below, each validated against a strict pattern, and appends
# the database steps (server.py) as a heredoc.
#
#   ACTION    provision | check | add-key | revoke | keys
#   DB        database, Unix account and MySQL user
#   PWFILE    this database's password file
#   ADMIN_USER, ADMIN_PWFILE   the MySQL admin login
#   SSHD      sshd drop-in holding AllowUsers
#   BACKUP    the filesystem backup script
#   KEY       add-key: "type base64 [comment]"
#   PATTERN   revoke: a key's base64 blob or comment
#
# Every outcome is one line: RESULT<TAB>ok|warn|fail<TAB>name<TAB>detail<TAB>fix
set -uo pipefail
umask 077

OPTS="command=\"cat $PWFILE\",restrict,port-forwarding,permitopen=\"127.0.0.1:3306\""
res() { printf 'RESULT\t%s\t%s\t%s\t%s\n' "$1" "$2" "${3:-}" "${4:-}"; }
apply() { [ "$ACTION" = provision ]; }
home_of() { getent passwd "$1" | cut -d: -f6; }

step_account() {
  if id "$DB" >/dev/null 2>&1; then
    if [ -e "$PWFILE" ] || [ -d "$(dirname "$PWFILE")" ]; then
      res ok "account $DB" "exists"
    else
      res fail "account $DB" "a Unix account named $DB already exists and is not a beads account" "choose another database name"
      return 1
    fi
  elif apply; then
    # A shell is needed for the forced command to run; restrict and the forced
    # command keep the key from using it.
    # useradd leaves the account locked ("!"), and sshd without PAM refuses
    # locked accounts outright. "*" is unlocked with no usable password.
    if useradd -m -s /bin/sh "$DB" && usermod -p '*' "$DB"; then res ok "account $DB" "created"
    else res fail "account $DB" "useradd failed" ""; return 1; fi
  else
    res fail "account $DB" "missing" "beads-remote server provision"
    return 1
  fi
}

step_password() {
  local dir; dir=$(dirname "$PWFILE")
  if [ -s "$PWFILE" ]; then
    local st; st=$(stat -c '%U:%G %a' "$PWFILE")
    if [ "$st" = "root:$DB 640" ]; then
      res ok "password file" "$PWFILE root:$DB 0640"
    elif apply; then
      chown "root:$DB" "$PWFILE" && chmod 0640 "$PWFILE" && res ok "password file" "ownership fixed"
    else
      res fail "password file" "$PWFILE is $st, want root:$DB 640" "beads-remote server provision"
      return 1
    fi
  elif apply; then
    install -d -o root -g "$DB" -m 0750 "$dir" || { res fail "password file" "cannot create $dir" ""; return 1; }
    local tmp; tmp=$(mktemp)
    head -c 48 /dev/urandom | base64 | tr -d '\n=+/' | cut -c1-32 > "$tmp"
    install -o root -g "$DB" -m 0640 "$tmp" "$PWFILE"
    shred -u "$tmp" 2>/dev/null || rm -f "$tmp"
    res ok "password file" "created"
  else
    res fail "password file" "missing" "beads-remote server provision"
    return 1
  fi
}

step_sshd() {
  if [ ! -f "$SSHD" ] || ! grep -q '^AllowUsers' "$SSHD"; then
    res warn "ssh login allowed" "no AllowUsers line in $SSHD, so sshd lets every account try" "deploy/bootstrap.sh writes one"
    return 0
  fi
  if grep -Eq "^AllowUsers([[:space:]].*)?[[:space:]]$DB([[:space:]]|\$)" "$SSHD"; then
    res ok "ssh login allowed" "$DB in AllowUsers"
  elif apply; then
    cp -p "$SSHD" "$SSHD.bak"
    sed -i -E "s/^(AllowUsers .*)\$/\\1 $DB/" "$SSHD"
    if sshd -t 2>/dev/null; then
      if systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null; then
        rm -f "$SSHD.bak"
        res ok "ssh login allowed" "added $DB to AllowUsers"
      else
        res fail "ssh login allowed" "added $DB to AllowUsers, but sshd did not reload" "reload sshd on the server, then: beads-remote server check"
        return 1
      fi
    else
      mv "$SSHD.bak" "$SSHD"
      res fail "ssh login allowed" "sshd -t rejected the change; restored" "inspect $SSHD"
      return 1
    fi
  else
    res fail "ssh login allowed" "$DB is not in AllowUsers" "beads-remote server provision"
    return 1
  fi
}

# A key's port-forwarding option allows remote (-R) forwards too; only sshd's
# own AllowTcpForwarding can stop those.
step_forwarding() {
  local fwd
  fwd=$(sshd -T -C "user=$DB,host=localhost,addr=127.0.0.1" 2>/dev/null | awk '$1 == "allowtcpforwarding" { print $2 }')
  case "$fwd" in
    local|no) res ok "remote forwarding" "off (AllowTcpForwarding $fwd)" ;;
    *) res warn "remote forwarding" "AllowTcpForwarding is ${fwd:-unknown}, so tunnel keys can open listening ports on the server" "set AllowTcpForwarding local in sshd (deploy/bootstrap.sh does)" ;;
  esac
}

# Every key must carry exactly our options. A line that does not (an older
# `restrict` without the forced command, say) is rewritten on provision.
step_keys() {
  local home ak; home=$(home_of "$DB"); ak="$home/.ssh/authorized_keys"
  if [ ! -s "$ak" ]; then
    res warn "tunnel keys" "none yet" "beads-remote server add-key <developer's .pub>"
    return 0
  fi
  local total loose
  total=$(grep -cEv '^[[:space:]]*(#|$)' "$ak")
  loose=$(grep -Ev '^[[:space:]]*(#|$)' "$ak" | grep -cvF "$OPTS ")
  if [ "$loose" -eq 0 ]; then
    res ok "tunnel keys" "$total, each limited to the tunnel and the password"
  elif apply; then
    local tmp; tmp=$(mktemp)
    awk -v opts="$OPTS" '
      /^[[:space:]]*(#|$)/ { print; next }
      { for (i = 1; i <= NF; i++) if ($i ~ /^(ssh-ed25519|sk-ssh-ed25519@openssh\.com|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ecdsa-sha2-nistp256@openssh\.com)$/) break
        if (i > NF) { print "# beads-remote: unparseable, disabled: " $0; next }
        line = opts; for (j = i; j <= NF; j++) line = line " " $j; print line }' "$ak" > "$tmp"
    install -o "$DB" -g "$DB" -m 0600 "$tmp" "$ak"; rm -f "$tmp"
    res ok "tunnel keys" "$loose of $total rewritten to the forced command"
  else
    res fail "tunnel keys" "$loose of $total can run commands or forward elsewhere" "beads-remote server provision"
    return 1
  fi
}

step_backup() {
  if [ ! -f "$BACKUP" ]; then
    res warn "backups" "$BACKUP not found" "deploy/bootstrap.sh installs it"
    return 0
  fi
  if ! grep -q 'DATABASES=(' "$BACKUP"; then
    res ok "backups" "$BACKUP covers every database"
  elif grep -Eq "DATABASES=\\(.*\\b$DB\\b" "$BACKUP"; then
    res ok "backups" "$DB listed in $BACKUP"
  elif apply; then
    cp -p "$BACKUP" "$BACKUP.bak"
    sed -i -E "s/^([[:space:]]*DATABASES=\\()/\\1$DB /" "$BACKUP"
    if bash -n "$BACKUP"; then rm -f "$BACKUP.bak"; res ok "backups" "added $DB to $BACKUP"
    else mv "$BACKUP.bak" "$BACKUP"; res fail "backups" "edit broke $BACKUP; restored" "add $DB to DATABASES by hand"; return 1; fi
  else
    res fail "backups" "$DB is not in DATABASES in $BACKUP" "beads-remote server provision"
    return 1
  fi
}

add_key() {
  local home ak blob; home=$(home_of "$DB"); ak="$home/.ssh/authorized_keys"
  [ -n "$home" ] || { res fail "add key" "account $DB missing" "beads-remote server provision"; return 1; }
  blob=$(printf '%s\n' "$KEY" | awk '{ print $2 }')
  install -d -o "$DB" -g "$DB" -m 0700 "$home/.ssh"
  touch "$ak"
  local tmp; tmp=$(mktemp)
  grep -vF " $blob" "$ak" > "$tmp" || true
  printf '%s %s\n' "$OPTS" "$KEY" >> "$tmp"
  install -o "$DB" -g "$DB" -m 0600 "$tmp" "$ak"; rm -f "$tmp"
  res ok "add key" "$(printf '%s\n' "$KEY" | ssh-keygen -lf - | awk '{ print $2, $3 }'); $(grep -cEv '^[[:space:]]*(#|$)' "$ak") key(s) now"
}

# key_of LINE prints "fingerprint<TAB>blob<TAB>comment" for an authorized_keys
# line, options and all, or nothing when ssh-keygen cannot read it. The key is
# the type token whose "type blob" pair has the line's own fingerprint, so a
# key type quoted inside an option is never mistaken for the key.
key_of() {
  local line=$1 fp i
  fp=$(printf '%s\n' "$line" | ssh-keygen -lf - 2>/dev/null | awk '{ print $2 }')
  [ -n "$fp" ] || return 0
  local -a f; read -r -a f <<< "$line"
  for ((i = 0; i + 1 < ${#f[@]}; i++)); do
    [[ ${f[i]} =~ ^(ssh-ed25519|sk-ssh-ed25519@openssh\.com|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ecdsa-sha2-nistp256@openssh\.com)$ ]] || continue
    if [ "$(printf '%s %s\n' "${f[i]}" "${f[i+1]}" | ssh-keygen -lf - 2>/dev/null | awk '{ print $2 }')" = "$fp" ]; then
      printf '%s\t%s\t%s\n' "$fp" "${f[i+1]}" "${f[*]:i+2}"
      return 0
    fi
  done
}

# Removes the keys whose fingerprint (SHA256:...), base64 blob or whole
# comment equals PATTERN exactly. Never a substring: every line shares the
# same options, so a loose match could remove every key at once. Every line
# carrying a matched key goes, so a duplicate under another comment cannot
# keep it authorized. A comment is the developer's own choice, so one that
# names more than one key is refused in favour of a fingerprint.
revoke() {
  local home ak; home=$(home_of "$DB"); ak="$home/.ssh/authorized_keys"
  [ -s "$ak" ] || { res fail "revoke" "no keys" ""; return 1; }
  local -a lines=() blobs=()
  local -A hit=() by_comment=() names=()
  local line k fp blob comment n=0
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%$'\r'}
    lines+=("$line"); blobs+=("")
    n=$((n + 1))
    [[ $line =~ ^[[:space:]]*(#|$) ]] && continue
    k=$(key_of "$line")
    if [ -z "$k" ]; then
      res warn "revoke" "line $n is not a key ssh-keygen can read; left in place" "inspect $ak"
      continue
    fi
    IFS=$'\t' read -r fp blob comment <<< "$k"
    blobs[n-1]=$blob
    names[$blob]=$fp${comment:+ $comment}
    if [ "$PATTERN" = "$fp" ] || [ "$PATTERN" = "$blob" ]; then hit[$blob]=1
    elif [ "$PATTERN" = "$comment" ]; then by_comment[$blob]=1; fi
  done < "$ak"
  if [ ${#by_comment[@]} -gt 1 ]; then
    res fail "revoke" "${#by_comment[@]} different keys have the comment $PATTERN" "revoke by fingerprint: beads-remote server keys"
    return 1
  fi
  for k in "${!by_comment[@]}"; do hit[$k]=1; done
  if [ ${#hit[@]} -eq 0 ]; then
    res fail "revoke" "no key's fingerprint, blob or comment is exactly $PATTERN" "beads-remote server keys"
    return 1
  fi
  local tmp kept=0 removed=0 i; tmp=$(mktemp)
  for i in "${!lines[@]}"; do
    if [ -n "${blobs[i]}" ] && [ -n "${hit[${blobs[i]}]:-}" ]; then removed=$((removed + 1)); continue; fi
    printf '%s\n' "${lines[i]}" >> "$tmp"
    if [ -n "${blobs[i]}" ]; then kept=$((kept + 1)); fi
  done
  install -o "$DB" -g "$DB" -m 0600 "$tmp" "$ak"; rm -f "$tmp"
  for k in "${!hit[@]}"; do res ok "revoked" "${names[$k]}"; done
  res ok "revoke" "$removed line(s) removed, $kept key line(s) left; no password change needed"
}

keys() {
  local home ak; home=$(home_of "$DB"); ak="$home/.ssh/authorized_keys"
  [ -s "$ak" ] || { res warn "keys" "none" "beads-remote server add-key"; return 0; }
  grep -Ev '^[[:space:]]*(#|$)' "$ak" | while IFS= read -r line; do
    local fp; fp=$(printf '%s\n' "$line" | ssh-keygen -lf - 2>/dev/null | awk '{ print $2, $3 }')
    if printf '%s' "$line" | grep -qF "$OPTS "; then res ok "key" "$fp"; else res warn "key" "$fp (not restricted)" "beads-remote server provision"; fi
  done
}

server_steps() {
  step_account || return 1
  step_password || return 1
  step_sshd || return 1
  step_forwarding
  step_keys || return 1
  step_backup || return 1
  database_steps
}

case "$ACTION" in
  provision|check) server_steps ;;
  add-key) add_key ;;
  revoke) revoke ;;
  keys) keys ;;
  *) res fail "action" "unknown: $ACTION" "" ;;
esac
