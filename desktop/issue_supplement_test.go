package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func supplementTarget() issueSupplementTarget {
	return issueSupplementTarget{ContactID: "contact-x", IssueID: "issue-a", RequirementsRevision: 3}
}

func persistLedger(l *issueSupplementLedger, dst *[]byte) func() error {
	return func() error {
		data, err := json.Marshal(l)
		if err != nil {
			return err
		}
		*dst = append((*dst)[:0], data...)
		return nil
	}
}

func reloadLedger(t *testing.T, data []byte) *issueSupplementLedger {
	t.Helper()
	var l issueSupplementLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	return &l
}

func addSupplement(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget, text string, dst *[]byte) *issueSupplement {
	t.Helper()
	item, err := l.Add(target, text, persistLedger(l, dst))
	if err != nil {
		t.Fatalf("add supplement: %v", err)
	}
	return item
}

func dispatchSupplement(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget, dst *[]byte) (*issueSupplementBatch, *issueExecution) {
	t.Helper()
	execution := &issueExecution{ID: "execution-" + newIssueSupplementID()}
	batch, err := l.PrepareDispatch(target, execution, persistLedger(l, dst))
	if err != nil {
		t.Fatalf("dispatch supplement: %v", err)
	}
	return batch, execution
}

func TestIssueSupplementReceivedUsesSavedSnapshot(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	item := addSupplement(t, l, target, "support SMS", &data)
	if item.Text != "support SMS" || item.State != supplementStatePending {
		t.Fatalf("added item = %+v", item)
	}
	restored := reloadLedger(t, data)
	if len(restored.Items) != 1 || restored.Items[0].Text != "support SMS" || restored.pendingCount() != 1 {
		t.Fatalf("JSON receipt = %+v", restored.Items)
	}
	failed := newIssueSupplementLedger()
	if _, err := failed.Add(target, "not saved", func() error { return errSupplementSaveRequired }); err == nil || len(failed.Items) != 0 {
		t.Fatalf("failed Add = err %v items %+v", err, failed.Items)
	}
}

func TestIssueSupplementPrepareDispatchIsOneAtomicSnapshot(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	addSupplement(t, l, supplementTarget(), "atomic", &data)
	batch, execution := dispatchSupplement(t, l, supplementTarget(), &data)
	if batch.State != supplementBatchDispatching || batch.ExecutionID != execution.ID ||
		execution.SupplementBatchID != batch.ID || len(batch.Snapshot) != 1 ||
		batch.Snapshot[0].RequirementsRevision != supplementTarget().RequirementsRevision {
		t.Fatalf("dispatch = %+v / %+v", batch, execution)
	}
	restored := reloadLedger(t, data)
	if restored.Batches[0].State != supplementBatchDispatching || restored.Batches[0].ExecutionID != execution.ID {
		t.Fatalf("persisted dispatch = %+v", restored.Batches)
	}
	failed := newIssueSupplementLedger()
	failedData := []byte{}
	addSupplement(t, failed, supplementTarget(), "rollback", &failedData)
	badExecution := &issueExecution{ID: "execution-failed"}
	if batch, err := failed.PrepareDispatch(supplementTarget(), badExecution, func() error { return errSupplementSaveRequired }); err == nil || batch != nil ||
		badExecution.SupplementBatchID != "" || len(failed.Batches) != 0 || failed.pendingCount() != 1 {
		t.Fatalf("failed dispatch = %+v / %+v / %+v / %v", batch, badExecution, failed.Batches, err)
	}
}

func TestIssueSupplementPendingUsesCurrentRevisionWithoutRelabeling(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	received := supplementTarget()
	first := addSupplement(t, l, received, "first", &data)
	second := addSupplement(t, l, received, "second", &data)
	current := received
	current.RequirementsRevision++
	batch, _ := dispatchSupplement(t, l, current, &data)
	if batch.Target != current || batch.Text != "first\nsecond" || len(batch.Snapshot) != 2 ||
		first.RequirementsRevision != received.RequirementsRevision || second.RequirementsRevision != received.RequirementsRevision {
		t.Fatalf("current batch = %+v / receipts %+v / %+v", batch, first, second)
	}
	restored := reloadLedger(t, data)
	if restored.Batches[0].Snapshot[0].RequirementsRevision != received.RequirementsRevision ||
		restored.Batches[0].Snapshot[1].RequirementsRevision != received.RequirementsRevision {
		t.Fatalf("receipt revisions changed in JSON = %+v", restored.Batches[0].Snapshot)
	}

	future := newIssueSupplementLedger()
	futureData := []byte{}
	futureTarget := received
	futureTarget.RequirementsRevision += 2
	addSupplement(t, future, futureTarget, "future", &futureData)
	if _, err := future.PrepareDispatch(received, &issueExecution{ID: "too-early"}, persistLedger(future, &futureData)); err == nil {
		t.Fatal("future receipt was dispatched early")
	}
	futureBatch, _ := dispatchSupplement(t, future, futureTarget, &futureData)
	if futureBatch.Target.RequirementsRevision != futureTarget.RequirementsRevision {
		t.Fatalf("future batch target = %+v", futureBatch.Target)
	}

	other := newIssueSupplementLedger()
	otherData := []byte{}
	targetB := received
	targetB.IssueID = "issue-b"
	addSupplement(t, other, targetB, "only b", &otherData)
	if _, err := other.PrepareDispatch(received, &issueExecution{ID: "wrong-issue"}, persistLedger(other, &otherData)); err == nil {
		t.Fatal("supplement crossed issue owner")
	}
}

