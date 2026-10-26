package main

import (
	"strings"
	"testing"
)

func supplementTarget() issueSupplementTarget {
	return issueSupplementTarget{ContactID: "contact-x", IssueID: "issue-a", RequirementsRevision: 3}
}

func saveOK(calls *int) func() error {
	return func() error {
		*calls++
		return nil
	}
}

func addSupplement(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget, text string) *issueSupplement {
	t.Helper()
	calls := 0
	item, err := l.Add(target, text, saveOK(&calls))
	if err != nil {
		t.Fatalf("add supplement: %v", err)
	}
	if calls != 1 || !item.Stored || item.State != supplementStatePending {
		t.Fatalf("add persistence facts = %+v calls=%d", item, calls)
	}
	return item
}

func acceptedBatch(t *testing.T, l *issueSupplementLedger, target issueSupplementTarget) *issueSupplementBatch {
	t.Helper()
	batch, err := l.Prepare(target)
	if err != nil {
		t.Fatal(err)
	}
	if !l.MarkBatchStored(batch) {
		t.Fatal("batch did not store")
	}
	if !l.Accept(batch, issueSupplementAssociation{
		SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}) {
		t.Fatal("batch did not accept a complete association")
	}
	return batch
}

func TestIssueSupplementReceivedRequiresSaveAndFailedSaveLeavesNothing(t *testing.T) {
	l := newIssueSupplementLedger()
	if _, err := l.Add(supplementTarget(), "keep me", nil); err != errSupplementSaveRequired {
		t.Fatalf("nil save error = %v", err)
	}
	if len(l.Items) != 0 {
		t.Fatal("nil save created an in-memory received supplement")
	}
	calls := 0
	_, err := l.Add(supplementTarget(), "keep me", func() error {
		calls++
		return errSupplementSaveRequired
	})
	if err == nil || calls != 1 || len(l.Items) != 0 {
		t.Fatalf("failed save left supplement: calls=%d items=%d err=%v", calls, len(l.Items), err)
	}
}

func TestIssueSupplementBatchIsImmutableAcrossEditsAdditionsAndTargetVersions(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	first := addSupplement(t, l, target, "support SMS")
	second := addSupplement(t, l, target, "show errors")
	batch, err := l.Prepare(target)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Text != "support SMS\nshow errors" || len(batch.Snapshot) != 2 {
		t.Fatalf("batch snapshot = %+v / %q", batch.Snapshot, batch.Text)
	}
	first.Text = "mutated later"
	second.Text = "also mutated"
	otherVersion := target
	otherVersion.RequirementsRevision++
	addSupplement(t, l, otherVersion, "added later")
	if batch.Text != "support SMS\nshow errors" || batch.Snapshot[0].Text != "support SMS" || batch.Snapshot[1].Text != "show errors" {
		t.Fatalf("batch followed mutable supplements: %+v / %q", batch.Snapshot, batch.Text)
	}
	other, err := l.Prepare(otherVersion)
	if err != nil {
		t.Fatal(err)
	}
	if other.Text != "added later" || other.Target.RequirementsRevision != target.RequirementsRevision+1 {
		t.Fatalf("target version isolation = %+v", other)
	}
	if !l.MarkBatchStored(batch) || !l.Accept(batch, issueSupplementAssociation{
		SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e",
	}) {
		t.Fatal("original batch did not accept")
	}
	if batch.Text != "support SMS\nshow errors" {
		t.Fatalf("accept mutated batch text: %q", batch.Text)
	}
}

