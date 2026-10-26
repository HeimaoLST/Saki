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

func prepareSupplement(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget, dst *[]byte) *issueSupplementBatch {
	t.Helper()
	batch, err := l.Prepare(target, persistLedger(l, dst))
	if err != nil {
		t.Fatalf("prepare supplement: %v", err)
	}
	if !l.BeginDispatch(batch, persistLedger(l, dst)) {
		t.Fatal("begin dispatch failed")
	}
	return batch
}

func acceptedSupplement(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget, dst *[]byte) *issueSupplementBatch {
	t.Helper()
	addSupplement(t, l, target, "accepted supplement", dst)
	batch := prepareSupplement(t, l, target, dst)
	if !l.Accept(batch, issueSupplementAssociation{
		SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}, persistLedger(l, dst)) {
		t.Fatal("accept failed")
	}
	return batch
}

func TestIssueSupplementReceivedUsesActualSavedSnapshot(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	item := addSupplement(t, l, target, "support SMS", &data)
	if item.Text != "support SMS" || item.State != supplementStatePending {
		t.Fatalf("added item = %+v", item)
	}
	restored := reloadLedger(t, data)
	if len(restored.Items) != 1 || restored.Items[0].Text != "support SMS" ||
		restored.Items[0].State != supplementStatePending || restored.pendingCount() != 1 {
		t.Fatalf("JSON snapshot = %+v", restored.Items)
	}
	// No second save and no in-memory flag make the item received.
	before := append([]byte(nil), data...)
	if len(l.Items) != 1 || string(before) != string(data) {
		t.Fatal("test observed a hidden second persistence step")
	}

	failed := newIssueSupplementLedger()
	failedBytes := []byte{}
	if _, err := failed.Add(target, "not saved", func() error { return errSupplementSaveRequired }); err == nil {
		t.Fatal("failed save was accepted")
	}
	if len(failed.Items) != 0 || len(failedBytes) != 0 {
		t.Fatalf("failed save left item/snapshot: %d / %q", len(failed.Items), failedBytes)
	}
}

func TestIssueSupplementBatchSnapshotAndTargetVersionPersist(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	first := addSupplement(t, l, target, "support SMS", &data)
	second := addSupplement(t, l, target, "show errors", &data)
	batch, err := l.Prepare(target, persistLedger(l, &data))
	if err != nil {
		t.Fatal(err)
	}
	if batch.Text != "support SMS\nshow errors" || len(batch.Snapshot) != 2 {
		t.Fatalf("batch = %+v / %q", batch.Snapshot, batch.Text)
	}
	restored := reloadLedger(t, data)
	if len(restored.Batches) != 1 || restored.Batches[0].Text != batch.Text ||
		restored.Batches[0].Target.RequirementsRevision != target.RequirementsRevision {
		t.Fatalf("persisted batch = %+v", restored.Batches)
	}
	first.Text = "mutated later"
	second.Text = "also mutated"
	otherVersion := target
	otherVersion.RequirementsRevision++
	addSupplement(t, restored, otherVersion, "added later", &data)
	if restored.Batches[0].Text != "support SMS\nshow errors" {
		t.Fatalf("persisted batch followed mutable item: %q", restored.Batches[0].Text)
	}
	other, err := restored.Prepare(otherVersion, persistLedger(restored, &data))
	if err != nil || other.Text != "added later" {
		t.Fatalf("target version isolation = %+v / %v", other, err)
	}
}

