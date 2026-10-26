package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueSupplementBusyAdmissionKeepsPendingAndAutomaticIntent(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Busy goal", "Keep the supplement pending") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if issue == nil {
		t.Fatal("issue missing")
	}
	target := issueSupplementTargetFor(ct, issue)
	if err := a.addIssueSupplement(ct, issue.ID, "wait for capacity"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-busy", SessionID: "work-session", Prompt: "pending", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	a.failIssueSubmit(ct.ID, issue.ID, &runtimeError{Code: "busy", Reason: "capacity_exceeded", Message: "runtime task capacity is full"}, true)
	if !issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 ||
		len(issue.Supplements.Batches) != 1 || issue.Supplements.Batches[0].State != supplementBatchCanceled ||
		issue.Supplements.Items[0].State != supplementStatePending ||
		execution.Status != issueExecutionNotDispatched || issue.Work.SendState != sendIdle ||
		issue.Work.SendError == "" || issue.Status != issueStatusInProgress {
		t.Fatalf("busy admission state = auto=%v pending=%d batch=%+v item=%+v execution=%+v work=%v/%q issue=%q",
			issue.Supplements.AutoContinue, issue.Supplements.pendingCount(), issue.Supplements.Batches,
			issue.Supplements.Items[0], execution, issue.Work.SendState, issue.Work.SendError, issue.Status)
	}
}

func TestIssueSupplementConfirmedRejectionStopsIntentAndKeepsText(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Reject goal", "Keep rejected text") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	target := issueSupplementTargetFor(ct, issue)
	if err := a.addIssueSupplement(ct, issue.ID, "rejected text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-reject", SessionID: "work-session", Prompt: "pending", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	a.failIssueSubmit(ct.ID, issue.ID, &runtimeError{Code: "invalid_request", Message: "provider rejected"}, true)
	if issue.Supplements.AutoContinue || issue.Supplements.Items[0].Text != "rejected text" ||
		issue.Supplements.Items[0].State != supplementStateRejected || execution.Status != issueExecutionRejected ||
		issue.Status != issueStatusNeedsAttention {
		t.Fatalf("confirmed rejection state = auto=%v item=%+v execution=%+v issue=%q",
			issue.Supplements.AutoContinue, issue.Supplements.Items[0], execution, issue.Status)
	}
}

func TestIssueSupplementAcceptedSaveFailureDisablesAutoWithoutBatch(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("Accept save failure", "Do not auto retry") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if err := a.addIssueSupplement(ct, issue.ID, "pending after accepted failure"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-accept-failure", SessionID: "work-session", Prompt: "prompt", Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusInProgress
	badParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := writeBlockedPathForTest(badParent); err != nil {
		t.Fatal(err)
	}
	oldPath := a.store.path
	a.store.path = filepath.Join(badParent, "contacts.json")
	a.acceptIssueTask(ct, issue, execution, "prompt", runtimeSendResult{TaskID: "task-accepted", MessageID: "message-accepted"})
	a.store.path = oldPath
	if issue.Supplements.AutoContinue || issue.Supplements.pendingCount() != 1 ||
		execution.Status != issueExecutionUnknown || execution.TaskID != "task-accepted" ||
		execution.MessageID != "message-accepted" || issue.Work.SendError == "" {
		t.Fatalf("accept save failure = auto=%v pending=%d execution=%+v work=%q",
			issue.Supplements.AutoContinue, issue.Supplements.pendingCount(), execution, issue.Work.SendError)
	}
}

func writeBlockedPathForTest(path string) error {
	return os.WriteFile(path, []byte("not a directory"), 0o600)
}

func TestIssueSupplementConfirmNotDispatchedSaveFailureRestoresAllWorkState(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("confirm save failure", "keep state") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	if err := a.addIssueSupplement(ct, issue.ID, "unknown text"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "execution-confirm-fail", Status: issueExecutionUnknown, Detail: "unknown"}
	issue.Executions = append(issue.Executions, execution)
	issue.Work.SendState = sendUnknown
	issue.Work.SendError = "unknown delivery"
	issue.Work.pendingExecutionID = execution.ID
	issue.Status = issueStatusNeedsAttention
	oldAuto := issue.Supplements.AutoContinue
	oldStore := a.store.path
	badParent := filepath.Join(t.TempDir(), "blocked-confirm-save")
	if err := writeBlockedPathForTest(badParent); err != nil {
		t.Fatal(err)
	}
	a.store.path = filepath.Join(badParent, "contacts.json")
	ok := a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID)
	a.store.path = oldStore
	if ok || execution.Status != issueExecutionUnknown || execution.Detail != "unknown" ||
		issue.Work.SendState != sendUnknown || issue.Work.SendError != "unknown delivery" ||
		issue.Work.pendingExecutionID != execution.ID || issue.Supplements.AutoContinue != oldAuto {
		t.Fatalf("failed confirm changed state: ok=%v execution=%+v work=%v/%q/%q auto=%v",
			ok, execution, issue.Work.SendState, issue.Work.SendError, issue.Work.pendingExecutionID, issue.Supplements.AutoContinue)
	}
}

func supplementRawHistoryRecords(t *testing.T, taskID, messageID, body string, contexts []taskContextItem, withTaskStart bool) []historyRecord {
	t.Helper()
	parts := []map[string]any{{"type": "text", "text": body}}
	for _, item := range contexts {
		envelope, err := json.Marshal(map[string]any{
			"type": "caller_context", "label": "Caller-provided context (user data)",
			"name": item.Name, "text": item.Text,
		})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, map[string]any{"type": "text", "text": string(envelope)})
	}
	content, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	var records []historyRecord
	if withTaskStart {
		records = append(records, historyRecord{Record: journalRecord{Type: "task_started", TaskID: taskID, MessageID: messageID}})
	}
	records = append(records, historyRecord{Record: journalRecord{
		Type:  "messages",
		Items: []inputItem{{Type: "message", Role: "user", Content: content}},
	}})
	records = append(records, historyRecord{Record: journalRecord{Type: "task_finished", TaskID: taskID, MessageID: messageID, Status: "completed"}})
	return records
}

