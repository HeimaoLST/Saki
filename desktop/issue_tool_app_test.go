package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func newIssueToolTestApp(t *testing.T) (*appModel, *contactStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{}
	a.store = store
	a.contacts = contactsFromStore(store)
	a.selected = 0
	a.issueToolLedger = store.issueToolLedger
	a.issueToolScopes = store.issueToolScopes
	a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	return a, store
}

func activateIssueToolTestScope(t *testing.T, a *appModel, scope *IssueToolScope) {
	t.Helper()
	ct := a.contactByID(scope.ContactID)
	if ct == nil {
		t.Fatal("scope test contact missing")
	}
	ct.SessionID = scope.RuntimeSession()
	ct.SendState = sendAccepted
	ct.ActiveTaskID = "task-test"
	scope.RuntimeTaskID = "task-test"
	a.issueToolScopes[scope.Ref] = *scope
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
}
func issueToolActionArgs(t *testing.T, scope IssueToolScope, action string, extra map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{"scope": scope.Ref, "action": action}
	for key, value := range extra {
		args[key] = value
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func issueToolResponseJSON(t *testing.T, response issueToolForwardResponse) []byte {
	t.Helper()
	if len(response.Content) != 1 {
		t.Fatalf("response content = %+v", response.Content)
	}
	return []byte(response.Content[0].Text)
}

func TestIssueToolOperationRepeatsAndPersistsAcrossReload(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{
		"goal": "first goal", "requirements": []string{"requirement one"},
	})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first action = %+v err=%v", first, err)
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("first action state issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	issueID := ct.Issues[0].ID
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	b := &appModel{}
	b.store = reloaded
	b.contacts = contactsFromStore(reloaded)
	b.selected = 0
	b.issueToolLedger = reloaded.issueToolLedger
	b.issueToolScopes = reloaded.issueToolScopes
	b.issueToolWaiters = map[string]chan IssueToolReceipt{}
	second, err := b.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("duplicate after reload = %+v err=%v", second, err)
	}
	if len(b.contacts[0].Issues) != 1 || b.contacts[0].Issues[0].ID != issueID || len(b.issueToolLedger.Operations) != 1 {
		t.Fatalf("duplicate changed state issues=%d ops=%d", len(b.contacts[0].Issues), len(b.issueToolLedger.Operations))
	}
	conflictArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "different goal"})
	conflict, err := b.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: conflictArgs})
	if err != nil || !conflict.IsError {
		t.Fatalf("different payload = %+v err=%v", conflict, err)
	}
	if len(b.contacts[0].Issues) != 1 {
		t.Fatal("different payload created another issue")
	}
	if !strings.Contains(string(issueToolResponseJSON(t, conflict)), "different payload") {
		t.Fatalf("conflict response = %s", issueToolResponseJSON(t, conflict))
	}
}

func TestIssueToolSaveFailureDoesNotCreateIssue(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-2", "turn-2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	a.store.path = t.TempDir() // a directory cannot be replaced by the atomic save
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "must not persist"}),
	})
	if err != nil || !response.IsError {
		t.Fatalf("save failure response = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 0 || len(a.issueToolLedger.Operations) != 0 {
		t.Fatalf("save failure changed state issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	if !strings.Contains(string(issueToolResponseJSON(t, response)), "save issue tool operation") {
		t.Fatalf("save failure response = %s", issueToolResponseJSON(t, response))
	}
}

func TestIssueToolListUsesCursor(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	if _, err := a.createIssueFor(ct, "A", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.createIssueFor(ct, "B", "b"); err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-3", "turn-3")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "limit": 1})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first list = %+v err=%v", first, err)
	}
	var page IssueToolIssueListResult
	if err := json.Unmarshal(issueToolResponseJSON(t, first), &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextCursor == "" || len(page.Issues) != 1 {
		t.Fatalf("first page = %+v", page)
	}
	args, _ = json.Marshal(map[string]any{"scope": scope.Ref, "limit": 1, "cursor": page.NextCursor})
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("second list = %+v err=%v", second, err)
	}
	var next IssueToolIssueListResult
	if err := json.Unmarshal(issueToolResponseJSON(t, second), &next); err != nil {
		t.Fatal(err)
	}
	if len(next.Issues) != 1 || next.Issues[0].ID == page.Issues[0].ID {
		t.Fatalf("cursor repeated first page: first=%+v second=%+v", page, next)
	}
}

func TestIssueToolQueryDoesNotSwitchSelectedContactOrPage(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "query goal", "query requirement")
	if err != nil {
		t.Fatal(err)
	}
	ct.SelectedIssueID = issue.ID
	scope, err := a.issueToolNewScope(ct.ID, "conversation-4", "turn-4")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	beforeSelected := ct.SelectedIssueID
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": issue.ID})
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: args})
	if err != nil || response.IsError {
		t.Fatalf("get = %+v err=%v", response, err)
	}
	if a.selectedContact().ID != ct.ID || ct.SelectedIssueID != beforeSelected || a.issueResult.Open || a.aux == "issue-result" {
		t.Fatalf("query changed selection/page: contact=%q issue=%q result=%v aux=%q", a.selectedContact().ID, ct.SelectedIssueID, a.issueResult.Open, a.aux)
	}
}

func TestIssueToolRunWithoutRuntimeRecordsUnknownAndDoesNotDuplicate(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "run goal", "run requirement")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-5", "turn-5")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args := issueToolActionArgs(t, *scope, issueToolActionRun, map[string]any{"issue_id": issue.ID})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || !first.IsError {
		t.Fatalf("run without runtime = %+v err=%v", first, err)
	}
	if len(issue.Executions) != 0 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("run failure state executions=%d ops=%d", len(issue.Executions), len(a.issueToolLedger.Operations))
	}
	operation := a.issueToolLedger.Operations[0]
	if operation.Receipt == nil || operation.Receipt.State != issueToolReceiptUnknown || operation.Receipt.Error == nil {
		t.Fatalf("run failure receipt = %+v", operation.Receipt)
	}
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || !second.IsError || len(issue.Executions) != 0 {
		t.Fatalf("duplicate run failure = %+v err=%v executions=%d", second, err, len(issue.Executions))
	}
}

