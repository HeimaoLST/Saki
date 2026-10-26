package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
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
