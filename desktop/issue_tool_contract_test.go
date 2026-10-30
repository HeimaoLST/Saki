package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func issueToolScopeForTest() IssueToolScope {
	return IssueToolScope{
		Ref: "scope-turn-1", ContactID: "contact-x", ConversationSessionID: "conversation-1",
		TurnID: "turn-1", AllowedIssueIDs: []string{"issue-a", "issue-b"}, AllowCreate: true,
		CreatedAt: 1,
	}
}

func issueToolTestIssue(id, contactID, status string) *productIssue {
	return &productIssue{
		ID: id, ContactID: contactID, Goal: "goal " + id, Status: status, RequirementsRevision: 3,
		Requirements: []*issueRequirement{{ID: "req-" + id, Text: "requirement " + id, State: requirementUnverified}},
	}
}

func issueToolCreateRequest(scope IssueToolScope, goal string) IssueToolActionRequest {
	return IssueToolActionRequest{ScopeRef: scope.Ref, Action: issueToolActionCreate, Goal: goal}
}

// runReserved is the module consumer: it runs the side effect only after a new
// operation was durably reserved. Duplicate/unknown/error paths do not call it.
func runReserved(ledger *IssueToolOperationLedger, scope IssueToolScope, request IssueToolActionRequest, save func() error, execute func(*IssueToolOperation)) (*IssueToolOperation, bool, error) {
	operation, isNew, err := ledger.Reserve(scope, request, save)
	if err != nil {
		return nil, false, err
	}
	if isNew {
		execute(operation)
	}
	return operation, isNew, nil
}

func TestIssueToolScopeIsOwnerBoundBeforeRuntimeTask(t *testing.T) {
	scope := issueToolScopeForTest()
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	if scope.RuntimeTaskID != "" {
		t.Fatal("scope unexpectedly requires a Runtime task ID")
	}
	if !scope.AllowsIssue(issueToolTestIssue("issue-a", "contact-x", issueStatusInProgress)) {
		t.Fatal("owner issue was not allowed")
	}
	if scope.AllowsIssue(issueToolTestIssue("issue-c", "contact-x", issueStatusInProgress)) {
		t.Fatal("unlisted same-owner issue was allowed")
	}
	if scope.AllowsIssue(issueToolTestIssue("issue-a", "contact-y", issueStatusInProgress)) {
		t.Fatal("other owner's issue was allowed")
	}
	if err := (IssueToolActionRequest{ScopeRef: scope.Ref, Action: issueToolActionRun, IssueID: "issue-c"}).Validate(scope); err == nil {
		t.Fatal("cross-issue action validation succeeded")
	}
}

