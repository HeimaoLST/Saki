package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func layoutAppAt(t *testing.T, width int) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.windowWidth = width
	a.workspaceOpen = true
	return a, ct
}

func TestLayoutNewAndLegacyStoresUseClosedRightPaneDefaults(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Layout != nil {
		t.Fatal("new store should not invent a layout preference")
	}
	a := &appModel{}
	applyStoreSelection(a, store)
	if a.workspaceOpen || a.railWidth() != defaultLeftPaneWidth || a.workWidth() != defaultRightPaneWidth {
		t.Fatalf("new-store layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}

	store.state.Layout = &storedLayout{LeftWidth: 240, RightWidth: 380, WorkspaceOpen: true}
	applyStoreSelection(a, store)
	if !a.workspaceOpen || a.railWidth() != 240 || a.workWidth() != 380 {
		t.Fatalf("stored layout = open=%v left=%v right=%v", a.workspaceOpen, a.railWidth(), a.workWidth())
	}
	store.state.Layout = nil
	applyStoreSelection(a, store)
	if a.workspaceOpen {
		t.Fatal("legacy store without layout must start with the right pane closed")
	}
}

func TestLayoutEntryPointsStartWithRightPaneClosed(t *testing.T) {
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen {
		t.Fatal("demo app started with the right pane open")
	}
	u := newUnavailableApp(errors.New("unreadable contact store"))
	if u.workspaceOpen {
		t.Fatal("unavailable app started with the right pane open")
	}

	storePath := filepath.Join(t.TempDir(), "contacts.json")
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_SESSION_DIR", t.TempDir())
	t.Setenv("SAKI_GUI_WORKDIR", t.TempDir())
	r, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if r.workspaceOpen {
		t.Fatal("first runtime app started with the right pane open")
	}
}

func TestLayoutPersistsGlobalPreferenceAndReloads(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	a.leftPaneWidth = 225
	a.rightPaneWidth = 365
	a.workspaceOpen = true
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.LeftWidth != 225 ||
		reloaded.state.Layout.RightWidth != 365 || !reloaded.state.Layout.WorkspaceOpen {
		t.Fatalf("stored layout = %+v", reloaded.state.Layout)
	}
	b := &appModel{}
	applyStoreSelection(b, reloaded)
	if !b.workspaceOpen || b.railWidth() != 225 || b.workWidth() != 365 {
		t.Fatalf("reloaded layout = open=%v left=%v right=%v", b.workspaceOpen, b.railWidth(), b.workWidth())
	}
}

func TestLayoutDragClampsBothEdgesAndKeepsCenterWidest(t *testing.T) {
	a, ct := layoutAppAt(t, 1040)
	ct.Draft = "keep this draft"
	ct.ActiveTaskID = "task-keep"
	ct.LastTaskID = "task-keep"
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "keep this message"}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()

	tr.Press(200, 300)
	tr.Move(260, 300)
	// Still held: OnChange must clamp before the release/rebuild.
	if center := float32(1040) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("left held center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	tr.Release(260, 300)
	tr.Frame()
	if a.railWidth() < minLeftPaneWidth || a.railWidth() > maxLeftPaneWidth {
		t.Fatalf("left drag width = %v", a.railWidth())
	}
	if center := float32(1040) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("left drag center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	if ct.Draft != "keep this draft" || ct.ActiveTaskID != "task-keep" || len(ct.Messages) != 1 {
		t.Fatal("left divider drag changed chat/task state")
	}
	if r, ok := tr.Find("Close work"); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 1040 {
		t.Fatalf("close-work control after left drag = ok=%v rect=%+v", ok, r)
	}

	a, ct = layoutAppAt(t, 1040)
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	rightX := float32(1040) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 300)
	tr.Move(rightX-100, 300)
	if center := float32(1040) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("right held center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	tr.Release(rightX-100, 300)
	tr.Frame()
	if a.workWidth() < minRightPaneWidth || a.workWidth() > maxRightPaneWidth {
		t.Fatalf("right drag width = %v", a.workWidth())
	}
	if center := float32(1040) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth || center <= a.railWidth() || center <= a.workWidth() {
		t.Fatalf("right drag center = %v, left=%v right=%v", center, a.railWidth(), a.workWidth())
	}
	if ct.Draft != "" || ct.ActiveTaskID != "" || len(ct.Messages) != 0 {
		t.Fatal("right divider drag changed chat/task state")
	}
}

func TestLayoutPreferenceIsNotOverwrittenByNarrowViewport(t *testing.T) {
	a, _ := layoutAppAt(t, 1380)
	a.leftPaneWidth = 280
	a.rightPaneWidth = 440
	tr := ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	if a.workOverlay() {
		t.Fatal("1380 with minimum center should remain three-pane")
	}
	a.windowWidth = 960
	tr.SetSize(960, 640)
	tr.Frame()
	if !a.workOverlay() {
		t.Fatal("960 should use the work overlay when the columns do not fit")
	}
	if a.railWidth() != 280 || a.workWidth() != 440 {
		t.Fatalf("narrow viewport rewrote preference: left=%v right=%v", a.railWidth(), a.workWidth())
	}
	a.windowWidth = 1380
	tr.SetSize(1380, 900)
	tr.Frame()
	if a.workOverlay() || a.railWidth() != 280 || a.workWidth() != 440 {
		t.Fatalf("wide viewport did not restore preference: overlay=%v left=%v right=%v", a.workOverlay(), a.railWidth(), a.workWidth())
	}
}

func TestLayoutOverlayAt960And720ClosesWithEscape(t *testing.T) {
	for _, width := range []int{960, 720} {
		a, _ := layoutAppAt(t, width)
		tr := ui.NewTester(a.view, width, 640)
		tr.Frame()
		if !a.workOverlay() || !tr.HasText("Close work") {
			t.Fatalf("%d overlay missing: overlay=%v", width, a.workOverlay())
		}
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
		if a.workspaceOpen || !tr.Focused("Work") {
			t.Fatalf("%d Escape = open=%v focusedWork=%v", width, a.workspaceOpen, tr.Focused("Work"))
		}
	}
}

func TestLayoutEventsAndContactSwitchDoNotToggleWork(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.contacts = append(a.contacts, &contact{ID: "mio", Name: "Mio", Role: "Assistant"})
	a.selectContact(1)
	if a.workspaceOpen {
		t.Fatal("contact switch opened work")
	}
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_started"}})
	if a.workspaceOpen {
		t.Fatal("task start opened work")
	}
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"}})
	if a.workspaceOpen {
		t.Fatal("task finish opened work")
	}
	a.workspaceOpen = true
	a.selectContact(0)
	a.applyEvent(ct, runtimeEventEnvelope{Version: 1, Type: "event", TaskID: "t2",
		Event: &runtimeEvent{Type: "task_started"}})
	if !a.workspaceOpen {
		t.Fatal("task start closed work")
	}
}

func TestClosedWorkEntryShowsRealWorkStatus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	ct.Status = statusWorking
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Work running") || len(ct.Messages) != 0 {
		t.Fatalf("running hint = %v messages=%d", tr.Texts(), len(ct.Messages))
	}
	ct.Status = statusAttention
	tr.Frame()
	if !tr.HasText("Work needs attention") || len(ct.Messages) != 0 {
		t.Fatalf("attention hint = %v messages=%d", tr.Texts(), len(ct.Messages))
	}
}