func TestIssueToolScopeDeniesOtherContactIssue(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	first := a.selectedContact()
	second := &contact{ID: "contact-other", Name: "Other", Workspace: a.workDir}
	otherIssue, err := a.createIssueFor(second, "other goal", "")
	if err != nil {
		t.Fatal(err)
	}
	a.contacts = append(a.contacts, second)
	scope, err := a.issueToolNewScope(first.ID, "conversation-6", "turn-6")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	args, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": otherIssue.ID})
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: args})
	if err != nil || !response.IsError {
		t.Fatalf("cross-contact get = %+v err=%v", response, err)
	}
	var toolErr IssueToolError
	if err := json.Unmarshal(issueToolResponseJSON(t, response), &toolErr); err != nil {
		t.Fatal(err)
	}
	if toolErr.Code != "not_found" && toolErr.Code != "scope_denied" {
		t.Fatalf("cross-contact error = %+v", toolErr)
	}
}

func TestIssueToolCapturedCreateLeavesSelectedContactFormAndDraftAlone(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	first := a.selectedContact()
	second := &contact{ID: "contact-second", Name: "Second", Workspace: a.workDir, Draft: "second draft", SelectedIssueID: "second-issue"}
	a.contacts = append(a.contacts, second)
	a.selected = 1
	a.issueFormOpen = true
	a.issueGoal = "form goal"
	a.issueRequirements = "form requirements"
	scope, err := a.issueToolNewScope(first.ID, "conversation-7", "turn-7")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "captured goal"}),
	})
	if err != nil || response.IsError {
		t.Fatalf("captured create = %+v err=%v", response, err)
	}
	if a.selected != 1 || a.selectedContact() != second || second.Draft != "second draft" || second.SelectedIssueID != "second-issue" {
		t.Fatalf("captured create changed selected contact state: selected=%d contact=%+v", a.selected, a.selectedContact())
	}
	if !a.issueFormOpen || a.issueGoal != "form goal" || a.issueRequirements != "form requirements" {
		t.Fatalf("captured create changed issue form: open=%v goal=%q requirements=%q", a.issueFormOpen, a.issueGoal, a.issueRequirements)
	}
	if len(first.Issues) != 1 || first.Issues[0].Goal != "captured goal" {
		t.Fatalf("captured create did not attach to captured contact: %+v", first.Issues)
	}
}

func TestIssueToolScopeUsesLatestTaskBinding(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	ct.SessionID = "conversation-late"
	ct.SendState = sendSubmitting
	ct.pendingSend = &pendingSend{ToolScopeRef: ""}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-late", "turn-late")
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend.ToolScopeRef = scope.Ref
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("scope was not active before Runtime task binding")
	}
	captured := *scope // RuntimeTaskID intentionally still empty
	a.issueToolBindTask(scope.Ref, "task-late")
	ct.SendState = sendAccepted
	ct.ActiveTaskID = "task-late"
	request := IssueToolActionRequest{ScopeRef: captured.Ref, Action: issueToolActionCreate, Goal: "late binding"}
	operation, created, existing, _, err := a.issueToolReserveForTurn(captured, request)
	if err != nil || !created || existing || operation == nil {
		t.Fatalf("consumer reserve with captured old scope = op=%+v created=%v existing=%v err=%v", operation, created, existing, err)
	}
	response, err := a.issueToolCreate(captured, request, operation)
	if err != nil || response.IsError {
		t.Fatalf("consumer create with captured old scope = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 1 || ct.Issues[0].Goal != "late binding" {
		t.Fatalf("late-bound create = %+v", ct.Issues)
	}
}

func TestIssueToolInactiveScopeKeepsOldReceiptButRejectsNewAction(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-old", "turn-old")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	createArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "old goal"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || first.IsError {
		t.Fatalf("active create = %+v err=%v", first, err)
	}
	issue := ct.Issues[0]
	// End the captured turn without creating a new scope.
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	ct.pendingSend = nil
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || duplicate.IsError || len(ct.Issues) != 1 {
		t.Fatalf("inactive duplicate receipt = %+v err=%v issues=%d", duplicate, err, len(ct.Issues))
	}
	updateArgs := issueToolActionArgs(t, *scope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "goal": "new goal",
	})
	newAction, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: updateArgs})
	if err != nil || !newAction.IsError {
		t.Fatalf("inactive new action = %+v err=%v", newAction, err)
	}
	if issue.Goal != "old goal" || len(ct.Issues) != 1 {
		t.Fatalf("inactive scope mutated issue: %+v", issue)
	}
	listArgs, _ := json.Marshal(map[string]any{"scope": scope.Ref})
	list, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: listArgs})
	if err != nil || list.IsError {
		t.Fatalf("inactive retained read query = %+v err=%v", list, err)
	}
}

func TestIssueToolTruncatePreservesUTF8(t *testing.T) {
	text := strings.Repeat("中文", 10)
	got := truncateIssueToolText(text, 3)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "中文中") || !strings.Contains(got, "truncated") {
		t.Fatalf("UTF-8 truncation = %q", got)
	}
}

func TestIssueToolConcurrentSameCreateDoesNotDuplicate(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-concurrent", "turn-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	pump := newUIPump(t, a)
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "one create"})
	type result struct {
		response issueToolForwardResponse
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
			results <- result{response: response, err: err}
		}()
	}
	wg.Wait()
	close(results)
	pump.close()
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("concurrent create duplicated: issues=%d ops=%d", len(ct.Issues), len(a.issueToolLedger.Operations))
	}
}

