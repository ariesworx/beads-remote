// Package mcpserver serves beads-remote's tunnel commands over the Model
// Context Protocol on stdin and stdout, for MCP clients without a shell.
//
// It runs on the developer's machine as the developer, so it uses the same
// SSH key, agent, pinned host key and cached password as the CLI: there is no
// second sign-on. Only the four developer commands are tools. Nothing that
// prompts (setup, init), nothing that re-pins a host key (--repin), and none
// of the server admin commands, which run as root on the server, are offered.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ariesworx/beads-remote/internal/remote"
)

type server struct {
	dir  string
	home string
	mu   sync.Mutex // one command at a time, as at a terminal
}

// New returns the MCP server for the repository at or above dir. The config
// is read on every call, so a pulled change to remote.yaml takes effect
// without a restart.
func New(dir, home, version string) *mcp.Server {
	s := &server{dir: dir, home: home}
	srv := mcp.NewServer(&mcp.Implementation{Name: "beads-remote", Version: version}, &mcp.ServerOptions{
		Instructions: "bd reaches this repository's shared beads server through an SSH tunnel. Call up before using bd. " +
			"Results list only failures and warnings, each with a fix; ok with no results means all passed.",
	})
	no := false
	tools := []struct {
		tool *mcp.Tool
		run  func(*remote.Config, remote.Env) int
	}{
		{&mcp.Tool{
			Name:        "status",
			Description: "Is the tunnel to the beads server up? Changes nothing.",
			Annotations: &mcp.ToolAnnotations{Title: "Tunnel status", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no},
		}, remote.Status},
		{&mcp.Tool{
			Name:        "check",
			Description: "Check bd, the SSH key, the pinned host key, the tunnel, the password and bd's config. Changes nothing.",
			Annotations: &mcp.ToolAnnotations{Title: "Check setup", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no},
		}, remote.Check},
		{&mcp.Tool{
			Name:        "up",
			Description: "Open the tunnel and repair bd's local config. Safe to repeat. A changed server host key is refused until a person runs `beads-remote up --repin`.",
			Annotations: &mcp.ToolAnnotations{Title: "Open tunnel", IdempotentHint: true, DestructiveHint: &no},
		}, remote.Up},
		{&mcp.Tool{
			Name:        "down",
			Description: "Close the tunnel. Safe to repeat.",
			Annotations: &mcp.ToolAnnotations{Title: "Close tunnel", IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
		}, remote.Down},
	}
	for _, t := range tools {
		// Every tool's output is the same document as `beads-remote --json`.
		mcp.AddTool(srv, t.tool, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, remote.Report, error) {
			return s.call(t.run)
		})
	}
	return srv
}

func (s *server) call(run func(*remote.Config, remote.Env) int) (*mcp.CallToolResult, remote.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, root, err := remote.Load(s.dir)
	if err != nil {
		r := remote.Report{Results: []remote.Result{{Name: "config", Detail: err.Error(), Fix: "start the server in a repository with " + remote.ConfigFile}}}
		return &mcp.CallToolResult{IsError: true}, r, nil
	}
	var out bytes.Buffer
	env := remote.Env{Home: s.home, RepoRoot: root, In: strings.NewReader(""), Out: &out, JSON: true}
	run(cfg, env)
	var r remote.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return nil, remote.Report{}, fmt.Errorf("unreadable result: %w", err)
	}
	return &mcp.CallToolResult{IsError: !r.OK}, trim(r), nil
}

// toolFix matches a fix that is one of this server's own tools.
var toolFix = regexp.MustCompile(`^beads-remote (status|check|up|down)\b`)

// trim keeps what a model must act on: passes are dropped, as at the
// terminal without -v, and a fix that names a tool says to call it.
func trim(r remote.Report) remote.Report {
	kept := []remote.Result{}
	for _, res := range r.Results {
		if res.OK && !res.Warn {
			continue
		}
		res.Fix = toolFix.ReplaceAllString(res.Fix, "call $1")
		kept = append(kept, res)
	}
	r.Results = kept
	return r
}

// Serve runs the server until the client closes the connection. Nothing else
// may write to stdout: it carries the protocol.
func Serve(ctx context.Context, dir, home, version string, in io.ReadCloser, out io.WriteCloser) error {
	return New(dir, home, version).Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}
