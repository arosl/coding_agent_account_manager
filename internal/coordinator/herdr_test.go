package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeHerdrScript stands in for the herdr CLI. It logs each call as its
// argument count and arguments, NUL-separated, so control bytes and empty
// arguments survive. It answers pane list from FAKE_HERDR_LIST (a file) and
// pane read with FAKE_HERDR_READ. With FAKE_HERDR_FAIL set, it prints that
// error object and exits 1, as herdr does when a call fails.
const fakeHerdrScript = `#!/bin/sh
printf '%s\0' "$#" "$@" >> "$FAKE_HERDR_LOG"
if [ -n "$FAKE_HERDR_FAIL" ]; then
	printf '%s\n' "$FAKE_HERDR_FAIL"
	exit 1
fi
case "$1 $2" in
"pane list") cat "$FAKE_HERDR_LIST" ;;
"pane read") printf '%s' "$FAKE_HERDR_READ" ;;
esac
`

// fakeHerdr returns a HerdrClient that runs the stand-in, the path of the
// file it lists panes from (seeded with the herdr 0.9.3 fixture), and a
// function that returns the calls made so far.
func fakeHerdr(t *testing.T) (*HerdrClient, string, func() [][]string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(fakeHerdrScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "herdr_pane_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "list.json")
	if err := os.WriteFile(list, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	t.Setenv("FAKE_HERDR_LOG", log)
	t.Setenv("FAKE_HERDR_LIST", list)
	t.Setenv("FAKE_HERDR_READ", "")
	t.Setenv("FAKE_HERDR_FAIL", "")

	client := NewHerdrClient()
	client.binaryPath = bin

	calls := func() [][]string {
		t.Helper()
		data, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(string(data), "\x00")
		var out [][]string
		for i := 0; i < len(fields)-1; {
			n, err := strconv.Atoi(fields[i])
			if err != nil || i+1+n > len(fields)-1 {
				t.Fatalf("malformed call log %q", data)
			}
			out = append(out, fields[i+1:i+1+n])
			i += 1 + n
		}
		return out
	}
	return client, list, calls
}

func TestHerdrListPanesGivesEachPaneOneInteger(t *testing.T) {
	client, list, calls := fakeHerdr(t)
	ctx := context.Background()

	panes, err := client.ListPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Pane{
		{PaneID: 1, WorkspaceID: "w1", Domain: "w1:p1", Title: "dev@host:~/project", CWD: "/home/dev/project", Rows: 48},
		// The foreground process's directory wins over the pane's.
		{PaneID: 2, WorkspaceID: "w1", Domain: "w1:p1F", Title: "✳ Refactoring the parser", CWD: "/home/dev/project/sub", IsActive: true, Rows: 40},
		// Without one, the pane's directory.
		{PaneID: 3, WorkspaceID: "w2", Domain: "w2:p1", CWD: "/home/dev/other", Rows: 24},
	}
	if !slices.Equal(panes, want) {
		t.Fatalf("panes =\n%+v\nwant\n%+v", panes, want)
	}

	// A later list keeps each pane's integer. A new pane gets the next one,
	// and the integer of a pane that closed is not given again.
	later := `{"id":"cli:pane:list","result":{"panes":[{"pane_id":"w3:p1","workspace_id":"w3"},{"pane_id":"w2:p1","workspace_id":"w2"}]}}`
	if err := os.WriteFile(list, []byte(later), 0o644); err != nil {
		t.Fatal(err)
	}
	panes, err = client.ListPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 2 || panes[0].PaneID != 4 || panes[0].Domain != "w3:p1" || panes[1].PaneID != 3 {
		t.Fatalf("later panes = %+v, want w3:p1 as 4 and w2:p1 still 3", panes)
	}

	if !client.IsAvailable(ctx) {
		t.Fatal("IsAvailable = false while herdr answers")
	}
	for _, c := range calls() {
		if !slices.Equal(c, []string{"pane", "list"}) {
			t.Fatalf("call %q, want only pane list", c)
		}
	}
}

func TestHerdrGetTextReadsTheUnwrappedTail(t *testing.T) {
	client, _, calls := fakeHerdr(t)
	ctx := context.Background()
	if _, err := client.ListPanes(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERDR_READ", "Paste code here if prompted >")

	text, err := client.GetText(ctx, 2, -100)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Paste code here if prompted >" {
		t.Fatalf("text = %q", text)
	}
	if _, err := client.GetText(ctx, 2, 0); err != nil {
		t.Fatal(err)
	}
	// herdr reads from the end only.
	if _, err := client.GetText(ctx, 2, 5); err == nil {
		t.Fatal("GetText with a positive start line succeeded")
	}

	got := calls()[1:]
	want := [][]string{
		{"pane", "read", "w1:p1F", "--source", "recent-unwrapped", "--lines", "100"},
		{"pane", "read", "w1:p1F", "--source", "recent-unwrapped"},
	}
	if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestHerdrSendTextTypesTheBytesAsOneArgument(t *testing.T) {
	client, _, calls := fakeHerdr(t)
	ctx := context.Background()
	if _, err := client.ListPanes(ctx); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		text    string
		noPaste bool
		want    string
	}{
		{"/login\n", true, "/login\r"},
		{KeyEscape, true, "\x1b"},
		{KeyKillLine, true, "\x15"},
		// herdr types every argument after the pane ID, so a leading dash
		// is text and no "--" goes before it.
		{"-code#state\n", true, "-code#state\r"},
		{"line one\nline two\n", true, "line one\nline two\r"},
		{"pasted\n", false, "pasted\n"},
	}
	for _, tc := range cases {
		if err := client.SendText(ctx, 2, tc.text, tc.noPaste); err != nil {
			t.Fatalf("SendText(%q): %v", tc.text, err)
		}
		all := calls()
		got := all[len(all)-1]
		if want := []string{"pane", "send-text", "w1:p1F", tc.want}; !slices.Equal(got, want) {
			t.Errorf("SendText(%q, %v) ran %q, want %q", tc.text, tc.noPaste, got, want)
		}
	}
}

// At a usage limit the coordinator closes Claude Code's menu, empties the
// prompt line and runs /login. herdr writes send-text's bytes unchanged:
// on herdr 0.9.3 these are the bytes of its esc, ctrl+e, ctrl+u and enter
// keys (1b, 05, 15, 0d).
func TestHerdrLoginKeysAtTheUsageLimitMenu(t *testing.T) {
	client, _, calls := fakeHerdr(t)
	ctx := context.Background()
	if _, err := client.ListPanes(ctx); err != nil {
		t.Fatal(err)
	}

	if err := sendKeys(ctx, client, 2, LoginKeys(usageLimitMenu)); err != nil {
		t.Fatal(err)
	}

	var typed []string
	for _, c := range calls()[1:] {
		if len(c) != 4 || c[0] != "pane" || c[1] != "send-text" || c[2] != "w1:p1F" {
			t.Fatalf("call %q, want pane send-text w1:p1F <text>", c)
		}
		typed = append(typed, c[3])
	}
	if want := []string{"\x1b", "\x05", "\x15", "/login\r"}; !slices.Equal(typed, want) {
		t.Fatalf("typed %q, want %q", typed, want)
	}
}

func TestHerdrRefusesAPaneItHasNotListed(t *testing.T) {
	client, _, calls := fakeHerdr(t)
	ctx := context.Background()

	if err := client.SendText(ctx, 1, "/login\n", true); err == nil {
		t.Fatal("SendText to an unlisted pane succeeded")
	}
	if _, err := client.GetText(ctx, 1, -100); err == nil {
		t.Fatal("GetText from an unlisted pane succeeded")
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("herdr was called: %q", c)
	}
}

func TestHerdrErrorNamesHerdrsCode(t *testing.T) {
	client, _, _ := fakeHerdr(t)
	ctx := context.Background()
	t.Setenv("FAKE_HERDR_FAIL", `{"id":"cli:pane:list","error":{"code":"server_not_running","message":"no herdr server is running at /run/herdr.sock; run `+"`herdr`"+` to start or attach it"}}`)

	_, err := client.ListPanes(ctx)
	if err == nil || !strings.Contains(err.Error(), "herdr pane list: server_not_running: no herdr server is running") {
		t.Fatalf("err = %v, want herdr's code and message", err)
	}
	if client.IsAvailable(ctx) {
		t.Fatal("IsAvailable = true without a herdr server")
	}
}

func TestHerdrListWithoutAResultIsAnError(t *testing.T) {
	client, list, _ := fakeHerdr(t)
	ctx := context.Background()

	// An error object, even with exit status 0, is no pane list.
	if err := os.WriteFile(list, []byte(`{"id":"cli:pane:list","error":{"code":"internal","message":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if panes, err := client.ListPanes(ctx); err == nil {
		t.Fatalf("ListPanes = %+v without a result, want an error", panes)
	}

	// An empty result is an empty list.
	if err := os.WriteFile(list, []byte(`{"id":"cli:pane:list","result":{"panes":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if panes, err := client.ListPanes(ctx); err != nil || len(panes) != 0 {
		t.Fatalf("ListPanes = %+v, %v; want no panes and no error", panes, err)
	}
}

func TestHerdrBackendIsSelectable(t *testing.T) {
	for _, in := range []string{"herdr", "HERDR", " herdr "} {
		got, err := ParseBackend(in)
		if err != nil || got != BackendHerdr {
			t.Errorf("ParseBackend(%q) = %q, %v; want herdr", in, got, err)
		}
	}
	if _, err := ParseBackend("screen"); err == nil || !strings.Contains(err.Error(), "herdr") {
		t.Errorf("ParseBackend(screen) error = %v, want it to name herdr", err)
	}

	cfg, err := FileConfig{Backend: "herdr"}.Apply(DefaultConfig())
	if err != nil || cfg.Backend != BackendHerdr {
		t.Fatalf("config backend = %q, %v; want herdr", cfg.Backend, err)
	}
	client := selectPaneClient(cfg.Backend, slog.Default())
	if _, ok := client.(*HerdrClient); !ok || client.Backend() != "herdr" {
		t.Fatalf("selectPaneClient(herdr) = %T", client)
	}
}
