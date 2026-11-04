package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

// openIssueDetailForTest is the explicit UI-equivalent action used by older
// issue-detail tests now that opening Work defaults to the overview.
func openIssueDetailForTest(a *appModel, ct *contact, issueID string) {
	if ct == nil {
		return
	}
	ct.SelectedIssueID = issueID
	nav := ct.issueNavigation()
	nav.DetailID = issueID
	nav.ShowConversationWork = false
}

func openConversationWorkForTest(ct *contact) {
	if ct == nil {
		return
	}
	ct.issueNavigation().ShowConversationWork = true
}

func openConversationWorkPanel(t *testing.T, tr *ui.Tester) {
	t.Helper()
	if err := tr.Click("Chat runs"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
}

func TestIssueOverviewDefaultsToRowsAndOneFilter(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	ct.Issues = []*productIssue{
		{ID: "i-active", ContactID: ct.ID, Goal: "active goal", Status: issueStatusNotStarted},
		{ID: "i-wait", ContactID: ct.ID, Goal: "waiting goal", Status: issueStatusWaitingUser},
		{ID: "i-attn", ContactID: ct.ID, Goal: "attention goal", Status: issueStatusNeedsAttention},
		{ID: "i-done", ContactID: ct.ID, Goal: "completed goal", Status: issueStatusCompleted},
		{ID: "i-cancel", ContactID: ct.ID, Goal: "canceled goal", Status: issueStatusCanceled},
		{ID: "i-arch", ContactID: ct.ID, Goal: "archived goal", Status: issueStatusCompleted, Archived: true},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"active goal", "waiting goal", "attention goal"} {
		if !tr.HasText(want) {
			t.Fatalf("unfinished overview missing %q: %v", want, tr.Texts())
		}
	}
	for _, notWant := range []string{"completed goal", "canceled goal", "archived goal", "Latest execution", "More issue actions"} {
		if tr.HasText(notWant) {
			t.Fatalf("overview rendered detail/out-of-filter content %q: %v", notWant, tr.Texts())
		}
	}
	if err := tr.Click("Unfinished"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Completed"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().Filter != issueFilterCompleted || !tr.HasText("completed goal") ||
		tr.HasText("active goal") || tr.HasText("canceled goal") {
		t.Fatalf("completed projection = filter=%q texts=%v", ct.issueNavigation().Filter, tr.Texts())
	}
	if err := tr.Click("Completed"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("All"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().Filter != issueFilterAll || !tr.HasText("canceled goal") || !tr.HasText("archived goal") {
		t.Fatalf("all projection = filter=%q texts=%v", ct.issueNavigation().Filter, tr.Texts())
	}
	if !tr.HasText("Archived") {
		t.Fatalf("archived row did not mark management state: %v", tr.Texts())
	}
}

func TestIssueOverviewDetailBackKeepsRowScrollFocusAndDraft(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	ct.Draft = "draft remains"
	for i := 0; i < 20; i++ {
		ct.Issues = append(ct.Issues, &productIssue{
			ID: fmt.Sprintf("i-%02d", i), ContactID: ct.ID,
			Goal: fmt.Sprintf("Issue %02d", i), Status: issueStatusNotStarted,
		})
	}
	ct.SelectedIssueID = "i-00"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Scroll(900, 300, 0, 900)
	tr.Frame()
	const target = "Issue 15"
	if _, ok := tr.Find(target); !ok {
		t.Fatalf("scrolled target row missing: %v", tr.Texts())
	}
	nav := ct.issueNavigation()
	scrollBefore := nav.OverviewScroll.Y
	if err := tr.Click(target); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if nav.DetailID != "i-15" || ct.SelectedIssueID != "i-15" || !tr.HasText("Back to tasks") {
		t.Fatalf("detail open = nav=%q selected=%q texts=%v", nav.DetailID, ct.SelectedIssueID, tr.Texts())
	}
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if nav.DetailID != "" || ct.SelectedIssueID != "i-15" || !tr.Focused(target) {
		t.Fatalf("back = detail=%q selected=%q focused=%v texts=%v", nav.DetailID, ct.SelectedIssueID, tr.Focused(target), tr.Texts())
	}
	if nav.OverviewScroll.Y != scrollBefore {
		t.Fatalf("back changed overview scroll: %v -> %v", scrollBefore, nav.OverviewScroll.Y)
	}
	if ct.Draft != "draft remains" {
		t.Fatalf("detail/back changed draft: %q", ct.Draft)
	}
}

func TestIssueNavigationIsPerContact(t *testing.T) {
	a, first := workspaceTestApp(t)
	a.windowWidth = 1040
	first.Issues = []*productIssue{{ID: "ia", ContactID: first.ID, Goal: "first issue", Status: issueStatusNotStarted}}
	second := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Draft: "second draft"}
	second.Issues = []*productIssue{{ID: "ib", ContactID: second.ID, Goal: "second issue", Status: issueStatusCompleted}}
	a.contacts = append(a.contacts, second)
	openIssueDetailForTest(a, first, "ia")
	openIssueDetailForTest(a, second, "ib")
	first.issueNavigation().Filter = issueFilterAll
	second.issueNavigation().Filter = issueFilterCompleted
	a.selected = 0
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("first issue") || tr.HasText("second issue") {
		t.Fatalf("first contact detail leaked: %v", tr.Texts())
	}
	a.selectContact(1)
	tr.Frame()
	if !tr.HasText("second issue") || tr.HasText("first issue") || second.Draft != "second draft" {
		t.Fatalf("second contact detail/draft = %v", tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()
	if !tr.HasText("first issue") || first.issueNavigation().DetailID != "ia" ||
		first.issueNavigation().Filter != issueFilterAll {
		t.Fatalf("first contact navigation lost: %+v", first.issueNavigation())
	}
}

func TestAcceptTaskDoesNotStealIssueDetail(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i-task", ContactID: ct.ID, Goal: "task issue", Status: issueStatusInProgress,
		Executions: []*issueExecution{{ID: "e-task", Status: issueExecutionPending, Prompt: "task"}},
	}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	ct.pendingIssueExecution = "e-task"
	a.acceptTask(ct, "task", runtimeSendResult{TaskID: "task-1", MessageID: "message-1"})
	if ct.SelectedWorkTaskID != "" || ct.issueNavigation().DetailID != issue.ID {
		t.Fatalf("accepted task stole navigation: work=%q detail=%q", ct.SelectedWorkTaskID, ct.issueNavigation().DetailID)
	}
}

func TestIssueEscapeLayersInWideAndOverlayWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		overlay bool
	}{
		{"wide", 1040, false},
		{"overlay720", 720, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth = tc.width
			a.workspaceOpen = true
			issue := &productIssue{ID: "i-esc", ContactID: ct.ID, Goal: "escape issue", Status: issueStatusInProgress}
			ct.Issues = []*productIssue{issue}
			openIssueDetailForTest(a, ct, issue.ID)
			tr := ui.NewTester(a.view, tc.width, 640)
			tr.Frame()
			if (a.workOverlay() != tc.overlay) || !tr.HasText("Back to tasks") {
				t.Fatalf("setup = overlay=%v detail=%v", a.workOverlay(), tr.HasText("Back to tasks"))
			}
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			if ct.issueNavigation().DetailID != "" || !a.workspaceOpen {
				t.Fatalf("first Escape did not return to overview: detail=%q open=%v", ct.issueNavigation().DetailID, a.workspaceOpen)
			}
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			if a.workspaceOpen || !tr.Focused("Work") {
				t.Fatalf("second Escape did not close/focus Work: open=%v", a.workspaceOpen)
			}
		})
	}
}

func TestIssueEscapeExecutionThenDetailThenOverlay(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 720
	a.workspaceOpen = true
	issue := &productIssue{ID: "i-exec", ContactID: ct.ID, Goal: "execution issue", Status: issueStatusInProgress,
		Executions: []*issueExecution{{ID: "e-exec", TaskID: "task-exec", Status: issueExecutionCompleted}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.SelectedWorkTaskID = "task-exec"
	ct.Works = []*workItem{{TaskID: "task-exec", State: workCompleted}}
	ct.issueNavigation().DetailID = issue.ID
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if ct.SelectedWorkTaskID != "" || ct.issueNavigation().DetailID != issue.ID || !a.workspaceOpen {
		t.Fatalf("first Escape = work=%q detail=%q open=%v", ct.SelectedWorkTaskID, ct.issueNavigation().DetailID, a.workspaceOpen)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if ct.issueNavigation().DetailID != "" || !a.workspaceOpen {
		t.Fatalf("second Escape = detail=%q open=%v", ct.issueNavigation().DetailID, a.workspaceOpen)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen {
		t.Fatal("third Escape did not close the overlay")
	}
}

func TestIssueAuxEscapePreservesDetailLayer(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 720
	a.workspaceOpen = true
	issue := &productIssue{ID: "i-aux", ContactID: ct.ID, Goal: "aux issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	a.openSettings()
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabContact || !tr.HasText("Contact profile") {
		t.Fatalf("settings did not open over detail: open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if !a.settingsOpen || !a.workspaceOpen || ct.issueNavigation().DetailID != issue.ID {
		t.Fatalf("Escape closed settings/work layer: open=%v work=%v detail=%q", a.settingsOpen, a.workspaceOpen, ct.issueNavigation().DetailID)
	}
	a.closeSettings()
	tr.Frame()
	if !a.workspaceOpen || ct.issueNavigation().DetailID != issue.ID {
		t.Fatalf("return changed work layer: open=%v detail=%q", a.workspaceOpen, ct.issueNavigation().DetailID)
	}
}

func TestIssueCompletedExecutionDoesNotImplyWholeDone(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i-partial", ContactID: ct.ID, Goal: "partial issue", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("must remain unverified"),
		Executions: []*issueExecution{{ID: "e-partial", TaskID: "task-partial", Status: issueExecutionCompleted}},
	}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if issue.Status != issueStatusInProgress || issue.Requirements[0].State != requirementUnverified {
		t.Fatalf("execution terminal changed issue/requirement: %q / %q", issue.Status, issue.Requirements[0].State)
	}
	if !tr.HasText("This execution ended; the issue remains open.") || !tr.HasText("Unverified") {
		t.Fatalf("partial completed execution was not explained: %v", tr.Texts())
	}
}

func TestManualCompleteShowsUserSourceAndKeepsUnverified(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i-manual", ContactID: ct.ID, Goal: "manual issue", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("must remain unverified")}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	if !a.completeIssue(issue.ID, "reviewed by the user") {
		t.Fatal("manual complete failed")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if issue.Status != issueStatusCompleted || issue.Requirements[0].State != requirementUnverified ||
		!tr.HasText("Marked by user: reviewed by the user") || !tr.HasText("Unverified") {
		t.Fatalf("manual completion = status=%q req=%q texts=%v", issue.Status, issue.Requirements[0].State, tr.Texts())
	}
}

func TestIssueBackAfterOutOfFilterActionKeepsFilterAndScroll(t *testing.T) {
	cases := []struct {
		name string
		act  func(*appModel, string) bool
	}{
		{"cancel", func(a *appModel, id string) bool { return a.cancelIssueProduct(id) }},
		{"archive", func(a *appModel, id string) bool { return a.archiveIssue(id) }},
		{"complete", func(a *appModel, id string) bool { return a.completeIssue(id, "user evidence") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth = 1040
			issue := &productIssue{ID: "i-out", ContactID: ct.ID, Goal: "leaves filter", Status: issueStatusInProgress,
				RequirementsRevision: 1, Requirements: parseIssueRequirements("still unverified")}
			ct.Issues = []*productIssue{issue}
			nav := ct.issueNavigation()
			nav.Filter = issueFilterUnfinished
			nav.OverviewScroll.Y = 123
			openIssueDetailForTest(a, ct, issue.ID)
			if !tc.act(a, issue.ID) {
				t.Fatal("action failed")
			}
			a.backToIssueOverview()
			if nav.Filter != issueFilterUnfinished || nav.OverviewScroll.Y != 123 {
				t.Fatalf("back changed filter/scroll: filter=%q y=%v", nav.Filter, nav.OverviewScroll.Y)
			}
			if nav.DetailID != "" || nav.FocusRowID != issue.ID || ct.SelectedIssueID != issue.ID {
				t.Fatalf("back state = detail=%q focus=%q selected=%q", nav.DetailID, nav.FocusRowID, ct.SelectedIssueID)
			}
		})
	}
}

func TestIssueDetailTopRowFitsNarrowWorkPanel(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.rightPaneWidth = 280
	a.workspaceOpen = true
	issue := &productIssue{ID: "i-narrow", ContactID: ct.ID, Goal: "narrow panel issue", Status: issueStatusInProgress,
		Work:       issueWorkSession{Loaded: true, ActiveTaskID: "task-narrow", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "e-narrow", TaskID: "task-narrow", Status: issueExecutionRunning}},
	}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "task-narrow", State: workRunning, Prompt: "narrow"}}
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	panel := float32(1040) - a.workWidth() - paneSeparatorWidth
	back, backOK := tr.Find("Back to tasks")
	stop, stopOK := tr.Find("Stop issue task")
	if !backOK || !stopOK || back.W <= 0 || stop.W <= 0 || back.X < panel || stop.X+stop.W > 1040 ||
		back.X+back.W > stop.X {
		t.Fatalf("narrow top row = panel=%v back=%+v/%v stop=%+v/%v", panel, back, backOK, stop, stopOK)
	}
}

func TestIssueStoppingDetailDisablesStopLabel(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	issue := &productIssue{ID: "i-stopping", ContactID: ct.ID, Goal: "stopping issue", Status: issueStatusInProgress,
		Work:       issueWorkSession{Loaded: true, ActiveTaskID: "task-stopping", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "e-stopping", TaskID: "task-stopping", Status: issueExecutionRunning}},
	}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "task-stopping", State: workStopping, Prompt: "stopping prompt"}}
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stopping issue…") || tr.HasText("Stop issue task") {
		t.Fatalf("stopping detail = %v", tr.Texts())
	}
}

