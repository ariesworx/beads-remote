// Package mcpserver serves this repository's beads issues over the Model
// Context Protocol on stdin and stdout. It is a Go port of the issue tools
// in beads-mcp (github.com/gastownhall/beads, integrations/beads-mcp, MIT).
//
// Each tool runs bd in the repository, after making sure the SSH tunnel to
// the shared server is up, so an agent never manages the tunnel itself. It
// runs on the developer's machine as the developer: the same SSH key, pinned
// host key and cached password as the CLI, and no second sign-on.
//
// Left out on purpose: anything that deletes or repairs data (beads-mcp's
// admin tool), anything that re-points the repository (bd init), and the
// server admin commands, which run as root on the server.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ariesworx/beads-remote/internal/beads"
	"github.com/ariesworx/beads-remote/internal/remote"
)

// Values bd 1.2 accepts. The input schemas carry them as enums, so a client
// sees the choices and the SDK rejects anything else before bd runs.
var (
	issueTypes   = []any{"task", "bug", "feature", "epic", "chore", "decision"}
	openStatuses = []any{"open", "in_progress", "blocked", "deferred"}
	allStatuses  = append(append([]any{}, openStatuses...), "closed")
	depTypes     = []any{"blocks", "related", "parent-child", "discovered-from", "tracks", "until", "caused-by", "validates", "relates-to", "supersedes"}
)

type server struct {
	dir  string
	home string
	bd   string                       // the bd executable
	open func() (beads.Client, error) // finds the repository and opens the tunnel
	mu   sync.Mutex                   // one bd command at a time, as at a terminal
}

// New returns the MCP server for the repository at or above dir.
func New(dir, home, version string) *mcp.Server {
	s := &server{dir: dir, home: home, bd: "bd"}
	s.open = s.tunnel
	return s.mcp(version)
}

func (s *server) mcp(version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "beads-remote", Version: version}, &mcp.ServerOptions{
		Instructions: "Issue tracker for this repository (beads). Start with ready, claim one, comment as you go, close it when done. " +
			"Each tool opens the tunnel to the shared server itself.",
	})
	s.register(srv)
	return srv
}

// Serve runs the server until the client closes the connection. Nothing else
// may write to stdout: it carries the protocol.
func Serve(ctx context.Context, dir, home, version string, in io.ReadCloser, out io.WriteCloser) error {
	return New(dir, home, version).Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}

// ID names one issue.
type ID struct {
	ID string `json:"id" jsonschema:"issue id, e.g. bd-a1b2"`
}

// Issues is a list result.
type Issues struct {
	Issues []beads.Summary `json:"issues"`
}

// Change is the result of a write: the issue and its status afterwards.
type Change struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}

type ReadyIn struct {
	Limit      int      `json:"limit,omitempty" jsonschema:"at most this many; default 10"`
	IssueType  string   `json:"issue_type,omitempty"`
	Priority   *int     `json:"priority,omitempty" jsonschema:"0 is highest"`
	Assignee   string   `json:"assignee,omitempty"`
	Labels     []string `json:"labels,omitempty" jsonschema:"must have all of these"`
	Unassigned bool     `json:"unassigned,omitempty"`
}

type ListIn struct {
	Status    string   `json:"status,omitempty" jsonschema:"default: all but closed"`
	IssueType string   `json:"issue_type,omitempty"`
	Priority  *int     `json:"priority,omitempty" jsonschema:"0 is highest"`
	Assignee  string   `json:"assignee,omitempty"`
	Labels    []string `json:"labels,omitempty" jsonschema:"must have all of these"`
	Query     string   `json:"query,omitempty" jsonschema:"text in the title"`
	Limit     int      `json:"limit,omitempty" jsonschema:"at most this many; default 20"`
}

type CreateIn struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	IssueType   string   `json:"issue_type,omitempty" jsonschema:"default task"`
	Priority    *int     `json:"priority,omitempty" jsonschema:"0 is highest; default 2"`
	Labels      []string `json:"labels,omitempty"`
	Parent      string   `json:"parent,omitempty" jsonschema:"parent issue id, for a subtask"`
	Deps        []string `json:"deps,omitempty" jsonschema:"issues this depends on: id or type:id, e.g. discovered-from:bd-a1b2"`
	Assignee    string   `json:"assignee,omitempty"`
	Design      string   `json:"design,omitempty"`
	Acceptance  string   `json:"acceptance,omitempty" jsonschema:"acceptance criteria"`
	ExternalRef string   `json:"external_ref,omitempty" jsonschema:"e.g. gh-12"`
}

type UpdateIn struct {
	ID           string   `json:"id"`
	Status       string   `json:"status,omitempty" jsonschema:"to close, use close"`
	Priority     *int     `json:"priority,omitempty" jsonschema:"0 is highest"`
	Assignee     string   `json:"assignee,omitempty"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty" jsonschema:"replaces the description"`
	Design       string   `json:"design,omitempty"`
	Acceptance   string   `json:"acceptance,omitempty" jsonschema:"acceptance criteria"`
	AddLabels    []string `json:"add_labels,omitempty"`
	RemoveLabels []string `json:"remove_labels,omitempty"`
	ExternalRef  string   `json:"external_ref,omitempty"`
}