func TestIssueToolListAndGetAreBoundedAndUseExistingFacts(t *testing.T) {
	scope := issueToolScopeForTest()
	issues := []*productIssue{
		issueToolTestIssue("issue-a", "contact-x", issueStatusInProgress),
		issueToolTestIssue("issue-b", "contact-x", issueStatusCompleted),
		issueToolTestIssue("issue-c", "contact-y", issueStatusInProgress),
	}
	first, err := issueToolList(scope, issues, false, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Issues) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := issueToolList(scope, issues, false, 10, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Issues) != 1 || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
	if _, err := issueToolList(scope, issues, false, 1, "not-a-cursor"); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
	if _, err := issueToolGet(scope, issues, "issue-c"); err == nil {
		t.Fatal("cross-owner get succeeded")
	}
	detail, err := issueToolGet(scope, issues, "issue-a")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Issue.ID != "issue-a" || len(detail.Requirements) != 1 || detail.Requirements[0].Text != "requirement issue-a" {
		t.Fatalf("get detail = %+v", detail)
	}

	issue := issueToolTestIssue("issue-long", "contact-x", issueStatusInProgress)
	scope.AllowedIssueIDs = append(scope.AllowedIssueIDs, issue.ID)
	for i := 0; i < 21; i++ {
		issue.Executions = append(issue.Executions, &issueExecution{ID: "exec-" + string(rune('a'+i)), Status: issueExecutionCompleted})
	}
	longDetail, err := issueToolGet(scope, []*productIssue{issue}, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if longDetail.ExecutionCount != 21 || len(longDetail.Executions) != 20 || !longDetail.ExecutionsHasMore {
		t.Fatalf("execution bound = count %d returned %d hasMore %v", longDetail.ExecutionCount, len(longDetail.Executions), longDetail.ExecutionsHasMore)
	}
	if issues[0].Status != issueStatusInProgress || issues[0].Requirements[0].State != requirementUnverified {
		t.Fatalf("query mutated issue facts = %+v", issues[0])
	}
}

func TestIssueToolReservationConsumerDoesNotRepeatOrReusePayload(t *testing.T) {
	scope := issueToolScopeForTest()
	ledger := &IssueToolOperationLedger{}
	saves, executions := 0, 0
	executedIDs := []string{}
	save := func() error {
		saves++
		_, err := json.Marshal(ledger)
		return err
	}
	execute := func(operation *IssueToolOperation) {
		executions++
		executedIDs = append(executedIDs, operation.ID)
	}
	request := issueToolCreateRequest(scope, "first goal")
	created, isNew, err := runReserved(ledger, scope, request, save, execute)
	if err != nil || !isNew || created == nil || saves != 1 || executions != 1 {
		t.Fatalf("first reserve = %+v new=%v saves=%d executions=%d err=%v", created, isNew, saves, executions, err)
	}
	duplicate, isNew, err := runReserved(ledger, scope, request, save, execute)
	if err != nil || isNew || duplicate.ID != created.ID || saves != 1 || executions != 1 {
		t.Fatalf("duplicate reserve = %+v new=%v saves=%d executions=%d err=%v", duplicate, isNew, saves, executions, err)
	}
	if len(executedIDs) != 1 || executedIDs[0] != created.ID {
		t.Fatalf("executed operation IDs = %+v", executedIDs)
	}
	// Different payload in the same target/action slot is a conflict, not a new
	// create operation. Cross-issue is rejected before reservation.
	if _, _, err := runReserved(ledger, scope, issueToolCreateRequest(scope, "different goal"), save, execute); err == nil ||
		!strings.Contains(err.Error(), "different payload") {
		t.Fatalf("different payload = %v", err)
	}
	if _, _, err := runReserved(ledger, scope, IssueToolActionRequest{ScopeRef: scope.Ref, Action: issueToolActionRun, IssueID: "issue-c"}, save, execute); err == nil ||
		!strings.Contains(err.Error(), "scope") {
		t.Fatalf("cross-issue = %v", err)
	}
	if saves != 1 || executions != 1 || len(ledger.Operations) != 1 {
		t.Fatalf("consumer side effects = saves %d executions %d operations %d", saves, executions, len(ledger.Operations))
	}
}

func TestIssueToolOperationPersistsAndDoesNotReplay(t *testing.T) {
	scope := issueToolScopeForTest()
	ledger := &IssueToolOperationLedger{}
	saves, executions := 0, 0
	save := func() error {
		saves++
		_, err := json.Marshal(ledger)
		return err
	}
	first, isNew, err := runReserved(ledger, scope, issueToolCreateRequest(scope, "reload goal"), save, func(*IssueToolOperation) { executions++ })
	if err != nil || !isNew || first == nil {
		t.Fatalf("first = %+v new=%v err=%v", first, isNew, err)
	}
	if err := ledger.SetReceipt(first.ID, &IssueToolReceipt{State: issueToolReceiptUnknown}, save); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded IssueToolOperationLedger
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	reloadedSaves, reloadedExecutions := 0, 0
	existing, isNew, err := runReserved(&reloaded, scope, issueToolCreateRequest(scope, "reload goal"), func() error {
		reloadedSaves++
		return nil
	}, func(*IssueToolOperation) { reloadedExecutions++ })
	if err != nil || isNew || existing.ID != first.ID || reloadedSaves != 0 || reloadedExecutions != 0 {
		t.Fatalf("reloaded duplicate = %+v new=%v saves=%d executions=%d err=%v", existing, isNew, reloadedSaves, reloadedExecutions, err)
	}
	if existing.Receipt == nil || existing.Receipt.State != issueToolReceiptUnknown {
		t.Fatalf("receipt after reload = %+v", existing.Receipt)
	}
}

func TestIssueToolReserveSaveFailureDoesNotConsumeAction(t *testing.T) {
	scope := issueToolScopeForTest()
	ledger := &IssueToolOperationLedger{}
	saves, executions := 0, 0
	_, isNew, err := runReserved(ledger, scope, issueToolCreateRequest(scope, "save failure"), func() error {
		saves++
		return errIssueToolSaveRequired
	}, func(*IssueToolOperation) { executions++ })
	if err == nil || isNew || len(ledger.Operations) != 0 || saves != 1 || executions != 0 {
		t.Fatalf("save failure = new=%v operations=%d saves=%d executions=%d err=%v", isNew, len(ledger.Operations), saves, executions, err)
	}
}

func TestIssueToolSetReceiptRequiresCurrentLedgerMemberAndTarget(t *testing.T) {
	scope := issueToolScopeForTest()
	ledger := &IssueToolOperationLedger{}
	saves := 0
	save := func() error { saves++; return nil }
	operation, _, err := runReserved(ledger, scope, IssueToolActionRequest{ScopeRef: scope.Ref, Action: issueToolActionRun, IssueID: "issue-a"}, save, func(*IssueToolOperation) {})
	if err != nil {
		t.Fatal(err)
	}
	wrong := &IssueToolOperationLedger{}
	if err := wrong.SetReceipt(operation.ID, &IssueToolReceipt{State: issueToolReceiptUnknown}, save); err == nil {
		t.Fatal("foreign ledger accepted a receipt")
	}
	if saves != 1 {
		t.Fatalf("foreign receipt save count = %d", saves)
	}
	accepted := &IssueToolReceipt{State: issueToolReceiptAccepted, IssueID: "issue-a", ExecutionID: "e", SessionID: "s", TaskID: "t"}
	if err := ValidateIssueToolReceipt(accepted); err == nil {
		t.Fatal("accepted receipt without message id passed")
	}
	accepted.MessageID = "m"
	if err := ledger.SetReceipt(operation.ID, &IssueToolReceipt{State: issueToolReceiptAccepted, IssueID: "issue-b", ExecutionID: "e", SessionID: "s", TaskID: "t", MessageID: "m"}, save); err == nil {
		t.Fatal("receipt for another issue passed")
	}
	if err := ledger.SetReceipt(operation.ID, accepted, save); err != nil {
		t.Fatal(err)
	}
	if operation.Receipt != accepted || saves != 2 {
		t.Fatalf("current ledger receipt = %+v saves=%d", operation.Receipt, saves)
	}
}

func TestIssueToolRevisionValidationKeepsInvalidActionSentinel(t *testing.T) {
	scope := issueToolScopeForTest()
	for _, action := range []string{issueToolActionUpdate, issueToolActionWait} {
		request := IssueToolActionRequest{
			ScopeRef: scope.Ref, Action: action, IssueID: "issue-a", Goal: "goal", Text: "text",
		}
		err := request.Validate(scope)
		if !errors.Is(err, errIssueToolAction) {
			t.Fatalf("%s revision error = %v, want errIssueToolAction", action, err)
		}
	}
}
