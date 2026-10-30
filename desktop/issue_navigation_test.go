package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
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
	if err := tr.Click("Conversation work"); err != nil {
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
	if nav.DetailID != "i-15" || ct.SelectedIssueID != "i-15" || !tr.HasText("Back to issues") {
		t.Fatalf("detail open = nav=%q selected=%q texts=%v", nav.DetailID, ct.SelectedIssueID, tr.Texts())
	}
	if err := tr.Click("Back to issues"); err != nil {
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
			if (a.workOverlay() != tc.overlay) || !tr.HasText("Back to issues") {
				t.Fatalf("setup = overlay=%v detail=%v", a.workOverlay(), tr.HasText("Back to issues"))
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
		!tr.HasText("Marked by user · reviewed by the user") || !tr.HasText("Unverified") {
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
	back, backOK := tr.Find("Back to issues")
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
	if !a.workOverlay() || !tr.HasText("Back to issues") {
		t.Fatal("overlay detail did not open")
	}
	tr.ClickAt(50, 300) // outside content must not act like a modal backdrop
	tr.Frame()
	if !a.workspaceOpen {
		t.Fatal("a click outside the non-modal page closed it")
	}
	if err := tr.Click("Close work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen {
		t.Fatal("Back did not close the detail page")
	}
	if nav.DetailID != issue.ID || nav.Filter != issueFilterAll || ct.SelectedIssueID != issue.ID || ct.Draft != "draft survives backdrop" {
		t.Fatalf("Back changed navigation/draft: detail=%q filter=%q selected=%q draft=%q",
			nav.DetailID, nav.Filter, ct.SelectedIssueID, ct.Draft)
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
	if err := tr.Click("Back to issues"); err != nil {
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
		name   string
		status string
		taskID string
		want   string
	}{
		{"unknown-empty-task", issueExecutionUnknown, "", "Result unknown"},
		{"pending-empty-task", issueExecutionPending, "", "Prepared, not dispatched"},
		{"unknown-trusted-task", issueExecutionUnknown, "task-latest", "Result unknown"},
		{"failed-trusted-task", issueExecutionFailed, "task-latest", "Execution failed"},
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
			if !tr.HasText(tc.want) {
				t.Fatalf("latest %s row = %v, want %q", tc.status, tr.Texts(), tc.want)
			}
			if tr.HasText("Last action: read") || tr.HasText("Last action: write") || tr.HasText("Execution completed") {
				t.Fatalf("terminal/uncertain latest was masked by work/tool: %v", tr.Texts())
			}
		})
	}
}
