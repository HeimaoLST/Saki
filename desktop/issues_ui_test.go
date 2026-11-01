package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestIssueWorkCardAndComposerStopLabelsAreSeparate(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	ct.ActiveTaskID = "task-B"
	ct.SendState = sendAccepted
	ct.Works = []*workItem{
		{TaskID: "task-A", State: workRunning, Prompt: "work A"},
		{TaskID: "task-B", State: workRunning, Prompt: "conversation B"},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stop issue task") || !tr.HasText("Stop conversation") {
		t.Fatalf("separate stop labels missing: %v", tr.Texts())
	}
	if !tr.HasText("Working") {
		t.Fatal("issue work state was not visible in detail")
	}
}

func TestIssueResultUsesIssueWorkMessages(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionCompleted}}
	issue.Work.Messages = []*message{{Key: "work-live", Kind: msgContact, TaskID: "task-A", Text: "issue-owned result"}}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "task-A", State: workCompleted}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("issue-owned result") {
		t.Fatalf("issue work result missing from issue UI: %v", tr.Texts())
	}
}

func TestRailShowsIssueWorkAttentionWithoutChatUnread(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "failed work", Status: issueStatusNotStarted}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionFailed, Detail: "provider failed"}}
	ct.Issues = []*productIssue{issue}
	ct.Unread = 2
	ct.Messages = nil
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Attention") {
		t.Fatalf("rail did not surface issue-work failure: %v", tr.Texts())
	}
	if ct.Unread != 2 {
		t.Fatalf("issue-work notice changed chat unread: %d", ct.Unread)
	}
}

func TestIssueRecoveryControlsRenderForUnknownExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "recover me", Status: issueStatusNotStarted}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", Status: issueExecutionUnknown, Detail: "unknown"}}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if !tr.HasText("Check execution") || !tr.HasText("Confirm not dispatched") {
		t.Fatalf("recovery controls missing: %v", tr.Texts())
	}
}

func TestCapacityUnknownNoticeIsDistinctFromRealSingleSlot(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.runtimeCapabilitiesLoaded = false
	a.runtimeCapabilitiesErr = ""
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("capacity is not loaded") || !tr.HasText("Retry capacity") {
		t.Fatalf("unknown capacity was not disclosed: %v", tr.Texts())
	}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 1
	tr.Frame()
	if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
		t.Fatalf("real single-slot capacity still shows unknown state: %v", tr.Texts())
	}
}

func TestCapacityNoticeHiddenBeforeRuntimeReady(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.rt != nil {
		t.Fatal("unconfigured first launch unexpectedly started a runtime")
	}
	if a.runtimeStatus == runtimeStateReady {
		t.Fatal("unconfigured first launch unexpectedly reported a ready runtime")
	}
	a.windowWidth = 1040
	render := func(dark bool) *ui.Tester {
		tr := ui.NewTester(a.view, 1040, 720)
		tr.SetDark(dark)
		tr.Frame()
		return tr
	}
	previewDir := os.Getenv("SAKI_PREVIEW_DIR")
	if previewDir != "" {
		if err := os.MkdirAll(previewDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
			t.Fatalf("unconfigured first launch showed a capacity notice: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-"+theme+"-1040x720.png"), tr.Image())
		}
	}

	a.closeSettings()
	startupError := a.runtimeErr
	if startupError == "" {
		t.Fatal("unconfigured first launch lost its real startup error")
	}
	for _, dark := range []bool{false, true} {
		tr := render(dark)
		if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
			t.Fatalf("unconfigured composer showed a capacity notice: %v", tr.Texts())
		}
		if !tr.HasText(startupError) || !tr.HasText("Configuration details") {
			t.Fatalf("unconfigured composer lost startup recovery controls: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-unconfigured-composer-"+theme+"-1040x720.png"), tr.Image())
		}
	}
	tr := render(false)
	if err := tr.Click("Configuration details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced || !tr.HasText("Startup & configuration") {
		t.Fatalf("Configuration details did not open Advanced: open=%v tab=%q texts=%v", a.settingsOpen, a.settingsTab, tr.Texts())
	}
	a.closeSettings()

	// A nil Runtime must not show an actionable retry even if status is ready.
	a.runtimeStatus = runtimeStateReady
	a.rt = nil
	a.runtimeCapabilitiesLoaded = false
	a.runtimeCapabilitiesErr = "Runtime capacity is not loaded: fixture failure."
	tr = render(false)
	if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
		t.Fatalf("nil Runtime with ready status showed a dead retry: %v", tr.Texts())
	}

	// Only a ready Runtime with a retry path may show the notice.
	a.rt = &runtimeClient{}
	for _, status := range []string{runtimeStateStarting, runtimeStateFailed, runtimeStateDraining, runtimeStateExited} {
		a.runtimeStatus = status
		tr = render(false)
		if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
			t.Fatalf("non-ready runtime %q showed an actionable capacity notice: %v", status, tr.Texts())
		}
	}

	a.runtimeStatus = runtimeStateReady
	a.runtimeErr = ""
	a.runtimeHint = ""
	a.runtimeStage = ""
	for _, dark := range []bool{false, true} {
		tr = render(dark)
		if !tr.HasText("capacity is not loaded") || !tr.HasText("Retry capacity") {
			t.Fatalf("ready runtime with failed capacity did not keep the notice and retry: %v", tr.Texts())
		}
		if previewDir != "" {
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(previewDir, "capacity-ready-unknown-"+theme+"-1040x720.png"), tr.Image())
		}
	}
}