func TestIssueToolReservedActionRechecksOwnerBeforeSideEffect(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-atomic", "turn-atomic")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	request := IssueToolActionRequest{
		ScopeRef: scope.Ref, Action: issueToolActionCreate, Goal: "atomic goal",
	}
	operation, created, existing, _, err := a.issueToolReserveForTurn(*scope, request)
	if err != nil || !created || existing || operation == nil {
		t.Fatalf("reserve = %+v created=%v existing=%v err=%v", operation, created, existing, err)
	}
	ct.Archived = true
	response, err := a.issueToolCreate(*scope, request, operation)
	if err != nil || !response.IsError {
		t.Fatalf("archived reserved action = %+v err=%v", response, err)
	}
	if len(ct.Issues) != 0 || operation.Receipt != nil {
		t.Fatalf("reserved action reached side effect: issues=%d receipt=%+v", len(ct.Issues), operation.Receipt)
	}
}

func TestIssueToolMergedSelectionPreservesEmptyBuiltinAndAliases(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	a.issueToolServer = &issueToolIPCServer{}
	a.issueToolEndpoint = "/tmp/test-issue-tool.sock"
	a.issueToolHelper = "/tmp/test-issue-tool-helper"
	current := runtimeToolSelection{
		Builtin: []string{},
		External: []runtimeExternalToolBinding{{
			SourceID: "other-source", ToolName: "other-tool", ExposedName: "alias-other",
		}},
	}
	merged, err := a.issueToolMergedSelection(current)
	if err != nil {
		t.Fatal(err)
	}
	builtin, _ := merged["builtin"].([]string)
	if len(builtin) != 0 {
		t.Fatalf("empty builtin was replaced: %v", builtin)
	}
	external, _ := merged["external"].([]map[string]string)
	if len(external) != 1+len(issueToolDefinitions()) {
		t.Fatalf("external bindings = %+v", external)
	}
	if external[0]["source_id"] != "other-source" || external[0]["exposed_name"] != "alias-other" {
		t.Fatalf("alias was not preserved: %+v", external[0])
	}
	conflict := runtimeToolSelection{External: []runtimeExternalToolBinding{{
		SourceID: "other-source", ToolName: "other-tool", ExposedName: issueToolDefinitions()[0].Name,
	}}}
	if _, err := a.issueToolMergedSelection(conflict); err == nil {
		t.Fatal("exposed-name conflict was silently accepted")
	}
}

func TestIssueToolAcceptedSaveFailureKeepsKnownIDs(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "known ids", "")
	if err != nil {
		t.Fatal(err)
	}
	operation := &IssueToolOperation{ID: "issue-op-known", Action: issueToolActionRun, TargetIssueID: issue.ID}
	a.issueToolLedger.Operations = append(a.issueToolLedger.Operations, operation)
	execution := &issueExecution{
		ID: "execution-known", SessionID: "session-known", TaskID: "task-known",
		MessageID: "message-known", ToolOperationID: operation.ID,
	}
	waiter := make(chan IssueToolReceipt, 1)
	a.issueToolWaiters[operation.ID] = waiter
	a.store.path = t.TempDir() // force receipt persistence to fail
	a.issueToolCompleteExecution(execution, true, "")
	receipt := <-waiter
	if receipt.State != issueToolReceiptUnknown || receipt.IssueID != issue.ID ||
		receipt.ExecutionID != execution.ID || receipt.SessionID != execution.SessionID ||
		receipt.TaskID != execution.TaskID || receipt.MessageID != execution.MessageID {
		t.Fatalf("accepted save failure receipt lost known identity: %+v", receipt)
	}
}

func TestIssueToolShutdownRejectsNewActionButKeepsReceipt(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	a.issueToolShutdown = make(chan struct{})
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "conversation-shutdown", "turn-shutdown")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	createArgs := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "before shutdown"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || first.IsError {
		t.Fatalf("pre-shutdown create = %+v err=%v", first, err)
	}
	issue := ct.Issues[0]
	a.beginShutdown()
	updateArgs := issueToolActionArgs(t, *scope, issueToolActionUpdate, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "goal": "after shutdown",
	})
	blocked, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: updateArgs})
	if err != nil || !blocked.IsError || len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("shutdown action = %+v err=%v issues=%d ops=%d", blocked, err, len(ct.Issues), len(a.issueToolLedger.Operations))
	}
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: createArgs})
	if err != nil || duplicate.IsError || len(a.issueToolLedger.Operations) != 1 {
		t.Fatalf("shutdown old receipt = %+v err=%v ops=%d", duplicate, err, len(a.issueToolLedger.Operations))
	}
}

func attachIssueToolTestExecution(issue *productIssue) {
	issue.Executions = append(issue.Executions, &issueExecution{
		ID: "execution-test", SessionID: "work-session", TaskID: "work-task",
		Status: issueExecutionCompleted, RequirementsRevision: issue.RequirementsRevision,
	})
}

