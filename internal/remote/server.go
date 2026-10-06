package remote

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

//go:embed server.sh
var serverSh string

//go:embed server.py
var serverPy string

var (
	// A public key line: type, base64 blob, optional plain comment. Nothing
	// that could close a quote or start an option.
	keyRE     = regexp.MustCompile(`^(ssh-ed25519|sk-ssh-ed25519@openssh\.com) [A-Za-z0-9+/]+=*( [A-Za-z0-9@._+-]{1,100})?$`)
	patternRE = regexp.MustCompile(`^[A-Za-z0-9+/=@._-]{6,}$`)
)

// ParseKey reads a public key file and returns its single line, or why it is
// not acceptable. Only ED25519 keys are accepted.
func ParseKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	if strings.Contains(line, "PRIVATE KEY") {
		return "", fmt.Errorf("%s is a PRIVATE key; send only the .pub file", path)
	}
	if strings.Contains(line, "\n") || !keyRE.MatchString(line) {
		return "", fmt.Errorf("%s is not one ED25519 public key line (type, key, and a comment of letters, digits, @ . _ + -)", path)
	}
	return line, nil
}

// script assembles the program sent to the server. Every value was validated
// against a pattern with no quotes or shell metacharacters, so single quoting
// is safe.
func (c *Config) script(action, key, pattern string) string {
	q := func(s string) string { return "'" + s + "'" }
	var b strings.Builder
	for _, kv := range [][2]string{
		{"ACTION", action}, {"DB", c.Database}, {"PWFILE", c.Paths.PasswordFile},
		{"ADMIN_USER", c.Paths.AdminUser}, {"ADMIN_PWFILE", c.Paths.AdminPasswordFile},
		{"SSHD", c.Paths.SSHDConfig}, {"BACKUP", c.Paths.BackupScript},
		{"KEY", key}, {"PATTERN", pattern},
	} {
		fmt.Fprintf(&b, "export %s=%s\n", kv[0], q(kv[1]))
	}
	b.WriteString("database_steps() {\npython3 - <<'BEADS_REMOTE_PY'\n")
	b.WriteString(serverPy)
	b.WriteString("\nBEADS_REMOTE_PY\n}\n")
	b.WriteString(serverSh)
	return b.String()
}

// adminArgs connect as the admin: their own key and agent, but the same
// pinned host key as the tunnel.
func (e Env) adminArgs(c *Config) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + e.knownHosts(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlias=" + c.Server.Host,
		"-p", strconv.Itoa(c.Server.SSHPort),
		c.Server.Admin, "sudo -n bash -s",
	}
}

func (e Env) server(c *Config, action, key, pattern string) int {
	r := &report{env: e}
	if !e.pinHostKey(r, c) {
		return r.finish("")
	}
	out, err := run(e.RepoRoot, c.script(action, key, pattern), "ssh", e.adminArgs(c)...)
	for _, line := range strings.Split(out, "\n") {
		// run trims the output, so the last line may have lost its empty
		// trailing field.
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 5)
		if len(f) < 4 || f[0] != "RESULT" {
			continue
		}
		for len(f) < 5 {
			f = append(f, "")
		}
		switch f[1] {
		case "ok":
			r.ok(f[2], f[3])
		case "warn":
			r.warn(f[2], f[3], f[4])
		default:
			r.fail(f[2], f[3], f[4])
		}
	}
	if err != nil && r.failed() == 0 {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "a password is required"), strings.Contains(msg, "a terminal is required"):
			r.fail("admin access", "sudo needs a password for "+c.Server.Admin, "give that account passwordless sudo, or set server.admin to one that has it")
		default:
			d, f := sshFailure(err, "")
			r.fail("admin access", d, f)
		}
	}
	if len(r.results) <= 1 && r.failed() == 0 {
		d := "no result from the server"
		if err != nil {
			d += ": " + firstLine(err.Error())
		}
		r.fail("server", d, "check that "+c.Server.Admin+" can run: sudo -n bash")
	}
	done := map[string]string{
		"provision": "server ready: " + c.Database + " on " + c.Server.Host,
		"check":     "server ok: " + c.Database + " on " + c.Server.Host,
		"add-key":   "key added; the developer can run beads-remote setup",
		"revoke":    "key revoked",
		"keys":      "keys listed",
	}
	return r.finish(done[action])
}

// Provision creates or repairs everything the database needs on the server.
// Idempotent: a provisioned database reports every step as ok.
func Provision(c *Config, e Env) int { return e.server(c, "provision", "", "") }

// ServerCheck reports the same steps as Provision without changing anything.
func ServerCheck(c *Config, e Env) int { return e.server(c, "check", "", "") }

// AddKey authorizes a developer's public key for the tunnel only.
func AddKey(c *Config, e Env, pubFile string) int {
	key, err := ParseKey(pubFile)
	if err != nil {
		r := &report{env: e}
		r.fail("add key", err.Error(), "the developer sends their ~/.ssh/<key>.pub")
		return r.finish("")
	}
	return e.server(c, "add-key", key, "")
}

// Revoke removes every key whose line contains pattern (its base64 blob or
// its comment). The password need not change: a key is what grants it.
func Revoke(c *Config, e Env, pattern string) int {
	if !patternRE.MatchString(pattern) {
		r := &report{env: e}
		r.fail("revoke", "give at least 6 characters of the key or its comment (letters, digits, + / = @ . _ -)", "beads-remote server keys")
		return r.finish("")
	}
	return e.server(c, "revoke", "", pattern)
}

// Keys lists the authorized keys by fingerprint and comment.
func Keys(c *Config, e Env) int {
	e.Verbose = true
	return e.server(c, "keys", "", "")
}
