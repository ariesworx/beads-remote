# Security policy

beads-remote guards access to a team's issue database, so we treat security
reports as the highest priority.

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub:
[Security → Report a vulnerability](https://github.com/ariesworx/beads-remote/security/advisories/new).

Please include what you found, how to reproduce it, and what an attacker gains.
A person reads every report. We will acknowledge it, keep you informed while
we work on a fix, and credit you in the advisory unless you prefer otherwise.

## Supported versions

Fixes go into the latest release only.

## In scope

- Anything that lets a developer key do more than forward to
  `127.0.0.1:3306` and read its own database password
- One database's account or key reaching another database
- Host-key pinning being bypassed or silently re-learned
- A secret written to argv, logs, the repository, or a world-readable file
- Command injection through `remote.yaml`, a public key, or a revoke pattern
- `deploy/bootstrap.sh` or the OpenTofu leaving a server more exposed than
  documented