func TestIssueToolWaitAnswerResumeUsesQuestionAnd142Ledger(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "needs input", "current requirement")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, err := a.issueToolNewScope(ct.ID, "conversation-q", "turn-q")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	waitArgs := issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "Which method should I use?",
	})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || first.IsError {
		t.Fatalf("wait = %+v err=%v", first, err)
	}
	var waitReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, first), &waitReceipt); err != nil {
		t.Fatal(err)
	}
	if waitReceipt.QuestionRef == "" || waitReceipt.IssueStatus != issueStatusWaitingUser || waitReceipt.QuestionState != issueQuestionOpen ||
		issue.Status != issueStatusWaitingUser || issue.openQuestion() == nil || issue.Supplements.AutoContinue || len(issue.Questions) != 1 {
		t.Fatalf("wait state = receipt=%+v question=%+v status=%q auto=%v", waitReceipt, issue.openQuestion(), issue.Status, issue.Supplements.AutoContinue)
	}
	questionRef := waitReceipt.QuestionRef
	if a.runIssue(issue.ID) {
		t.Fatal("run bypassed the open structured question")
	}
	repeated, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || repeated.IsError || len(issue.Questions) != 1 {
		t.Fatalf("repeated wait = %+v err=%v questions=%d", repeated, err, len(issue.Questions))
	}
	resumeArgs := issueToolActionArgs(t, *scope, issueToolActionResume, map[string]any{"issue_id": issue.ID})
	if a.resumeIssueFor(ct, issue.ID) {
		t.Fatal("UI resume bypassed the open structured question")
	}
	resumed, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: resumeArgs})
	if err != nil || !resumed.IsError || issue.Status != issueStatusWaitingUser || issue.openQuestion() == nil {
		t.Fatalf("resume with open question = %+v err=%v status=%q question=%+v", resumed, err, issue.Status, issue.openQuestion())
	}
	answerArgs := issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use SMS.", "question_ref": questionRef,
	})
	answered, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || answered.IsError {
		t.Fatalf("answer = %+v err=%v", answered, err)
	}
	var answerReceipt IssueToolReceipt
	if err := json.Unmarshal(issueToolResponseJSON(t, answered), &answerReceipt); err != nil {
		t.Fatal(err)
	}
	question := issue.questionByRef(questionRef)
	if question == nil || question.State != issueQuestionAnswered || question.AnswerSupplementID == "" ||
		answerReceipt.SupplementID != question.AnswerSupplementID || answerReceipt.IssueStatus != issueStatusInProgress ||
		answerReceipt.QuestionState != issueQuestionAnswered || issue.Supplements.pendingCount() != 1 ||
		issue.Status != issueStatusInProgress || !issue.Supplements.AutoContinue {
		t.Fatalf("answer state = question=%+v receipt=%+v pending=%d status=%q auto=%v", question, answerReceipt, issue.Supplements.pendingCount(), issue.Status, issue.Supplements.AutoContinue)
	}
	duplicate, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || duplicate.IsError || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("duplicate answer = %+v err=%v pending=%d", duplicate, err, issue.Supplements.pendingCount())
	}
	// A new turn cannot answer the same open question twice.
	secondScope, err := a.issueToolNewScope(ct.ID, "conversation-q2", "turn-q2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, secondScope)
	crossTurn, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *secondScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use SMS.", "question_ref": questionRef,
	})})
	if err != nil || crossTurn.IsError || issue.Supplements.pendingCount() != 1 {
		t.Fatalf("cross-turn duplicate answer = %+v err=%v pending=%d", crossTurn, err, issue.Supplements.pendingCount())
	}
	changedScope, err := a.issueToolNewScope(ct.ID, "conversation-q3", "turn-q3")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, changedScope)
	changed, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *changedScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "Use email instead.", "question_ref": questionRef,
	})})
	if err != nil || !changed.IsError || !strings.Contains(string(issueToolResponseJSON(t, changed)), "question_answer_conflict") ||
		issue.Supplements.pendingCount() != 1 {
		t.Fatalf("changed answer = %+v err=%v pending=%d", changed, err, issue.Supplements.pendingCount())
	}
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedContacts := contactsFromStore(reloaded)
	if len(reloadedContacts) != 1 || len(reloadedContacts[0].Issues) != 1 {
		t.Fatalf("reloaded question state = %+v", reloadedContacts)
	}
	reloadedIssue := reloadedContacts[0].Issues[0]
	reloadedQuestion := reloadedIssue.questionByRef(questionRef)
	if reloadedQuestion == nil || reloadedQuestion.State != issueQuestionAnswered ||
		reloadedQuestion.AnswerSupplementID != question.AnswerSupplementID || reloadedIssue.Supplements.pendingCount() != 1 {
		t.Fatalf("reloaded question/answer = %+v", reloadedIssue)
	}
}

func TestIssueToolWrongOrStaleQuestionRefIsRejected(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "question errors", "")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-qe", "turn-qe")
	activateIssueToolTestScope(t, a, scope)
	waitArgs := issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "Need input",
	})
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: waitArgs})
	if err != nil || wait.IsError {
		t.Fatalf("wait setup = %+v err=%v", wait, err)
	}
	bad, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": "question-wrong",
	})})
	if err != nil || !bad.IsError || !strings.Contains(string(issueToolResponseJSON(t, bad)), "question_not_found") {
		t.Fatalf("wrong question = %+v err=%v", bad, err)
	}
	question := issue.openQuestion()
	question.RequirementsRevision = 0
	staleScope, err := a.issueToolNewScope(ct.ID, "conversation-qe2", "turn-qe2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, staleScope)
	stale, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *staleScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": question.Ref,
	})})
	if err != nil || !stale.IsError || !strings.Contains(string(issueToolResponseJSON(t, stale)), "revision_mismatch") {
		t.Fatalf("stale question = %+v err=%v", stale, err)
	}
}

func TestIssueToolAnswerAfterProductCancelIsRejected(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "cancel question", "")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-cancel", "turn-cancel")
	activateIssueToolTestScope(t, a, scope)
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "need answer",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	question := issue.openQuestion()
	if !a.cancelIssueProductFor(ct, issue.ID) || issue.Status != issueStatusCanceled {
		t.Fatalf("product cancel = status %q", issue.Status)
	}
	answer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": question.Ref,
	})})
	if err != nil || !answer.IsError || issue.Supplements.pendingCount() != 0 {
		t.Fatalf("canceled answer = %+v err=%v pending=%d", answer, err, issue.Supplements.pendingCount())
	}
}

