package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// identity returns the private key chosen at setup, or "" to let ssh use the
// agent and its defaults.
func (e Env) identity() string {
	b, err := os.ReadFile(e.identityFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// sshArgs are the options every connection to the server uses. They pin the
// host key to our own known_hosts file and never prompt.
func (e Env) sshArgs(c *Config) []string {
	a := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + e.knownHosts(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlgorithms=ssh-ed25519",
		"-p", strconv.Itoa(c.Server.SSHPort),
	}
	if id := e.identity(); id != "" {
		a = append(a, "-o", "IdentitiesOnly=yes", "-i", id)
	}
	return a
}

func (e Env) ssh(c *Config, stdin string, args ...string) (string, error) {
	return run(e.RepoRoot, stdin, "ssh", append(e.sshArgs(c), args...)...)
}

// sshFailure turns ssh's stderr into what went wrong and what to do.
func sshFailure(err error, pub string) (detail, fix string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "REMOTE HOST IDENTIFICATION HAS CHANGED"),
		strings.Contains(msg, "Host key verification failed"):
		return "the server's host key does not match the pinned one",
			"do not connect; ask the server admin whether the server was rebuilt, then update server.host_key"
	case strings.Contains(msg, "Permission denied"):
		f := "ask the server admin to run: beads-remote server add-key <your .pub file>"
		if pub != "" {
			f = "send " + pub + " (the public key, never the private one) to the server admin, who runs: beads-remote server add-key " + filepath.Base(pub)
		}
		return "the server refused your key", f
	case strings.Contains(msg, "Could not resolve"), strings.Contains(msg, "timed out"),
		strings.Contains(msg, "Connection refused"), strings.Contains(msg, "No route"),
		strings.Contains(msg, "Network is unreachable"):
		return firstLine(msg), "check your network; work is not blocked, run beads-remote up again later"
	}
	return firstLine(msg), "run with -v, or try: ssh -v " + "<user>@<host>"
}

func pubFor(identity string) string {
	if identity == "" {
		return ""
	}
	if _, err := os.Stat(identity + ".pub"); err == nil {
		return identity + ".pub"
	}
	return ""
}

func (e Env) healthy(c *Config) bool {
	_, err := run(e.RepoRoot, "", "ssh", "-O", "check", "-S", e.socket(c), c.dest())
	return err == nil
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

var fprOut = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)

// fingerprints returns the SHA256 fingerprints of the keys in known_hosts
// format text.
func fingerprints(keys string) []string {
	out, err := run("", keys, "ssh-keygen", "-lf", "-")
	if err != nil {
		return nil
	}
	return fprOut.FindAllString(out, -1)
}

// pinHostKey makes sure our known_hosts holds the server key whose
// fingerprint is in the config. It scans the server once and refuses any
// other key; it never trusts on first use.
func (e Env) pinHostKey(r *report, c *Config) bool {
	const name = "server host key pinned"
	if b, err := os.ReadFile(e.knownHosts()); err == nil {
		for _, f := range fingerprints(string(b)) {
			if f == c.Server.HostKey {
				return r.ok(name, c.Server.HostKey)
			}
		}
	}
	scanned, err := run("", "", "ssh-keyscan", "-t", "ed25519", "-p", strconv.Itoa(c.Server.SSHPort), "-T", "10", c.Server.Host)
	if err != nil && scanned == "" {
		return r.fail(name, "could not reach "+c.Server.Host, "check your network and server.host")
	}
	var keep []string
	for _, line := range strings.Split(scanned, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fp := fingerprints(line); len(fp) == 1 && fp[0] == c.Server.HostKey {
			// Name the key by the bare host as well, so admin connections
			// through an ssh alias (HostKeyAlias) find it on any port.
			f := strings.Fields(line)
			names := c.Server.Host
			if c.Server.SSHPort != 22 {
				names += fmt.Sprintf(",[%s]:%d", c.Server.Host, c.Server.SSHPort)
			}
			keep = append(keep, names+" "+strings.Join(f[1:], " "))
		}
	}
	if len(keep) == 0 {
		got := strings.Join(fingerprints(scanned), ", ")
		if got == "" {
			got = "no ED25519 key"
		}
		return r.fail(name, c.Server.Host+" presented "+got+", expected "+c.Server.HostKey,
			"do not connect; confirm the fingerprint with the server admin out of band")
	}
	if err := writePrivate(e.knownHosts(), []byte(strings.Join(keep, "\n")+"\n")); err != nil {
		return r.fail(name, err.Error(), "check permissions on "+e.stateDir())
	}
	return r.ok(name, c.Server.HostKey)
}