func TestIssueSupplementAcceptedAssociationAndHistoryMatch(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	addSupplement(t, l, target, "support SMS")
	batch, err := l.Prepare(target)
	if err != nil {
		t.Fatal(err)
	}
	if l.Accept(batch, issueSupplementAssociation{SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e"}) {
		t.Fatal("accepted an unsaved batch")
	}
	if !l.MarkBatchStored(batch) {
		t.Fatal("batch did not store")
	}
	if l.Accept(batch, issueSupplementAssociation{SessionID: "s", TaskID: "t", MessageID: "m"}) {
		t.Fatal("accepted without execution id")
	}
	if !l.Accept(batch, issueSupplementAssociation{SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e"}) {
		t.Fatal("complete association was rejected")
	}
	if batch.State != supplementBatchAccepted || !batch.MatchHistory([]issueSupplementHistory{{
		Text: batch.Text, SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e",
	}}) {
		t.Fatal("accepted batch did not match the exact history record")
	}
	if batch.MatchHistory([]issueSupplementHistory{{Text: "support", SessionID: "s", TaskID: "t", MessageID: "m", ExecutionID: "e"}}) {
		t.Fatal("partial history text matched")
	}
	if batch.MatchHistory([]issueSupplementHistory{{Text: batch.Text, SessionID: "s", TaskID: "wrong", MessageID: "m", ExecutionID: "e"}}) {
		t.Fatal("wrong task association matched")
	}
	// Accepted items are not pending and cannot be prepared or submitted twice.
	if _, err := l.Prepare(target); err == nil {
		t.Fatal("accepted supplement was prepared again")
	}
	if l.Accept(batch, issueSupplementAssociation{SessionID: "s2", TaskID: "t2", MessageID: "m2", ExecutionID: "e2"}) {
		t.Fatal("same batch accepted twice")
	}
}

func TestIssueSupplementAcceptedItemsDoNotRedispatchAfterReload(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	item := addSupplement(t, l, target, "one supplement")
	batch := acceptedBatch(t, l, target)
	reloaded := &issueSupplementLedger{Items: l.Items, AutoContinue: true}
	if reloaded.Items[0] != item {
		t.Fatal("test reload lost item identity")
	}
	if _, err := reloaded.Prepare(target); err == nil {
		t.Fatal("accepted supplement was eligible after reload")
	}
	if !batch.MatchHistory([]issueSupplementHistory{{
		Text: batch.Text, SessionID: "session-1", TaskID: "task-1", MessageID: "message-1", ExecutionID: "execution-1",
	}}) {
		t.Fatal("accepted item lost its exact history after reload")
	}
}

func TestIssueSupplementUnknownRejectionAndStopDoNotLoseText(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	addSupplement(t, l, target, "keep text")
	batch, err := l.Prepare(target)
	if err != nil {
		t.Fatal(err)
	}
	if !l.MarkBatchStored(batch) || !l.Unknown(batch, "delivery not confirmed") {
		t.Fatal("unknown transition failed")
	}
	if l.AutoContinue || l.Items[0].State != supplementStateUnknown || l.Items[0].Text != "keep text" {
		t.Fatalf("unknown state lost intent/text: %+v auto=%v", l.Items[0], l.AutoContinue)
	}
	if _, err := l.Prepare(target); err == nil {
		t.Fatal("unknown supplement was auto-prepared")
	}
	l.ResumeAutoContinue()
	if !l.AutoContinue {
		t.Fatal("resume did not restore automatic continuation")
	}
	// Unknown is not automatically retried; it remains a record for the user.
	if _, err := l.Prepare(target); err == nil {
		t.Fatal("unknown supplement was re-prepared after resume")
	}

	l2 := newIssueSupplementLedger()
	batch2, err := l2.Prepare(target)
	if err == nil || batch2 != nil {
		t.Fatal("empty ledger prepared")
	}
	addSupplement(t, l2, target, "stop keeps me")
	batch2, err = l2.Prepare(target)
	if err != nil {
		t.Fatal(err)
	}
	l2.StopAutoContinue(batch2)
	if l2.AutoContinue || l2.Items[0].Text != "stop keeps me" || l2.Items[0].State != supplementStatePending {
		t.Fatalf("stop lost pending text: %+v auto=%v", l2.Items[0], l2.AutoContinue)
	}
	if _, err := l2.Prepare(target); err == nil {
		t.Fatal("stop still allowed automatic preparation")
	}
	l2.ResumeAutoContinue()
	if _, err := l2.Prepare(target); err != nil {
		t.Fatalf("explicit resume did not restore preparation: %v", err)
	}
}

func TestIssueSupplementLimitsAreVisibleAndDoNotDropContent(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	if _, err := l.Add(target, strings.Repeat("x", maxSupplementTextBytes+1), saveOK(new(int))); err == nil {
		t.Fatal("oversized supplement was accepted")
	}
	for i := 0; i < maxPendingSupplements; i++ {
		addSupplement(t, l, target, "bounded")
	}
	if _, err := l.Add(target, "one too many", saveOK(new(int))); err == nil ||
		!strings.Contains(err.Error(), "too many pending") {
		t.Fatalf("pending count limit = %v", err)
	}
	if len(l.Items) != maxPendingSupplements || l.pendingCount() != maxPendingSupplements {
		t.Fatalf("limit dropped text: items=%d pending=%d", len(l.Items), l.pendingCount())
	}
}

func TestIssueSupplementRejectedAndNotDispatchedKeepText(t *testing.T) {
	for _, mode := range []string{"rejected", "not_dispatched"} {
		t.Run(mode, func(t *testing.T) {
			l := newIssueSupplementLedger()
			target := supplementTarget()
			addSupplement(t, l, target, "keep rejected text")
			batch, err := l.Prepare(target)
			if err != nil {
				t.Fatal(err)
			}
			if !l.MarkBatchStored(batch) {
				t.Fatal("batch did not store")
			}
			switch mode {
			case "rejected":
				if !l.Reject(batch, "provider rejected") {
					t.Fatal("reject failed")
				}
			case "not_dispatched":
				if !l.NotDispatched(batch, "send was not issued") {
					t.Fatal("not-dispatched failed")
				}
			}
			if l.Items[0].Text != "keep rejected text" || l.Items[0].State == supplementStateSubmitted {
				t.Fatalf("failure lost text/state: %+v", l.Items[0])
			}
			if _, err := l.Prepare(target); err == nil {
				t.Fatal("failed batch was automatically prepared again")
			}
		})
	}
}

func TestIssueSupplementPendingByteLimitIsVisible(t *testing.T) {
	l := newIssueSupplementLedger()
	target := supplementTarget()
	chunk := strings.Repeat("x", maxSupplementTextBytes)
	for i := 0; i < maxPendingSupplementBytes/maxSupplementTextBytes; i++ {
		addSupplement(t, l, target, chunk)
	}
	if _, err := l.Add(target, "x", saveOK(new(int))); err == nil ||
		!strings.Contains(err.Error(), "pending supplement text is too large") {
		t.Fatalf("pending byte limit = %v", err)
	}
	if l.pendingBytes() > maxPendingSupplementBytes {
		t.Fatalf("pending bytes exceeded limit: %d", l.pendingBytes())
	}
}
