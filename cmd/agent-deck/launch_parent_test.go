package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A session picked up automatically from a sub-session lands under that
// sub-session's parent. One selector serves launch, add and the capacity
// pre-check.

type parentFixture struct {
	parent, child, other *session.Instance
	all                  []*session.Instance
}

func newParentFixture() parentFixture {
	parent := session.NewInstanceWithGroupAndTool("Parent", "/proj/parent", "parent-group", "claude")
	parent.ID = "par-0001"
	child := session.NewInstanceWithGroupAndTool("Child", "/proj/child", "child-group", "claude")
	child.ID = "chd-0001"
	child.SetParentWithPath(parent.ID, parent.ProjectPath)
	other := session.NewInstanceWithGroupAndTool("Other", "/proj/other", "other-group", "claude")
	other.ID = "oth-0001"
	return parentFixture{parent: parent, child: child, other: other, all: []*session.Instance{parent, child, other}}
}

func setCaller(t *testing.T, id string) {
	t.Helper()
	t.Setenv("AGENT_DECK_SESSION_ID", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", id)
	t.Setenv("TMUX", "")
}

func lpErr(t *testing.T, err error) *launchParentError {
	t.Helper()
	var lpe *launchParentError
	if !errors.As(err, &lpe) {
		t.Fatalf("error %v (%T) is not a *launchParentError", err, err)
	}
	return lpe
}

func TestLaunchParent_TopLevelCallerBecomesParent(t *testing.T) {
	f := newParentFixture()
	setCaller(t, f.parent.ID)
	p, note, err := selectLaunchParent("", false, f.all)
	if err != nil || p != f.parent || note != "" {
		t.Fatalf("got parent=%v note=%q err=%v, want the parent, no note", p, note, err)
	}
}

func TestLaunchParent_SubsessionCallerAttachesToItsParent(t *testing.T) {
	f := newParentFixture()
	setCaller(t, f.child.ID)
	p, note, err := selectLaunchParent("", false, f.all)
	if err != nil || p != f.parent {
		t.Fatalf("got parent=%v err=%v, want the parent", p, err)
	}
	if p.ProjectPath != "/proj/parent" {
		t.Fatalf("parent project path %q, want the parent's", p.ProjectPath)
	}
	if !strings.Contains(note, f.child.Title) || !strings.Contains(note, f.parent.Title) {
		t.Fatalf("note %q must name both titles", note)
	}
}

func TestLaunchParent_ExplicitSubsessionParentKeepsTheOldError(t *testing.T) {
	f := newParentFixture()
	for _, caller := range []string{"", f.child.ID} {
		setCaller(t, caller)
		for _, explicit := range []string{f.child.Title, f.child.ID} {
			p, note, err := selectLaunchParent(explicit, false, f.all)
			if p != nil || note != "" || err == nil {
				t.Fatalf("explicit %q from %q: got parent=%v note=%q err=%v, want the single-level error", explicit, caller, p, note, err)
			}
			lpe := lpErr(t, err)
			if lpe.Message != "cannot create sub-session of a sub-session (single level only)" || lpe.Code != ErrCodeInvalidOperation {
				t.Fatalf("explicit %q from %q: got %+v, want the original message and code", explicit, caller, lpe)
			}
		}
	}
}

func TestLaunchParent_ExplicitTopLevelParentUnchanged(t *testing.T) {
	f := newParentFixture()
	setCaller(t, f.child.ID)
	p, note, err := selectLaunchParent(f.parent.Title, false, f.all)
	if err != nil || p != f.parent || note != "" {
		t.Fatalf("got parent=%v note=%q err=%v, want the parent, no note", p, note, err)
	}
}

func TestLaunchParent_ExplicitParentUnknownErrors(t *testing.T) {
	f := newParentFixture()
	setCaller(t, "")
	p, _, err := selectLaunchParent("no-such-session", false, f.all)
	if p != nil || err == nil {
		t.Fatalf("got parent=%v err=%v, want not-found error", p, err)
	}
	_, wantMsg, _ := ResolveSession("no-such-session", f.all)
	if lpe := lpErr(t, err); lpe.Message != wantMsg || lpe.Code != ErrCodeNotFound {
		t.Fatalf("got %+v, want message %q code %q", lpe, wantMsg, ErrCodeNotFound)
	}
}

func TestLaunchParent_GrandparentMissingOrNotTopLevelErrors(t *testing.T) {
	f := newParentFixture()

	orphan := session.NewInstanceWithGroupAndTool("Orphan", "/proj/o", "g", "claude")
	orphan.ID = "orp-0001"
	orphan.SetParentWithPath("gone-0001", "/proj/gone")
	setCaller(t, orphan.ID)
	p, _, err := selectLaunchParent("", false, append(f.all, orphan))
	if p != nil || err == nil || !strings.Contains(err.Error(), orphan.ID) || !strings.Contains(err.Error(), "gone-0001") {
		t.Fatalf("dangling parent: got parent=%v err=%v, want an error naming both ids", p, err)
	}

	deep := session.NewInstanceWithGroupAndTool("Deep", "/proj/d", "g", "claude")
	deep.ID = "dep-0001"
	deep.SetParentWithPath(f.child.ID, f.child.ProjectPath)
	setCaller(t, deep.ID)
	p, _, err = selectLaunchParent("", false, append(f.all, deep))
	if p != nil || err == nil || !strings.Contains(err.Error(), deep.ID) || !strings.Contains(err.Error(), f.child.ID) {
		t.Fatalf("two-level chain: got parent=%v err=%v, want an error naming both ids, no walking further", p, err)
	}
	p, _, err = selectLaunchParent(deep.Title, false, append(f.all, deep))
	if p != nil || err == nil {
		t.Fatalf("explicit two-level chain: got parent=%v err=%v, want an error", p, err)
	}
}

func TestLaunchParent_NoParentFlagStaysTopLevel(t *testing.T) {
	f := newParentFixture()
	for _, tc := range []struct {
		name, caller string
		wantNote     bool
	}{
		{"no identity", "", false},
		{"top-level", f.parent.ID, false},
		{"sub-session", f.child.ID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setCaller(t, tc.caller)
			p, note, err := selectLaunchParent("", true, f.all)
			if err != nil || p != nil {
				t.Fatalf("got parent=%v err=%v, want top-level", p, err)
			}
			if tc.wantNote {
				if !strings.Contains(note, f.parent.Title) {
					t.Fatalf("note %q must name the parent %q", note, f.parent.Title)
				}
			} else if note != "" {
				t.Fatalf("unexpected note %q", note)
			}
		})
	}
}