// Up opens the tunnel if it is down and makes every local file right.
func Up(c *Config, e Env) int {
	r := &report{env: e}
	if e.up(r, c) {
		e.local(r, c)
	}
	return r.finish(fmt.Sprintf("beads up: %s on 127.0.0.1:%d", c.Database, c.Port))
}

func (e Env) up(r *report, c *Config) bool {
	if e.healthy(c) {
		return r.ok("tunnel", "already up")
	}
	_ = os.MkdirAll(e.stateDir(), 0o700)
	_, _ = run(e.RepoRoot, "", "ssh", "-O", "exit", "-S", e.socket(c), c.dest())
	_ = os.Remove(e.socket(c))
	if !portFree(c.Port) {
		return r.fail("tunnel", fmt.Sprintf("local port %d is in use by another program", c.Port),
			fmt.Sprintf("find it with: lsof -nP -iTCP:%d -sTCP:LISTEN, or choose another port in %s", c.Port, ConfigFile))
	}
	if !e.pinHostKey(r, c) {
		return false
	}
	args := append(e.sshArgs(c),
		"-f", "-N", "-M", "-S", e.socket(c),
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=60",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:3306", c.Port),
		c.dest())
	if _, err := run(e.RepoRoot, "", "ssh", args...); err != nil {
		d, f := sshFailure(err, pubFor(e.identity()))
		return r.fail("tunnel", d, f)
	}
	if !e.healthy(c) {
		return r.fail("tunnel", "opened, but the control socket does not answer", "run beads-remote down, then up")
	}
	return r.ok("tunnel", fmt.Sprintf("127.0.0.1:%d → %s:3306", c.Port, c.Server.Host))
}

// local makes the password cache, bd's credentials and the repository's
// .beads files right. Each step is idempotent.
func (e Env) local(r *report, c *Config) {
	if !e.fetchPassword(r, c) {
		return
	}
	e.ensureCredentials(r, c)
	e.ensureMetadata(r, c)
	e.ensureDirMode(r)
	e.ensurePrefix(r, c)
	e.ensureRole(r)
	e.ensureSchema(r, c)
}

func (e Env) fetchPassword(r *report, c *Config) bool {
	const name = "password cached"
	p := e.pwPath(c)
	if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
		if fi.Mode().Perm() != 0o600 {
			_ = os.Chmod(p, 0o600)
		}
		return r.ok(name, "")
	}
	// The key's forced command prints the password whatever we ask for.
	pw, err := e.ssh(c, "", c.dest(), "cat "+c.remotePW())
	if err != nil {
		d, f := sshFailure(err, pubFor(e.identity()))
		return r.fail(name, d, f)
	}
	if pw == "" || strings.ContainsAny(pw, " \t\n") {
		return r.fail(name, "the server returned no usable password", "ask the server admin to run: beads-remote server check")
	}
	if err := writePrivate(p, []byte(pw+"\n")); err != nil {
		return r.fail(name, err.Error(), "check permissions on "+e.stateDir())
	}
	return r.ok(name, "fetched")
}

func (e Env) password(c *Config) string {
	b, _ := os.ReadFile(e.pwPath(c))
	return strings.TrimSpace(string(b))
}

func (c *Config) section() string { return fmt.Sprintf("[127.0.0.1:%d]", c.Port) }

// credsCurrent reports whether bd's credentials file already holds our
// section with the cached password.
func (e Env) credsCurrent(c *Config) bool {
	b, err := os.ReadFile(e.credsFile())
	if err != nil {
		return false
	}
	pw := e.password(c)
	inside := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == c.section():
			inside = true
		case strings.HasPrefix(line, "["):
			inside = false
		case inside && pw != "" && line == "password="+pw:
			return true
		}
	}
	return false
}

