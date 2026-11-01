package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueDetailDefaultsToResultAndKeepsSecondaryDetailsCollapsed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{
		ID: "i1", ContactID: ct.ID, Goal: "prepare the readable summary", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("keep the source facts"),
		WorkSessionID: "work-1", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e1", SessionID: "work-1", TaskID: "task-1", MessageID: "message-1",
			Prompt: "the exact sent snapshot", Status: issueExecutionCompleted, RequirementsRevision: 1,
		}},
	}
	issue.Work.Messages = []*message{{Key: "result", Kind: msgContact, TaskID: "task-1", Text: "readable result body"}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	storeBefore, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Latest execution", "readable result body", "Requirements", "Continue issue", "More issue actions"} {
		if !tr.HasText(want) {
			t.Fatalf("default issue detail missing %q: %v", want, tr.Texts())
		}
	}
	if tr.HasText("Execution history") || tr.HasText("Sent snapshot:") || tr.HasText("Sent task context") {
		t.Fatalf("secondary execution details visible by default: %v", tr.Texts())
	}
	goal, _ := tr.Find("prepare the readable summary")
	latest, _ := tr.Find("Latest execution")
	requirements, _ := tr.Find("Requirements")
	primary, _ := tr.Find("Continue issue")
	more, _ := tr.Find("More issue actions")
	if !(goal.Y < latest.Y && latest.Y < requirements.Y && requirements.Y < primary.Y && primary.Y < more.Y) {
		t.Fatalf("default issue detail order = goal %.1f latest %.1f requirements %.1f primary %.1f more %.1f", goal.Y, latest.Y, requirements.Y, primary.Y, more.Y)
	}
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Execution history") || !tr.HasText("Show sent snapshot") {
		t.Fatalf("expanded issue detail did not expose execution details: %v", tr.Texts())
	}
	clickAfterBoundedScroll(t, tr, "Show sent snapshot", 1040, 720)
	tr.Frame()
	tr.Scroll(850, 500, 0, 500)
	tr.Frame()
	if !tr.HasText("Sent snapshot: the exact sent snapshot") {
		t.Fatalf("sent snapshot was not readable after explicit expansion: %v", tr.Texts())
	}
	storeAfter, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(storeAfter) != string(storeBefore) {
		t.Fatal("browsing/collapsing issue detail changed the contact store")
	}
	if issue.Status != issueStatusInProgress || len(issue.Executions) != 1 || issue.Executions[0].Status != issueExecutionCompleted {
		t.Fatalf("viewing issue detail changed lifecycle: %+v", issue)
	}
}

func TestIssueDetailsOpenStateDoesNotCrossContacts(t *testing.T) {
	a, first := workspaceTestApp(t)
	firstIssue := &productIssue{ID: "i-a", ContactID: first.ID, Goal: "first", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("first requirement")}
	first.Issues = []*productIssue{firstIssue}
	first.SelectedIssueID = firstIssue.ID
	first.issueNavigation().DetailID = firstIssue.ID
	second := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Loaded: true}
	secondIssue := &productIssue{ID: "i-b", ContactID: second.ID, Goal: "second", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("second requirement")}
	second.Issues = []*productIssue{secondIssue}
	second.SelectedIssueID = secondIssue.ID
	second.issueNavigation().DetailID = secondIssue.ID
	a.contacts = append(a.contacts, second)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if !a.issueDetailOpen[firstIssue.ID] {
		t.Fatal("first issue details did not expand")
	}
	firstDraft := a.requirementDraft(firstIssue.Requirements[0])
	*firstDraft = "first contact evidence draft"
	a.selectContact(1)
	tr.Frame()
	if tr.HasText("Execution history") || a.issueDetailOpen[secondIssue.ID] || tr.HasText("first contact evidence draft") {
		t.Fatalf("second contact inherited first edit/detail state: %v", tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()
	if !a.issueDetailOpen[firstIssue.ID] || !tr.HasText("Hide issue actions") ||
		firstDraft != a.requirementDraft(firstIssue.Requirements[0]) || *firstDraft != "first contact evidence draft" {
		t.Fatal("first issue details/draft were lost after contact switch")
	}
}

func TestIssueViewExecutionOpensAtTopAndBackRestoresCompact(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{ID: "i-view", ContactID: ct.ID, Goal: "view execution", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("result requirement"), WorkSessionID: "work-view",
		Work:       issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-view", SessionID: "work-view", TaskID: "task-view", Prompt: "sent view prompt", Status: issueExecutionCompleted, RequirementsRevision: 1}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "task-view", MessageID: "message-view", State: workCompleted, Prompt: "view work", Tools: []*workTool{{CallID: "call-view", Name: "write", Output: `{"content":"tool raw output"}`}}}}
	issue.Work.Messages = []*message{{Key: "result-view", Kind: msgContact, TaskID: "task-view", Text: "view result"}}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if _, back := tr.Find("Back to issue"); back || tr.HasText("Execution history") {
		t.Fatalf("execution detail visible before explicit View execution: %v", tr.Texts())
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.SelectedWorkTaskID != "task-view" || !tr.HasText("Back to issue") || !tr.HasText("view result") || !tr.HasText("tool raw output") {
		t.Fatalf("View execution did not open the selected detail: %+v texts=%v", ct.SelectedWorkTaskID, tr.Texts())
	}
	back, ok := tr.Find("Back to issue")
	if !ok || back.Y > 720 {
		t.Fatalf("Back to issue is not reachable: %+v ok=%v", back, ok)
	}
	if err := tr.Click("Back to issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.SelectedWorkTaskID != "" || func() bool { _, ok := tr.Find("Back to issue"); return ok }() || tr.HasText("Execution history") {
		t.Fatalf("Back to issue did not restore compact detail: selected=%q texts=%v", ct.SelectedWorkTaskID, tr.Texts())
	}
	after, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("View execution/back browsing changed the contact store")
	}
	if len(issue.Executions) != 1 || issue.Executions[0].Status != issueExecutionCompleted {
		t.Fatalf("View execution changed execution state: %+v", issue.Executions)
	}
}

func TestIssueAttentionClearsAfterViewingFailedExecutionDetail(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	const detail = "provider failed after dispatch"
	issue := &productIssue{
		ID: "i-attention", ContactID: ct.ID, Goal: "failed work", Status: issueStatusNeedsAttention,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("keep the failure facts"),
		WorkSessionID: "work-attention", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e-attention", SessionID: "work-attention", TaskID: "task-attention",
			Status: issueExecutionFailed, Detail: detail,
		}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "task-attention", State: workFailed, Detail: detail}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("unseen failed execution did not raise attention: %+v", notice)
	}
	if !tr.HasText("Execution failed") || !tr.HasText(detail) {
		t.Fatalf("failed status/reason was not visible before viewing: %v", tr.Texts())
	}
	if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
		tr.SetDark(false)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-before-light.png"), tr.Image())
		tr.SetDark(true)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-before-dark.png"), tr.Image())
		tr.SetDark(false)
		tr.Frame()
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.SelectedWorkTaskID != "task-attention" || !tr.HasText("Back to issue") || !tr.HasText("Failed") ||
		!tr.HasText(detail) || !tr.HasText("Activity") {
		t.Fatalf("View execution did not display the failed detail: selected=%q texts=%v", ct.SelectedWorkTaskID, tr.Texts())
	}
	if notice := a.issueWorkNotice(ct); notice.Attention || notice.Text != "" {
		t.Fatalf("viewing the failed execution did not clear its attention: %+v", notice)
	}
	if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
		tr.SetDark(false)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-after-light.png"), tr.Image())
		tr.SetDark(true)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-after-dark.png"), tr.Image())
		tr.SetDark(false)
		tr.Frame()
	}
}