func TestRailShowsActiveIssueWorkCount(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "active work", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	ct.Messages = nil
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("1 work") {
		t.Fatalf("rail did not show active work count: %v", tr.Texts())
	}
	if ct.Unread != 0 {
		t.Fatalf("active work count changed chat unread: %d", ct.Unread)
	}
}

func TestIssueConfirmNotDispatchedTargetsSelectedExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "recover me", Status: issueStatusNotStarted}
	target := &issueExecution{ID: "e1", SessionID: "work-session", Status: issueExecutionUnknown, Prompt: "target"}
	other := &issueExecution{ID: "e2", SessionID: "other-session", TaskID: "task-other", Status: issueExecutionCompleted, Prompt: "other"}
	issue.Executions = []*issueExecution{target, other}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	clickAfterBoundedScroll(t, tr, "Confirm not dispatched", 1040, 720)
	tr.Frame()
	if target.Status != issueExecutionNotDispatched {
		t.Fatalf("target execution = %+v", target)
	}
	if other.Status != issueExecutionCompleted || other.TaskID != "task-other" {
		t.Fatalf("confirm changed another execution: %+v", other)
	}
}

func TestCapacityRetryButtonRecoversThroughRuntimeClient(t *testing.T) {
	a, _ := workspaceTestApp(t)
	callbacks := make(chan func(), 8)
	a.setUIHook(func(fn func()) { callbacks <- fn })
	runtime := startTestRuntime(t, "RUNTIME_CAPABILITIES_FAIL_FIRST=1")
	a.rt = runtime.client
	a.runtimeStatus = runtimeStateReady
	a.runtimeCapabilitiesLoaded = false
	a.runtimeCapabilitiesErr = ""
	a.connectionGeneration = 0

	awaitStderr := func(want string) {
		t.Helper()
		select {
		case got := <-runtime.stderr:
			if got != want {
				t.Fatalf("runtime stderr = %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for runtime stderr %q", want)
		}
	}
	consumeCallback := func(label string) {
		t.Helper()
		select {
		case fn := <-callbacks:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s UI callback", label)
		}
	}

	a.loadCapabilities(runtime.client, a.connectionGeneration)
	consumeCallback("initial capability failure")
	awaitStderr("runtime.capabilities:1:error")
	if a.runtimeCapabilitiesLoaded || !strings.Contains(a.runtimeCapabilitiesErr, "Runtime capacity is not loaded") {
		t.Fatalf("initial failure state = loaded=%v err=%q", a.runtimeCapabilitiesLoaded, a.runtimeCapabilitiesErr)
	}
	if a.capacityKnown() || a.maxActiveTasks() != 1 {
		t.Fatalf("initial failure capacity = known=%v max=%d, want unknown/1", a.capacityKnown(), a.maxActiveTasks())
	}

	tester := ui.NewTester(a.view, 1040, 720)
	tester.Frame()
	if !tester.HasText("capacity is not loaded") || !tester.HasText("Retry capacity") {
		t.Fatalf("initial failed capability did not render unknown/retry: %v", tester.Texts())
	}
	if err := tester.Click("Retry capacity"); err != nil {
		t.Fatal(err)
	}

	consumeCallback("retry success")
	awaitStderr("runtime.capabilities:2:ok")
	if !a.runtimeCapabilitiesLoaded || a.runtimeCapabilitiesErr != "" || a.runtimeMaxActiveTasks != 3 {
		t.Fatalf("retry state = loaded=%v err=%q max=%d", a.runtimeCapabilitiesLoaded, a.runtimeCapabilitiesErr, a.runtimeMaxActiveTasks)
	}
	tester.Frame()
	if !a.capacityKnown() || a.maxActiveTasks() != 3 {
		t.Fatalf("retry did not restore known capacity: known=%v max=%d", a.capacityKnown(), a.maxActiveTasks())
	}
	if tester.HasText("capacity is not loaded") || tester.HasText("Retry capacity") {
		t.Fatalf("successful retry left unknown capacity UI: %v", tester.Texts())
	}

	if err := runtime.client.shutdown(); err != nil {
		t.Fatalf("runtime shutdown: %v", err)
	}
	awaitStderr("runtime.capabilities:final:2")
}