// ensureCredentials writes [127.0.0.1:PORT] into bd's credentials file and
// leaves every other section as it was.
func (e Env) ensureCredentials(r *report, c *Config) {
	const name = "bd credentials"
	f := e.credsFile()
	if e.credsCurrent(c) {
		_ = os.Chmod(f, 0o600)
		r.ok(name, c.section())
		return
	}
	old, _ := os.ReadFile(f)
	var out []string
	skip := false
	for _, line := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		t := strings.TrimRight(line, "\r")
		if t == c.section() {
			skip = true
			continue
		}
		if strings.HasPrefix(t, "[") {
			skip = false
		}
		if !skip && !(t == "" && len(out) == 0) {
			out = append(out, line)
		}
	}
	out = append(out, c.section(), "password="+e.password(c))
	if err := writePrivate(f, []byte(strings.Join(out, "\n")+"\n")); err != nil {
		r.fail(name, err.Error(), "check permissions on "+filepath.Dir(f))
		return
	}
	r.ok(name, "wrote "+c.section())
}

type metadata struct {
	Database string `json:"database"`
	Backend  string `json:"backend"`
	Mode     string `json:"dolt_mode"`
	DB       string `json:"dolt_database"`
	Host     string `json:"dolt_server_host"`
	Port     int    `json:"dolt_server_port"`
	User     string `json:"dolt_server_user"`
}

func (c *Config) metadata() metadata {
	return metadata{"dolt", "dolt", "server", c.Database, "127.0.0.1", c.Port, c.user()}
}

// metadataOK reports whether .beads/metadata.json points bd at our database.
// Without it bd silently uses an empty local database (bd 1.2.2).
func (e Env) metadataOK(c *Config) bool {
	b, err := os.ReadFile(filepath.Join(e.beadsDir(), "metadata.json"))
	if err != nil {
		return false
	}
	var m metadata
	return json.Unmarshal(b, &m) == nil && m == c.metadata()
}

func (e Env) ensureMetadata(r *report, c *Config) {
	const name = "bd server mode (.beads/metadata.json)"
	if e.metadataOK(c) {
		r.ok(name, "")
	} else {
		b, _ := json.MarshalIndent(c.metadata(), "", "  ")
		if err := os.MkdirAll(e.beadsDir(), 0o700); err != nil {
			r.fail(name, err.Error(), "")
			return
		}
		if err := os.WriteFile(filepath.Join(e.beadsDir(), "metadata.json"), append(b, '\n'), 0o644); err != nil {
			r.fail(name, err.Error(), "")
			return
		}
		r.ok(name, "written")
	}
	if _, err := os.Stat(filepath.Join(e.beadsDir(), "embeddeddolt")); err == nil {
		r.warn("no local database", ".beads/embeddeddolt exists: bd fell back to a local database at some point",
			"check it holds nothing you need, then rm -rf .beads/embeddeddolt")
	}
}

func (e Env) ensureDirMode(r *report) {
	const name = ".beads is 0700"
	if modeIs(e.beadsDir(), 0o700) {
		r.ok(name, "")
		return
	}
	if err := os.Chmod(e.beadsDir(), 0o700); err != nil {
		r.fail(name, err.Error(), "chmod 700 .beads")
		return
	}
	r.ok(name, "set")
}

var prefixRE = regexp.MustCompile(`(?m)^issue-prefix:\s*"?([a-z0-9-]*)"?\s*$`)

func (e Env) prefixSet(c *Config) bool {
	b, err := os.ReadFile(filepath.Join(e.beadsDir(), "config.yaml"))
	if err != nil {
		return false
	}
	m := prefixRE.FindSubmatch(b)
	return m != nil && string(m[1]) == c.Prefix
}

// ensurePrefix adds issue-prefix to .beads/config.yaml. Without it every bd
// write fails with "issue_prefix config is missing".
func (e Env) ensurePrefix(r *report, c *Config) {
	const name = "issue prefix"
	if e.prefixSet(c) {
		r.ok(name, c.Prefix)
		return
	}
	p := filepath.Join(e.beadsDir(), "config.yaml")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.fail(name, err.Error(), "")
		return
	}
	if prefixRE.Match(b) {
		r.fail(name, ".beads/config.yaml sets a different issue-prefix", "set issue-prefix: \""+c.Prefix+"\" there, or prefix: in "+ConfigFile)
		return
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, []byte("issue-prefix: \""+c.Prefix+"\"\n")...)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		r.fail(name, err.Error(), "")
		return
	}
	r.ok(name, "added issue-prefix to .beads/config.yaml (commit it)")
}