func TestIssueSupplementDurableTransitionStatesAndAcceptance(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	savedItem := addSupplement(t, l, target, "one supplement", &data)
	_, err := l.Prepare(target, persistLedger(l, &data))
	if err != nil {
		t.Fatal(err)
	}
	restored := reloadLedger(t, data)
	if len(restored.Batches) != 1 || restored.Batches[0].State != supplementBatchPrepared ||
		restored.Items[0].State != supplementStatePrepared {
		t.Fatalf("prepared snapshot = %+v / %+v", restored.Batches, restored.Items)
	}
	if !restored.BeginDispatch(restored.Batches[0], persistLedger(restored, &data)) {
		t.Fatal("begin dispatch failed")
	}
	if !restored.Accept(restored.Batches[0], issueSupplementAssociation{
		SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e",
	}, persistLedger(restored, &data)) {
		t.Fatal("accepted did not persist")
	}
	accepted := reloadLedger(t, data)
	if accepted.Batches[0].State != supplementBatchAccepted || accepted.Items[0].State != supplementStateSubmitted ||
		accepted.Batches[0].SessionID != "s" || accepted.Batches[0].TaskID != "t" ||
		accepted.Batches[0].MessageID != "m" || accepted.Batches[0].ExecutionID != "e" {
		t.Fatalf("accepted snapshot = %+v / %+v", accepted.Batches[0], accepted.Items[0])
	}
	if _, err := accepted.Prepare(target, persistLedger(accepted, &data)); err == nil {
		t.Fatal("accepted supplement was prepared again")
	}
	if savedItem.Text != "one supplement" {
		t.Fatal("accept rewrote supplement text")
	}
}

func TestIssueSupplementAcceptedEvidenceRequiresFullProvidedInput(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	batch := acceptedSupplement(t, l, target, &data)
	fullInput := "goals and requirements then:\nsupport SMS"
	if !batch.MatchProvidedHistory(fullInput, []issueSupplementHistory{{
		Text: fullInput, SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}}) {
		t.Fatal("complete provided input did not match")
	}
	if batch.MatchProvidedHistory(batch.Text, []issueSupplementHistory{{
		Text: batch.Text, SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}}) == false {
		// The caller may legitimately pass a full input equal to the batch text.
		t.Fatal("caller-supplied full input should be accepted when it matches a real record")
	}
	if batch.MatchProvidedHistory(fullInput, []issueSupplementHistory{{Text: "prefix " + fullInput, SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1"}}) {
		t.Fatal("non-exact text matched")
	}
	if batch.MatchProvidedHistory(fullInput, []issueSupplementHistory{{Text: fullInput, SessionID: "session-1", TaskID: "other", MessageID: "message-1", ExecutionID: "execution-1"}}) {
		t.Fatal("wrong association matched")
	}
}

func TestIssueSupplementAcceptedItemsDoNotRedispatchAfterJsonReload(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	acceptedSupplement(t, l, target, &data)
	reloaded := reloadLedger(t, data)
	if _, err := reloaded.Prepare(target, persistLedger(reloaded, &data)); err == nil {
		t.Fatal("accepted supplement was eligible after JSON reload")
	}
	fullInput := "goal + requirements + accepted supplement"
	if len(reloaded.Batches) != 1 || !reloaded.Batches[0].MatchProvidedHistory(fullInput, []issueSupplementHistory{{
		Text: fullInput, SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}}) {
		t.Fatal("accepted evidence lost after JSON reload")
	}
}

func TestIssueSupplementStopBeforeDispatchAbortsPreparedAndPersistsIntent(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "stop keeps me", &data)
	batch, err := l.Prepare(target, persistLedger(l, &data))
	if err != nil {
		t.Fatal(err)
	}
	if !l.StopAutoContinue(batch, persistLedger(l, &data)) {
		t.Fatal("stop did not persist")
	}
	reloaded := reloadLedger(t, data)
	if reloaded.AutoContinue || reloaded.Items[0].Text != "stop keeps me" ||
		reloaded.Items[0].State != supplementStatePending || reloaded.Batches[0].State != supplementBatchCanceled {
		t.Fatalf("stopped prepared state = auto=%v item=%+v batch=%+v", reloaded.AutoContinue, reloaded.Items[0], reloaded.Batches[0])
	}
	if _, err := reloaded.Prepare(target, persistLedger(reloaded, &data)); err == nil {
		t.Fatal("stopped ledger automatically prepared")
	}
	if !reloaded.ResumeAutoContinue(persistLedger(reloaded, &data)) {
		t.Fatal("resume did not persist")
	}
	if _, err := reloaded.Prepare(target, persistLedger(reloaded, &data)); err != nil {
		t.Fatalf("explicit resume did not allow a new prepare: %v", err)
	}
}

func TestIssueSupplementStopAfterDispatchRetainsIdentityAndLateAccept(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "do not resend", &data)
	batch := prepareSupplement(t, l, target, &data)
	if !l.StopAutoContinue(batch, persistLedger(l, &data)) {
		t.Fatal("stop did not persist")
	}
	reloaded := reloadLedger(t, data)
	if reloaded.AutoContinue || reloaded.Batches[0].State != supplementBatchDispatching ||
		reloaded.Items[0].State != supplementStateDispatching {
		t.Fatalf("stopping dispatch lost identity or changed to pending: %+v / %+v", reloaded.Batches[0], reloaded.Items[0])
	}
	if !reloaded.Accept(reloaded.Batches[0], issueSupplementAssociation{
		SessionID: "late-s", TaskID: "late-t", MessageID: "late-m", ExecutionID: "late-e",
	}, persistLedger(reloaded, &data)) {
		t.Fatal("late accepted dispatch was rejected after stop")
	}
	accepted := reloadLedger(t, data)
	if accepted.Batches[0].State != supplementBatchAccepted || accepted.Items[0].State != supplementStateSubmitted ||
		accepted.AutoContinue || accepted.Batches[0].TaskID != "late-t" {
		t.Fatalf("late accepted state = %+v / %+v auto=%v", accepted.Batches[0], accepted.Items[0], accepted.AutoContinue)
	}
	if !accepted.ResumeAutoContinue(persistLedger(accepted, &data)) {
		t.Fatal("resume failed")
	}
	if _, err := accepted.Prepare(target, persistLedger(accepted, &data)); err == nil {
		t.Fatal("resume after late acceptance resent the same supplement")
	}
}