func TestIssueAttentionViewRequiresMatchingReasonAndResultPageConfirms(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = nil
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	const reasonA = "A failed after dispatch"
	issueA := &productIssue{
		ID: "i-attention-a", ContactID: ct.ID, Goal: "A failed work", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-a", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e-attention-a", SessionID: "work-a", TaskID: "task-a", Status: issueExecutionFailed, Detail: reasonA,
		}},
	}
	issueB := &productIssue{
		ID: "i-attention-b", ContactID: ct.ID, Goal: "B failed work", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-b", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e-attention-b", SessionID: "work-b", TaskID: "task-b", Status: issueExecutionFailed, Detail: "B failed after dispatch",
		}},
	}
	workA := &workItem{TaskID: "task-a", State: workFailed, Detail: "stale older reason"}
	workB := &workItem{TaskID: "task-b", State: workFailed, Detail: "B failed after dispatch"}
	ct.Issues = []*productIssue{issueA, issueB}
	ct.Works = []*workItem{workA, workB}
	ct.SelectedIssueID = issueA.ID
	ct.Unread = 2
	openIssueDetailForTest(a, ct, issueA.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issueA.ID {
		t.Fatalf("unseen A did not raise attention: %+v", notice)
	}

	// The work card shows a stale reason, not this execution's reason, so the
	// actual View execution click must not confirm A.
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Back to issue") || !tr.HasText("stale older reason") || !tr.HasText("Activity") {
		t.Fatalf("View execution did not open the stale work card: %v", tr.Texts())
	}
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issueA.ID ||
		issueA.Executions[0].AttentionSeenStatus != "" {
		t.Fatalf("stale work reason confirmed A: notice=%+v seen=%q", notice, issueA.Executions[0].AttentionSeenStatus)
	}

	// The existing result entry now presents the execution's real Detail and
	// confirms only A; B remains unseen.
	if err := tr.Click("Read latest result"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.issueResult.Open || a.issueResult.Panel.RunDetail != reasonA || !tr.HasText(reasonA) {
		t.Fatalf("result page did not present A's real reason: open=%v detail=%q texts=%v",
			a.issueResult.Open, a.issueResult.Panel.RunDetail, tr.Texts())
	}
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issueB.ID ||
		issueA.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("result page confirmation was not scoped to A: notice=%+v seen=%q", notice, issueA.Executions[0].AttentionSeenStatus)
	}
	if err := tr.Click("Close results"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.selectIssue(issueB.ID)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issueB.ID {
		t.Fatalf("selecting B changed its unseen attention: %+v", notice)
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if notice := a.issueWorkNotice(ct); notice.Attention || notice.Text != "" {
		t.Fatalf("B confirmation did not clear B: %+v", notice)
	}
	if issueA.Status != issueStatusNeedsAttention || issueB.Status != issueStatusNeedsAttention ||
		issueA.Executions[0].Status != issueExecutionFailed || issueB.Executions[0].Status != issueExecutionFailed ||
		issueA.Executions[0].Detail != reasonA || ct.Unread != 2 {
		t.Fatalf("viewing changed retained facts: A=%+v B=%+v unread=%d", issueA.Executions[0], issueB.Executions[0], ct.Unread)
	}

	other := &contact{ID: "i-attention-other-contact", Name: "Other", Loaded: true}
	otherIssue := &productIssue{ID: "i-attention-other", ContactID: other.ID, Goal: "other contact failure",
		Status: issueStatusNeedsAttention, WorkSessionID: "work-other", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-attention-other", SessionID: "work-other", TaskID: "task-other",
			Status: issueExecutionFailed, Detail: "other contact unseen"}}}
	other.Issues = []*productIssue{otherIssue}
	other.SelectedIssueID = otherIssue.ID
	other.Works = []*workItem{{TaskID: "task-other", State: workFailed, Detail: "other contact unseen"}}
	a.contacts = append(a.contacts, other)
	a.selectContact(1)
	tr.Frame()
	if notice := a.issueWorkNotice(other); !notice.Attention || notice.IssueID != otherIssue.ID ||
		otherIssue.Executions[0].AttentionSeenStatus != "" {
		t.Fatalf("another contact's unseen exception was cleared: %+v seen=%q",
			notice, otherIssue.Executions[0].AttentionSeenStatus)
	}
}