// ensureRole sets beads.role, which bd otherwise warns about on every
// command, and reports auto-routing, which would move new issues off the
// server into a personal database.
func (e Env) ensureRole(r *report) {
	if out, _ := run(e.RepoRoot, "", "git", "config", "--get", "beads.role"); out == "" {
		if _, err := run(e.RepoRoot, "", "git", "config", "beads.role", "contributor"); err != nil {
			r.warn("git config beads.role", err.Error(), "git config beads.role contributor")
		} else {
			r.ok("git config beads.role", "set to contributor")
		}
	} else {
		r.ok("git config beads.role", out)
	}
	if !have("bd") {
		return
	}
	out, _ := run(e.RepoRoot, "", "bd", "config", "get", "routing.mode")
	f := strings.Fields(out)
	if len(f) > 0 && (f[len(f)-1] == "auto" || f[len(f)-1] == "contributor") {
		r.fail("bd routing.mode", f[len(f)-1]+": new issues would leave the shared server", "bd config unset routing.mode")
		return
	}
	r.ok("bd routing.mode", "unset")
}

// dbPrefix reads the issue prefix stored in the server database: "" when the
// database has never been initialized.
func (e Env) dbPrefix() (string, error) {
	out, err := run(e.RepoRoot, "", "bd", "config", "get", "issue_prefix")
	if err != nil {
		return "", fmt.Errorf("%s", firstLine(err.Error()))
	}
	f := strings.Fields(out)
	if len(f) == 0 || strings.Contains(out, "(not set)") {
		return "", nil
	}
	return f[len(f)-1], nil
}

// ensureSchema initializes an empty server database on first use. provision
// creates the database but not bd's tables, and bd only creates those from
// bd init. bd init in this repository would also rewrite .beads, install git
// hooks and refuse a workspace that already has metadata.json, so it runs in
// a throwaway directory pointed at the same server database instead.
func (e Env) ensureSchema(r *report, c *Config) {
	const name = "server database initialized"
	if !have("bd") {
		return
	}
	p, err := e.dbPrefix()
	switch {
	case err != nil:
		r.fail(name, err.Error(), "beads-remote check")
		return
	case p == c.Prefix:
		r.ok(name, "prefix "+p)
		return
	case p != "":
		r.fail(name, "the server database uses prefix "+p+", not "+c.Prefix, "set prefix: "+p+" in "+ConfigFile)
		return
	}
	tmp, err := os.MkdirTemp("", "beads-remote-init-")
	if err != nil {
		r.fail(name, err.Error(), "")
		return
	}
	defer os.RemoveAll(tmp)
	if _, err := run(tmp, "", "git", "init", "-q"); err != nil {
		r.fail(name, "git init: "+firstLine(err.Error()), "")
		return
	}
	_, err = runEnv([]string{"BEADS_DOLT_PASSWORD=" + e.password(c)}, tmp, "", "bd", "init",
		"--server", "--external", "--non-interactive", "--quiet",
		"--server-host", "127.0.0.1", "--server-port", strconv.Itoa(c.Port),
		"--server-user", c.Database, "--database", c.Database, "--prefix", c.Prefix,
		"--skip-hooks", "--skip-agents", "--role", "contributor")
	if err != nil {
		r.fail(name, "bd init: "+firstLine(err.Error()), "beads-remote server check (admin)")
		return
	}
	if p, _ := e.dbPrefix(); p != c.Prefix {
		r.fail(name, "bd init ran but the database still has no prefix", "beads-remote server check (admin)")
		return
	}
	r.ok(name, "created bd's tables, prefix "+c.Prefix)
}

// Down closes the tunnel.
func Down(c *Config, e Env) int {
	r := &report{env: e}
	if _, err := run(e.RepoRoot, "", "ssh", "-O", "exit", "-S", e.socket(c), c.dest()); err == nil {
		r.ok("tunnel", "closed")
	} else {
		_ = os.Remove(e.socket(c))
		r.ok("tunnel", "was not running")
	}
	return r.finish("beads down")
}

// Status reports whether the tunnel is up. Exit 1 when it is down.
func Status(c *Config, e Env) int {
	r := &report{env: e}
	if e.healthy(c) {
		r.ok("tunnel", fmt.Sprintf("up on 127.0.0.1:%d", c.Port))
		return r.finish(fmt.Sprintf("beads up: %s on 127.0.0.1:%d", c.Database, c.Port))
	}
	r.fail("tunnel", "down", "beads-remote up")
	return r.finish("")
}

