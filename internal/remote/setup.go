package remote

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ask prints a question and returns the answer, or def when the user just
// presses return, input has ended, or Yes is set.
func (e Env) ask(in *bufio.Reader, q, def string) string {
	if e.Yes || e.JSON {
		return def
	}
	if def != "" {
		fmt.Fprintf(e.Out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(e.Out, "%s: ", q)
	}
	line, err := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" || err != nil && line == "" {
		return def
	}
	return line
}

func yes(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// Setup walks a developer through connecting this repository: it picks or
// creates an SSH key, pins the server, opens the tunnel and writes every local
// file. It asks only for what it cannot work out. Safe to re-run.
func Setup(c *Config, e Env, key string) int {
	r := &report{env: e}
	in := bufio.NewReader(e.In)
	r.say("Connecting %s to database %s on %s.", filepath.Base(e.RepoRoot), c.Database, c.Server.Host)

	if have("bd") {
		r.ok("bd installed", "")
	} else {
		r.fail("bd installed", "not on PATH", "install bd: https://github.com/gastownhall/beads, then re-run setup")
	}
	if !e.chooseKey(r, in, key) {
		return r.finish("")
	}
	if !e.up(r, c) {
		if pub := pubFor(e.identity()); pub != "" {
			if b, err := os.ReadFile(pub); err == nil {
				r.say("\nYour public key (safe to share; send it to the server admin):\n\n%s", strings.TrimSpace(string(b)))
			}
		}
		return r.finish("")
	}
	e.local(r, c)
	if r.failed() == 0 && have("bd") {
		if _, err := run(e.RepoRoot, "", "bd", "ready", "--json"); err != nil {
			r.fail("bd reads the database", firstLine(err.Error()), "ask the server admin to run: beads-remote server check")
		} else {
			r.ok("bd reads the database", c.Database)
		}
	}
	return r.finish(fmt.Sprintf("beads ready: %s on %s through 127.0.0.1:%d", c.Database, c.Server.Host, c.Port))
}

// chooseKey settles which private key connects to the server, and remembers
// it in ~/.config/beads-remote/identity.
func (e Env) chooseKey(r *report, in *bufio.Reader, key string) bool {
	const name = "ssh key"
	if key == "" {
		key = e.identity()
	}
	if key == "" {
		cands := e.candidateKeys()
		switch {
		case len(cands) == 1:
			key = cands[0]
		case len(cands) > 1:
			r.say("Which SSH key should connect to the server?")
			for i, k := range cands {
				r.say("  %d) %s", i+1, k)
			}
			a := e.ask(in, "Number", "1")
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 || n > len(cands) {
				return r.fail(name, "no key chosen", "re-run with --key ~/.ssh/<key>")
			}
			key = cands[n-1]
		default:
			def := filepath.Join(e.Home, ".ssh", "id_ed25519")
			if !yes(e.ask(in, "No SSH key found. Create "+def+" now? (y/n)", "y")) {
				return r.fail(name, "none", "create one with ssh-keygen -t ed25519, then re-run setup")
			}
			if err := e.createKey(def); err != nil {
				return r.fail(name, err.Error(), "create one with ssh-keygen -t ed25519, then re-run setup")
			}
			key = def
		}
	}
	key = expandHome(key, e.Home)
	key = strings.TrimSuffix(key, ".pub")
	if _, err := os.Stat(key); err != nil {
		return r.fail(name, key+" not found", "pass the private key with --key")
	}
	if _, err := os.Stat(key + ".pub"); err != nil {
		return r.fail(name, key+".pub not found", "ssh-keygen -y -f "+key+" > "+key+".pub")
	}
	if !modeIs(key, 0o600) && !modeIs(key, 0o400) {
		return r.fail(name, key+" is readable by others", "chmod 600 "+key)
	}
	if err := writePrivate(e.identityFile(), []byte(key+"\n")); err != nil {
		return r.fail(name, err.Error(), "check permissions on "+e.stateDir())
	}
	return r.ok(name, key)
}

// candidateKeys lists private keys in ~/.ssh that have a .pub beside them,
// ED25519 first.
func (e Env) candidateKeys() []string {
	pubs, _ := filepath.Glob(filepath.Join(e.Home, ".ssh", "*.pub"))
	var out []string
	for _, p := range pubs {
		priv := strings.TrimSuffix(p, ".pub")
		if _, err := os.Stat(priv); err == nil {
			out = append(out, priv)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Contains(out[i], "ed25519") && !strings.Contains(out[j], "ed25519")
	})
	return out
}

// createKey runs ssh-keygen in the terminal so the user sets a passphrase,
// then adds the key to the agent (and, on macOS, the keychain) so BatchMode
// connections can use it.
func (e Env) createKey(path string) error {
	host, _ := os.Hostname()
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-C", os.Getenv("USER")+"@"+host+" beads") //nolint:gosec // argv, no shell; USER only labels the key
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	add := []string{path}
	if runtime.GOOS == "darwin" {
		add = []string{"--apple-use-keychain", path}
	}
	cmd = exec.Command("ssh-add", add...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Init writes .beads/remote.yaml for a repository that has none. It shows the
// server's fingerprint and asks the user to confirm it out of band, so the
// pin is never trust-on-first-use.
func Init(dir string, e Env, host, database string, port int) int {
	r := &report{env: e}
	in := bufio.NewReader(e.In)
	p := filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(p); err == nil {
		r.fail("init", ConfigFile+" already exists", "edit it, or delete it first")
		return r.finish("")
	}
	if host == "" {
		host = e.ask(in, "Server host name", "")
	}
	if database == "" {
		database = strings.ReplaceAll(strings.ToLower(filepath.Base(dir)), "-", "_")
		database = e.ask(in, "Database name", database)
	}
	if port == 0 {
		port = 3311
		for !portFree(port) && port < 3399 {
			port++
		}
		if a := e.ask(in, "Local port for the tunnel", strconv.Itoa(port)); a != "" {
			port, _ = strconv.Atoi(a)
		}
	}
	scanned, _ := run("", "", "ssh-keyscan", "-t", "ed25519", "-T", "10", host)
	fps := fingerprints(scanned)
	if len(fps) != 1 {
		r.fail("server host key", "could not read an ED25519 key from "+host, "check the host name and your network")
		return r.finish("")
	}
	r.say("%s presents ED25519 key %s", host, fps[0])
	if !yes(e.ask(in, "Does that match the fingerprint the server admin gave you? (y/n)", "n")) {
		r.fail("server host key", "not confirmed", "get the fingerprint from the server admin (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server), then re-run")
		return r.finish("")
	}
	body := fmt.Sprintf(Template, host, fps[0], host, database, port, strings.ReplaceAll(database, "_", "-"))
	if _, err := Parse([]byte(body)); err != nil {
		r.fail("init", err.Error(), "")
		return r.finish("")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		r.fail("init", err.Error(), "")
		return r.finish("")
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil { //nolint:gosec // .beads/remote.yaml is committed, not secret
		r.fail("init", err.Error(), "")
		return r.finish("")
	}
	r.ok("init", "wrote "+ConfigFile)
	return r.finish("wrote " + ConfigFile + "; commit it, ask the server admin to run `beads-remote server provision`, then run `beads-remote setup`")
}
