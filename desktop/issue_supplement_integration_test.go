package main

// Real-Runtime gates for #142 supplement preparation and dispatch. These use
// the fixed offline helper and a local controlled provider; no paid model or
// native GUI evidence is claimed.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueSupplementAutoDispatchAcrossInFlightRevision(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := newGatedProvider("supp B", "supp A")
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(), 2)
	defer func() {
		provider.release("supp A")
		provider.release("supp B")
		if !h.closed {
			h.shutdown(t)
		}
	}()

	var issueID string
	var setupErr error
	h.pump.do(func() {
		if !h.app.createIssue("Issue goal", "Current requirement") {
			setupErr = errors.New(h.app.issueFormError)
			return
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		if issueID == "" {
			setupErr = errors.New("issue was not selected")
			return
		}
		issue := ct.issueByID(issueID)
		if _, err := issue.Context.AddSnippet("Issue context", "context value"); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}

	// This issue has never run. Saving supplement A is a receipt, not an
	// authorization to start the first execution.
	var addErr error
	h.pump.do(func() { addErr = h.app.addIssueSupplement(h.app.selectedContact(), issueID, "supp A") })
	if addErr != nil {
		t.Fatal(addErr)
	}
	if provider.count() != 0 {
		t.Fatalf("first-run supplement started a task without explicit Run: %d", provider.count())
	}
	var pendingBeforeRun bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		pendingBeforeRun = issue != nil && issue.Supplements.pendingCount() == 1 && len(issue.Executions) == 0
	})
	if !pendingBeforeRun {
		t.Fatal("first supplement was not retained pending before explicit Run")
	}

	var runOK bool
	var runErr string
	h.pump.do(func() {
		runOK = h.app.runIssue(issueID)
		runErr = h.app.issueFormError
	})
	if !runOK {
		t.Fatalf("explicit Run failed: %s", runErr)
	}
	select {
	case <-provider.arrived["supp A"]:
	case <-time.After(15 * time.Second):
		t.Fatal("first supplement did not reach the provider")
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != "" && len(issue.Executions) == 1
	})
	var firstTask, firstBatchState string
	var firstAssocOK bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue == nil || len(issue.Executions) != 1 || issue.Supplements == nil || len(issue.Supplements.Batches) != 1 {
			return
		}
		execution, batch := issue.Executions[0], issue.Supplements.Batches[0]
		firstTask = execution.TaskID
		firstBatchState = batch.State
		firstAssocOK = execution.TaskID != "" && batch.State == supplementBatchAccepted &&
			execution.SupplementBatchID == batch.ID && batch.ExecutionID == execution.ID
	})
	if !firstAssocOK {
		t.Fatalf("first accepted execution/batch = task %q state %q assoc=%v", firstTask, firstBatchState, firstAssocOK)
	}

	// B is received while A is in flight, at the old revision. The requirement
	// edit then changes the current target for the next safe boundary.
	var addErrB, editErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		addErrB = h.app.addIssueSupplement(ct, issueID, "supp B")
		if addErrB != nil {
			return
		}
		if !h.app.editIssueFor(ct, issueID, issue.Goal, "Current requirement changed") {
			editErr = errors.New(h.app.issueFormError)
		}
	})
	if addErrB != nil || editErr != nil {
		t.Fatalf("in-flight add/edit: %v / %v", addErrB, editErr)
	}
	if provider.count() != 1 {
		t.Fatalf("new supplement dispatched while A was active: provider=%d", provider.count())
	}
	var pendingOK bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		pendingOK = issue != nil && issue.RequirementsRevision == 2 &&
			issue.Supplements.pendingCount() == 1 &&
			issue.Supplements.Items[1].RequirementsRevision == 1
	})
	if !pendingOK {
		t.Fatal("in-flight supplement receipt/revision was not retained pending")
	}

	provider.release("supp A")
	select {
	case <-provider.arrived["supp B"]:
	case <-time.After(15 * time.Second):
		t.Fatal("second supplement did not dispatch after the first terminal")
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != "" && len(issue.Executions) == 2 &&
			issue.Executions[0].Status == issueExecutionCompleted &&
			(issue.Executions[1].Status == issueExecutionAccepted || issue.Executions[1].Status == issueExecutionRunning)
	})
	var secondOK bool
	var firstPrompt, secondPrompt string
	var secondContextCount, issueContextCount int
	var secondBatchTarget, secondReceiptRevision int
	var secondBatchState string
	var secondText string
	h.snapshot(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		if issue == nil || len(issue.Executions) != 2 || len(issue.Supplements.Batches) != 2 {
			return
		}
		first, second := issue.Executions[0], issue.Executions[1]
		batch1, batch2 := issue.Supplements.Batches[0], issue.Supplements.Batches[1]
		secondOK = first.TaskID == firstTask && first.Status == issueExecutionCompleted &&
			batch1.Target.RequirementsRevision == 1 && batch1.State == supplementBatchAccepted &&
			len(batch1.Snapshot) == 1 && batch1.Snapshot[0].RequirementsRevision == 1 &&
			batch2.Target.RequirementsRevision == 2 &&
			batch2.ExecutionID == second.ID && second.SupplementBatchID == batch2.ID &&
			second.RequirementsRevision == 2 && !strings.Contains(second.Prompt, "supp A") &&
			strings.Contains(second.Prompt, "supp B") &&
			strings.Contains(second.Prompt, "Current requirement changed")
		secondBatchState = batch2.State
		secondBatchTarget = batch2.Target.RequirementsRevision
		if len(batch2.Snapshot) == 1 {
			secondReceiptRevision = batch2.Snapshot[0].RequirementsRevision
			secondText = batch2.Snapshot[0].Text
		}
		firstPrompt, secondPrompt = first.Prompt, second.Prompt
		secondContextCount = len(second.Context)
		issueContextCount = len(issue.Context.snapshot())
	})
	if !secondOK || (secondBatchState != supplementBatchDispatching && secondBatchState != supplementBatchAccepted) ||
		secondBatchTarget != 2 || secondReceiptRevision != 1 || secondText != "supp B" ||
		secondContextCount != 0 || issueContextCount != 0 {
		t.Fatalf("second dispatch state = ok:%v batch:%s target:%d receipt:%d text:%q contexts:%d/%d",
			secondOK, secondBatchState, secondBatchTarget, secondReceiptRevision, secondText, secondContextCount, issueContextCount)
	}
	if firstPrompt == "" || secondPrompt == "" {
		t.Fatal("execution prompts were empty")
	}
	if !strings.Contains(provider.body(0), "supp A") || !strings.Contains(provider.body(0), "Issue context") {
		t.Fatalf("first provider body missing prompt/context: %s", provider.body(0))
	}
	if !strings.Contains(provider.body(1), "supp B") || !strings.Contains(provider.body(1), "Current requirement changed") {
		t.Fatalf("second provider body missing current target: %s", provider.body(1))
	}

	provider.release("supp B")
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 2 && issue.Executions[1].Status == issueExecutionCompleted &&
			issue.Supplements.Batches[1].State == supplementBatchAccepted
	})
	if provider.count() != 2 {
		t.Fatalf("supplement continuation produced %d provider requests, want 2", provider.count())
	}
	var finalOK bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		finalOK = issue != nil && issue.Supplements.pendingCount() == 0 &&
			issue.Supplements.Items[0].State == supplementStateSubmitted &&
			issue.Supplements.Items[1].State == supplementStateSubmitted &&
			issue.Supplements.Items[0].Text == "supp A" && issue.Supplements.Items[1].Text == "supp B" &&
			issue.Supplements.AutoContinue
	})
	if !finalOK {
		t.Fatal("final supplement receipts were not accepted without auto disable")
	}
}

