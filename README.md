# beads-remote

[![ci](https://github.com/ariesworx/beads-remote/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ariesworx/beads-remote/actions/workflows/ci.yml?query=branch%3Amain)
[![Go Report Card](https://goreportcard.com/badge/github.com/ariesworx/beads-remote)](https://goreportcard.com/report/github.com/ariesworx/beads-remote)
[![Go Reference](https://pkg.go.dev/badge/github.com/ariesworx/beads-remote.svg)](https://pkg.go.dev/github.com/ariesworx/beads-remote)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ariesworx/beads-remote/badge)](https://scorecard.dev/viewer/?uri=github.com/ariesworx/beads-remote)
[![Go version](https://img.shields.io/github/go-mod/go-version/ariesworx/beads-remote)](go.mod)
[![License](https://img.shields.io/github/license/ariesworx/beads-remote)](LICENSE)

Connect a repository to its database on a shared [beads](https://github.com/gastownhall/beads)
(`bd`) server over a pinned SSH tunnel, and provision that server.

```text
your machine                              beads server   (firewall: 22/tcp only)
────────────                              ────────────────────────────────────────
bd ──> 127.0.0.1:PORT ══ ssh:22 ════════> 127.0.0.1:3306  dolt sql-server
       beads-remote up                      └── <database>   ← this repository
```

One command replaces the copy-and-paste setup: it pins the server's host key,
opens the tunnel, fetches the database password, and writes bd's credentials
and server-mode settings. Output is one line per problem and one summary line,
and `--json` gives agents a single document, so it fits into scripts and agent
workflows.

> beads-remote is an independent tool. It is not part of, or endorsed by,
> the beads or Dolt projects.

## Install

```sh
go install github.com/ariesworx/beads-remote@latest
```

You also need `bd` and OpenSSH on your PATH.

## Developer: connect a repository

The repository carries `.beads/remote.yaml` (committed; no secrets). Then:

```sh
beads-remote setup      # once: choose or create a key, pin the host key, connect
beads-remote up         # each session; idempotent and quick when already up
beads-remote check      # verify everything, read-only
beads-remote down
```

If the server doesn't know your key yet, `setup` prints your public key and the
exact command to send to the server admin.

| Exit | Meaning |
|---|---|
| 0 | ok |
| 1 | something failed; each failure prints a `fix:` line |
| 2 | usage error |

`-v` also lists what passed, `--json` prints one document, `--no-color` or
`NO_COLOR` turns colour off, and `-C DIR` runs against another repository.

**Every command is idempotent.** Running one again succeeds and changes
nothing that is already right: `init` accepts an existing `remote.yaml` that
names the same server, `down` is fine when nothing is up, `add-key` replaces
rather than duplicates, `revoke` of a key that is already gone warns and
succeeds, and `deploy/bootstrap.sh` rewrites, reloads and restarts only what
changed. The tests run each command twice and compare the files it manages.

## Admin: add a database and developers

Needs an SSH login to the server with passwordless sudo (`server.admin`).

```sh
beads-remote init --host beads.example.com --database myproject --port 3311
beads-remote server provision          # account, password, grants, AllowUsers, backups
beads-remote server add-key alice.pub  # tunnel-only access for one developer
beads-remote server keys
beads-remote server revoke alice@laptop
```

`provision` is idempotent and repairs drift, including key lines that lost
their restrictions. `server check` reports the same steps without changing
anything.

## Build a server

[`deploy/bootstrap.sh`](deploy/bootstrap.sh) turns a fresh Ubuntu 22.04+ or
Debian 12+ machine into a beads server: pinned Dolt on loopback only, sshd
keys-only, ufw allowing 22, unattended upgrades, and nightly backups (a
filesystem archive that keeps Dolt history, and a SQL dump per database) sent
anywhere rclone can write.

```sh
sudo ADMIN_USER=alice BACKUP_REMOTE=backup:my-bucket/beads ./bootstrap.sh
```

It refuses to run unless `ADMIN_USER` already has an SSH key and passwordless
sudo, because it is about to turn passwords off. It prints the host key
fingerprint for `remote.yaml` when it finishes.

Or let OpenTofu build the whole thing on **DigitalOcean**, **Google Cloud** or
**AWS**: the machine, a firewall, a backup bucket the server can write but not
read (on GCP and AWS; DigitalOcean keys are limited to the one bucket), and a host key generated in advance so its fingerprint is known before
the server boots. See [`deploy/README.md`](deploy/README.md).

## Security model

- **The host key is pinned.** beads-remote keeps its own `known_hosts` under
  `~/.config/beads-remote`, uses `StrictHostKeyChecking=yes`, and never edits
  `~/.ssh`. A changed server key is refused, not re-learned.
- **A developer key can do exactly two things:** forward to `127.0.0.1:3306`
  and read its database password. Every key line is
  `command="cat <password file>",restrict,port-forwarding,permitopen="127.0.0.1:3306"`.
  There is no shell, no other forward, no agent or X11 forwarding, and sshd's
  `AllowTcpForwarding local` stops remote (`-R`) forwards (`server check` warns
  if it is missing).
- **One database per account.** Each database has its own Unix account and
  MySQL user, granted on that one schema only; it cannot list other databases.
- **Database logins cannot touch the server's files.** Dolt otherwise lets any
  login use `LOAD_FILE` and `INTO OUTFILE` whatever its grants; the bootstrap
  sets `secure_file_priv`, and `server check` fails unless it is NULL or a
  path that does not exist, or if a login can read or write a file.
- **A changed host or host key in `remote.yaml` is refused** once you have
  connected, so a pull request cannot quietly point developers at another
  server. Confirm the new server with the admin, then `up --repin`.
- **Revoking deletes every line carrying the key**, chosen by exact
  fingerprint, key or comment, never a substring; a comment shared by two
  keys is refused. The password grants nothing without a key, so it need
  not change.
- **Secrets stay out of argv and the repository.** The password is cached at
  `~/.config/beads-remote/<db>@<host>.pw` (0600) and written to bd's
  credentials file; it is passed to child processes through the environment.
- **Server commands are validated, not interpolated.** Every value sent to the
  server matches a strict pattern and is single-quoted.

Report vulnerabilities privately: see [SECURITY.md](SECURITY.md).

## Develop

```sh
golangci-lint run ./...                    # errcheck, vet, staticcheck, gosec and more (.golangci.yml)
go test ./...                              # unit tests with stub ssh and bd
sudo test/e2e.sh ./beads-remote            # real Dolt, sshd and bd (disposable machine)
sudo test/bootstrap-e2e.sh ./beads-remote  # deploy/bootstrap.sh, then the CLI against it
```

Both end-to-end tests need root and create accounts, so run them in a throwaway
VM or container. CI runs all of it on every pull request, plus `govulncheck`,
shellcheck, and a weekly OpenSSF Scorecard; the badges above show the result. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