func TestIssueToolAnsweredQuestionMissingSupplementIsUnknown(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "missing answer", "")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-missing", "turn-missing")
	activateIssueToolTestScope(t, a, scope)
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "question",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	question := issue.openQuestion()
	question.State = issueQuestionAnswered
	question.AnswerSupplementID = "supplement-missing"
	newScope, _ := a.issueToolNewScope(ct.ID, "conversation-missing2", "turn-missing2")
	activateIssueToolTestScope(t, a, newScope)
	answer, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *newScope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer", "question_ref": question.Ref,
	})})
	if err != nil || !answer.IsError || !strings.Contains(string(issueToolResponseJSON(t, answer)), "operation_unknown") ||
		issue.Supplements.pendingCount() != 0 {
		t.Fatalf("missing answer supplement = %+v err=%v pending=%d", answer, err, issue.Supplements.pendingCount())
	}
}

func TestIssueToolWaitSupersedesChangedQuestionAndRevision(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "supersede", "first requirement")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-sup", "turn-sup")
	activateIssueToolTestScope(t, a, scope)
	wait := func(scope *IssueToolScope, text string, revision int) {
		t.Helper()
		response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
			"issue_id": issue.ID, "expected_revision": revision, "text": text,
		})})
		if err != nil || response.IsError {
			t.Fatalf("wait %q = %+v err=%v", text, response, err)
		}
	}
	wait(scope, "first question", issue.RequirementsRevision)
	firstRef := issue.openQuestion().Ref
	secondScope, _ := a.issueToolNewScope(ct.ID, "conversation-sup2", "turn-sup2")
	activateIssueToolTestScope(t, a, secondScope)
	wait(secondScope, "corrected question", issue.RequirementsRevision)
	if len(issue.Questions) != 2 || issue.Questions[0].Ref != firstRef || issue.Questions[0].State != issueQuestionSuperseded ||
		issue.Questions[1].State != issueQuestionOpen || issue.Questions[1].Text != "corrected question" {
		t.Fatalf("superseded questions = %+v", issue.Questions)
	}
	secondRef := issue.openQuestion().Ref
	if !a.editIssueFor(ct, issue.ID, issue.Goal, "second requirement") {
		t.Fatalf("edit requirements: %s", a.issueFormError)
	}
	thirdScope, _ := a.issueToolNewScope(ct.ID, "conversation-sup3", "turn-sup3")
	activateIssueToolTestScope(t, a, thirdScope)
	wait(thirdScope, "new revision question", issue.RequirementsRevision)
	if len(issue.Questions) != 3 || issue.questionByRef(secondRef).State != issueQuestionSuperseded {
		t.Fatalf("revision supersede = %+v", issue.Questions)
	}
	detail, err := issueToolGet(*thirdScope, []*productIssue{issue}, issue.ID)
	if err != nil || detail.Issue.QuestionRevision != issue.RequirementsRevision || detail.Issue.Question == "" {
		t.Fatalf("query question/revision = %+v err=%v", detail, err)
	}
}

func TestIssueToolResumeWithoutStructuredQuestionStillWorks(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "manual waiting", "")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	if !a.markIssueWaitingFor(ct, issue.ID) || issue.Status != issueStatusWaitingUser || issue.openQuestion() != nil {
		t.Fatalf("manual wait state = status=%q question=%+v", issue.Status, issue.openQuestion())
	}
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-manual", "turn-manual")
	activateIssueToolTestScope(t, a, scope)
	resume, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionResume, map[string]any{"issue_id": issue.ID})})
	if err != nil || resume.IsError || issue.Status != issueStatusInProgress {
		t.Fatalf("manual resume = %+v err=%v status=%q", resume, err, issue.Status)
	}
}

func TestIssueToolReplacementQuestionSaveFailureKeepsOriginalOpen(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "atomic replacement", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	attachIssueToolTestExecution(issue)
	scope, _ := a.issueToolNewScope(ct.ID, "conversation-atomic-q", "turn-atomic-q")
	activateIssueToolTestScope(t, a, scope)
	wait, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "original question",
	})})
	if err != nil || wait.IsError {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	original := issue.openQuestion()
	originalRef, originalText := original.Ref, original.Text
	issue.Supplements.AutoContinue = true
	replacement := &issueQuestion{
		Ref: "question-replacement", Text: "replacement question", RequirementsRevision: issue.RequirementsRevision,
		SourceScopeRef: scope.Ref, SourceTurnID: scope.TurnID, State: issueQuestionOpen, CreatedAt: nowMillis(),
	}
	operation := &IssueToolOperation{ID: "issue-op-replacement", Action: issueToolActionWait, TargetIssueID: issue.ID}
	receipt := &IssueToolReceipt{State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: replacement.Ref}
	a.store.path = t.TempDir() // atomic save fails after in-memory mutations
	if a.markIssueWaitingForQuestion(ct, issue.ID, originalRef, replacement, operation, receipt) {
		t.Fatal("replacement wait unexpectedly saved with a failing store")
	}
	if issue.openQuestion() == nil || issue.openQuestion().Ref != originalRef || issue.openQuestion().Text != originalText ||
		issue.openQuestion().State != issueQuestionOpen || len(issue.Questions) != 1 ||
		issue.Status != issueStatusWaitingUser || !issue.Supplements.AutoContinue || operation.Receipt != nil {
		t.Fatalf("failed replacement lost original question: questions=%+v status=%q auto=%v receipt=%+v", issue.Questions, issue.Status, issue.Supplements.AutoContinue, operation.Receipt)
	}
}

