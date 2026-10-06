# Agent instructions

Canonical instructions for any AI coding agent working in this repository
(Claude Code, Codex, Gemini CLI, Cursor and others). `CLAUDE.md` and
`GEMINI.md` import this file; put shared guidance here, not there.

## What this is

beads-remote is a public, Apache-2.0 Go CLI. It connects a repository's `bd`
(beads) to its database on a shared Dolt server through a pinned SSH tunnel,
and provisions that server. `deploy/` holds OpenTofu stacks that build a
server on DigitalOcean, GCP or AWS. Read [README.md](README.md) for usage and
[CONTRIBUTING.md](CONTRIBUTING.md) for the ground rules.

## Layout

| Path | Holds |
|---|---|
| `main.go` | Flags, subcommand dispatch, exit codes. No logic beyond that. |
| `internal/remote/` | Config, the developer commands (`client.go`, `setup.go`), the server commands (`server.go`) and the report printer |
| `internal/remote/server.sh`, `server.py` | Embedded programs sent to the server over `sudo bash -s` |
| `deploy/bootstrap.sh` | Turns a fresh Ubuntu host into a beads server; idempotent |
| `deploy/{digitalocean,gcp,aws}`, `deploy/modules/server` | OpenTofu stacks and their shared module |
| `test/` | End-to-end suites; they need root and create accounts |

## Commands

```sh
golangci-lint run ./... && go test -race ./...
shellcheck internal/remote/server.sh deploy/bootstrap.sh test/*.sh
tofu fmt -check -recursive deploy && tofu -chdir=deploy/modules/server test
```

The end-to-end suites (`test/e2e.sh`, `test/bootstrap-e2e.sh`) run as root in
a throwaway VM or container only, never on a workstation. CI runs all of it.

## Rules

1. **Nothing organisation-specific** in code, tests or docs: no real host,
   fingerprint, path or database name. Use `beads.example.com` and invented
   values.
2. **No secret in argv, logs, output or the repository.** Passwords travel on
   stdin or in a child's environment, and files holding them are 0600.
3. **Validate everything sent to the server** against the strict patterns in
   `config.go`. A new config field gets a pattern and a test that rejects a
   hostile value.
4. **Every command is idempotent.** Running it twice succeeds and changes
   nothing the first run got right. The tests run commands twice and compare
   the files they manage; extend them when you add a command or a file.
5. **Output stays terse.** Failures and warnings print; passes only with `-v`;
   one summary line; `--json` prints one document. Every failure carries a
   `fix:` the user can run.
6. **Go:** standard library first; a new dependency needs a reason in the PR.
   `gofmt`/`goimports`, table-driven tests, errors wrapped with `%w`, no
   panics beyond `regexp.MustCompile` on constant patterns. Nothing in `internal/` writes to `os.Stdout` or
   reads `os.Stdin`; use the `Env` it is given.
7. **Shell:** `set -euo pipefail` (`server.sh` omits `-e` on purpose, so it
   can report every step), shellcheck-clean, and safe to re-run.

## Git

- `main` is protected: squash merges through a pull request with green CI.
- Branch with a type prefix (`feature/`, `fix/`, `maintenance/`, `docs/`,
  `refactor/`) and title the PR the same way (`fix: …`).
- Commit subjects are imperative; the body says why.
- Do not push, open PRs or merge unless asked.