func TestLayoutDragIsNotPersistedPerMove(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Press(200, 300)
	tr.Move(240, 300)
	tr.Release(240, 300)
	tr.Frame()
	if a.railWidth() == defaultLeftPaneWidth {
		t.Fatal("drag did not change the in-memory left width")
	}
	if store.state.Layout != nil {
		t.Fatal("dragging wrote the store before an explicit boundary")
	}
	a.setWorkspaceOpen(false)
	if store.state.Layout == nil || store.state.Layout.WorkspaceOpen || store.state.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("explicit close layout = %+v", store.state.Layout)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.WorkspaceOpen || reloaded.state.Layout.LeftWidth != a.railWidth() {
		t.Fatalf("reloaded explicit layout = %+v", reloaded.state.Layout)
	}
}

func TestLayoutExplicitIssueExecutionOpensAndClosesWork(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	issue := &productIssue{ID: "i-layout", ContactID: ct.ID, Goal: "open execution", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "t-layout", State: workCompleted}}
	issue.Executions = []*issueExecution{{ID: "e-layout", TaskID: "t-layout", Status: issueExecutionCompleted}}
	a.showIssueWork(issue.ID, "t-layout")
	if !a.workspaceOpen || ct.SelectedWorkTaskID != "t-layout" {
		t.Fatalf("explicit execution open = open=%v selected=%q", a.workspaceOpen, ct.SelectedWorkTaskID)
	}
	a.closeWorkspace()
	if a.workspaceOpen {
		t.Fatal("explicit execution close did not close work")
	}
}