func TestIssueAttentionResultPageConfirmsNoTaskNoDetailExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = nil
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{
		ID: "i-no-task", ContactID: ct.ID, Goal: "no task unknown", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-no-task", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e-no-task", Status: issueExecutionUnknown,
		}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Unread = 2
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("unseen no-task execution did not raise attention: %+v", notice)
	}
	if err := tr.Click("Read latest result"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	const absent = "No execution detail was recorded for this run."
	if !a.issueResult.Open || a.issueResult.Panel.RunDetail != absent || !tr.HasText(absent) {
		t.Fatalf("no-task result page did not present the explicit absence of detail: open=%v detail=%q texts=%v",
			a.issueResult.Open, a.issueResult.Panel.RunDetail, tr.Texts())
	}
	if issue.Executions[0].AttentionSeenStatus != issueExecutionUnknown {
		t.Fatalf("no-task result entry did not confirm unknown: %+v", issue.Executions[0])
	}
	if notice := a.issueWorkNotice(ct); notice.Attention || notice.Text != "" {
		t.Fatalf("no-task result entry did not clear attention: %+v", notice)
	}
	if issue.Status != issueStatusNeedsAttention || issue.Executions[0].Status != issueExecutionUnknown || ct.Unread != 2 {
		t.Fatalf("no-task result entry changed retained facts: %+v status=%q unread=%d", issue.Executions[0], issue.Status, ct.Unread)
	}

	// A user-requested Reload displays the new status/reason and confirms it.
	// Background reads and closing remain non-confirming.
	issue.Executions[0].Status = issueExecutionFailed
	issue.Executions[0].Detail = "new no-task failure"
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("new failed status did not raise attention before reload: %+v", notice)
	}
	generation := a.issueResult.Generation
	if err := tr.Click("Reload result"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.issueResult.Generation <= generation || a.issueResult.Panel.RunDetail != "new no-task failure" ||
		!tr.HasText("new no-task failure") {
		t.Fatalf("reload did not display the new status/reason: generation=%d detail=%q texts=%v",
			a.issueResult.Generation, a.issueResult.Panel.RunDetail, tr.Texts())
	}
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("active reload did not confirm the displayed failed status: %+v", issue.Executions[0])
	}
	if notice := a.issueWorkNotice(ct); notice.Attention || notice.Text != "" {
		t.Fatalf("active reload left the displayed failure in attention: %+v", notice)
	}
	if issue.Status != issueStatusNeedsAttention || issue.Executions[0].Status != issueExecutionFailed ||
		issue.Executions[0].Detail != "new no-task failure" || ct.Unread != 2 {
		t.Fatalf("active reload changed retained facts: %+v status=%q unread=%d",
			issue.Executions[0], issue.Status, ct.Unread)
	}

	// A later unseen interrupted state is not confirmed by hidden/stale/other
	// owner reload attempts or by merely closing the result page.
	issue.Executions[0].Status = issueExecutionInterrupted
	issue.Executions[0].Detail = "later interrupted"
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("new interrupted status did not raise attention before close: %+v", notice)
	}
	visibleGeneration := a.issueResult.Generation
	a.issueResult.Panel.ReaderKey = "stale-reader-page"
	a.reloadIssueResult(visibleGeneration)
	a.issueResult.Panel.ReaderKey = ""
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("reader page reload confirmed the unseen status: %+v", issue.Executions[0])
	}
	a.settingsOpen = true
	a.reloadIssueResult(visibleGeneration)
	a.settingsOpen = false
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("hidden Settings reload confirmed the unseen status: %+v", issue.Executions[0])
	}
	a.aux = "model"
	a.reloadIssueResult(visibleGeneration)
	a.aux = "issue-result"
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("other aux reload confirmed the unseen status: %+v", issue.Executions[0])
	}
	a.reloadIssueResult(visibleGeneration - 1)
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("stale generation reload confirmed the unseen status: %+v", issue.Executions[0])
	}
	other := &contact{ID: "reload-other-owner", Name: "Other", Loaded: true}
	a.contacts = append(a.contacts, other)
	selected := a.selected
	a.selected = len(a.contacts) - 1
	a.reloadIssueResult(visibleGeneration)
	a.selected = selected
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("other contact reload confirmed the unseen status: %+v", issue.Executions[0])
	}
	tr.Frame()
	if err := tr.Click("Close results"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("closing the result page confirmed the unseen status: %+v", issue.Executions[0])
	}
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("closing the result page cleared the unseen status: %+v", notice)
	}
}