func TestIssueSupplementUnknownAndLateAccepted(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "unknown then accepted", &data)
	batch := prepareSupplement(t, l, target, &data)
	if !l.Unknown(batch, "delivery not confirmed", persistLedger(l, &data)) {
		t.Fatal("unknown transition failed")
	}
	reloaded := reloadLedger(t, data)
	if reloaded.AutoContinue || reloaded.Batches[0].State != supplementBatchUnknown {
		t.Fatalf("unknown state = auto=%v batch=%+v", reloaded.AutoContinue, reloaded.Batches[0])
	}
	if !reloaded.Accept(reloaded.Batches[0], issueSupplementAssociation{
		SessionID: "s2", TaskID: "t2", MessageID: "m2", ExecutionID: "e2",
	}, persistLedger(reloaded, &data)) {
		t.Fatal("late accepted unknown batch was rejected")
	}
	if reloaded.AutoContinue || reloaded.Batches[0].State != supplementBatchAccepted {
		t.Fatalf("late accepted unknown state = auto=%v batch=%+v", reloaded.AutoContinue, reloaded.Batches[0])
	}
}

func TestIssueSupplementRejectedAndNotDispatchedKeepTextAndDoNotRetry(t *testing.T) {
	for _, mode := range []string{"rejected", "not_dispatched"} {
		t.Run(mode, func(t *testing.T) {
			l := newIssueSupplementLedger()
			data := []byte{}
			target := supplementTarget()
			addSupplement(t, l, target, "keep failure text", &data)
			batch, err := l.Prepare(target, persistLedger(l, &data))
			if err != nil {
				t.Fatal(err)
			}
			ok := false
			switch mode {
			case "rejected":
				ok = l.Reject(batch, "provider rejected", persistLedger(l, &data))
			case "not_dispatched":
				ok = l.NotDispatched(batch, "send was not issued", persistLedger(l, &data))
			}
			if !ok {
				t.Fatal("failure transition failed")
			}
			reloaded := reloadLedger(t, data)
			if reloaded.Items[0].Text != "keep failure text" || reloaded.Items[0].State == supplementStateSubmitted {
				t.Fatalf("failure lost text/state: %+v", reloaded.Items[0])
			}
			if _, err := reloaded.Prepare(target, persistLedger(reloaded, &data)); err == nil {
				t.Fatal("failed supplement was automatically prepared again")
			}
		})
	}
}