func TestIssueSupplementHistoryMatchesRealJournalShape(t *testing.T) {
	execution := &issueExecution{
		SessionID: "session-real", TaskID: "task-real", MessageID: "message-real", Prompt: "goal body",
		Context: []taskContextItem{{ID: "c1", Name: "ctx", Text: "context text", Revision: 1}},
	}
	records := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, true)
	// The Messages journal record intentionally has no TaskID/MessageID fields:
	// ownership comes only from the preceding TaskStarted record.
	if records[1].Record.TaskID != "" || records[1].Record.MessageID != "" {
		t.Fatal("test fixture incorrectly put ownership on Messages")
	}
	if !supplementHistoryMatches(execution, records) {
		t.Fatal("real journal shape did not match exact Prompt/Context")
	}
	wrong := *execution
	wrong.Prompt = "different body"
	if supplementHistoryMatches(&wrong, records) {
		t.Fatal("incorrect body matched")
	}
	wrong = *execution
	wrong.Context = []taskContextItem{{ID: "c1", Name: "ctx", Text: "different context", Revision: 1}}
	if supplementHistoryMatches(&wrong, records) {
		t.Fatal("incorrect context matched")
	}
	if supplementHistoryMatches(execution, supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, false)) {
		t.Fatal("missing TaskStarted ownership matched")
	}
	assistantRecords := supplementRawHistoryRecords(t, execution.TaskID, execution.MessageID, execution.Prompt, execution.Context, true)
	assistantRecords[1].Record.Items[0].Role = "assistant"
	if supplementHistoryMatches(execution, assistantRecords) {
		t.Fatal("assistant text was accepted as recorded user input")
	}
}

func TestIssueSupplementUITesterPreviewCopyAndSaveFailure(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("UI supplement goal", "UI supplement requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	issueID := issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Add supplement"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issueID) = "exact supplement text\n第二行"
	if err := tr.Click("Save supplement"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Supplements == nil || issue.Supplements.pendingCount() != 1 {
		t.Fatal("UI add did not persist pending supplement")
	}
	if err := tr.Click("Preview full text"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Copy supplement text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "exact supplement text\n第二行" {
		t.Fatalf("supplement copy = %q", got)
	}
	if err := tr.Click("Add supplement"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issueID) = "must survive save failure"
	oldStore := a.store.path
	a.store.path = t.TempDir()
	if err := tr.Click("Save supplement"); err != nil {
		t.Fatal(err)
	}
	a.store.path = oldStore
	tr.Frame()
	if *a.issueSupplementDraft(issueID) != "must survive save failure" ||
		issue.Supplements.pendingCount() != 1 {
		t.Fatalf("save failure lost draft/added item: %q / %d", *a.issueSupplementDraft(issueID), issue.Supplements.pendingCount())
	}
}
