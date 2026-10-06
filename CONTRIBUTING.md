# Contributing

Thanks for helping. Please read the [Code of Conduct](CODE_OF_CONDUCT.md) first.

## Before you start

Open an issue for anything larger than a small fix, so we can agree on the
approach before you write it. Security problems go through
[SECURITY.md](SECURITY.md), never a public issue.

## Making a change

1. Branch from `main` with a type prefix: `feature/`, `fix/`, `maintenance/`,
   `docs/` or `refactor/`, then lowercase kebab-case.
2. Keep the change focused. Match the surrounding code.
3. Run the checks:

   ```sh
   golangci-lint run ./... && go test ./...
   shellcheck internal/remote/server.sh deploy/bootstrap.sh test/*.sh
   ```

   If you touched `server.sh`, `server.py`, `bootstrap.sh` or anything that
   talks to ssh or bd, also run the end-to-end tests in a throwaway VM or
   container (they need root and create accounts):

   ```sh
   go build -o beads-remote .
   sudo test/e2e.sh ./beads-remote
   sudo test/bootstrap-e2e.sh ./beads-remote
   ```

4. Title the pull request with the same type: `fix: …`, `feature: …`.

## Ground rules for this codebase

- Output stays terse: failures and warnings, one summary line, `--json` for
  machines. Every failure carries a `fix:` the user can run.
- No secret in argv, logs or the repository.
- Every value sent to the server is validated against a strict pattern.
- Nothing organization-specific (hosts, fingerprints, paths) in code or tests.

By contributing you agree that your contribution is licensed under Apache-2.0.
