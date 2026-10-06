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
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ariesworx/beads-remote/internal/remote"
)

// Report is every tool's output: the same document as `beads-remote --json`.
type Report struct {
	OK      bool            `json:"ok" jsonschema:"true when every check passed"`
	Results []remote.Result `json:"results" jsonschema:"one entry per check or step, with a fix for each failure"`
}

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
		Instructions: "Connects this repository's bd (beads) to its shared Dolt server through an SSH tunnel. " +
			"Call up before using bd; it is quick and safe to repeat. Every result lists each check, and every failure carries a fix.",
	})
	no := false
	tools := []struct {
		tool *mcp.Tool
		run  func(*remote.Config, remote.Env) int
	}{
		{&mcp.Tool{
			Name:        "status",
			Description: "Report whether the tunnel to the beads server is up.",
			Annotations: &mcp.ToolAnnotations{Title: "Tunnel status", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no},
		}, remote.Status},
		{&mcp.Tool{
			Name:        "check",
			Description: "Check the whole client side without changing anything: bd, the SSH key, the pinned host key, the tunnel, the cached password, bd's credentials and metadata.",
			Annotations: &mcp.ToolAnnotations{Title: "Check setup", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no},
		}, remote.Check},
		{&mcp.Tool{
			Name:        "up",
			Description: "Open the tunnel and repair the local setup bd needs. Quick when already up, and safe to repeat. Refuses a server host or host key that changed in remote.yaml; a person must confirm that with `beads-remote up --repin`.",
			Annotations: &mcp.ToolAnnotations{Title: "Open tunnel", IdempotentHint: true, DestructiveHint: &no},
		}, remote.Up},
		{&mcp.Tool{
			Name:        "down",
			Description: "Close the tunnel. Succeeds when it is already closed.",
			Annotations: &mcp.ToolAnnotations{Title: "Close tunnel", IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
		}, remote.Down},
	}
	for _, t := range tools {
		run := t.run
		mcp.AddTool(srv, t.tool, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Report, error) {
			return s.call(run)
		})
	}
	return srv
}

func (s *server) call(run func(*remote.Config, remote.Env) int) (*mcp.CallToolResult, Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, root, err := remote.Load(s.dir)
	if err != nil {
		r := Report{Results: []remote.Result{{Name: "config", Detail: err.Error(), Fix: "start the server in a repository with " + remote.ConfigFile}}}
		return &mcp.CallToolResult{IsError: true}, r, nil
	}
	var out bytes.Buffer
	env := remote.Env{Home: s.home, RepoRoot: root, In: strings.NewReader(""), Out: &out, JSON: true}
	run(cfg, env)
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return nil, Report{}, fmt.Errorf("unreadable result: %w", err)
	}
	return &mcp.CallToolResult{IsError: !r.OK}, r, nil
}

// Serve runs the server until the client closes the connection. Nothing else
// may write to stdout: it carries the protocol.
func Serve(ctx context.Context, dir, home, version string, in io.ReadCloser, out io.WriteCloser) error {
	return New(dir, home, version).Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}