func TestIssueAttentionPendingSeenStillShowsOlderException(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = nil
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	older := &issueExecution{ID: "e-older-visible", TaskID: "task-older", Status: issueExecutionFailed, Detail: "older unseen failure"}
	latest := &issueExecution{ID: "e-pending-visible", Status: issueExecutionUnknown}
	issue := &productIssue{
		ID: "i-pending-visible", ContactID: ct.ID, Goal: "pending then older", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-pending", Work: issueWorkSession{Loaded: true, SendState: sendUnknown,
			SendError: "delivery not confirmed", pendingExecutionID: latest.ID},
		Executions: []*issueExecution{older, latest},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("pending unknown did not raise attention: %+v", notice)
	}
	if err := tr.Click("Read latest result"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if latest.AttentionSeenStatus != issueExecutionUnknown {
		t.Fatalf("pending result page did not confirm its execution: %+v", latest)
	}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.Text, "older unseen failure") ||
		older.AttentionSeenStatus != "" {
		t.Fatalf("seen pending execution hid the older unseen exception: %+v older=%+v", notice, older)
	}
}

func TestIssueAttentionTracksStatusReplayAndNewExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	const initialDetail = "delivery was not confirmed"
	issue := &productIssue{
		ID: "i-attention-status", ContactID: ct.ID, Goal: "unknown then failed", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-status", Work: issueWorkSession{Loaded: true, LastTaskID: "task-status",
			SendState: sendUnknown, SendError: initialDetail},
		Executions: []*issueExecution{{
			ID: "e-attention-status", SessionID: "work-status", TaskID: "task-status",
			Status: issueExecutionUnknown, Detail: initialDetail,
		}},
	}
	work := &workItem{TaskID: "task-status", State: workUnknown, Detail: initialDetail}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{work}
	ct.SelectedIssueID = issue.ID
	ct.Unread = 3
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("unseen unknown did not raise attention: %+v", notice)
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	execution := issue.Executions[0]
	if execution.Status != issueExecutionUnknown || execution.Detail != initialDetail ||
		execution.AttentionSeenStatus != issueExecutionUnknown || ct.Unread != 3 {
		t.Fatalf("viewing unknown changed retained facts or seen status: %+v unread=%d", execution, ct.Unread)
	}
	if notice := a.issueWorkNotice(ct); notice.Attention || notice.Text != "" {
		t.Fatalf("seen unknown still raised attention: %+v", notice)
	}
	tr.Frame()
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		t.Fatalf("same-status replay raised attention: %+v", notice)
	}

	execution.Status = issueExecutionFailed
	execution.Detail = "later failed"
	work.State = workFailed
	work.Detail = "later failed"
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("new failed status did not re-prompt: %+v", notice)
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if execution.AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("failed status was not confirmed: %+v", execution)
	}
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		t.Fatalf("confirmed failed status raised attention: %+v", notice)
	}
	reason := "replayed failure"
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "task-status", MessageID: "message-status"}},
		{Offset: 1, Record: journalRecord{Type: "task_finished", TaskID: "task-status", MessageID: "message-status",
			Status: "failed", Error: &reason}},
	}
	a.applyIssueWorkHistory(ct, issue, records, "failed")
	work = workFor(ct, "task-status")
	tr.Frame()
	if execution.Status != issueExecutionFailed || !strings.Contains(execution.Detail, reason) ||
		execution.AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("failed history replay changed the seen execution: %+v", execution)
	}
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		t.Fatalf("failed same-status history replay raised attention: %+v", notice)
	}

	execution.Status = issueExecutionCompleted
	execution.Detail = ""
	work.State = workCompleted
	work.Detail = ""
	issue.Work.SendState = sendIdle
	issue.Work.SendError = ""
	tr.Frame()
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		t.Fatalf("later success re-lit a seen failure: %+v", notice)
	}

	next := &issueExecution{ID: "e-attention-next", SessionID: "work-status", TaskID: "task-status-next",
		Status: issueExecutionFailed, Detail: "next failure"}
	issue.Executions = append(issue.Executions, next)
	issue.Work.LastTaskID = next.TaskID
	ct.Works = append(ct.Works, &workItem{TaskID: next.TaskID, State: workFailed, Detail: "next failure"})
	tr.Frame()
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID || next.AttentionSeenStatus != "" {
		t.Fatalf("new execution did not raise attention: %+v next=%+v", notice, next)
	}
	if issue.Status != issueStatusNeedsAttention || ct.Unread != 3 {
		t.Fatalf("attention tracking changed issue status/unread: status=%q unread=%d", issue.Status, ct.Unread)
	}
}

func TestIssueAttentionExpiredResultCallbackDoesNotConfirm(t *testing.T) {
	a, ct := workspaceTestApp(t)
	seen := &issueExecution{ID: "e-already-seen", SessionID: "work-expired", TaskID: "task-seen",
		Status: issueExecutionFailed, Detail: "already seen failure", AttentionSeenStatus: issueExecutionFailed}
	expired := &issueExecution{ID: "e-expired", SessionID: "work-expired", TaskID: "task-expired",
		Status: issueExecutionFailed, Detail: "expired failure"}
	issue := &productIssue{ID: "i-expired", ContactID: ct.ID, Goal: "expired callback", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-expired", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{seen, expired}}
	ct.Issues = []*productIssue{issue}
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: "e-expired",
		SessionID: "work-expired", TaskID: "task-expired", Generation: 2,
	}
	a.applyIssueResultLoaded(nil, 0, 1, ct.ID, issue.ID, "e-expired", "work-expired", "task-expired",
		issueResultDerivation{}, "stale result", "")
	if seen.AttentionSeenStatus != issueExecutionFailed || expired.AttentionSeenStatus != "" {
		t.Fatalf("expired result callback changed seen fields: seen=%q expired=%q",
			seen.AttentionSeenStatus, expired.AttentionSeenStatus)
	}
	if notice := a.issueWorkNotice(ct); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("expired callback changed attention: %+v", notice)
	}
}

