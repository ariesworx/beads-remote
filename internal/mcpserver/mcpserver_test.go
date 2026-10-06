package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(dir, t.TempDir(), "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// Exactly the four developer commands are offered, and the two that only
// look are marked read-only for clients that auto-approve those.
func TestTools(t *testing.T) {
	cs := connect(t, t.TempDir())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		a := tool.Annotations
		if a == nil || !a.IdempotentHint {
			t.Errorf("%s is not marked idempotent", tool.Name)
			continue
		}
		if ro := tool.Name == "status" || tool.Name == "check"; a.ReadOnlyHint != ro {
			t.Errorf("%s: readOnlyHint = %v", tool.Name, a.ReadOnlyHint)
		}
	}
	slices.Sort(names)
	if want := []string{"check", "down", "status", "up"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// Outside a configured repository every tool fails the same way a command
// would: a tool error the model can read, with the fix, not a protocol error.
func TestNoConfig(t *testing.T) {
	cs := connect(t, t.TempDir())
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("status outside a repository did not report an error")
	}
	var r Report
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.OK || len(r.Results) != 1 || r.Results[0].Name != "config" || r.Results[0].Fix == "" {
		t.Errorf("result = %+v", r)
	}
}

// A configured repository whose tunnel is down: status reports it as a
// result, through the same code as `beads-remote status --json`.
func TestStatusDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "server:\n  host: beads.example.com\n  host_key: SHA256:et6CqkKsyU2BxzA7Ws+V18rXrtM9Gj6dk1/m0C5TqvU\ndatabase: hq\nport: 3399\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "remote.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no ssh: the tunnel is down
	cs := connect(t, dir)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, `"tunnel"`) || !strings.Contains(text, "beads-remote up") {
		t.Errorf("status with the tunnel down: isError=%v %s", res.IsError, text)
	}
}
