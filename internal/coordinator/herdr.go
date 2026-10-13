// Package coordinator implements the auth recovery coordinator daemon.
package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// HerdrClient wraps the herdr CLI for pane operations (arosl fork).
//
// herdr (https://herdr.dev) is the terminal multiplexer of the ACFS fork,
// which runs neither WezTerm nor tmux. The client uses herdr's pane
// commands: pane list, pane read and pane send-text. The herdr CLI finds
// its server through HERDR_SOCKET_PATH, else the default session, so a
// coordinator started by systemd or cron reaches it as well.
//
// herdr pane IDs are strings like "w1:p2". The client gives each pane the
// next integer the first time it lists it, and never reuses an integer,
// as TmuxClient strips the "%" of tmux's "%0". Domain carries the herdr
// pane ID, so a user can see which pane an integer means.
type HerdrClient struct {
	binaryPath string

	mu    sync.Mutex
	ids   map[string]int // herdr pane ID -> PaneID
	names map[int]string // PaneID -> herdr pane ID
	next  int
}

// NewHerdrClient creates a new herdr CLI client.
func NewHerdrClient() *HerdrClient {
	return &HerdrClient{
		binaryPath: "herdr",
		ids:        make(map[string]int),
		names:      make(map[int]string),
	}
}

// herdrPane is one pane of herdr's pane list result.
type herdrPane struct {
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Title         string `json:"terminal_title"`
	Focused       bool   `json:"focused"`
	Scroll        struct {
		ViewportRows int `json:"viewport_rows"`
	} `json:"scroll"`
}

// run runs herdr with args and returns its stdout. When a call fails,
// herdr prints an error object on stdout; the error names its code and
// message.
func (c *HerdrClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		name := "herdr " + strings.Join(args[:min(2, len(args))], " ")
		var reply struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &reply) == nil && reply.Error != nil {
			return nil, fmt.Errorf("%s: %s: %s", name, reply.Error.Code, reply.Error.Message)
		}
		return nil, fmt.Errorf("%s: %w (stderr: %s)", name, err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// ListPanes returns all panes across all workspaces.
func (c *HerdrClient) ListPanes(ctx context.Context) ([]Pane, error) {
	out, err := c.run(ctx, "pane", "list")
	if err != nil {
		return nil, err
	}

	var reply struct {
		Result *struct {
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("parse herdr pane list: %w", err)
	}
	// Without a result (an error object, say), there is no pane list,
	// which differs from an empty one.
	if reply.Result == nil {
		return nil, fmt.Errorf("herdr pane list: no result in %q", out)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	panes := make([]Pane, 0, len(reply.Result.Panes))
	for _, p := range reply.Result.Panes {
		if p.PaneID == "" {
			continue // Skip malformed entries
		}
		// The foreground process's directory, as tmux's
		// pane_current_path and WezTerm's cwd give it.
		cwd := p.ForegroundCWD
		if cwd == "" {
			cwd = p.CWD
		}
		panes = append(panes, Pane{
			PaneID:      c.idFor(p.PaneID),
			WorkspaceID: p.WorkspaceID,
			Domain:      p.PaneID,
			Title:       p.Title,
			CWD:         cwd,
			IsActive:    p.Focused,
			Rows:        p.Scroll.ViewportRows,
		})
	}

	return panes, nil
}

// idFor returns the integer for a herdr pane ID, and gives a pane seen for
// the first time the next one. The caller holds c.mu.
func (c *HerdrClient) idFor(pane string) int {
	if id, ok := c.ids[pane]; ok {
		return id
	}
	c.next++
	c.ids[pane] = c.next
	c.names[c.next] = pane
	return c.next
}

// paneName returns the herdr pane ID behind an integer from ListPanes.
func (c *HerdrClient) paneName(paneID int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pane, ok := c.names[paneID]
	if !ok {
		return "", fmt.Errorf("herdr: unknown pane %d (not in a pane list yet)", paneID)
	}
	return pane, nil
}

// GetText retrieves text content from a pane.
// startLine is negative for lines from the end (e.g., -50 for last 50 lines).
// herdr reads from the end only, so a positive startLine is refused.
func (c *HerdrClient) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	if startLine > 0 {
		return "", fmt.Errorf("herdr pane read: start line %d: herdr reads only the last lines of a pane", startLine)
	}
	pane, err := c.paneName(paneID)
	if err != nil {
		return "", err
	}

	// recent-unwrapped joins soft-wrapped lines: an OAuth URL is far wider
	// than a pane, and split across lines it would be extracted truncated.
	args := []string{"pane", "read", pane, "--source", "recent-unwrapped"}
	if startLine < 0 {
		args = append(args, "--lines", strconv.Itoa(-startLine))
	}

	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}

	return string(out), nil
}

// SendText injects text into a pane.
// herdr pane send-text writes the text's bytes as they are, like typing,
// also when the program in the pane has turned on bracketed paste; herdr
// has no paste command. If noPaste is true, the text is prepared for typing
// (TypedText), as the other backends do.
func (c *HerdrClient) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	pane, err := c.paneName(paneID)
	if err != nil {
		return err
	}
	if noPaste {
		text = TypedText(text)
	}

	// herdr types every argument after the pane ID, "--" included, so the
	// text goes as one argument and nothing else follows the pane ID.
	_, err = c.run(ctx, "pane", "send-text", pane, text)
	return err
}

// IsAvailable checks if herdr is installed and its server answers.
func (c *HerdrClient) IsAvailable(ctx context.Context) bool {
	_, err := c.run(ctx, "pane", "list")
	return err == nil
}

// Backend returns the backend name.
func (c *HerdrClient) Backend() string {
	return "herdr"
}