func TestIssueToolSchemaMarkerIsRuntimeGenerationBound(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	rt1, rt2 := &runtimeClient{}, &runtimeClient{}
	a.connectionGeneration = 1
	if a.issueToolSchemaReady(rt1, "session-schema") {
		t.Fatal("empty schema marker was ready")
	}
	a.markIssueToolSchemaReady(rt1, "session-schema")
	if !a.issueToolSchemaReady(rt1, "session-schema") {
		t.Fatal("same runtime/generation marker was not ready")
	}
	if a.issueToolSchemaReady(rt2, "session-schema") {
		t.Fatal("schema marker leaked to a different runtime")
	}
	a.connectionGeneration = 2
	if a.issueToolSchemaReady(rt1, "session-schema") {
		t.Fatal("schema marker leaked to a different generation")
	}
	a.clearIssueToolSchemaReady("session-schema")
	if a.issueToolSchemaReady(rt1, "session-schema") {
		t.Fatal("schema marker survived clear")
	}
}

func TestIssueToolWaitBeforeTaskBindingUsesScopeThenRealTask(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "wait before accept", "")
	if err != nil {
		t.Fatal(err)
	}
	ct.SessionID = "conversation-pre-bind"
	ct.SendState = sendSubmitting
	ct.pendingSend = &pendingSend{}
	scope, err := a.issueToolNewScope(ct.ID, "conversation-pre-bind", "turn-pre-bind")
	if err != nil {
		t.Fatal(err)
	}
	ct.pendingSend.ToolScopeRef = scope.Ref
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("submitting scope not active before accept")
	}
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionWait, map[string]any{
		"issue_id": issue.ID, "expected_revision": issue.RequirementsRevision, "text": "question before accept",
	})})
	if err != nil || response.IsError {
		t.Fatalf("pre-accept wait = %+v err=%v", response, err)
	}
	question := issue.openQuestion()
	if question == nil || question.SourceTaskID != "" || question.SourceScopeRef != scope.Ref || question.SourceTurnID != scope.TurnID {
		t.Fatalf("pre-accept question source = %+v", question)
	}
	a.issueToolBindTask(scope.Ref, "task-after-accept")
	if question.SourceTaskID != "task-after-accept" {
		t.Fatalf("question task was not bound to the real accepted task: %+v", question)
	}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(a.store.path, a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	reloadedIssue := contactsFromStore(reloaded)[0].Issues[0]
	reloadedQuestion := reloadedIssue.questionByRef(question.Ref)
	if reloadedQuestion == nil || reloadedQuestion.SourceTaskID != "task-after-accept" || reloadedQuestion.SourceScopeRef != scope.Ref {
		t.Fatalf("reloaded bound question = %+v", reloadedQuestion)
	}
}

func TestIssueToolInternalScopeIsFilteredFromUserHistoryOnlyAtTail(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "history-session", "history-turn")
	if err != nil {
		t.Fatal(err)
	}
	scopeJSON, _ := json.Marshal(scope)
	// Simulate a same-turn create/task binding changing mutable scope fields
	// after the wire snapshot was emitted.
	scope.AllowedIssueIDs = append(scope.AllowedIssueIDs, "issue-created-later")
	scope.RuntimeTaskID = "task-bound-later"
	a.issueToolScopes[scope.Ref] = *scope
	user := taskContextItem{ID: "user-1", Name: "user-note", Text: "literal {\"type\":\"caller_context\"}", Revision: 1}
	scopeItem := taskContextItem{Name: issueToolScopeContextName, Text: string(scopeJSON), Revision: 1}
	records := supplementRawHistoryRecords(t, "history-task", "history-message", "user body", []taskContextItem{user, scopeItem}, true)
	filtered := parseHistoryWithIssueToolScopeFilter("history-session", records, nil, a.issueToolScopeIndexSnapshot())
	raw := parseHistory("history-session", records, nil)
	if len(filtered.messages) != 1 || len(filtered.messages[0].Context) != 1 ||
		filtered.messages[0].Context[0].Name != user.Name || filtered.messages[0].Context[0].Text != user.Text {
		t.Fatalf("filtered user history = %+v", filtered.messages)
	}
	if len(raw.messages) != 1 || len(raw.messages[0].Context) != 2 {
		t.Fatalf("raw history was mutated: %+v", raw.messages)
	}
	// A same-named user item is preserved when it is not the actual trailing
	// app-issued scope.
	userScopeLike := taskContextItem{ID: "user-2", Name: issueToolScopeContextName, Text: string(scopeJSON), Revision: 1}
	records = supplementRawHistoryRecords(t, "history-task", "history-message", "user body", []taskContextItem{userScopeLike, user}, true)
	filtered = parseHistoryWithIssueToolScopeFilter("history-session", records, nil, a.issueToolScopeIndexSnapshot())
	if len(filtered.messages[0].Context) != 2 {
		t.Fatalf("non-tail same-named user context was removed: %+v", filtered.messages[0].Context)
	}
}

func TestIssueToolContextWireBudgetIncludesInternalScope(t *testing.T) {
	userText := strings.Repeat("x", taskContextMaxBytes-64)
	inputs := taskContextInputs([]taskContextItem{{ID: "u", Name: "user", Text: userText, Revision: 1}})
	scope := runtimeContextInput{Name: issueToolScopeContextName, Text: `{"ref":"scope-budget"}`}
	inputs = append(inputs, scope)
	if err := validateRuntimeContextInputs(inputs); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("wire budget error = %v", err)
	}
}