func TestIssueSupplementActiveBatchBlocksAcrossRevision(t *testing.T) {
	for _, state := range []string{"dispatching", "unknown"} {
		t.Run(state, func(t *testing.T) {
			l := newIssueSupplementLedger()
			data := []byte{}
			target := supplementTarget()
			addSupplement(t, l, target, "in flight", &data)
			batch, _ := dispatchSupplement(t, l, target, &data)
			if state == "unknown" && !l.Unknown(batch, "unknown", persistLedger(l, &data)) {
				t.Fatal("unknown transition failed")
			}
			current := target
			current.RequirementsRevision++
			if _, err := l.PrepareDispatch(current, &issueExecution{ID: "blocked"}, persistLedger(l, &data)); err == nil ||
				!strings.Contains(err.Error(), "already") {
				t.Fatalf("active batch did not block: %v", err)
			}
			restored := reloadLedger(t, data)
			if restored.Batches[0].State != batch.State {
				t.Fatalf("persisted active state = %+v", restored.Batches[0])
			}
		})
	}
}

func TestIssueSupplementAcceptStopAndLateReceipt(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "do not resend", &data)
	batch, execution := dispatchSupplement(t, l, target, &data)
	if !l.StopAutoContinue(batch, persistLedger(l, &data)) || l.AutoContinue {
		t.Fatal("stop did not persist")
	}
	restored := reloadLedger(t, data)
	if restored.Batches[0].State != supplementBatchDispatching || restored.Items[0].State != supplementStateDispatching {
		t.Fatalf("stop rewrote in-flight batch = %+v / %+v", restored.Batches[0], restored.Items[0])
	}
	if accepted, err := restored.Accept(restored.Batches[0], issueSupplementAssociation{
		SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: execution.ID,
	}, persistLedger(restored, &data)); !accepted || err != nil {
		t.Fatalf("late accepted = %v / %v", accepted, err)
	}
	accepted := reloadLedger(t, data)
	if accepted.Batches[0].State != supplementBatchAccepted || accepted.Items[0].State != supplementStateSubmitted ||
		accepted.Batches[0].TaskID != "t" || accepted.AutoContinue {
		t.Fatalf("accepted receipt = %+v / %+v auto=%v", accepted.Batches[0], accepted.Items[0], accepted.AutoContinue)
	}
}

func TestIssueSupplementUnknownLateAcceptAndMatch(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "unknown text", &data)
	batch, execution := dispatchSupplement(t, l, target, &data)
	if !l.Unknown(batch, "delivery unknown", persistLedger(l, &data)) || l.AutoContinue {
		t.Fatal("unknown did not stop auto")
	}
	if accepted, err := l.Accept(batch, issueSupplementAssociation{
		SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: execution.ID,
	}, persistLedger(l, &data)); !accepted || err != nil {
		t.Fatalf("unknown late accept = %v / %v", accepted, err)
	}
	full := "goal and requirements\nunknown text"
	if !batch.MatchProvidedHistory(full, []issueSupplementHistory{{
		Text: full, SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: execution.ID,
	}}) {
		t.Fatal("full provided input did not match")
	}
}

func TestIssueSupplementRejectedAndNotDispatchedRetryExplicitly(t *testing.T) {
	for _, mode := range []string{"rejected", "not_dispatched"} {
		t.Run(mode, func(t *testing.T) {
			l := newIssueSupplementLedger()
			data := []byte{}
			target := supplementTarget()
			addSupplement(t, l, target, "keep failure text", &data)
			batch, _ := dispatchSupplement(t, l, target, &data)
			ok := false
			if mode == "rejected" {
				ok = l.Reject(batch, "rejected", persistLedger(l, &data))
			} else {
				ok = l.NotDispatched(batch, "not dispatched", persistLedger(l, &data))
			}
			if !ok || l.pendingCount() != 0 {
				t.Fatalf("%s transition = %v pending=%d", mode, ok, l.pendingCount())
			}
			if !l.RetryUnaccepted(batch, persistLedger(l, &data)) || l.pendingCount() != 1 ||
				l.Items[0].Text != "keep failure text" {
				t.Fatalf("%s explicit retry = %+v", mode, l.Items)
			}
			restored := reloadLedger(t, data)
			if restored.Items[0].State != supplementStatePending || restored.Batches[0].State != supplementBatchCanceled {
				t.Fatalf("retry persisted = %+v / %+v", restored.Batches[0], restored.Items[0])
			}
			next, _ := dispatchSupplement(t, restored, target, &data)
			if next.Text != "keep failure text" {
				t.Fatalf("retry did not resend original text: %q", next.Text)
			}
			if restored.RetryUnaccepted(next, persistLedger(restored, &data)) {
				t.Fatal("dispatching batch was retryable")
			}
		})
	}
}

func TestIssueSupplementDeferDispatchPreservesAutoAndPending(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "wait for capacity", &data)
	batch, _ := dispatchSupplement(t, l, target, &data)
	if !l.DeferDispatch(batch, persistLedger(l, &data)) || !l.AutoContinue ||
		l.pendingCount() != 1 || batch.State != supplementBatchCanceled {
		t.Fatalf("defer dispatch = %+v auto=%v", batch, l.AutoContinue)
	}
	restored := reloadLedger(t, data)
	if !restored.AutoContinue || restored.Items[0].State != supplementStatePending {
		t.Fatalf("deferred restart = %+v auto=%v", restored.Items[0], restored.AutoContinue)
	}
}

func TestIssueSupplementBoundsAndSaveFailure(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	if _, err := l.Add(target, strings.Repeat("x", maxSupplementTextBytes+1), persistLedger(l, &data)); err == nil {
		t.Fatal("oversized supplement accepted")
	}
	for i := 0; i < maxPendingSupplements; i++ {
		addSupplement(t, l, target, "bounded", &data)
	}
	if _, err := l.Add(target, "over", persistLedger(l, &data)); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("count bound = %v", err)
	}
}