func TestLaunchParent_NoIdentityStaysTopLevel(t *testing.T) {
	f := newParentFixture()
	setCaller(t, "")
	p, note, err := selectLaunchParent("", false, f.all)
	if p != nil || note != "" || err != nil {
		t.Fatalf("got parent=%v note=%q err=%v, want none of them", p, note, err)
	}
}

func TestLaunchParent_StaleIdentityErrors(t *testing.T) {
	f := newParentFixture()
	setCaller(t, "stale-9999")
	p, _, err := selectLaunchParent("", false, f.all)
	if p != nil || err == nil || !strings.Contains(err.Error(), "stale-9999") {
		t.Fatalf("got parent=%v err=%v, want an error naming the stale id", p, err)
	}
}

func TestLaunchParent_NoParentIgnoresStaleIdentity(t *testing.T) {
	f := newParentFixture()
	setCaller(t, "stale-9999")
	p, note, err := selectLaunchParent("", true, f.all)
	if p != nil || note != "" || err != nil {
		t.Fatalf("got parent=%v note=%q err=%v, want top-level, no error, no note", p, note, err)
	}
}

func TestLaunchParent_SelectorErrorCarriesTheFailingID(t *testing.T) {
	f := newParentFixture()
	setCaller(t, "stale-9999")
	_, _, err := selectLaunchParent("", false, f.all)
	if lpe := lpErr(t, err); lpe.UnresolvedID != "stale-9999" {
		t.Fatalf("UnresolvedID %q, want the stale id", lpe.UnresolvedID)
	}
}