func TestIssueToolAnswerBeforeWorkTerminalWaitsForRealTerminal(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "answer before terminal", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{
		ID: "execution-waiting", SessionID: "work-session", TaskID: "work-task",
		Status: issueExecutionRunning, RequirementsRevision: issue.RequirementsRevision,
		WaitRequested: true,
	}
	issue.Executions = []*issueExecution{execution}
	issue.WorkSessionID = "work-session"
	issue.Work.ActiveTaskID = "work-task"
	issue.Work.SendState = sendAccepted
	question := &issueQuestion{
		Ref: "question-before-terminal", Text: "Need input", State: issueQuestionOpen,
		RequirementsRevision: issue.RequirementsRevision, SourceTaskID: "work-task", SourceScopeRef: "work-scope",
		CreatedAt: nowMillis(),
	}
	issue.Questions = []*issueQuestion{question}
	issue.Status = issueStatusWaitingUser
	issue.Supplements = newIssueSupplementLedger()
	issue.Supplements.AutoContinue = false
	ct.SessionID = "conversation-answer"
	scope, err := a.issueToolNewScope(ct.ID, "conversation-answer", "answer-turn")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "answer before terminal", "question_ref": question.Ref,
	})})
	if err != nil || response.IsError {
		t.Fatalf("answer before terminal = %+v err=%v", response, err)
	}
	if question.State != issueQuestionAnswered || issue.Status != issueStatusInProgress || !issue.Supplements.AutoContinue {
		t.Fatalf("answer-before-terminal state = question=%+v status=%q auto=%v", question, issue.Status, issue.Supplements.AutoContinue)
	}
	if len(issue.Executions) != 1 {
		t.Fatalf("answer before terminal dispatched early: %d", len(issue.Executions))
	}
	// A later real terminal must not turn this answer into an already-completed work.
	a.applyIssueTerminalLifecycle(issue, "work-task", "interrupted")
	if issue.Status != issueStatusInProgress || len(issue.Executions) != 1 {
		t.Fatalf("answer-before-terminal terminal state = status=%q executions=%d", issue.Status, len(issue.Executions))
	}
	// A user Stop after the answer closes automatic continuation intent.
	issue.Work.StopRequestedTasks = map[string]bool{"work-task": true}
	issue.Supplements.AutoContinue = false
	a.applyIssueTerminalLifecycle(issue, "work-task", "interrupted")
	if issue.Status != issueStatusInProgress || issue.Supplements.AutoContinue {
		t.Fatalf("stop-after-answer state = status=%q auto=%v", issue.Status, issue.Supplements.AutoContinue)
	}
}

func TestIssueToolWorkWaitBeforeAcceptLatchesRealTask(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, err := a.createIssueFor(ct, "preaccept work wait", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	issue.WorkSessionID = "work-preaccept"
	issue.Work.SendState = sendSubmitting
	execution := &issueExecution{ID: "execution-preaccept", SessionID: "work-preaccept", Status: issueExecutionPending}
	issue.Executions = []*issueExecution{execution}
	scope, err := a.issueToolNewWorkScope(ct.ID, issue.ID, execution.ID, issue.WorkSessionID)
	if err != nil {
		t.Fatal(err)
	}
	execution.ToolScopeRef = scope.Ref
	execution.ToolScopeInput = issueToolScopeText(scope)
	if !a.issueToolScopeActive(*scope) {
		t.Fatal("work scope was not active while awaiting accept")
	}
	request := IssueToolActionRequest{ScopeRef: scope.Ref, Action: issueToolActionWait, IssueID: issue.ID, ExpectedRevision: issueToolRevisionPointer(issue.RequirementsRevision), Text: "preaccept question"}
	operation, created, existing, _, err := a.issueToolReserveForTurn(*scope, request)
	if err != nil || !created || existing {
		t.Fatalf("preaccept reserve = %+v created=%v existing=%v err=%v", operation, created, existing, err)
	}
	response, err := a.issueToolWorkWait(*scope, request, operation)
	if err != nil || response.IsError {
		t.Fatalf("preaccept work wait = %+v err=%v", response, err)
	}
	question := issue.openQuestion()
	if question == nil || question.SourceTaskID != "" || execution.WaitRequested != true {
		t.Fatalf("preaccept wait state = question=%+v execution=%+v", question, execution)
	}
	a.acceptIssueTask(ct, issue, execution, "preaccept question", runtimeSendResult{TaskID: "task-preaccept", MessageID: "message-preaccept"})
	if execution.TaskID != "task-preaccept" || question.SourceTaskID != "task-preaccept" || !execution.WaitRequested {
		t.Fatalf("accept did not bind wait scope/task: execution=%+v question=%+v", execution, question)
	}
}

func TestIssueToolWorkScopeRejectsConversationAndActionTools(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, _ := a.createIssueFor(ct, "work tool boundary", "")
	issue.WorkSessionID = "work-boundary"
	execution := &issueExecution{ID: "exec-boundary", SessionID: "work-boundary", TaskID: "task-boundary", Status: issueExecutionRunning}
	issue.Executions = []*issueExecution{execution}
	issue.Work.ActiveTaskID = execution.TaskID
	issue.Work.SendState = sendAccepted
	scope, _ := a.issueToolNewWorkScope(ct.ID, issue.ID, execution.ID, issue.WorkSessionID)
	scope.RuntimeTaskID = execution.TaskID
	a.issueToolScopes[scope.Ref] = *scope
	response, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionRun, map[string]any{"issue_id": issue.ID})})
	if err != nil || !response.IsError {
		t.Fatalf("work scope action bypass = %+v err=%v", response, err)
	}
}

func TestIssueToolLateWaitStopCannotCancelNewExecution(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	issue, _ := a.createIssueFor(ct, "late stop", "")
	execution := &issueExecution{
		ID: "execution-old", SessionID: "session-old", TaskID: "task-old", Status: issueExecutionRunning,
		WaitRequested: true,
	}
	issue.Executions = []*issueExecution{execution}
	issue.WorkSessionID = "session-old"
	issue.Work.ActiveTaskID = "task-new"
	issue.Work.SendState = sendAccepted
	a.rt = &runtimeClient{}
	if a.requestIssueWaitStop(ct, issue, execution.ID, execution.SessionID, execution.TaskID) {
		t.Fatal("late wait stop accepted an old execution after a new task became active")
	}
	if issue.Work.StopRequestedTasks["task-new"] || issue.Work.StopRequestedTasks["task-old"] {
		t.Fatal("late wait stop recorded a user Stop on any task")
	}
}

