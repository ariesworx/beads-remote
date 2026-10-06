// Package beads runs the bd command-line tool and decodes its --json output
// into typed values.
//
// Every value a caller passes goes into argv after a "--" or as --flag=value,
// so free text that starts with "-" is never read as a flag. No shell is
// involved.
package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Issue is one issue as `bd show --json` reports it.
type Issue struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Status             string   `json:"status"`
	Priority           int      `json:"priority"`
	IssueType          string   `json:"issue_type"`
	Assignee           string   `json:"assignee,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	Description        string   `json:"description,omitempty"`
	Design             string   `json:"design,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Notes              string   `json:"notes,omitempty"`
	ExternalRef        string   `json:"external_ref,omitempty"`
	CloseReason        string   `json:"close_reason,omitempty"`
	CreatedAt          string   `json:"created_at,omitempty"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
	ClosedAt           string   `json:"closed_at,omitempty"`
	Dependencies       []Link   `json:"dependencies,omitempty"`
	Dependents         []Link   `json:"dependents,omitempty"`
	CommentCount       int      `json:"comment_count,omitempty"`
}

// Link is an issue on the other end of a dependency.
type Link struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type,omitempty"`
}

// Summary is an issue in a list: enough to choose one, then show it.
type Summary struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Priority  int      `json:"priority"`
	IssueType string   `json:"issue_type"`
	Assignee  string   `json:"assignee,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// Comment is one comment on an issue.
type Comment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// Stats is the summary from `bd stats --json`.
type Stats struct {
	Total      int     `json:"total_issues"`
	Open       int     `json:"open_issues"`
	InProgress int     `json:"in_progress_issues"`
	Blocked    int     `json:"blocked_issues"`
	Deferred   int     `json:"deferred_issues"`
	Ready      int     `json:"ready_issues"`
	Closed     int     `json:"closed_issues"`
	LeadTimeH  float64 `json:"average_lead_time_hours"`
}

// Dependency is the result of `bd dep add --json`.
type Dependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// Client runs bd in one repository.
type Client struct {
	Bin string // the bd executable
	Dir string // the repository root
}

// Error is a failure bd reported.
type Error struct{ Msg string }

func (e *Error) Error() string { return "bd: " + e.Msg }

// Run executes bd with words (the subcommand), flags and positional
// arguments, and decodes its JSON output into out. Flags must be in
// --name=value form; positional arguments follow "--".
func (c Client) Run(ctx context.Context, out any, words, flags, positional []string) error {
	args := make([]string, 0, len(words)+len(flags)+len(positional)+2)
	args = append(args, words...)
	args = append(args, flags...)
	args = append(args, "--json")
	if len(positional) > 0 {
		args = append(args, "--")
		args = append(args, positional...)
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// bd reports most failures as {"error": "..."} on stdout.
	var fail struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(stdout.Bytes(), &fail) == nil && fail.Error != "" {
		return &Error{fail.Error}
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = runErr.Error()
		}
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return fmt.Errorf("running bd: %w", runErr)
		}
		return &Error{msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("bd %s: unreadable output: %w", strings.Join(words, " "), err)
	}
	return nil
}

// Flags builds --name=value flags, skipping empty values.
type Flags []string

// Str adds --name=v when v is not empty.
func (f *Flags) Str(name, v string) {
	if v != "" {
		*f = append(*f, "--"+name+"="+v)
	}
}

// Int adds --name=v when v is set.
func (f *Flags) Int(name string, v *int) {
	if v != nil {
		*f = append(*f, fmt.Sprintf("--%s=%d", name, *v))
	}
}

// Each adds --name=v once per value.
func (f *Flags) Each(name string, vs []string) {
	for _, v := range vs {
		f.Str(name, v)
	}
}

// Bool adds --name when b is true.
func (f *Flags) Bool(name string, b bool) {
	if b {
		*f = append(*f, "--"+name)
	}
}