type CloseIn struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty" jsonschema:"what was done; default Completed"`
}

type DepIn struct {
	ID        string `json:"id" jsonschema:"the issue that depends"`
	DependsOn string `json:"depends_on" jsonschema:"the issue it depends on"`
	Type      string `json:"type,omitempty" jsonschema:"default blocks"`
}

type TextIn struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type Comments struct {
	Comments []beads.Comment `json:"comments"`
}

func (s *server) register(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	write := &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	idem := &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}

	add(srv, s, "ready", "Open issues with nothing blocking them, highest priority first.", ro,
		enums{"issue_type": issueTypes}, func(ctx context.Context, c beads.Client, in ReadyIn) (Issues, error) {
			var f beads.Flags
			f.Int("limit", ptr(orDefault(in.Limit, 10)))
			f.Str("type", in.IssueType)
			f.Int("priority", in.Priority)
			f.Str("assignee", in.Assignee)
			f.Each("label", in.Labels)
			f.Bool("unassigned", in.Unassigned)
			var out Issues
			return out, c.Run(ctx, &out.Issues, words("ready"), f, nil)
		})
	add(srv, s, "list", "List issues, filtered. Closed issues only when status is closed.", ro,
		enums{"status": allStatuses, "issue_type": issueTypes}, func(ctx context.Context, c beads.Client, in ListIn) (Issues, error) {
			var f beads.Flags
			f.Str("status", in.Status)
			f.Str("type", in.IssueType)
			f.Int("priority", in.Priority)
			f.Str("assignee", in.Assignee)
			f.Each("label", in.Labels)
			f.Str("title", in.Query)
			f.Int("limit", ptr(orDefault(in.Limit, 20)))
			var out Issues
			return out, c.Run(ctx, &out.Issues, words("list"), f, nil)
		})
	add(srv, s, "show", "One issue in full, with its dependencies.", ro,
		nil, func(ctx context.Context, c beads.Client, in ID) (beads.Issue, error) {
			var out []beads.Issue
			if err := c.Run(ctx, &out, words("show"), nil, []string{in.ID}); err != nil {
				return beads.Issue{}, err
			}
			if len(out) != 1 {
				return beads.Issue{}, &beads.Error{Msg: "no issue " + in.ID}
			}
			return out[0], nil
		})
	add(srv, s, "create", "Create an issue. Returns its id.", write,
		enums{"issue_type": issueTypes}, func(ctx context.Context, c beads.Client, in CreateIn) (Change, error) {
			f := beads.Flags{"--title=" + in.Title}
			f.Str("description", in.Description)
			f.Str("type", in.IssueType)
			f.Int("priority", in.Priority)
			f.Str("labels", strings.Join(in.Labels, ","))
			f.Str("parent", in.Parent)
			f.Str("deps", strings.Join(in.Deps, ","))
			f.Str("assignee", in.Assignee)
			f.Str("design", in.Design)
			f.Str("acceptance", in.Acceptance)
			f.Str("external-ref", in.ExternalRef)
			var out beads.Issue
			err := c.Run(ctx, &out, words("create"), f, nil)
			return Change{ID: out.ID, Status: out.Status}, err
		})
	add(srv, s, "claim", "Assign an issue to yourself and mark it in_progress. Fails if someone else has it.", idem,
		nil, func(ctx context.Context, c beads.Client, in ID) (Change, error) {
			return change(ctx, c, words("update"), beads.Flags{"--claim"}, in.ID)
		})
	add(srv, s, "update", "Change an issue's fields. Only the fields given change.", idem,
		enums{"status": openStatuses}, func(ctx context.Context, c beads.Client, in UpdateIn) (Change, error) {
			var f beads.Flags
			f.Str("status", in.Status)
			f.Int("priority", in.Priority)
			f.Str("assignee", in.Assignee)
			f.Str("title", in.Title)
			f.Str("description", in.Description)
			f.Str("design", in.Design)
			f.Str("acceptance", in.Acceptance)
			f.Each("add-label", in.AddLabels)
			f.Each("remove-label", in.RemoveLabels)
			f.Str("external-ref", in.ExternalRef)
			if len(f) == 0 {
				return Change{}, errors.New("nothing to change: give at least one field")
			}
			return change(ctx, c, words("update"), f, in.ID)
		})
	add(srv, s, "close", "Close an issue that is done.", idem,
		nil, func(ctx context.Context, c beads.Client, in CloseIn) (Change, error) {
			return change(ctx, c, words("close"), beads.Flags{"--reason=" + orDefault(in.Reason, "Completed")}, in.ID)
		})
	add(srv, s, "reopen", "Reopen a closed issue.", idem,
		nil, func(ctx context.Context, c beads.Client, in CloseIn) (Change, error) {
			var f beads.Flags
			f.Str("reason", in.Reason)
			return change(ctx, c, words("reopen"), f, in.ID)
		})
	add(srv, s, "dep", "Record that one issue depends on another. blocks keeps it out of ready until the other closes.", idem,
		enums{"type": depTypes}, func(ctx context.Context, c beads.Client, in DepIn) (beads.Dependency, error) {
			var out beads.Dependency
			return out, c.Run(ctx, &out, words("dep", "add"), beads.Flags{"--type=" + orDefault(in.Type, "blocks")}, []string{in.ID, in.DependsOn})
		})
	add(srv, s, "comment", "Add a comment: what changed, was decided or was verified.", write,
		nil, func(ctx context.Context, c beads.Client, in TextIn) (beads.Comment, error) {
			var out beads.Comment
			return out, c.Run(ctx, &out, words("comments", "add"), nil, []string{in.ID, in.Text})
		})
	add(srv, s, "comments", "An issue's comments, oldest first.", ro,
		nil, func(ctx context.Context, c beads.Client, in ID) (Comments, error) {
			var out Comments
			return out, c.Run(ctx, &out.Comments, words("comments"), nil, []string{in.ID})
		})
	add(srv, s, "note", "Append text to an issue's notes.", write,
		nil, func(ctx context.Context, c beads.Client, in TextIn) (Change, error) {
			var out beads.Issue
			err := c.Run(ctx, &out, words("note"), nil, []string{in.ID, in.Text})
			return Change{ID: out.ID, Status: out.Status}, err
		})
	add(srv, s, "blocked", "Open issues waiting on others, with what blocks each.", ro,
		nil, func(ctx context.Context, c beads.Client, _ struct{}) (Issues, error) {
			var out Issues
			return out, c.Run(ctx, &out.Issues, words("blocked"), nil, nil)
		})
	add(srv, s, "stats", "Issue counts by status.", ro,
		nil, func(ctx context.Context, c beads.Client, _ struct{}) (beads.Stats, error) {
			var out struct {
				Summary beads.Stats `json:"summary"`
			}
			return out.Summary, c.Run(ctx, &out, words("stats"), nil, nil)
		})
}

