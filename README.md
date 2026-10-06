# beads-remote

[![ci](https://github.com/ariesworx/beads-remote/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ariesworx/beads-remote/actions/workflows/ci.yml?query=branch%3Amain)
[![Go Reference](https://pkg.go.dev/badge/github.com/ariesworx/beads-remote.svg)](https://pkg.go.dev/github.com/ariesworx/beads-remote)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ariesworx/beads-remote/badge)](https://scorecard.dev/viewer/?uri=github.com/ariesworx/beads-remote)
[![Go version](https://img.shields.io/github/go-mod/go-version/ariesworx/beads-remote)](go.mod)
[![License](https://img.shields.io/github/license/ariesworx/beads-remote)](LICENSE)

Connect a repository to its database on a shared [beads](https://github.com/gastownhall/beads)
(`bd`) server over a pinned SSH tunnel, and provision that server. beads
stores its issues in [Dolt](https://github.com/dolthub/dolt), a SQL database
with Git-style version control; the server runs `dolt sql-server`.

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
> the [beads](https://github.com/gastownhall/beads) or
> [Dolt](https://github.com/dolthub/dolt) projects.

## Install

```sh
go install github.com/ariesworx/beads-remote@latest
```

Or download a release for Linux or macOS from
[Releases](https://github.com/ariesworx/beads-remote/releases). Each release's
`checksums.txt` is signed with keyless cosign, and every archive carries a
GitHub build-provenance attestation. To verify:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/ariesworx/beads-remote/\.github/workflows/release\.yml@refs/tags/v'
sha256sum --ignore-missing -c checksums.txt     # shasum -a 256 on macOS
gh attestation verify beads-remote_*.tar.gz --repo ariesworx/beads-remote
```

You also need `bd` and OpenSSH on your PATH.

## Quick start

Three cases, from nothing to an agent using bd.

**The repository already has `.beads/remote.yaml`** (someone set it up):

```sh
go install github.com/ariesworx/beads-remote@latest
beads-remote setup      # pick or create a key, pin the server, connect
```

If `setup` says the server does not know your key, send the admin the command
it prints, wait for them to run it, then run `beads-remote up`. When
`beads-remote check` ends with `beads ok`, `bd ready` works.

**The repository has no `remote.yaml` yet** and the server exists: an admin
runs `init` and `server provision` (see [Admin](#admin-add-a-database-and-developers)),
commits `.beads/remote.yaml`, and developers follow the case above. The file
holds no secrets:

```yaml
server:
  host: beads.example.com
  host_key: SHA256:…          # the server's ED25519 fingerprint, pinned
  admin: you@beads.example.com # only for `server` commands
database: myproject
port: 3311                    # local end of the tunnel; one per repository
```

**There is no server yet:** see [Build a server](#build-a-server).

Then give your agent the issues: add the MCP server (next section). Its tools
open the tunnel themselves.

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
`NO_COLOR` turns color off, and `-C DIR` runs against another repository.

**Every command is idempotent.** Running one again succeeds and changes
nothing that is already right: `init` accepts an existing `remote.yaml` that
names the same server, `down` is fine when nothing is up, `add-key` replaces
rather than duplicates, `revoke` of a key that is already gone warns and
succeeds, and `deploy/bootstrap.sh` rewrites, reloads and restarts only what
changed. The tests run each command twice and compare the files it manages.

## Agents: MCP server

`beads-remote mcp` gives an MCP client this repository's issues, over stdio:

| Tool | Does |
|---|---|
| `ready` | Open issues with nothing blocking them, highest priority first |
| `list` | Issues filtered by status, type, priority, assignee, labels or title |
| `show` | One issue in full, with its dependencies |
| `create` | A new issue; returns its id |
| `claim` | Assign an issue to yourself and mark it `in_progress` |
| `update` | Change status, priority, assignee, title, text or labels |
| `close`, `reopen` | Finish an issue, or undo that |
| `dep` | Record that one issue depends on another |
| `comment`, `comments`, `note` | Add a comment, read them, append to notes |
| `blocked`, `stats` | What is waiting on what; counts by status |

Every tool opens the SSH tunnel first if it is down, so an agent never runs
`up`, and needs no shell. Each runs `bd --json` in the repository and returns
typed results; lists carry only id, title, status, priority, type, assignee
and labels, and `show` has the rest. Arguments are checked against the input
schema (types, priorities 0 to 4, allowed statuses) before bd runs, and text
is passed so that it can never be read as a flag.

It runs on your machine as you, with the same key, pinned host key and cached
password as the CLI, so there is nothing new to sign in to. A key with a
passphrase must be in ssh-agent, as for `up`. There is deliberately nothing
that deletes or repairs issues, re-initializes the repository (`bd init`) or
runs the `server` commands; those stay at the terminal.

The tools are a Go port of the issue tools in
[beads-mcp](https://github.com/gastownhall/beads/tree/main/integrations/beads-mcp)
(MIT), checked against bd 1.2.2.

Run `beads-remote setup` at a terminal first; the MCP server cannot answer its
questions. Then register it with your client:

| Client | Configuration |
|---|---|
| Claude Code, per repository | `.mcp.json`: `{ "mcpServers": { "beads": { "command": "beads-remote", "args": ["mcp"] } } }`, or `claude mcp add --scope project beads -- beads-remote mcp` |
| Codex CLI | `~/.codex/config.toml`: `[mcp_servers.beads]` with `command = "beads-remote"` and `args = ["mcp"]` |
| Gemini CLI | `.gemini/settings.json`: the same `mcpServers` object as `.mcp.json` |
| Claude desktop, other clients | Their MCP config, with the repository named: `"args": ["-C", "/path/to/repo", "mcp"]` |

Two things trip people up:

- **PATH.** `go install` puts the binary in `$(go env GOPATH)/bin`. A client
  launched from a dock or menu may not have that on its PATH; use the absolute
  path as `command`.
- **Working directory.** The server finds `.beads/remote.yaml` from the
  directory it starts in. A client that starts servers elsewhere needs `-C`.

In Claude Code, `/mcp` shows whether `beads` connected and lists its tools.
`test/mcpe2e` drives the server the way a client does.

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