func TestLayoutRightDividerKeyboardUsesPhysicalDirection(t *testing.T) {
	a, _ := layoutAppAt(t, 1040)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	rightX := float32(1040) - a.workWidth() - paneSeparatorWidth
	tr.ClickAt(rightX, 300)
	tr.Frame()
	before := a.workWidth()
	tr.Key(0, ui.KeyLeft)
	tr.Frame()
	if a.workWidth() <= before {
		t.Fatalf("Left on right divider should widen work: before=%v after=%v", before, a.workWidth())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if a.workWidth() != before {
		t.Fatalf("Right on right divider should restore work: before=%v after=%v", before, a.workWidth())
	}
}

func TestLayoutWideDragAllowsPreferredRanges(t *testing.T) {
	a, _ := layoutAppAt(t, 1380)
	tr := ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	tr.Press(200, 400)
	tr.Move(400, 400)
	tr.Release(400, 400)
	tr.Frame()
	if a.railWidth() != maxLeftPaneWidth {
		t.Fatalf("wide left max = %v", a.railWidth())
	}

	a, _ = layoutAppAt(t, 1380)
	tr = ui.NewTester(a.view, 1380, 900)
	tr.Frame()
	rightX := float32(1380) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 400)
	tr.Move(rightX-300, 400)
	tr.Release(rightX-300, 400)
	tr.Frame()
	if a.workWidth() != maxRightPaneWidth {
		t.Fatalf("wide right max = %v", a.workWidth())
	}
	if center := float32(1380) - a.railWidth() - a.workWidth() - 2*paneSeparatorWidth; center < minCenterPaneWidth {
		t.Fatalf("wide center = %v", center)
	}
}

func TestLayoutDragHonorsMinimumPaneWidths(t *testing.T) {
	a, _ := layoutAppAt(t, 1040)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Press(200, 300)
	tr.Move(0, 300)
	tr.Release(0, 300)
	tr.Frame()
	if a.railWidth() != minLeftPaneWidth {
		t.Fatalf("left minimum = %v", a.railWidth())
	}

	a, _ = layoutAppAt(t, 1040)
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	rightX := float32(1040) - a.workWidth() - paneSeparatorWidth
	tr.Press(rightX, 300)
	tr.Move(1040, 300)
	tr.Release(1040, 300)
	tr.Frame()
	if a.workWidth() != minRightPaneWidth {
		t.Fatalf("right minimum = %v", a.workWidth())
	}
}

func TestLayoutExplicitTogglePersistsOpenAndClosed(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store}
	applyStoreSelection(a, store)
	if a.workspaceOpen {
		t.Fatal("new layout app must start closed")
	}
	a.setWorkspaceOpen(true)
	if store.state.Layout == nil || !store.state.Layout.WorkspaceOpen {
		t.Fatalf("open was not persisted: %+v", store.state.Layout)
	}
	a.setWorkspaceOpen(false)
	if store.state.Layout == nil || store.state.Layout.WorkspaceOpen {
		t.Fatalf("close was not persisted: %+v", store.state.Layout)
	}
	reloaded, err := openContactStore(storePath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Layout == nil || reloaded.state.Layout.WorkspaceOpen {
		t.Fatalf("reloaded closed layout = %+v", reloaded.state.Layout)
	}
}