// Check verifies the whole client side without changing anything.
func Check(c *Config, e Env) int {
	r := &report{env: e}
	e.check(r, c)
	return r.finish(fmt.Sprintf("beads ok: %s on %s, %d checks", c.Database, c.Server.Host, len(r.results)))
}

func (e Env) check(r *report, c *Config) {
	if have("bd") {
		r.ok("bd installed", "")
	} else {
		r.fail("bd installed", "not on PATH", "install bd: https://github.com/gastownhall/beads")
	}
	if id := e.identity(); id != "" {
		if modeIs(id, 0o600) || modeIs(id, 0o400) {
			r.ok("ssh key", id)
		} else if _, err := os.Stat(id); err != nil {
			r.fail("ssh key", id+" is missing", "beads-remote setup")
		} else {
			r.fail("ssh key", id+" is readable by others", "chmod 600 "+id)
		}
	}
	pinned := false
	if b, err := os.ReadFile(e.knownHosts()); err == nil {
		for _, f := range fingerprints(string(b)) {
			pinned = pinned || f == c.Server.HostKey
		}
	}
	if pinned {
		r.ok("server host key pinned", c.Server.HostKey)
	} else {
		r.fail("server host key pinned", "not yet", "beads-remote up")
	}
	up := e.healthy(c)
	if up {
		r.ok("tunnel", fmt.Sprintf("up on 127.0.0.1:%d", c.Port))
	} else {
		r.fail("tunnel", "down", "beads-remote up")
	}
	if modeIs(e.pwPath(c), 0o600) {
		r.ok("password cached", "0600")
	} else if _, err := os.Stat(e.pwPath(c)); err == nil {
		r.fail("password cached", "mode is not 0600", "chmod 600 "+e.pwPath(c))
	} else {
		r.fail("password cached", "not yet", "beads-remote up")
	}
	if e.credsCurrent(c) {
		r.ok("bd credentials", c.section())
	} else {
		r.fail("bd credentials", c.section()+" missing or stale", "beads-remote up")
	}
	if e.metadataOK(c) {
		r.ok("bd server mode (.beads/metadata.json)", "")
	} else {
		r.fail("bd server mode (.beads/metadata.json)", "missing or not pointing at "+c.Database, "beads-remote up")
	}
	if _, err := os.Stat(filepath.Join(e.beadsDir(), "embeddeddolt")); err == nil {
		r.fail("no local database", ".beads/embeddeddolt exists", "check it holds nothing you need, then rm -rf .beads/embeddeddolt")
	} else {
		r.ok("no local database", "")
	}
	if modeIs(e.beadsDir(), 0o700) {
		r.ok(".beads is 0700", "")
	} else {
		r.fail(".beads is 0700", "", "beads-remote up, or chmod 700 .beads")
	}
	if e.prefixSet(c) {
		r.ok("issue prefix", c.Prefix)
	} else {
		r.fail("issue prefix", "issue-prefix is not "+c.Prefix+" in .beads/config.yaml", "beads-remote up, then commit .beads/config.yaml")
	}
	if !have("bd") {
		return
	}
	out, _ := run(e.RepoRoot, "", "bd", "config", "get", "routing.mode")
	if f := strings.Fields(out); len(f) > 0 && (f[len(f)-1] == "auto" || f[len(f)-1] == "contributor") {
		r.fail("bd routing.mode", f[len(f)-1], "bd config unset routing.mode")
	} else {
		r.ok("bd routing.mode", "unset")
	}
	if !up {
		return
	}
	if p, err := e.dbPrefix(); err != nil {
		r.fail("server database initialized", err.Error(), "beads-remote up")
	} else if p == "" {
		r.fail("server database initialized", "no bd tables yet", "beads-remote up")
	} else if p != c.Prefix {
		r.fail("server database initialized", "prefix "+p+", not "+c.Prefix, "set prefix: "+p+" in "+ConfigFile)
	} else {
		r.ok("server database initialized", "prefix "+p)
	}
	if _, err := run(e.RepoRoot, "", "bd", "ready", "--json"); err != nil {
		r.fail("bd reads the database", firstLine(err.Error()), "beads-remote server check (admin), then beads-remote up")
	} else {
		r.ok("bd reads the database", c.Database)
	}
}