// change runs a write on one issue and reports its status afterwards. bd
// prints the issue as a one-element list for these commands.
func change(ctx context.Context, c beads.Client, w []string, f beads.Flags, id string) (Change, error) {
	var out []beads.Issue
	if err := c.Run(ctx, &out, w, f, []string{id}); err != nil {
		return Change{}, err
	}
	if len(out) == 0 {
		return Change{ID: id}, nil
	}
	return Change{ID: out[0].ID, Status: out[0].Status}, nil
}

// enums maps an input field to its allowed values.
type enums map[string][]any

// add registers one tool. Its input schema is inferred from In, with
// priority bounded to 0-4, limit to 1-100, and the given enums.
func add[In, Out any](srv *mcp.Server, s *server, name, desc string, ann *mcp.ToolAnnotations, e enums,
	h func(context.Context, beads.Client, In) (Out, error)) {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: schema for %s: %v", name, err)) // a programming error, caught by the tests
	}
	for field, values := range e {
		schema.Properties[field].Enum = values
	}
	if p := schema.Properties["priority"]; p != nil {
		p.Minimum, p.Maximum = ptr(0.0), ptr(4.0)
	}
	if p := schema.Properties["limit"]; p != nil {
		p.Minimum, p.Maximum = ptr(1.0), ptr(100.0)
	}
	tool := &mcp.Tool{Name: name, Description: desc, Annotations: ann, InputSchema: schema}
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var zero Out
		c, err := s.open()
		if err != nil {
			return nil, zero, err
		}
		out, err := h(ctx, c, in)
		return nil, out, err
	})
}

// tunnel finds the repository and makes sure the tunnel is up, opening it if
// not. A failure names what is wrong and the command a person runs to fix it.
func (s *server) tunnel() (beads.Client, error) {
	cfg, root, err := remote.Load(s.dir)
	if err != nil {
		return beads.Client{}, fmt.Errorf("%w; start the server in a repository with %s, or pass -C", err, remote.ConfigFile)
	}
	var buf bytes.Buffer
	env := remote.Env{Home: s.home, RepoRoot: root, In: strings.NewReader(""), Out: &buf, JSON: true}
	if remote.Status(cfg, env) != 0 {
		buf.Reset()
		if remote.Up(cfg, env) != 0 {
			return beads.Client{}, tunnelError(buf.Bytes())
		}
	}
	return beads.Client{Bin: s.bd, Dir: root}, nil
}

// tunnelError turns a failed `up` report into one line per failure.
func tunnelError(doc []byte) error {
	var r remote.Report
	if err := json.Unmarshal(doc, &r); err != nil {
		return fmt.Errorf("cannot reach the beads server: %w", err)
	}
	var lines []string
	for _, res := range r.Results {
		if res.OK {
			continue
		}
		line := res.Name
		if res.Detail != "" {
			line += ": " + res.Detail
		}
		if res.Fix != "" {
			line += " (fix: " + res.Fix + ")"
		}
		lines = append(lines, line)
	}
	return errors.New("cannot reach the beads server: " + strings.Join(lines, "; "))
}

func words(w ...string) []string { return w }

func ptr[T any](v T) *T { return &v }

func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