func TestIssueSupplementDispatchSaveFailureStaysPendingAndOffline(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "issue-supplement-save-failure")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var issueID string
	var setupErr error
	h.pump.do(func() {
		if !h.app.createIssue("Save failure goal", "Keep this requirement") {
			setupErr = errors.New(h.app.issueFormError)
			return
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		if issue == nil || !h.app.setIssueSupplementAuto(ct, issueID, false) {
			setupErr = errors.New("could not disable auto for pending setup")
			return
		}
		if err := h.app.addIssueSupplement(ct, issueID, "saved pending text"); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	badParent := filepath.Join(h.root, "blocked-store-parent")
	if err := os.WriteFile(badParent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldStore := ""
	h.pump.do(func() {
		oldStore = h.app.store.path
		h.app.store.path = filepath.Join(badParent, "contacts.json")
	})
	var runOK bool
	var runErr string
	h.pump.do(func() {
		runOK = h.app.runIssue(issueID)
		runErr = h.app.issueFormError
	})
	if runOK || !strings.Contains(strings.ToLower(runErr), "save") {
		t.Fatalf("run with failing store = %v / %q", runOK, runErr)
	}
	if provider.count() != 0 {
		t.Fatalf("save failure produced %d provider requests", provider.count())
	}
	var pendingOK bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		pendingOK = issue != nil && len(issue.Executions) == 0 && issue.Work.ActiveTaskID == "" &&
			issue.Work.SendState == sendIdle && issue.Supplements != nil &&
			issue.Supplements.pendingCount() == 1 && len(issue.Supplements.Batches) == 0 &&
			issue.Supplements.Items[0].Text == "saved pending text" &&
			issue.Supplements.Items[0].State == supplementStatePending
	})
	if !pendingOK {
		t.Fatal("failed preparation did not leave the saved text pending and offline")
	}
	h.pump.do(func() { h.app.store.path = oldStore })
}