func TestIssueAttentionSeenPersistsAndSaveFailureStaysUnseen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ct := &contact{ID: store.state.Contacts[0].ID, Name: "Saki", Loaded: true}
	a := &appModel{store: store, runtimeStatus: runtimeStateReady, workspaceOpen: true, windowWidth: 1040, scrollTo: -1,
		contacts: []*contact{ct}, selected: 0}
	const detail = "persisted failure"
	issue := &productIssue{ID: "i-persist-attention", ContactID: ct.ID, Goal: "persist seen", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-persist", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-persist-attention", SessionID: "work-persist", TaskID: "task-persist",
			Status: issueExecutionFailed, Detail: detail}}}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "task-persist", State: workFailed, Detail: detail}}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	reloaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Issues) != 1 || len(reloaded.state.Issues[0].Executions) != 1 ||
		reloaded.state.Issues[0].Executions[0].AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("seen status was not persisted: %+v", reloaded.state.Issues)
	}
	b := &appModel{}
	applyStoreSelection(b, reloaded)
	if notice := b.issueWorkNotice(b.selectedContact()); notice.Attention || notice.Text != "" {
		t.Fatalf("reloaded seen execution raised attention: %+v", notice)
	}

	// A failed save must not claim persistence or clear the prompt.
	badParent := filepath.Join(t.TempDir(), "blocked-save")
	if err := os.WriteFile(badParent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	a2, ct2 := workspaceTestApp(t)
	a2.store = &contactStore{path: filepath.Join(badParent, "contacts.json")}
	const badDetail = "save failure reason"
	issue2 := &productIssue{ID: "i-save-failure", ContactID: ct2.ID, Goal: "save failure", Status: issueStatusNeedsAttention,
		WorkSessionID: "work-save-failure", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-save-failure", SessionID: "work-save-failure", TaskID: "task-save-failure",
			Status: issueExecutionFailed, Detail: badDetail}}}
	ct2.Issues = []*productIssue{issue2}
	ct2.Works = []*workItem{{TaskID: "task-save-failure", State: workFailed, Detail: badDetail}}
	ct2.SelectedIssueID = issue2.ID
	openIssueDetailForTest(a2, ct2, issue2.ID)
	tr2 := ui.NewTester(a2.view, 1040, 720)
	tr2.Frame()
	if err := tr2.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr2.Frame()
	if issue2.Executions[0].AttentionSeenStatus != "" {
		t.Fatalf("failed save left a false seen status: %+v", issue2.Executions[0])
	}
	if notice := a2.issueWorkNotice(ct2); !notice.Attention || !strings.Contains(a2.issueActionError, "Could not save that this execution was viewed") {
		t.Fatalf("failed save did not remain unseen/visible: notice=%+v error=%q", notice, a2.issueActionError)
	}

	// Read latest result must show the same existing localSaveErr on the result
	// main page, keep the seen rollback, and preserve it after a background load.
	if err := tr2.Click("Read latest result"); err != nil {
		t.Fatal(err)
	}
	tr2.Frame()
	if !a2.issueResult.Open || a2.localSaveErr == "" ||
		!strings.Contains(strings.Join(tr2.Texts(), "\n"), a2.localSaveErr) {
		t.Fatalf("result page did not display the existing save error: open=%v localSaveErr=%q texts=%v",
			a2.issueResult.Open, a2.localSaveErr, tr2.Texts())
	}
	if issue2.Executions[0].AttentionSeenStatus != "" || issue2.Executions[0].Status != issueExecutionFailed ||
		issue2.Executions[0].Detail != badDetail || issue2.Status != issueStatusNeedsAttention {
		t.Fatalf("result save failure changed facts or did not roll back: issue=%+v", issue2)
	}
	page := a2.issueResult
	a2.applyIssueResultLoaded(nil, 0, page.Generation, page.OwnerContactID, page.IssueID, page.ExecutionID,
		page.SessionID, page.TaskID, issueResultDerivation{}, "", "")
	tr2.Frame()
	if a2.localSaveErr == "" || !strings.Contains(strings.Join(tr2.Texts(), "\n"), a2.localSaveErr) {
		t.Fatalf("background load cleared/hid the save error: localSaveErr=%q texts=%v", a2.localSaveErr, tr2.Texts())
	}
	if issue2.Executions[0].AttentionSeenStatus != "" {
		t.Fatalf("background load confirmed the exception after save failure: %+v", issue2.Executions[0])
	}
	if err := tr2.Click("Reload result"); err != nil {
		t.Fatal(err)
	}
	tr2.Frame()
	if issue2.Executions[0].AttentionSeenStatus != "" || issue2.Executions[0].Status != issueExecutionFailed ||
		issue2.Executions[0].Detail != badDetail || issue2.Status != issueStatusNeedsAttention {
		t.Fatalf("reload after save failure did not preserve rollback/facts: %+v", issue2)
	}
	if a2.localSaveErr == "" || !strings.Contains(strings.Join(tr2.Texts(), "\n"), a2.localSaveErr) {
		t.Fatalf("reload after save failure cleared/hid localSaveErr: %q texts=%v", a2.localSaveErr, tr2.Texts())
	}
	if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
		tr2.SetDark(false)
		tr2.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-result-save-error-light.png"), tr2.Image())
		tr2.SetDark(true)
		tr2.Frame()
		writePreviewPNG(t, filepath.Join(dir, "headless-attention-result-save-error-dark.png"), tr2.Image())
		tr2.SetDark(false)
		tr2.Frame()
	}
}