func TestIssueNarrowPageHasNoBackdropAndBackKeepsNavigation(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 720
	a.workspaceOpen = true
	ct.Draft = "draft survives backdrop"
	issue := &productIssue{ID: "i-backdrop", ContactID: ct.ID, Goal: "backdrop issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	nav := ct.issueNavigation()
	nav.Filter = issueFilterAll
	nav.DetailID = issue.ID
	ct.SelectedIssueID = issue.ID
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if !a.workOverlay() || !tr.HasText("Back to tasks") {
		t.Fatal("overlay detail did not open")
	}
	tr.ClickAt(50, 300) // outside content must not act like a modal backdrop
	tr.Frame()
	if !a.workspaceOpen {
		t.Fatal("a click outside the non-modal page closed it")
	}
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.workspaceOpen || nav.DetailID != "" || nav.Filter != issueFilterAll ||
		ct.SelectedIssueID != issue.ID || ct.Draft != "draft survives backdrop" {
		t.Fatalf("narrow Back changed navigation/draft: open=%v detail=%q filter=%q selected=%q draft=%q",
			a.workspaceOpen, nav.DetailID, nav.Filter, ct.SelectedIssueID, ct.Draft)
	}
	if err := tr.Click("Back to chat"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen {
		t.Fatal("Back to chat did not close the detail page")
	}
	if nav.Filter != issueFilterAll || ct.SelectedIssueID != issue.ID || ct.Draft != "draft survives backdrop" {
		t.Fatalf("close changed navigation/draft: filter=%q selected=%q draft=%q", nav.Filter, ct.SelectedIssueID, ct.Draft)
	}
}

func TestIssueWorkStatusSingleAttentionTargetAcrossEntryAndToggle(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	ct.Draft = "draft survives status entry"
	waiting := &productIssue{ID: "issue-status-waiting", ContactID: ct.ID, Goal: "Waiting issue", Status: issueStatusInProgress,
		Questions: []*issueQuestion{{Ref: "q-status", Text: "answer", State: issueQuestionOpen}}}
	attention := &productIssue{ID: "issue-status-attention", ContactID: ct.ID, Goal: "Attention issue", Status: issueStatusInProgress,
		Executions: []*issueExecution{{ID: "run-status-attention", Status: issueExecutionFailed, Detail: "attention reason"}}}
	active := &productIssue{ID: "issue-status-active", ContactID: ct.ID, Goal: "Active issue", Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-status-active", SendState: sendAccepted}}
	ct.Issues = []*productIssue{waiting, attention, active}
	notice := a.issueWorkNotice(ct)
	if notice.IssueID != attention.ID || notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 1 {
		t.Fatalf("single status target = %+v", notice)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("3") || !tr.HasText("!") {
		t.Fatalf("header count/attention marker missing: %v", tr.Texts())
	}
	wantSummary := locales.In("en").TaskRailAttention(attention.Goal)
	if !tr.HasText(wantSummary) {
		t.Fatalf("rail summary missing target: %v", tr.Texts())
	}
	if err := tr.Click(wantSummary); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	nav := ct.issueNavigation()
	if !a.workspaceOpen || nav.DetailID != attention.ID || ct.SelectedIssueID != attention.ID ||
		ct.Draft != "draft survives status entry" {
		t.Fatalf("summary opened wrong target: open=%v detail=%q selected=%q draft=%q", a.workspaceOpen, nav.DetailID, ct.SelectedIssueID, ct.Draft)
	}
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen {
		t.Fatal("Work toggle did not close an already-open work panel")
	}
	// A later status change with the panel closed must not auto-open it.
	attention.Executions[0].AttentionSeenStatus = issueExecutionFailed
	attention.Status = issueStatusInProgress
	tr.Frame()
	if a.workspaceOpen || ct.Draft != "draft survives status entry" {
		t.Fatalf("closed Work auto-opened after status change: open=%v draft=%q", a.workspaceOpen, ct.Draft)
	}
	attention.Executions[0].AttentionSeenStatus = ""
	tr.Frame()
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.workspaceOpen || nav.DetailID != attention.ID || ct.Draft != "draft survives status entry" {
		t.Fatalf("Work toggle off->on did not open attention target: open=%v detail=%q draft=%q", a.workspaceOpen, nav.DetailID, ct.Draft)
	}
}

func TestClosedChatWaitingOrInterruptedShowsTaskFactAndView(t *testing.T) {
	caseRun := func(t *testing.T, interrupted bool) {
		a, ct := workspaceTestApp(t)
		a.windowWidth = 1040
		a.workspaceOpen = false
		ct.Messages = nil
		issue := &productIssue{ID: "issue-closed-work", ContactID: ct.ID, Goal: "Closed chat task", Status: issueStatusInProgress}
		wantFact := "Closed chat task is waiting for you"
		if interrupted {
			issue.Status = issueStatusNeedsAttention
			issue.Executions = []*issueExecution{{ID: "run-interrupted", Status: issueExecutionInterrupted, Detail: "interrupted reason"}}
			wantFact = "Closed chat task needs attention"
		} else {
			issue.Questions = []*issueQuestion{{Ref: "q-closed", Text: "answer", State: issueQuestionOpen}}
		}
		ct.Issues = []*productIssue{issue}
		tr := ui.NewTester(a.view, 1040, 720)
		tr.Frame()
		if a.workspaceOpen || len(ct.Messages) != 0 || !tr.HasText("View task") || !tr.HasText(wantFact) ||
			tr.HasText("Start a conversation") {
			t.Fatalf("closed chat %s fact/view/state = open=%v messages=%d texts=%v",
				map[bool]string{true: "interrupted", false: "waiting"}[interrupted], a.workspaceOpen, len(ct.Messages), tr.Texts())
		}
		if err := tr.Click("View task"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !a.workspaceOpen || ct.issueNavigation().DetailID != issue.ID || ct.SelectedIssueID != issue.ID || len(ct.Messages) != 0 {
			t.Fatalf("View task did not open issue: open=%v detail=%q selected=%q messages=%d", a.workspaceOpen, ct.issueNavigation().DetailID, ct.SelectedIssueID, len(ct.Messages))
		}
	}
	t.Run("waiting", func(t *testing.T) { caseRun(t, false) })
	t.Run("interrupted", func(t *testing.T) { caseRun(t, true) })
}

func TestIssuePrimaryActionsAndPersistentLifecycle(t *testing.T) {
	cases := []struct {
		name        string
		issue       *productIssue
		wantPrimary string
		wantLife    string
	}{
		{
			name: "question",
			issue: &productIssue{ID: "issue-action-question", Goal: "Question action", Status: issueStatusWaitingUser,
				Questions: []*issueQuestion{{Ref: "q-action", Text: "answer", State: issueQuestionOpen}}},
			wantPrimary: "Answer question", wantLife: "Cancel task",
		},
		{
			name: "continue",
			issue: &productIssue{ID: "issue-action-continue", Goal: "Continue action", Status: issueStatusInProgress,
				Executions: []*issueExecution{{ID: "run-continue", Status: issueExecutionCompleted}}},
			wantPrimary: "Continue task", wantLife: "Mark complete",
		},
		{
			name: "question-attention",
			issue: &productIssue{ID: "issue-action-question-attention", Goal: "Question attention", Status: issueStatusWaitingUser,
				Questions:  []*issueQuestion{{Ref: "q-attention", Text: "answer despite attention", State: issueQuestionOpen}},
				Executions: []*issueExecution{{ID: "run-attention", Status: issueExecutionFailed, Detail: "unseen failure"}}},
			wantPrimary: "Answer question", wantLife: "Cancel task",
		},
		{
			name: "result",
			issue: &productIssue{ID: "issue-action-result", Goal: "Result action", Status: issueStatusCompleted,
				Evidence: "done", Executions: []*issueExecution{{ID: "run-result", TaskID: "task-result", Status: issueExecutionCompleted}},
				Work: issueWorkSession{Messages: []*message{{Kind: msgContact, TaskID: "task-result", Text: "readable result"}}}},
			wantPrimary: "Read latest result", wantLife: "Reopen task",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.windowWidth = 1040
			a.rt = &runtimeClient{}
			a.runtimeCapabilitiesLoaded = true
			a.runtimeMaxActiveTasks = 2
			tc.issue.ContactID = ct.ID
			ct.Issues = []*productIssue{tc.issue}
			ct.SelectedIssueID = tc.issue.ID
			openIssueDetailForTest(a, ct, tc.issue.ID)
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText(tc.wantPrimary) || !tr.HasText(tc.wantLife) {
				t.Fatalf("primary/lifecycle = %v, want %q / %q", tr.Texts(), tc.wantPrimary, tc.wantLife)
			}
			if tc.name == "question" {
				ct.Draft = "question draft"
				beforeID, beforeDetail, beforeSession := ct.SelectedIssueID, ct.issueNavigation().DetailID, ct.SessionID
				beforeExecutions := len(tc.issue.Executions)
				if err := tr.Click("Answer question"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if a.issueSupplementFormID != tc.issue.ID || ct.SelectedIssueID != beforeID ||
					ct.issueNavigation().DetailID != beforeDetail || ct.Draft != "question draft" {
					t.Fatalf("Answer question changed owner/draft: form=%q selected=%q detail=%q draft=%q",
						a.issueSupplementFormID, ct.SelectedIssueID, ct.issueNavigation().DetailID, ct.Draft)
				}
				if err := tr.Click("Cancel task"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if tc.issue.Status != issueStatusCanceled || len(tc.issue.Executions) != beforeExecutions || ct.SessionID != beforeSession {
					t.Fatalf("Cancel task boundary: status=%q executions=%d session=%q (before %d/%q)",
						tc.issue.Status, len(tc.issue.Executions), ct.SessionID, beforeExecutions, beforeSession)
				}
			}
		})
	}

	a, ct := workspaceTestApp(t)
	a.windowWidth = 720
	qAttention := &productIssue{ID: "issue-action-question-attention-visible", ContactID: ct.ID, Goal: "Question attention visible",
		Status:     issueStatusWaitingUser,
		Questions:  []*issueQuestion{{Ref: "q-attention-visible", Text: "answer", State: issueQuestionOpen}},
		Executions: []*issueExecution{{ID: "run-attention-visible", Status: issueExecutionFailed, Detail: "unseen failure"}}}
	ct.Issues = []*productIssue{qAttention}
	ct.SelectedIssueID = qAttention.ID
	openIssueDetailForTest(a, ct, qAttention.ID)
	trAttention := ui.NewTester(a.view, 720, 640)
	trAttention.Frame()
	if !trAttention.HasText("Needs attention") || trAttention.HasText("Waiting for your answer") || !trAttention.HasText("Answer question") {
		t.Fatalf("question+attention projection = %v", trAttention.Texts())
	}
	if count := strings.Count(strings.Join(trAttention.Texts(), "\n"), "Needs attention"); count != 1 {
		t.Fatalf("question+attention rendered %d main statuses: %v", count, trAttention.Texts())
	}

	a.windowWidth = 1040
	active := &productIssue{ID: "issue-action-active", ContactID: ct.ID, Goal: "Active action", Status: issueStatusInProgress,
		Work:       issueWorkSession{Loaded: true, ActiveTaskID: "run-action-active", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "run-action-active", Status: issueExecutionRunning}}}
	ct.Issues = []*productIssue{active}
	ct.SelectedIssueID = active.ID
	openIssueDetailForTest(a, ct, active.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Next action unavailable:") || tr.HasText("Continue task") {
		t.Fatalf("active action should preserve reason without Continue: %v", tr.Texts())
	}
}

func TestPrimaryReadReturnFocusConsumesExactResultOwner(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.rt = nil
	issue := &productIssue{ID: "issue-primary-read", ContactID: ct.ID, Goal: "Primary readable result",
		Status: issueStatusCompleted, Evidence: "user completed",
		Executions: []*issueExecution{{ID: "run-primary-read", TaskID: "task-primary-read", Status: issueExecutionCompleted}},
		Work: issueWorkSession{Loaded: true, Messages: []*message{{
			Kind: msgContact, TaskID: "task-primary-read", Text: "readable primary result"}}}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()

	openRead := func() {
		t.Helper()
		if err := tr.Click("Read latest result"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !a.issueResult.Open || a.issueResult.OwnerContactID != ct.ID ||
			a.issueResult.IssueID != issue.ID || a.issueResult.ExecutionID != "run-primary-read" || !a.issueResult.ReturnLatest {
			t.Fatalf("primary Read owner/result = %+v", a.issueResult)
		}
	}
	assertReturned := func(label string) {
		t.Helper()
		tr.Frame()
		if !tr.Focused("Read latest result") {
			t.Fatalf("%s did not regain focus: pending=%v texts=%v", label, a.issueResult.ReturnFocusPending, tr.Texts())
		}
		if a.issueResult.ReturnFocusPending {
			t.Fatalf("%s left ReturnFocusPending set", label)
		}
	}

	openRead()
	if err := tr.Click("Close results"); err != nil {
		t.Fatal(err)
	}
	assertReturned("Back")

	openRead()
	tr.Key(0, ui.KeyEscape)
	assertReturned("Escape")

	// Move focus off Read before checking mismatches. These are visible renders
	// of the primary Read branch; a mismatch must leave the exact pending owner/
	// issue/execution/latest intent untouched and must not consume it.
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	cases := []struct {
		name   string
		mutate func()
	}{
		{name: "owner", mutate: func() { a.issueResult.OwnerContactID = "other-contact" }},
		{name: "issue", mutate: func() { a.issueResult.IssueID = "other-issue" }},
		{name: "execution", mutate: func() { a.issueResult.ExecutionID = "other-execution" }},
	}
	for _, tc := range cases {
		t.Run("mismatch-"+tc.name, func(t *testing.T) {
			a.issueResult.ReturnFocusPending = true
			a.issueResult.OwnerContactID = ct.ID
			a.issueResult.IssueID = issue.ID
			a.issueResult.ExecutionID = "run-primary-read"
			a.issueResult.ReturnLatest = true
			tc.mutate()
			tr.Frame()
			if !tr.HasText("Read latest result") {
				t.Fatalf("primary Read not rendered for mismatch %s: %v", tc.name, tr.Texts())
			}
			if !a.issueResult.ReturnFocusPending {
				t.Fatalf("mismatch %s consumed ReturnFocusPending", tc.name)
			}
		})
	}
}

func TestNarrowChatRunsBackUsesVisibleLayerAndKeepsHiddenDetail(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 720
	a.workspaceOpen = true
	issue := &productIssue{ID: "issue-visible-layer", ContactID: ct.ID, Goal: "Segment issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	nav := ct.issueNavigation()
	nav.DetailID = issue.ID
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if err := tr.Click("Chat runs"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.escapeWorkspaceLayer()
	if nav.ShowConversationWork || nav.DetailID != issue.ID {
		t.Fatalf("Escape changed hidden detail: nav=%+v", nav)
	}
	tr.Frame()
	if nav.FocusConversation {
		t.Fatal("FocusConversation was not consumed by the visible segmented entry")
	}
	if !tr.Focused("Tasks") {
		t.Fatalf("Chat runs Back did not focus the Tasks segment: %v", tr.Texts())
	}
	nav.FocusBack = true
	tr.Frame()
	if nav.FocusBack {
		t.Fatal("FocusBack was not consumed by the single narrow Back")
	}
	if !tr.Focused("Back to tasks") {
		t.Fatalf("detail Back flag did not focus Back to tasks: %v", tr.Texts())
	}
	if err := tr.Click("Chat runs"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if nav.ShowConversationWork || nav.DetailID != issue.ID || !tr.HasText("Segment issue") {
		t.Fatalf("Chat runs Back did not return to the retained detail: nav=%+v", nav)
	}
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if nav.DetailID != "" {
		t.Fatalf("detail Back did not return to the issue list: %+v", nav)
	}
	if err := tr.Click("Back to chat"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen {
		t.Fatal("Back to chat did not close the narrow work panel")
	}
}

func TestIssueHasReadableResultRequiresActualOutput(t *testing.T) {
	ct := &contact{ID: "c-readable", Works: []*workItem{
		{TaskID: "task-message"}, {TaskID: "task-output"}, {TaskID: "task-unfinished"}, {TaskID: "task-failed"},
	}}
	issue := &productIssue{ID: "issue-readable", Work: issueWorkSession{Messages: []*message{{Kind: msgContact, TaskID: "task-message", Text: "readable"}}}}
	ct.Issues = []*productIssue{issue}
	completed := &issueExecution{TaskID: "task-message", Status: issueExecutionCompleted}
	if !issueHasReadableResult(ct, completed) {
		t.Fatal("completed assistant message should be readable")
	}
	completed = &issueExecution{TaskID: "task-output", Status: issueExecutionCompleted}
	ct.Works[1].Tools = []*workTool{{Finished: true, Output: "file content"}}
	if !issueHasReadableResult(ct, completed) {
		t.Fatal("finished tool output should be readable")
	}
	ct.Works[2].Tools = []*workTool{{Finished: false, Output: "not final"}}
	if issueHasReadableResult(ct, &issueExecution{TaskID: "task-unfinished", Status: issueExecutionCompleted}) {
		t.Fatal("unfinished tool output must not become a primary result")
	}
	if issueHasReadableResult(ct, &issueExecution{TaskID: "task-failed", Status: issueExecutionFailed, Detail: "failed detail"}) {
		t.Fatal("failure detail alone must not become a primary result")
	}
}

func TestTasksChatRunsSegmentKeepsDetailAndState(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	issue := &productIssue{ID: "issue-segment", ContactID: ct.ID, Goal: "Segment issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Draft = "segment draft"
	ct.issueNavigation().DetailID = issue.ID
	ct.issueNavigation().Filter = issueFilterAll
	beforeTask, beforeSession := ct.LastTaskID, ct.SessionID
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Chat runs"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !ct.issueNavigation().ShowConversationWork || ct.issueNavigation().DetailID != issue.ID ||
		ct.SelectedIssueID != issue.ID || ct.Draft != "segment draft" || ct.LastTaskID != beforeTask || ct.SessionID != beforeSession {
		t.Fatalf("Chat runs changed detail/state: nav=%+v selected=%q draft=%q", ct.issueNavigation(), ct.SelectedIssueID, ct.Draft)
	}
	if err := tr.Click("Tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().ShowConversationWork || ct.issueNavigation().DetailID != issue.ID || ct.Draft != "segment draft" {
		t.Fatalf("Tasks did not restore detail: nav=%+v draft=%q", ct.issueNavigation(), ct.Draft)
	}
}

func TestIssueBackFocusesDisplayedDetailNotBackgroundSelection(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = true
	first := &productIssue{ID: "i-a", ContactID: ct.ID, Goal: "A displayed issue", Status: issueStatusInProgress}
	background := &productIssue{ID: "i-b", ContactID: ct.ID, Goal: "B background issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{first, background}
	nav := ct.issueNavigation()
	nav.Filter = issueFilterAll
	nav.OverviewScroll.Y = 0
	nav.DetailID = first.ID
	ct.SelectedIssueID = background.ID // captured/background work may update this
	ct.Draft = "draft stays"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("A displayed issue") || tr.HasText("B background issue") {
		t.Fatalf("displayed detail = %v", tr.Texts())
	}
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if nav.DetailID != "" || nav.Filter != issueFilterAll || nav.OverviewScroll.Y != 0 {
		t.Fatalf("back filter/scroll = detail=%q filter=%q y=%v", nav.DetailID, nav.Filter, nav.OverviewScroll.Y)
	}
	if ct.SelectedIssueID != background.ID {
		t.Fatalf("persistent selection changed on Back: %q", ct.SelectedIssueID)
	}
	if !tr.Focused("Issue A displayed issue") {
		t.Fatalf("Back did not focus displayed A row: %v", tr.Texts())
	}
	if ct.Draft != "draft stays" {
		t.Fatalf("Back changed draft: %q", ct.Draft)
	}
}

func TestIssueOverviewRecentUsesLatestExecutionNotOlderToolWork(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		taskID     string
		wantStatus string
		wantRun    string
	}{
		{"unknown-empty-task", issueExecutionUnknown, "", "Needs attention", "Run result unknown"},
		{"pending-empty-task", issueExecutionPending, "", "In progress", "Run prepared, not dispatched"},
		{"unknown-trusted-task", issueExecutionUnknown, "task-latest", "Needs attention", "Run result unknown"},
		{"failed-trusted-task", issueExecutionFailed, "task-latest", "Needs attention", "Run failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.workspaceOpen = true
			issue := &productIssue{ID: "i-latest", ContactID: ct.ID, Goal: "latest execution", Status: issueStatusInProgress,
				Executions: []*issueExecution{
					{ID: "old", TaskID: "task-old", Status: issueExecutionCompleted},
					{ID: "latest", TaskID: tc.taskID, Status: tc.status},
				},
			}
			ct.Issues = []*productIssue{issue}
			ct.Works = []*workItem{
				{TaskID: "task-old", State: workCompleted,
					Tools: []*workTool{{CallID: "old-tool", Name: "read", Finished: true, Output: `{"content":"old"}`}}},
				{TaskID: "task-latest", State: workFailed,
					Tools: []*workTool{{CallID: "latest-tool", Name: "write", Finished: true, Output: `{"content":"latest"}`}}},
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText(tc.wantStatus) || !tr.HasText(tc.wantRun) {
				t.Fatalf("latest %s row = %v, want status %q and run %q", tc.status, tr.Texts(), tc.wantStatus, tc.wantRun)
			}
			if tr.HasText("Last action: read") || tr.HasText("Last action: write") || tr.HasText("Execution completed") {
				t.Fatalf("terminal/uncertain latest was masked by work/tool: %v", tr.Texts())
			}
		})
	}
}