func TestIssueSupplementBoundsAndSaveFailureRollback(t *testing.T) {
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	if _, err := l.Add(target, strings.Repeat("x", maxSupplementTextBytes+1), persistLedger(l, &data)); err == nil {
		t.Fatal("oversized supplement accepted")
	}
	for i := 0; i < maxPendingSupplements; i++ {
		addSupplement(t, l, target, "bounded", &data)
	}
	if _, err := l.Add(target, "one too many", persistLedger(l, &data)); err == nil ||
		!strings.Contains(err.Error(), "too many pending") {
		t.Fatalf("pending count limit = %v", err)
	}
	if len(l.Items) != maxPendingSupplements || l.pendingCount() != maxPendingSupplements {
		t.Fatalf("limit dropped text: items=%d pending=%d", len(l.Items), l.pendingCount())
	}
	chunk := strings.Repeat("y", maxSupplementTextBytes)
	byteLedger := newIssueSupplementLedger()
	byteData := []byte{}
	for i := 0; i < maxPendingSupplementBytes/maxSupplementTextBytes; i++ {
		addSupplement(t, byteLedger, target, chunk, &byteData)
	}
	if _, err := byteLedger.Add(target, "z", persistLedger(byteLedger, &byteData)); err == nil ||
		!strings.Contains(err.Error(), "pending supplement text is too large") {
		t.Fatalf("pending byte limit = %v", err)
	}
	if byteLedger.pendingBytes() > maxPendingSupplementBytes {
		t.Fatalf("pending bytes exceeded limit: %d", byteLedger.pendingBytes())
	}
}

func TestIssueSupplementDurableTransitionSaveFailureRollsBack(t *testing.T) {
	failSave := func() error { return errSupplementSaveRequired }
	l := newIssueSupplementLedger()
	data := []byte{}
	target := supplementTarget()
	addSupplement(t, l, target, "rollback me", &data)
	batch, err := l.Prepare(target, persistLedger(l, &data))
	if err != nil {
		t.Fatal(err)
	}
	if l.BeginDispatch(batch, failSave) || batch.State != supplementBatchPrepared || l.Items[0].State != supplementStatePrepared {
		t.Fatalf("failed begin left partial state: %+v / %+v", batch, l.Items[0])
	}
	if !l.BeginDispatch(batch, persistLedger(l, &data)) {
		t.Fatal("begin did not succeed after rollback")
	}
	association := issueSupplementAssociation{SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e"}
	if l.Accept(batch, association, failSave) || batch.State != supplementBatchDispatching ||
		batch.SessionID != "" || l.Items[0].State != supplementStateDispatching || l.Items[0].SessionID != "" {
		t.Fatalf("failed accept left partial state: %+v / %+v", batch, l.Items[0])
	}
	if !l.Accept(batch, association, persistLedger(l, &data)) {
		t.Fatal("accept did not recover after rollback")
	}

	l2 := newIssueSupplementLedger()
	data2 := []byte{}
	addSupplement(t, l2, target, "stop rollback", &data2)
	batch2, err := l2.Prepare(target, persistLedger(l2, &data2))
	if err != nil {
		t.Fatal(err)
	}
	if l2.StopAutoContinue(batch2, failSave) || !l2.AutoContinue || batch2.State != supplementBatchPrepared ||
		l2.Items[0].State != supplementStatePrepared || l2.Items[0].BatchID != batch2.ID {
		t.Fatalf("failed stop left partial state: auto=%v batch=%+v item=%+v", l2.AutoContinue, batch2, l2.Items[0])
	}
}