func TestActiveIssueWorkKeepsStopReachableBeforeRequirements(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{ID: "i-active", ContactID: ct.ID, Goal: "active work", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("one\n two\nthree"), WorkSessionID: "work-active",
		Work:       issueWorkSession{Loaded: true, ActiveTaskID: "task-active", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "e-active", SessionID: "work-active", TaskID: "task-active", Status: issueExecutionRunning}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "task-active", State: workRunning, Prompt: "active prompt"}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	stop, ok := tr.Find("Stop issue task")
	requirements, reqOK := tr.Find("Requirements")
	if !ok || !reqOK || !tr.HasText("Stop issue task") || stop.Y >= requirements.Y {
		t.Fatalf("active Stop is not reachable before requirements: stop=%+v ok=%v requirements=%+v ok=%v", stop, ok, requirements, reqOK)
	}
}

func clickAfterBoundedScroll(t *testing.T, tr *ui.Tester, label string, width, height int) {
	t.Helper()
	for attempt := 0; attempt < 12; attempt++ {
		if r, ok := tr.Find(label); ok && r.W > 0 && r.H > 0 && r.X >= 0 && r.X+r.W <= float32(width) && r.Y >= 0 && r.Y+r.H <= float32(height) {
			if err := tr.Click(label); err != nil {
				t.Fatal(err)
			}
			return
		}
		tr.Scroll(850, 500, 0, 300)
		tr.Frame()
	}
	t.Fatalf("could not reach %q within the bounded scroll area", label)
}

func TestIssueSupplementAt720ScrollsToSaveAndKeepsOwnerDraft(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("720 supplement goal", "720 supplement requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Add supplement"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issue.ID) = "720 visible supplement"
	clickAfterBoundedScroll(t, tr, "Save supplement", 1040, 720)
	tr.Frame()
	if issue.ContactID != ct.ID || a.selectedContact() != ct || issue.Supplements.pendingCount() != 1 ||
		a.issueSupplementFormID != "" || *a.issueSupplementDraft(issue.ID) != "" {
		t.Fatalf("720 supplement save = owner=%q selected=%q pending=%d form=%q draft=%q",
			issue.ContactID, a.selectedContact().ID, issue.Supplements.pendingCount(), a.issueSupplementFormID, *a.issueSupplementDraft(issue.ID))
	}
}

func TestIssueExecutionContextExpandsAndCopiesAt720(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i-ctx", ContactID: ct.ID, Goal: "context at 720", Status: issueStatusInProgress,
		RequirementsRevision: 1, WorkSessionID: "work-ctx", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-ctx", SessionID: "work-ctx", TaskID: "task-ctx", Status: issueExecutionCompleted,
			Context: []taskContextItem{{ID: "ctx", Name: "fixture", Text: "exact context text", Revision: 1}}}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	clickAfterBoundedScroll(t, tr, "Sent task context: 1", 1040, 720)
	tr.Frame()
	if !tr.HasText("exact context text") {
		t.Fatalf("folded snapshot context did not expand: %v", tr.Texts())
	}
	tr.SetClipboard("")
	clickAfterBoundedScroll(t, tr, "Copy context text", 1040, 720)
	if got := tr.Clipboard(); got != "exact context text" {
		t.Fatalf("720 context copy = %q", got)
	}
}

func TestIssueLatestExecutionShowsNonSuccessStatesAndToolOnly(t *testing.T) {
	cases := []struct {
		status string
		label  string
		detail string
	}{
		{issueExecutionPending, "Prepared, not dispatched", ""},
		{issueExecutionRejected, "Rejected", "Runtime rejected the task"},
		{issueExecutionFailed, "Execution failed", "provider failed after dispatch"},
		{issueExecutionUnknown, "Result unknown", "no trusted terminal state"},
		{issueExecutionInterrupted, "Execution interrupted", ""},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			issue := &productIssue{ID: "i-" + tc.status, ContactID: ct.ID, Goal: "latest status", Status: issueStatusInProgress,
				RequirementsRevision: 1, Requirements: parseIssueRequirements("remain visible"), WorkSessionID: "work-latest",
				Executions: []*issueExecution{
					{ID: "old", SessionID: "work-latest", TaskID: "old-task", Status: issueExecutionCompleted},
					{ID: "latest", SessionID: "work-latest", TaskID: "latest-task", Status: tc.status, Detail: tc.detail},
				},
			}
			ct.Issues = []*productIssue{issue}
			ct.SelectedIssueID = issue.ID
			openIssueDetailForTest(a, ct, issue.ID)
			ct.Works = []*workItem{{TaskID: "old-task", State: workCompleted}}
			issue.Work.Messages = []*message{{Key: "old-success", Kind: msgContact, TaskID: "old-task", Text: "old success"}}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText("Latest execution") || !tr.HasText(tc.label) {
				t.Fatalf("latest status %q not visible: %v", tc.label, tr.Texts())
			}
			if tc.detail != "" && !tr.HasText(tc.detail) {
				t.Fatalf("latest reason %q not visible: %v", tc.detail, tr.Texts())
			}
			if tr.HasText("old success") {
				t.Fatal("older successful execution was presented as latest")
			}
			if tc.status == issueExecutionPending && !tr.HasText("Prepared but not dispatched yet.") {
				t.Fatalf("pending execution lacks actionable short state: %v", tr.Texts())
			}
			if tc.status == issueExecutionInterrupted && (!tr.HasText("Execution interrupted; inspect the execution details for the recorded terminal state.") ||
				tr.HasText("terminal state is unknown")) {
				t.Fatalf("interrupted execution was described as unknown: %v", tr.Texts())
			}
			if err := tr.Click("More issue actions"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText("Hide issue actions") {
				t.Fatalf("issue actions did not expand: %v", tr.Texts())
			}
			if tr.HasText("Show sent snapshot") {
				t.Fatal("empty sent snapshot rendered a dead action")
			}
		})
	}

	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i-tool-only", ContactID: ct.ID, Goal: "tool only", Status: issueStatusInProgress,
		RequirementsRevision: 1, WorkSessionID: "work-tool-only", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "tool", SessionID: "work-tool-only", TaskID: "tool-task", Status: issueExecutionCompleted}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	ct.Works = []*workItem{{TaskID: "tool-task", State: workCompleted, Tools: []*workTool{{CallID: "tool", Name: "write", Finished: true, Output: `{"bytes_written":1}`}}}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("No assistant message was recorded; open the execution to inspect tool output.") || !tr.HasText("View execution") {
		t.Fatalf("tool-only execution lacks honest guidance: %v", tr.Texts())
	}
	if tr.HasText("tool raw output") || tr.HasText(`"bytes_written"`) {
		t.Fatal("tool-only execution leaked raw output into default result")
	}
}

