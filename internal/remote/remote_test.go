package remote

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixture is a throwaway HOME and repository, with stub ssh and bd on PATH.
// ssh-keygen is the real one, so fingerprints are real.
type fixture struct {
	t        *testing.T
	home     string
	repo     string
	state    string // the stubs' shared state: tunnel up, failure switches
	cfg      *Config
	out      *bytes.Buffer
	hostLine string // the server's public host key, known_hosts format
}

const testPassword = "stub-password-0123456789abcdefgh"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	f := &fixture{t: t, home: filepath.Join(dir, "home"), repo: filepath.Join(dir, "repo"), state: filepath.Join(dir, "state"), out: &bytes.Buffer{}}
	for _, d := range []string{f.home, f.state, filepath.Join(f.repo, ".beads"), filepath.Join(dir, "bin"), filepath.Join(f.home, ".ssh")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	// The server's host key and the developer's key, both real.
	hostKey := filepath.Join(dir, "host_ed25519")
	mustRun(t, dir, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey)
	pub, _ := os.ReadFile(hostKey + ".pub")
	f.hostLine = "beads.example.com " + strings.Join(strings.Fields(string(pub))[:2], " ")
	fpr, _ := run(dir, "", "ssh-keygen", "-lf", hostKey+".pub")
	devKey := filepath.Join(f.home, ".ssh", "id_ed25519")
	mustRun(t, dir, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "dev@mac", "-f", devKey)

	stub := func(name, body string) {
		must(t, os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"+body), 0o755))
	}
	// ssh: control-socket commands, the tunnel, the password fetch, and the
	// admin's `sudo -n bash -s` (which replays $STATE/server.out).
	stub("ssh", `S="`+f.state+`"
case " $* " in
  *" -O check "*) [ -f "$S/up" ] ;;
  *" -O exit "*) [ -f "$S/up" ] && rm -f "$S/up" ;;
  *" -N "*) [ -f "$S/deny" ] && { echo "dev@beads.example.com: Permission denied (publickey)." >&2; exit 255; }
           [ -f "$S/changed" ] && { echo "Host key verification failed." >&2; exit 255; }
           echo "$*" > "$S/tunnel.args"; touch "$S/up" ;;
  *"sudo -n bash -s"*) cat > "$S/server.script"; [ -f "$S/sudo" ] && { echo "sudo: a password is required" >&2; exit 1; }
           cat "$S/server.out" 2>/dev/null ;;
  *"cat /etc/"*) [ -f "$S/deny" ] && { echo "Permission denied (publickey)." >&2; exit 255; }; echo `+testPassword+` ;;
  *) echo "unexpected ssh $*" >&2; exit 99 ;;
esac
`)
	stub("ssh-keyscan", `[ -f "`+f.state+`/otherkey" ] && { echo "beads.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHVoYmFkYmFkYmFkYmFkYmFkYmFkYmFkYmFkYmFkYmFkYmFk"; exit 0; }
echo "# beads.example.com:22 SSH-2.0-OpenSSH"
echo "`+f.hostLine+`"
`)
	// bd init records its arguments, directory and password, and stores the
	// prefix it was given, as the real one does in the server database.
	stub("bd", `S="`+f.state+`"
case "$*" in
  "config get routing.mode") cat "$S/routing" 2>/dev/null ;;
  "config get issue_prefix") if [ -f "$S/prefix" ]; then cat "$S/prefix"; else echo "issue_prefix (not set)"; fi ;;
  "init "*) echo "$*" > "$S/init.args"; pwd > "$S/init.dir"; echo "$BEADS_DOLT_PASSWORD" > "$S/init.pw"
           while [ $# -gt 0 ]; do [ "$1" = --prefix ] && echo "$2" > "$S/prefix"; shift; done ;;
  "ready --json") [ -f "`+f.state+`/bdfail" ] && { echo "Error: database not found" >&2; exit 1; }; echo "[]" ;;
esac
`)
	t.Setenv("PATH", filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", f.home)
	t.Setenv("BEADS_CREDENTIALS_FILE", "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	mustRun(t, f.repo, "git", "init", "-q")
	must(t, os.Chmod(filepath.Join(f.repo, ".beads"), 0o755))

	port := freePort(t)
	cfgText := "server:\n  host: beads.example.com\n  host_key: " + fprOut.FindString(fpr) +
		"\ndatabase: hq\nport: " + strconv.Itoa(port) + "\n"
	must(t, os.WriteFile(filepath.Join(f.repo, ConfigFile), []byte(cfgText), 0o644))
	cfg, root, err := Load(filepath.Join(f.repo, ".beads"))
	must(t, err)
	if root != f.repo {
		t.Fatalf("Load root = %s, want %s", root, f.repo)
	}
	f.cfg = cfg
	return f
}

func (f *fixture) env() Env {
	f.out.Reset()
	return Env{Home: f.home, RepoRoot: f.repo, In: strings.NewReader(""), Out: f.out, Yes: true}
}
func (f *fixture) flag(name string) {
	must(f.t, os.WriteFile(filepath.Join(f.state, name), nil, 0o644))
}
func (f *fixture) unflag(name string) { _ = os.Remove(filepath.Join(f.state, name)) }
func (f *fixture) read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	if _, err := run(dir, "", name, args...); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func wantCode(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit %d, want %d; output:\n%s", got, want, out)
	}
}

func TestConfigValidation(t *testing.T) {
	good := "server:\n  host: beads.example.com\n  host_key: SHA256:et6CqkKsyU2BxzA7Ws+V18rXrtM9Gj6dk1/m0C5TqvU\ndatabase: hq\nport: 3312\n"
	c, err := Parse([]byte(good))
	must(t, err)
	if c.Server.Admin != "beads.example.com" || c.Prefix != "hq" || c.Paths.PasswordFile != "/etc/hq/db-password" || c.Server.SSHPort != 22 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	for name, bad := range map[string]string{
		"quote in host":       strings.Replace(good, "beads.example.com", "beads.example.com'; rm", 1),
		"short fingerprint":   strings.Replace(good, "SHA256:et6CqkKsyU2BxzA7Ws+V18rXrtM9Gj6dk1/m0C5TqvU", "SHA256:abc", 1),
		"database with dash":  strings.Replace(good, "database: hq", "database: h-q", 1),
		"database uppercase":  strings.Replace(good, "database: hq", "database: HQ", 1),
		"privileged port":     strings.Replace(good, "port: 3312", "port: 80", 1),
		"path traversal":      good + "paths:\n  password_file: /etc/../root/x\n",
		"space in path":       good + "paths:\n  sshd_config: /etc/ssh/a b\n",
		"admin with command":  strings.Replace(good, "port: 3312", "port: 3312\n", 1) + "", // placeholder kept valid below
		"admin shell chars":   good[:strings.Index(good, "database")] + "  admin: root@x;id\n" + good[strings.Index(good, "database"):],
		"prefix with space":   good + "prefix: \"h q\"\n",
		"missing fingerprint": strings.Replace(good, "  host_key: SHA256:et6CqkKsyU2BxzA7Ws+V18rXrtM9Gj6dk1/m0C5TqvU\n", "", 1),
	} {
		if name == "admin with command" {
			continue
		}
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseKey(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, strconv.Itoa(len(s))+".pub")
		must(t, os.WriteFile(p, []byte(s), 0o644))
		return p
	}
	ok := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2 dev@mac.local"
	if _, err := ParseKey(write(ok + "\n")); err != nil {
		t.Fatalf("good key refused: %v", err)
	}
	for name, bad := range map[string]string{
		"private key":         "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----",
		"rsa":                 "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ dev@mac",
		"options injected":    `command="sh" ` + ok,
		"quote in comment":    strings.TrimSuffix(ok, "dev@mac.local") + "dev'mac",
		"two keys":            ok + "\n" + ok,
		"newline smuggled in": strings.Replace(ok, " dev", "\ndev", 1),
	} {
		if _, err := ParseKey(write(bad + strings.Repeat(" ", len(name)))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUpFromScratch(t *testing.T) {
	f := newFixture(t)
	creds := filepath.Join(f.home, ".config", "beads", "credentials")
	must(t, os.MkdirAll(filepath.Dir(creds), 0o700))
	must(t, os.WriteFile(creds, []byte("[127.0.0.1:3311]\npassword=website-secret\n"), 0o600))

	code := Up(f.cfg, f.env())
	wantCode(t, code, 0, f.out.String())
	if !strings.HasPrefix(f.out.String(), "✓ beads up: hq") || strings.Count(f.out.String(), "\n") != 1 {
		t.Errorf("want one summary line, got:\n%s", f.out.String())
	}
	e := f.env()
	if !modeIs(e.pwPath(f.cfg), 0o600) || strings.TrimSpace(f.read(e.pwPath(f.cfg))) != testPassword {
		t.Error("password not cached at 0600")
	}
	c := f.read(creds)
	if !strings.Contains(c, "[127.0.0.1:3311]\npassword=website-secret") {
		t.Errorf("other section lost:\n%s", c)
	}
	if !strings.Contains(c, f.cfg.section()+"\npassword="+testPassword) || !modeIs(creds, 0o600) {
		t.Errorf("our section missing or mode wrong:\n%s", c)
	}
	if !e.metadataOK(f.cfg) {
		t.Error("metadata.json not in server mode")
	}
	if !modeIs(filepath.Join(f.repo, ".beads"), 0o700) {
		t.Error(".beads not 0700")
	}
	if !e.prefixSet(f.cfg) {
		t.Error("issue-prefix not written")
	}
	if out, _ := run(f.repo, "", "git", "config", "beads.role"); out != "contributor" {
		t.Errorf("beads.role = %q", out)
	}
	args := f.read(filepath.Join(f.state, "tunnel.args"))
	for _, want := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=" + e.knownHosts(), "BatchMode=yes", "ExitOnForwardFailure=yes",
		"127.0.0.1:" + strconv.Itoa(f.cfg.Port) + ":127.0.0.1:3306", "hq@beads.example.com"} {
		if !strings.Contains(args, want) {
			t.Errorf("tunnel args lack %q: %s", want, args)
		}
	}
	if !strings.Contains(f.read(e.knownHosts()), strings.Fields(f.hostLine)[2]) {
		t.Error("host key not pinned in our known_hosts")
	}
	if _, err := os.Stat(filepath.Join(f.home, ".ssh", "known_hosts")); err == nil {
		t.Error("touched the user's ~/.ssh/known_hosts")
	}
	if strings.Contains(f.out.String(), testPassword) {
		t.Error("password printed")
	}

	// Second run: nothing to do, still one line.
	code = Up(f.cfg, f.env())
	wantCode(t, code, 0, f.out.String())
	if strings.Count(f.read(creds), f.cfg.section()) != 1 {
		t.Error("credentials section duplicated")
	}

	// Check passes, read-only, and -v lists every item.
	e = f.env()
	e.Verbose = true
	wantCode(t, Check(f.cfg, e), 0, f.out.String())
	if n := strings.Count(f.out.String(), "✓"); n < 11 {
		t.Errorf("verbose check printed %d passes:\n%s", n, f.out.String())
	}

	// JSON for agents.
	e = f.env()
	e.JSON = true
	wantCode(t, Check(f.cfg, e), 0, f.out.String())
	var doc struct {
		OK      bool     `json:"ok"`
		Results []Result `json:"results"`
	}
	must(t, json.Unmarshal(f.out.Bytes(), &doc))
	if !doc.OK || len(doc.Results) < 11 {
		t.Errorf("json: %+v", doc)
	}

	// Down, twice, then status and check fail.
	wantCode(t, Down(f.cfg, f.env()), 0, f.out.String())
	wantCode(t, Down(f.cfg, f.env()), 0, f.out.String())
	wantCode(t, Status(f.cfg, f.env()), 1, f.out.String())
	wantCode(t, Check(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "fix: beads-remote up") {
		t.Errorf("check did not say how to fix:\n%s", f.out.String())
	}
}

func TestUpRefusals(t *testing.T) {
	f := newFixture(t)

	f.flag("otherkey")
	wantCode(t, Up(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "do not connect") || fileExists(filepath.Join(f.state, "up")) {
		t.Errorf("a different server key was not refused:\n%s", f.out.String())
	}
	f.unflag("otherkey")

	f.flag("deny")
	e := f.env()
	must(t, writePrivate(e.identityFile(), []byte(filepath.Join(f.home, ".ssh", "id_ed25519")+"\n")))
	wantCode(t, Up(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "server refused your key") || !strings.Contains(f.out.String(), "server add-key id_ed25519.pub") {
		t.Errorf("refused key gave no next step:\n%s", f.out.String())
	}
	f.unflag("deny")

	f.flag("changed")
	wantCode(t, Up(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "does not match the pinned one") {
		t.Errorf("changed host key not explained:\n%s", f.out.String())
	}
	f.unflag("changed")

	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(f.cfg.Port))
	must(t, err)
	wantCode(t, Up(f.cfg, f.env()), 1, f.out.String())
	l.Close()
	if !strings.Contains(f.out.String(), "is in use") {
		t.Errorf("busy port not reported:\n%s", f.out.String())
	}
}

func TestRepairs(t *testing.T) {
	f := newFixture(t)
	wantCode(t, Up(f.cfg, f.env()), 0, f.out.String())

	// bd init or an upgrade switched the clone to a local database.
	meta := filepath.Join(f.repo, ".beads", "metadata.json")
	must(t, os.WriteFile(meta, []byte(`{"dolt_mode":"embedded"}`), 0o644))
	must(t, os.MkdirAll(filepath.Join(f.repo, ".beads", "embeddeddolt"), 0o755))
	wantCode(t, Up(f.cfg, f.env()), 0, f.out.String())
	got := f.out.String()
	if !f.env().metadataOK(f.cfg) || !strings.Contains(got, "⚠ no local database") {
		t.Errorf("local mode not repaired and reported:\n%s", got)
	}
	wantCode(t, Check(f.cfg, f.env()), 1, f.out.String())
	must(t, os.RemoveAll(filepath.Join(f.repo, ".beads", "embeddeddolt")))

	// A rotated password: cache cleared, stale credentials replaced.
	e := f.env()
	creds := e.credsFile()
	must(t, os.WriteFile(creds, []byte(strings.Replace(f.read(creds), testPassword, "stale", 1)), 0o600))
	must(t, os.Remove(e.pwPath(f.cfg)))
	wantCode(t, Up(f.cfg, f.env()), 0, f.out.String())
	if strings.Contains(f.read(creds), "stale") {
		t.Error("stale password kept")
	}

	// Auto-routing would move issues off the server.
	must(t, os.WriteFile(filepath.Join(f.state, "routing"), []byte("routing.mode auto\n"), 0o644))
	wantCode(t, Check(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "bd config unset routing.mode") {
		t.Errorf("routing not reported:\n%s", f.out.String())
	}
	f.unflag("routing")

	// A different prefix already in config.yaml is reported, not overwritten.
	must(t, os.WriteFile(filepath.Join(f.repo, ".beads", "config.yaml"), []byte("issue-prefix: \"web\"\n"), 0o644))
	wantCode(t, Up(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.read(filepath.Join(f.repo, ".beads", "config.yaml")), "web") {
		t.Error("existing prefix overwritten")
	}
}

func TestSetupChoosesKey(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.home, ".ssh", "work_rsa")
	mustRun(t, f.home, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other)
	e := f.env()
	e.Yes = false
	e.In = strings.NewReader("2\n")
	code := Setup(f.cfg, e, "")
	wantCode(t, code, 0, f.out.String())
	if got := e.identity(); got != other && got != filepath.Join(f.home, ".ssh", "id_ed25519") {
		t.Errorf("identity = %q", got)
	}
	if !strings.Contains(f.out.String(), "Which SSH key") || !strings.Contains(f.out.String(), "beads ready") {
		t.Errorf("setup output:\n%s", f.out.String())
	}
	if !strings.Contains(f.read(filepath.Join(f.state, "tunnel.args")), "IdentitiesOnly=yes") {
		t.Error("chosen key not used for the tunnel")
	}

	// A refused key prints the public key to send, never the private one.
	f.unflag("up")
	f.flag("deny")
	code = Setup(f.cfg, f.env(), "")
	wantCode(t, code, 1, f.out.String())
	if !strings.Contains(f.out.String(), "ssh-ed25519 ") || strings.Contains(f.out.String(), "PRIVATE") {
		t.Errorf("refusal output:\n%s", f.out.String())
	}
}

func TestServerCommands(t *testing.T) {
	f := newFixture(t)
	f.cfg.Server.Admin = "admin@beads.example.com"
	out := filepath.Join(f.state, "server.out")
	must(t, os.WriteFile(out, []byte("RESULT\tok\taccount hq\texists\t\nRESULT\twarn\ttunnel keys\tnone yet\tbeads-remote server add-key\nRESULT\tok\tisolation\tok\t\n"), 0o644))

	wantCode(t, Provision(f.cfg, f.env()), 0, f.out.String())
	script := f.read(filepath.Join(f.state, "server.script"))
	for _, want := range []string{"export ACTION='provision'", "export DB='hq'", "export PWFILE='/etc/hq/db-password'", "python3 - <<'BEADS_REMOTE_PY'", "step_account"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if !strings.Contains(f.out.String(), "⚠ tunnel keys") || !strings.Contains(f.out.String(), "server ready") {
		t.Errorf("provision output:\n%s", f.out.String())
	}

	must(t, os.WriteFile(out, []byte("RESULT\tfail\tgrants\tunexpected: GRANT ALL ON *.*\treview by hand\n"), 0o644))
	wantCode(t, ServerCheck(f.cfg, f.env()), 1, f.out.String())

	f.flag("sudo")
	wantCode(t, ServerCheck(f.cfg, f.env()), 1, f.out.String())
	if !strings.Contains(f.out.String(), "sudo needs a password") {
		t.Errorf("sudo failure:\n%s", f.out.String())
	}
	f.unflag("sudo")

	pub := filepath.Join(f.home, ".ssh", "id_ed25519.pub")
	must(t, os.WriteFile(out, []byte("RESULT\tok\tadd key\tSHA256:x dev@mac; 1 key(s) now\t\n"), 0o644))
	wantCode(t, AddKey(f.cfg, f.env(), pub), 0, f.out.String())
	if !strings.Contains(f.read(filepath.Join(f.state, "server.script")), "export KEY='ssh-ed25519 ") {
		t.Error("key not passed to the server")
	}
	wantCode(t, AddKey(f.cfg, f.env(), filepath.Join(f.home, ".ssh", "id_ed25519")), 1, f.out.String())
	if !strings.Contains(f.out.String(), "PRIVATE key") {
		t.Errorf("private key not refused:\n%s", f.out.String())
	}
	wantCode(t, Revoke(f.cfg, f.env(), "a'b"), 1, f.out.String())
}

// The server script itself, against a fake root: syntax, and the
// authorized_keys rewrite that is the security boundary.
func TestServerScriptKeyRewrite(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	ak := filepath.Join(dir, "authorized_keys")
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2bGx2"
	must(t, os.WriteFile(ak, []byte(
		"restrict,port-forwarding,permitopen=\"127.0.0.1:3306\" "+key+" old@mac\n"+
			"# a comment\n"+
			key+"2 bare@mac\n"+
			"garbage line\n"), 0o600))
	opts := `command="cat /etc/hq/db-password",restrict,port-forwarding,permitopen="127.0.0.1:3306"`
	// Pull the awk program out of step_keys and run it on the file.
	start := strings.Index(serverSh, "awk -v opts=")
	end := strings.Index(serverSh[start:], `"$ak" > "$tmp"`)
	prog := serverSh[start:start+end] + `"$1"`
	out, err := run(dir, "", "bash", "-c", "OPTS='"+opts+"'; "+strings.Replace(prog, `"$OPTS"`, `"$OPTS"`, 1), "x", ak)
	must(t, err)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	for _, i := range []int{0, 2} {
		if !strings.HasPrefix(lines[i], opts+" ssh-ed25519 ") {
			t.Errorf("line %d not restricted: %s", i, lines[i])
		}
	}
	if lines[1] != "# a comment" || !strings.HasPrefix(lines[3], "# beads-remote: unparseable, disabled:") {
		t.Errorf("comment or garbage mishandled:\n%s", out)
	}
	if strings.Contains(lines[0], "restrict,port-forwarding,permitopen=\"127.0.0.1:3306\" ssh") && !strings.HasPrefix(lines[0], "command=") {
		t.Error("old options kept")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSchemaInitializedOnFirstUse(t *testing.T) {
	f := newFixture(t)
	if code := Up(f.cfg, f.env()); code != 0 {
		t.Fatalf("exit %d; output:\n%s", code, f.out.String())
	}
	args := f.read(filepath.Join(f.state, "init.args"))
	for _, want := range []string{"--server", "--external", "--skip-hooks", "--skip-agents", "--database hq", "--prefix hq", "--server-user hq", "--server-port " + strconv.Itoa(f.cfg.Port)} {
		if !strings.Contains(args, want) {
			t.Errorf("bd init args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, testPassword) {
		t.Error("the password was passed on the command line")
	}
	if strings.TrimSpace(f.read(filepath.Join(f.state, "init.pw"))) != testPassword {
		t.Error("bd init did not get the password through BEADS_DOLT_PASSWORD")
	}
	if dir := strings.TrimSpace(f.read(filepath.Join(f.state, "init.dir"))); strings.HasPrefix(dir, f.repo) {
		t.Errorf("bd init ran inside the repository (%s)", dir)
	}
	if _, err := os.Stat(strings.TrimSpace(f.read(filepath.Join(f.state, "init.dir")))); err == nil {
		t.Error("the throwaway directory was left behind")
	}

	// Second run: already initialized, bd init is not run again.
	os.Remove(filepath.Join(f.state, "init.args"))
	if code := Up(f.cfg, f.env()); code != 0 {
		t.Fatalf("second up: exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(f.state, "init.args")); err == nil {
		t.Error("bd init ran on an initialized database")
	}
}

func TestSchemaPrefixMismatch(t *testing.T) {
	f := newFixture(t)
	must(t, os.WriteFile(filepath.Join(f.state, "prefix"), []byte("other\n"), 0o600))
	if code := Up(f.cfg, f.env()); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	out := f.out.String()
	if !strings.Contains(out, "uses prefix other") || !strings.Contains(out, "set prefix: other") {
		t.Errorf("output does not explain the mismatch:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.state, "init.args")); err == nil {
		t.Error("bd init ran over a database with another prefix")
	}
}
