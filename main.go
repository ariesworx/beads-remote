// Command beads-remote connects a repository to its database on a shared,
// remote beads (bd) Dolt server over an SSH tunnel, and provisions databases
// and developer keys on that server.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ariesworx/beads-remote/internal/remote"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `beads-remote connects this repository to its database on a shared beads server.

Developer:
  setup [--key PATH]     connect this repository, asking only for what it can't find
  up                     open the tunnel and make local files right (idempotent)
  down                   close the tunnel
  status                 is the tunnel up? (exit 1 if not)
  check                  verify the whole setup, read-only

Server admin (needs ssh to server.admin with passwordless sudo):
  server provision       create or repair this repository's database, account and grants
  server check           the same checks, read-only
  server add-key PUB     authorize a developer's public key for the tunnel only
  server revoke PATTERN  remove keys matching a key blob or comment
  server keys            list authorized keys

New repository:
  init [--host H] [--database D] [--port N]   write .beads/remote.yaml

Flags (any command): -C DIR  --json  -v  --yes  --no-color
Output: one line per failure or warning, then one summary line; -v shows passes;
--json prints one document for agents. Exit 0 ok, 1 failed, 2 usage.
`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("beads-remote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("C", ".", "")
	jsonOut := fs.Bool("json", false, "")
	verbose := fs.Bool("v", false, "")
	yesAll := fs.Bool("yes", false, "")
	noColor := fs.Bool("no-color", false, "")
	key := fs.String("key", "", "")
	host := fs.String("host", "", "")
	database := fs.String("database", "", "")
	port := fs.Int("port", 0, "")

	// Flags may come before or after the command words.
	var words []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintf(stderr, "beads-remote: %v\n\n%s", err, usage)
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(words) == 0 || words[0] == "help" || words[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if words[0] == "version" {
		fmt.Fprintln(stdout, "beads-remote", version)
		return 0
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "beads-remote:", err)
		return 1
	}
	env := remote.Env{
		Home: home, In: stdin, Out: stdout,
		Color: !*noColor && !*jsonOut && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && isTerminal(stdout),
		JSON:  *jsonOut, Verbose: *verbose, Yes: *yesAll,
	}

	if words[0] == "init" {
		env.RepoRoot = *dir
		return remote.Init(*dir, env, *host, *database, *port)
	}

	cfg, root, err := remote.Load(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "beads-remote:", err)
		return 1
	}
	env.RepoRoot = root

	cmd := strings.Join(words, " ")
	switch {
	case cmd == "setup":
		return remote.Setup(cfg, env, *key)
	case cmd == "up":
		return remote.Up(cfg, env)
	case cmd == "down":
		return remote.Down(cfg, env)
	case cmd == "status":
		return remote.Status(cfg, env)
	case cmd == "check":
		return remote.Check(cfg, env)
	case cmd == "server provision":
		return remote.Provision(cfg, env)
	case cmd == "server check":
		return remote.ServerCheck(cfg, env)
	case cmd == "server keys":
		return remote.Keys(cfg, env)
	case len(words) == 3 && words[0] == "server" && words[1] == "add-key":
		return remote.AddKey(cfg, env, words[2])
	case len(words) == 3 && words[0] == "server" && words[1] == "revoke":
		return remote.Revoke(cfg, env, words[2])
	}
	fmt.Fprintf(stderr, "beads-remote: unknown command %q\n\n%s", cmd, usage)
	return 2
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