func TestIssueListLongGoalFitsAt320(t *testing.T) {
	a, ct := workspaceTestApp(t)
	short := &productIssue{ID: "i-short", ContactID: ct.ID, Goal: "短目标", Status: issueStatusInProgress, RequirementsRevision: 1}
	long := &productIssue{ID: "i-long", ContactID: ct.ID, Goal: "这是一个非常长的中文事项目标，需要换行显示但不能横向裁切或缩小字号", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{short, long}
	ct.SelectedIssueID = short.ID
	const width, height = 320, 360
	theme := resolveWorkbenchTheme(false).Theme
	tr := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Background(theme.Surface).Children(func() {
			ui.Column(c).Padding(12).Gap(8).Children(func() {
				a.issueList(c, theme, ct)
			})
		})
	}, width, height)
	tr.Frame()
	label := long.Goal
	var last ui.Rect
	for attempt := 0; attempt < 12; attempt++ {
		if r, ok := tr.Find(label); ok {
			last = r
			if r.W > 0 && r.H > 0 && r.X >= 0 && r.X+r.W <= width && r.Y >= 0 && r.Y+r.H <= height {
				if err := tr.Click(label); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if ct.SelectedIssueID != long.ID {
					t.Fatalf("clicking long issue-list entry selected %q, want %q", ct.SelectedIssueID, long.ID)
				}
				return
			}
		}
		tr.Scroll(160, height/2, 0, 200)
		tr.Frame()
	}
	t.Fatalf("long issue-list entry %q was not contained at %dx%d: rect=%+v selected=%q texts=%v", label, width, height, last, ct.SelectedIssueID, tr.Texts())
}

func issueHistoryFindVisible(t *testing.T, tr *ui.Tester, label string, panelX, width, height float32) ui.Rect {
	t.Helper()
	for attempt := 0; attempt < 24; attempt++ {
		if r, ok := issueHistoryVisibleRect(tr, label, panelX, width, height); ok {
			return r
		}
		tr.Scroll(panelX+40, height/2, 0, 220)
		tr.Frame()
	}
	t.Fatalf("could not find visible %q inside work panel [%.1f, %.1f] x [0, %.1f]", label, panelX, width, height)
	return ui.Rect{}
}

func issueHistoryVisibleRect(tr *ui.Tester, label string, panelX, width, height float32) (ui.Rect, bool) {
	r, ok := tr.Find(label)
	// Find returns the collected visible box, which can be a clipped slice.
	// Require a full text line (13-14px text is at least 12 high) and keep the
	// target inside a bounded, scrollable frame before comparing geometry.
	if !ok || r.W <= 0 || r.H < 12 || r.X < panelX || r.X+r.W > width || r.Y < 0 || r.Y+r.H > height-4 {
		return r, false
	}
	return r, true
}

func issueHistoryFindCard(t *testing.T, tr *ui.Tester, headerText, revisionText, openText, viewText string, panelX, width, height float32) (ui.Rect, ui.Rect, ui.Rect, ui.Rect) {
	t.Helper()
	for attempt := 0; attempt < 24; attempt++ {
		header, headerOK := issueHistoryVisibleRect(tr, headerText, panelX, width, height)
		revision, revisionOK := issueHistoryVisibleRect(tr, revisionText, panelX, width, height)
		open, openOK := issueHistoryVisibleRect(tr, openText, panelX, width, height)
		view, viewOK := issueHistoryVisibleRect(tr, viewText, panelX, width, height)
		if headerOK && revisionOK && openOK && viewOK {
			// Keep a full action control inside the readable viewport, not just
			// its text line; secondaryButton has a 32px minimum height.
			if view.Y+view.H+20 <= height {
				return header, revision, open, view
			}
		}
		tr.Scroll(panelX+40, height/2, 0, 180)
		tr.Frame()
	}
	t.Fatalf("could not find one visible execution card %q / %q / %q / %q inside work panel", headerText, revisionText, openText, viewText)
	return ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}
}