func TestLaunchParent_ReplaceCloneCallShapesUnchanged(t *testing.T) {
	f := newParentFixture()
	// Clone of a sub-session: -parent=<its top-level parent>, launched from anywhere.
	for _, caller := range []string{"", f.parent.ID, f.child.ID} {
		setCaller(t, caller)
		p, note, err := selectLaunchParent(f.parent.Title, false, f.all)
		if err != nil || p != f.parent || note != "" {
			t.Fatalf("sub-session clone from %q: parent=%v note=%q err=%v", caller, p, note, err)
		}
	}
	// Clone of a top-level session: -no-parent, launched from a top-level session or none.
	for _, caller := range []string{"", f.parent.ID, f.other.ID} {
		setCaller(t, caller)
		p, note, err := selectLaunchParent("", true, f.all)
		if err != nil || p != nil || note != "" {
			t.Fatalf("top-level clone from %q: parent=%v note=%q err=%v", caller, p, note, err)
		}
	}
}

func TestLaunchParent_OnlyTheSelectorCallsTheAutoResolver(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(raw, []byte("\n")) {
			s := strings.TrimSpace(string(line))
			if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "func resolveAutoParentInstanceChecked") {
				continue
			}
			if strings.Contains(s, "resolveAutoParentInstanceChecked(") {
				sites = append(sites, name)
				_ = i
			}
		}
	}
	if len(sites) != 1 || sites[0] != "launch_parent.go" {
		t.Fatalf("resolveAutoParentInstanceChecked call sites outside tests: %v, want exactly one, in launch_parent.go", sites)
	}
}

// seedParentRegistry stores the fixture (parent, child, other) in a profile.
func seedParentRegistry(t *testing.T, profile, parentPath string) parentFixture {
	t.Helper()
	f := newParentFixture()
	if parentPath != "" {
		f.parent.ProjectPath = parentPath
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	groupTree := session.NewGroupTreeWithGroups(f.all, []*session.GroupData{
		{Name: "parent-group", Path: "parent-group", Expanded: true, DefaultPath: parentPath},
		{Name: "child-group", Path: "child-group", Expanded: true},
		{Name: "other-group", Path: "other-group", Expanded: true},
	})
	if err := storage.SaveWithGroups(f.all, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	return f
}

func TestLaunchParent_PrecheckAcceptsSubsessionCaller(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)
	f := seedParentRegistry(t, profile, "")
	setCaller(t, f.child.ID)

	var group string
	if err := validateStartupQueryCapacity(profile, "", "", t.TempDir(), false, true, true, &group); err != nil {
		t.Fatalf("child caller refused by the pre-check: %v", err)
	}
	if group != "parent-group" {
		t.Fatalf("fallback group %q, want the parent's group, never the child's", group)
	}
	group = ""
	if err := validateStartupQueryCapacity(profile, "explicit-group", "", t.TempDir(), false, true, true, &group); err != nil {
		t.Fatalf("child caller with -g refused: %v", err)
	}
	if group != "explicit-group" {
		t.Fatalf("explicit group overridden: %q", group)
	}
	// The stale-id message is unchanged and still names the id.
	setCaller(t, "stale-9999")
	err := validateStartupQueryCapacity(profile, "", "", t.TempDir(), false, true, true, nil)
	if err == nil || err.Error() != `automatic parent "stale-9999" could not be resolved; use --no-parent` {
		t.Fatalf("stale-id pre-check message changed: %v", err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	return <-done
}

func TestLaunchParent_AddSubsessionCallerGroupFromParent(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	parentPath := filepath.Join(home, "parent-project")
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	f := seedParentRegistry(t, profile, parentPath)
	setCaller(t, f.child.ID)

	stderr := captureStderr(t, func() {
		captureStdout(t, func() { handleAdd(profile, []string{"--title", "helper", "--quiet"}) })
	})

	st, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	insts, _, err := st.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	var helper *session.Instance
	for _, inst := range insts {
		if inst.Title == "helper" {
			helper = inst
		}
	}
	if helper == nil {
		t.Fatal("helper not stored")
	}
	if helper.ParentSessionID != f.parent.ID {
		t.Fatalf("helper parent %q, want the parent %q", helper.ParentSessionID, f.parent.ID)
	}
	if helper.GroupPath != "parent-group" {
		t.Fatalf("helper group %q, want the parent's group, never the child's", helper.GroupPath)
	}
	if helper.ProjectPath != parentPath {
		t.Fatalf("helper path %q, want the parent group's default path %q", helper.ProjectPath, parentPath)
	}
	if !strings.Contains(stderr, "linked under its parent") {
		t.Fatalf("stderr %q lacks the note", stderr)
	}
}
