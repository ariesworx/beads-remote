// Command mcpe2e drives `beads-remote mcp` over real stdio, as an MCP client
// would, through one issue's life: create, ready, claim, comment, close.
// test/e2e.sh runs it against a real server with the tunnel closed, so the
// first call must open it.
//
//	mcpe2e BIN REPO
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mcpe2e BIN REPO")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "mcpe2e:", err)
		os.Exit(1)
	}
}

type issue struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func run(bin, repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.Command(bin, "-C", repo, "mcp") //nolint:gosec // a test driver: runs the binary it is given
	cmd.Stderr = os.Stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcpe2e"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return err
	}
	defer cs.Close()

	call := func(name string, args map[string]any, out any) error {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			for _, c := range res.Content {
				if t, ok := c.(*mcp.TextContent); ok {
					b = []byte(t.Text)
				}
			}
			return fmt.Errorf("%s: %s", name, b)
		}
		return json.Unmarshal(b, out)
	}

	var created issue
	if err := call("create", map[string]any{"title": "mcp round trip", "priority": 4}, &created); err != nil {
		return err
	}
	var ready struct{ Issues []issue }
	if err := call("ready", map[string]any{"limit": 100}, &ready); err != nil {
		return err
	}
	if !slices.ContainsFunc(ready.Issues, func(i issue) bool { return i.ID == created.ID }) {
		return fmt.Errorf("ready does not list %s", created.ID)
	}
	steps := []struct {
		tool   string
		args   map[string]any
		status string
	}{
		{"claim", map[string]any{"id": created.ID}, "in_progress"},
		{"close", map[string]any{"id": created.ID, "reason": "-- round trip done"}, "closed"},
		{"show", map[string]any{"id": created.ID}, "closed"},
	}
	for _, s := range steps {
		var got issue
		if err := call(s.tool, s.args, &got); err != nil {
			return err
		}
		if got.Status != s.status {
			return fmt.Errorf("%s: status %q, want %q", s.tool, got.Status, s.status)
		}
	}
	var c struct{ Text string }
	if err := call("comment", map[string]any{"id": created.ID, "text": "-v looks fine"}, &c); err != nil {
		return err
	}
	var cs2 struct{ Comments []struct{ Text string } }
	if err := call("comments", map[string]any{"id": created.ID}, &cs2); err != nil {
		return err
	}
	if len(cs2.Comments) != 1 || cs2.Comments[0].Text != "-v looks fine" {
		return fmt.Errorf("comments = %+v", cs2.Comments)
	}
	fmt.Println("ok", created.ID)
	return nil
}
