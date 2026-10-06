package remote

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env is everything outside the config: where things live and how to talk.
type Env struct {
	Home     string    // the user's home directory
	RepoRoot string    // the repository using the database
	In       io.Reader // answers to prompts
	Out      io.Writer
	Color    bool // green ✓, red ✗, yellow ⚠
	JSON     bool // one JSON document instead of lines, for agents
	Verbose  bool // print passing lines too
	Yes      bool // accept every default without asking
	Repin    bool // accept a changed server host key in the config
}

// State lives outside the repository, per user.
func (e Env) stateDir() string     { return filepath.Join(e.Home, ".config", "beads-remote") }
func (e Env) knownHosts() string   { return filepath.Join(e.stateDir(), "known_hosts") }
func (e Env) identityFile() string { return filepath.Join(e.stateDir(), "identity") }
func (e Env) socket(c *Config) string {
	return filepath.Join(e.stateDir(), "cm-"+c.Database+"@"+c.Server.Host)
}
func (e Env) pwPath(c *Config) string {
	return filepath.Join(e.stateDir(), c.Database+"@"+c.Server.Host+".pw")
}

// bd reads BEADS_CREDENTIALS_FILE, else ~/.config/beads/credentials.
func (e Env) credsFile() string {
	if f := os.Getenv("BEADS_CREDENTIALS_FILE"); f != "" {
		return f
	}
	return filepath.Join(e.Home, ".config", "beads", "credentials")
}
func (e Env) beadsDir() string { return filepath.Join(e.RepoRoot, ".beads") }

// Result is one line of output: a check, or a step that ran.
type Result struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// report collects results. Failures and warnings print as they happen;
// passes print only when verbose; JSON mode prints one document at the end.
type report struct {
	env     Env
	results []Result
}

func (r *report) add(res Result) bool {
	r.results = append(r.results, res)
	if !r.env.JSON {
		switch {
		case !res.OK:
			r.line("✗", "31", res)
		case res.Warn:
			r.line("⚠", "33", res)
		case r.env.Verbose:
			r.line("✓", "32", res)
		}
	}
	return res.OK
}

func (r *report) ok(name, detail string) bool {
	return r.add(Result{Name: name, OK: true, Detail: detail})
}
func (r *report) fail(name, detail, fix string) bool {
	return r.add(Result{Name: name, Detail: detail, Fix: fix})
}
func (r *report) warn(name, detail, fix string) bool {
	return r.add(Result{Name: name, OK: true, Warn: true, Detail: detail, Fix: fix})
}

func (r *report) line(mark, color string, res Result) {
	if r.env.Color {
		mark = "\033[" + color + "m" + mark + "\033[0m"
	}
	s := mark + " " + res.Name
	if res.Detail != "" {
		s += ": " + res.Detail
	}
	if res.Fix != "" {
		s += "\n    fix: " + res.Fix
	}
	fmt.Fprintln(r.env.Out, s)
}

// say prints a plain line (never in JSON mode).
func (r *report) say(format string, a ...any) {
	if !r.env.JSON {
		fmt.Fprintf(r.env.Out, format+"\n", a...)
	}
}

func (r *report) failed() int {
	n := 0
	for _, res := range r.results {
		if !res.OK {
			n++
		}
	}
	return n
}

// finish prints one summary line, or the JSON document, and returns the exit
// code: 0 when nothing failed, 1 otherwise.
func (r *report) finish(okLine string) int {
	n := r.failed()
	if r.env.JSON {
		enc := json.NewEncoder(r.env.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			OK      bool     `json:"ok"`
			Results []Result `json:"results"`
		}{n == 0, r.results})
	} else if n == 0 {
		r.line("✓", "32", Result{Name: okLine})
	} else {
		r.line("✗", "31", Result{Name: fmt.Sprintf("%d of %d failed; fix the first one first", n, len(r.results))})
	}
	if n > 0 {
		return 1
	}
	return 0
}

// run executes a command and returns its trimmed stdout. stderr is returned
// in the error, never printed, so nothing secret leaks through it.
func run(dir, stdin, name string, args ...string) (string, error) {
	return runEnv(nil, dir, stdin, name, args...)
}

// runEnv is run with extra environment variables. A secret passed this way
// reaches only the child process, never its argv.
func runEnv(env []string, dir, stdin, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(out.String()), fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func have(name string) bool { _, err := exec.LookPath(name); return err == nil }

func modeIs(path string, want os.FileMode) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm() == want
}

// writePrivate writes through a temporary file, so a reader never sees a
// partial file and the mode is 0600 from the first byte.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