func issueHistoryRectsOverlap(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func issueHistoryGap(a, b ui.Rect) (float32, float32) {
	horizontal := b.X - (a.X + a.W)
	if a.X > b.X+b.W {
		horizontal = a.X - (b.X + b.W)
	}
	vertical := b.Y - (a.Y + a.H)
	if a.Y > b.Y+b.H {
		vertical = a.Y - (b.Y + b.H)
	}
	return horizontal, vertical
}

func TestIssueExecutionHistoryLayoutKeepsHeaderRevisionAndActionsDistinct(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		right, left   float32
		dark          bool
	}{
		{name: "1040-default-light", width: 1040, height: 720, right: defaultRightPaneWidth, left: defaultLeftPaneWidth},
		{name: "1040-default-dark", width: 1040, height: 720, right: defaultRightPaneWidth, left: defaultLeftPaneWidth, dark: true},
		{name: "1040-min-light", width: 1040, height: 720, right: minRightPaneWidth, left: defaultLeftPaneWidth},
		{name: "1040-min-dark", width: 1040, height: 720, right: minRightPaneWidth, left: defaultLeftPaneWidth, dark: true},
		{name: "720-overlay-light", width: 720, height: 640, right: defaultRightPaneWidth, left: defaultLeftPaneWidth},
		{name: "720-overlay-dark", width: 720, height: 640, right: defaultRightPaneWidth, left: defaultLeftPaneWidth, dark: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			a.rt = nil
			a.windowWidth = tc.width
			a.leftPaneWidth = tc.left
			a.rightPaneWidth = tc.right
			a.runtimeCapabilitiesLoaded = true
			a.runtimeMaxActiveTasks = 2
			issue := &productIssue{
				ID: "i-history", ContactID: ct.ID, Goal: "execution history layout",
				Status: issueStatusInProgress, RequirementsRevision: 1,
				Requirements:  parseIssueRequirements("keep history readable"),
				WorkSessionID: "work-history", Work: issueWorkSession{Loaded: true},
				Executions: []*issueExecution{
					{ID: "e-1", SessionID: "work-history", TaskID: "task-1", MessageID: "msg-1",
						Status: issueExecutionCompleted, RequirementsRevision: 2},
					{ID: "e-2", SessionID: "work-history", TaskID: "task-2", MessageID: "msg-2",
						Status: issueExecutionInterrupted, RequirementsRevision: 12,
						Detail: "The execution stopped after the provider returned an incomplete result; inspect the recorded terminal state."},
				},
			}
			ct.Issues = []*productIssue{issue}
			ct.SelectedIssueID = issue.ID
			ct.Works = []*workItem{{TaskID: "task-1", State: workCompleted}, {TaskID: "task-2", State: workInterrupted}}
			a.issueDetailOpen = map[string]bool{issue.ID: true}
			openIssueDetailForTest(a, ct, issue.ID)
			tr := ui.NewTester(a.view, tc.width, tc.height)
			tr.SetDark(tc.dark)
			tr.Frame()
			panelX := float32(tc.width) - a.workWidth()
			if a.detailPageActive() {
				panelX = 0
			}
			history := issueHistoryFindVisible(t, tr, "Execution history", panelX, float32(tc.width), float32(tc.height))
			if history.X < panelX || history.X+history.W > float32(tc.width) {
				t.Errorf("Execution history bounds=%+v panelX=%.1f width=%d", history, panelX, tc.width)
			}
			for index, execution := range issue.Executions {
				headerText := "Execution " + fmt.Sprint(index+1) + ": " + issueExecutionLabel(execution)
				revisionText := "revision " + fmt.Sprint(execution.RequirementsRevision)
				openText := "Open result: run " + fmt.Sprint(index+1)
				viewText := "View execution: run " + fmt.Sprint(index+1)
				header, revision, open, view := issueHistoryFindCard(t, tr, headerText, revisionText, openText, viewText,
					panelX, float32(tc.width), float32(tc.height))
				if dir := os.Getenv("SAKI_PREVIEW_DIR"); dir != "" {
					writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf(
						"headless-issue-execution-history-%s-run-%d.png", tc.name, index+1)), tr.Image())
				}
				horizontalGap, verticalGap := issueHistoryGap(header, revision)
				t.Logf("layout %s run %d header=%+v revision=%+v open=%+v view=%+v gaps=(%.1f,%.1f) panelX=%.1f width=%d",
					tc.name, index+1, header, revision, open, view, horizontalGap, verticalGap, panelX, tc.width)
				if issueHistoryRectsOverlap(header, revision) {
					t.Errorf("execution header/revision overlap header=%+v revision=%+v", header, revision)
				}
				if horizontalGap < 2 && verticalGap < 2 {
					t.Errorf("execution header/revision have no readable gap header=%+v revision=%+v gaps=(%.1f,%.1f)", header, revision, horizontalGap, verticalGap)
				}
				if issueHistoryRectsOverlap(open, view) {
					t.Errorf("result actions overlap open=%+v view=%+v", open, view)
				}
				if view.W < open.W {
					t.Errorf("View execution label is clipped narrower than Open result: open=%+v view=%+v", open, view)
				}
				// Exercise each visible result entry and verify its real execution
				// ownership. Close through the existing result control so the
				// detail layer and its return focus remain observable.
				if err := tr.Click(openText); err != nil {
					t.Fatal(err)
				}
				if !a.issueResult.Open || a.issueResult.ExecutionID != execution.ID {
					t.Errorf("%s target = open=%v execution=%q, want open=true execution=%q",
						openText, a.issueResult.Open, a.issueResult.ExecutionID, execution.ID)
				}
				if err := tr.Click("Close results"); err != nil {
					t.Fatal(err)
				}
				tr.Frame()
				if a.issueResult.Open {
					t.Errorf("%s close left result page open", openText)
				}
				if !tr.Focused(openText) {
					t.Errorf("%s did not restore focus after Close results", openText)
				}
			}
		})
	}
}
