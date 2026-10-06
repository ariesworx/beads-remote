package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ariesworx/beads-remote/internal/beads"
)

// stubBD is a bd that records its argv, one argument per line, and prints
// a canned reply. exit sets its exit status.
func stubBD(t *testing.T, reply string, exit int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	replyFile := filepath.Join(dir, "reply")
	if err := os.WriteFile(replyFile, []byte(reply), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "bd")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat '" + replyFile + "'\nexit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

// connect serves s to an in-memory client. open is replaced, so no tunnel
// is involved unless the test asks for it.
func connect(t *testing.T, s *server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.mcp("test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func withBD(t *testing.T, bin string) *server {
	dir := t.TempDir()
	s := &server{dir: dir, home: t.TempDir(), bd: bin}
	s.open = func() (beads.Client, error) { return beads.Client{Bin: bin, Dir: dir}, nil }
	return s
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return tc.Text
}

// The tools are the issue tools and nothing that deletes, repairs or
// re-points the repository; read-only ones say so, for clients that
// auto-approve those.
func TestTools(t *testing.T) {
	cs := connect(t, withBD(t, "bd"))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := []string{"blocked", "comments", "list", "ready", "show", "stats"}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if ro := slices.Contains(readOnly, tool.Name); tool.Annotations.ReadOnlyHint != ro {
			t.Errorf("%s: readOnlyHint = %v", tool.Name, tool.Annotations.ReadOnlyHint)
		}
		if !tool.Annotations.ReadOnlyHint && (tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint) {
			t.Errorf("%s may be destructive", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"blocked", "claim", "close", "comment", "comments", "create", "dep", "list", "note", "ready", "reopen", "show", "stats", "update"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// Out-of-range and unknown values are refused before bd runs.
func TestSchemaRejects(t *testing.T) {
	bin, argsFile := stubBD(t, "[]", 0)
	cs := connect(t, withBD(t, bin))
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"create", map[string]any{"title": "x", "priority": 7}},
		{"create", map[string]any{"title": "x", "issue_type": "story"}},
		{"update", map[string]any{"id": "bd-1", "status": "closed"}},
		{"ready", map[string]any{"limit": 0}},
		{"dep", map[string]any{"id": "bd-1", "depends_on": "bd-2", "type": "maybe"}},
		{"show", map[string]any{}},
		{"show", map[string]any{"id": "bd-1", "extra": true}},
	} {
		if res := call(t, cs, tc.tool, tc.args); !res.IsError {
			t.Errorf("%s %v was accepted", tc.tool, tc.args)
		}
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("bd ran for a rejected call")
	}
}

// Each tool builds the bd command it should: values bound with =, and
// positional arguments after "--", so text starting with "-" stays text.
func TestCommands(t *testing.T) {
	issue := `[{"id":"bd-1","title":"T","status":"in_progress","priority":1,"issue_type":"bug"}]`
	for _, tc := range []struct {
		tool  string
		args  map[string]any
		reply string
		argv  []string
		out   any
	}{
		{"ready", map[string]any{"priority": 0, "labels": []any{"a", "b"}}, issue,
			[]string{"ready", "--limit=10", "--priority=0", "--label=a", "--label=b", "--json"},
			Issues{Issues: []beads.Summary{{ID: "bd-1", Title: "T", Status: "in_progress", Priority: 1, IssueType: "bug"}}}},
		{"list", map[string]any{"status": "closed", "query": "-x"}, "[]",
			[]string{"list", "--status=closed", "--title=-x", "--limit=20", "--json"}, Issues{Issues: []beads.Summary{}}},
		{"show", map[string]any{"id": "-bd-1"}, issue,
			[]string{"show", "--json", "--", "-bd-1"}, beads.Issue{ID: "bd-1", Title: "T", Status: "in_progress", Priority: 1, IssueType: "bug"}},
		{"create", map[string]any{"title": "--force", "labels": []any{"x", "y"}, "deps": []any{"discovered-from:bd-9"}, "priority": 1},
			`{"id":"bd-2","status":"open"}`,
			[]string{"create", "--title=--force", "--priority=1", "--labels=x,y", "--deps=discovered-from:bd-9", "--json"}, Change{ID: "bd-2", Status: "open"}},
		{"claim", map[string]any{"id": "bd-1"}, issue, []string{"update", "--claim", "--json", "--", "bd-1"}, Change{ID: "bd-1", Status: "in_progress"}},
		{"update", map[string]any{"id": "bd-1", "status": "blocked", "add_labels": []any{"x"}}, issue,
			[]string{"update", "--status=blocked", "--add-label=x", "--json", "--", "bd-1"}, Change{ID: "bd-1", Status: "in_progress"}},
		{"close", map[string]any{"id": "bd-1"}, issue, []string{"close", "--reason=Completed", "--json", "--", "bd-1"}, Change{ID: "bd-1", Status: "in_progress"}},
		{"reopen", map[string]any{"id": "bd-1"}, issue, []string{"reopen", "--json", "--", "bd-1"}, Change{ID: "bd-1", Status: "in_progress"}},
		{"dep", map[string]any{"id": "bd-1", "depends_on": "bd-2"}, `{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks","status":"added"}`,
			[]string{"dep", "add", "--type=blocks", "--json", "--", "bd-1", "bd-2"}, beads.Dependency{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks"}},
		{"comment", map[string]any{"id": "bd-1", "text": "-rf done"}, `{"id":"c1","author":"a","text":"-rf done","created_at":"t"}`,
			[]string{"comments", "add", "--json", "--", "bd-1", "-rf done"}, beads.Comment{ID: "c1", Author: "a", Text: "-rf done", CreatedAt: "t"}},
		{"comments", map[string]any{"id": "bd-1"}, `[]`, []string{"comments", "--json", "--", "bd-1"}, Comments{Comments: []beads.Comment{}}},
		{"note", map[string]any{"id": "bd-1", "text": "n"}, `{"id":"bd-1","status":"open"}`, []string{"note", "--json", "--", "bd-1", "n"}, Change{ID: "bd-1", Status: "open"}},
		{"blocked", nil, `[{"id":"bd-1","title":"T","status":"open","priority":2,"issue_type":"task","blocked_by":["bd-2"]}]`, []string{"blocked", "--json"},
			Issues{Issues: []beads.Summary{{ID: "bd-1", Title: "T", Status: "open", Priority: 2, IssueType: "task", BlockedBy: []string{"bd-2"}}}}},
		{"stats", nil, `{"summary":{"total_issues":3,"open_issues":2,"closed_issues":1}}`, []string{"stats", "--json"}, beads.Stats{Total: 3, Open: 2, Closed: 1}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			bin, argsFile := stubBD(t, tc.reply, 0)
			res := call(t, connect(t, withBD(t, bin)), tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("error: %s", text(t, res))
			}
			b, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"); !slices.Equal(got, tc.argv) {
				t.Errorf("argv = %q\n  want %q", got, tc.argv)
			}
			got := reflect.New(reflect.TypeOf(tc.out))
			if err := json.Unmarshal([]byte(text(t, res)), got.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), tc.out) {
				t.Errorf("output = %+v\n  want %+v", got.Elem().Interface(), tc.out)
			}
		})
	}
}

// bd's own error reaches the model as a tool error, not a protocol error.
func TestBDError(t *testing.T) {
	bin, _ := stubBD(t, `{"error":"no issue found matching \"bd-9\""}`, 1)
	res := call(t, connect(t, withBD(t, bin)), "show", map[string]any{"id": "bd-9"})
	if !res.IsError || !strings.Contains(text(t, res), `bd: no issue found matching "bd-9"`) {
		t.Errorf("isError=%v %s", res.IsError, text(t, res))
	}
}

// Outside a configured repository every tool says where it looked and what
// to do.
func TestNoConfig(t *testing.T) {
	s := &server{dir: t.TempDir(), home: t.TempDir(), bd: "bd"}
	s.open = s.tunnel
	res := call(t, connect(t, s), "ready", nil)
	if !res.IsError || !strings.Contains(text(t, res), "remote.yaml") {
		t.Errorf("isError=%v %s", res.IsError, text(t, res))
	}
}

// With the tunnel down and no way to open it, the tool fails with the
// reason and the fix, and bd does not run.
func TestTunnelDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "server:\n  host: beads.example.com\n  host_key: SHA256:et6CqkKsyU2BxzA7Ws+V18rXrtM9Gj6dk1/m0C5TqvU\ndatabase: hq\nport: 3399\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "remote.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, argsFile := stubBD(t, "[]", 0)
	t.Setenv("PATH", t.TempDir()) // no ssh
	s := &server{dir: dir, home: t.TempDir(), bd: bin}
	s.open = s.tunnel
	res := call(t, connect(t, s), "ready", nil)
	if msg := text(t, res); !res.IsError || !strings.Contains(msg, "cannot reach the beads server") || !strings.Contains(msg, "fix:") {
		t.Errorf("isError=%v %s", res.IsError, msg)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("bd ran with the tunnel down")
	}
}