func TestIssueToolReleaseCallbackParticipatesInShutdownDrain(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	quit := false
	a.requestQuit = func() { quit = true }
	a.issueToolReleaseInFlight = 1
	a.beginShutdown()
	if a.quitReady || quit {
		t.Fatal("shutdown completed while a work release callback was in flight")
	}
	a.issueToolReleaseInFlight = 0
	a.finishShutdown()
	if !a.quitReady || !quit {
		t.Fatal("shutdown did not complete after the final release callback")
	}
}

func TestIssueToolQueryResponsesRespectByteBudgetAndUTF8(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	longText := strings.Repeat("中文 requirement ", 2000)
	var firstIssueID string
	for i := 0; i < 60; i++ {
		issue, err := a.createIssueFor(ct, fmt.Sprintf("goal-%02d-%s", i, longText), longText)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstIssueID = issue.ID
		}
	}
	issue := ct.issueByID(firstIssueID)
	for i := 0; i < 32; i++ {
		issue.Requirements = append(issue.Requirements, &issueRequirement{
			ID: fmt.Sprintf("requirement-%02d", i), Text: fmt.Sprintf("%02d %s", i, longText), State: requirementUnverified,
		})
	}
	issue.RequirementsRevision = 9
	scope, err := a.issueToolNewScope(ct.ID, "query-budget-session", "query-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		args := map[string]any{"scope": scope.Ref, "limit": 50}
		if cursor != "" {
			args["cursor"] = cursor
		}
		listArgs, _ := json.Marshal(args)
		listResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_list", Arguments: listArgs})
		if err != nil || listResponse.IsError {
			t.Fatalf("budget list page %d = %+v err=%v", page, listResponse, err)
		}
		rawList, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget-probe"`), Result: listResponse})
		if len(rawList) > issueToolQueryMaxBytes || !utf8.Valid(rawList) {
			t.Fatalf("list MCP response bytes=%d validUTF8=%v", len(rawList), utf8.Valid(rawList))
		}
		var list IssueToolIssueListResult
		if err := json.Unmarshal(issueToolResponseJSON(t, listResponse), &list); err != nil {
			t.Fatal(err)
		}
		for _, summary := range list.Issues {
			if seen[summary.ID] {
				t.Fatalf("list cursor repeated issue %q on page %d", summary.ID, page)
			}
			seen[summary.ID] = true
		}
		if !list.HasMore {
			break
		}
		if list.NextCursor == "" {
			t.Fatalf("list page %d has_more without cursor", page)
		}
		cursor = list.NextCursor
	}
	if len(seen) != 60 {
		t.Fatalf("bounded paging saw %d issues, want 60", len(seen))
	}
	getArgs, _ := json.Marshal(map[string]any{"scope": scope.Ref, "issue_id": firstIssueID})
	getResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: getArgs})
	if err != nil || getResponse.IsError {
		t.Fatalf("budget get = %+v err=%v", getResponse, err)
	}
	rawGet, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget-probe"`), Result: getResponse})
	if len(rawGet) > issueToolQueryMaxBytes || !utf8.Valid(rawGet) {
		t.Fatalf("detail MCP response bytes=%d validUTF8=%v", len(rawGet), utf8.Valid(rawGet))
	}
	var detail IssueToolIssueDetail
	if err := json.Unmarshal(issueToolResponseJSON(t, getResponse), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Issue.ID != firstIssueID || detail.Issue.RequirementsRevision != 9 || len(detail.Executions) > 20 ||
		detail.RequirementCount != 33 || len(detail.Requirements) >= 33 || !detail.RequirementsHasMore {
		t.Fatalf("bounded detail identity/range = %+v", detail)
	}
}

func TestIssueToolDetailBudgetShrinksLongExcerptAndPreservesIdentity(t *testing.T) {
	long := strings.Repeat("中文\\\"\\n\\t", 900)
	detail := &IssueToolIssueDetail{
		Issue: IssueToolIssueSummary{
			ID: "issue-budget-identity", Goal: long, Status: issueStatusWaitingUser,
			RequirementsRevision: 7, QuestionRef: "question-budget", Question: long,
			QuestionRevision: 7,
		},
		Requirements:      []IssueToolRequirementView{{ID: "req-1", Text: long, Evidence: long}},
		ExecutionCount:    1,
		ResultExecutionID: "execution-budget",
		ResultExcerpt:     long + long,
	}
	bounded := boundIssueToolDetail(detail)
	responseBytes, _ := json.Marshal(issueToolRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`"budget"`), Result: mustIssueToolDetailForward(t, bounded)})
	if len(responseBytes) > issueToolQueryMaxBytes || !utf8.Valid(responseBytes) {
		t.Fatalf("long detail MCP bytes=%d validUTF8=%v", len(responseBytes), utf8.Valid(responseBytes))
	}
	if bounded.Issue.ID != "issue-budget-identity" || bounded.Issue.Status != issueStatusWaitingUser ||
		bounded.Issue.QuestionRef != "question-budget" || bounded.Issue.RequirementsRevision != 7 ||
		bounded.ResultExecutionID != "execution-budget" || !bounded.ResultExcerptHasMore {
		t.Fatalf("bounded identity/excerpt = %+v", bounded)
	}
}

func mustIssueToolDetailForward(t *testing.T, detail *IssueToolIssueDetail) issueToolForwardResponse {
	t.Helper()
	response, err := issueToolJSONResponse(detail, false)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
