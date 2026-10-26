package main

// Issue supplement domain model. This file is intentionally independent of the
// GUI store and Runtime client so the app can attach it to productIssue later
// and persist it in the same atomic save as the issue. It does not dispatch
// tasks or keep a second execution loop.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxPendingSupplements     = 32
	maxSupplementTextBytes    = 16 * 1024
	maxPendingSupplementBytes = 64 * 1024
)

const (
	supplementStatePending       = "pending"
	supplementStatePrepared      = "prepared"
	supplementStateSubmitted     = "submitted"
	supplementStateRejected      = "rejected"
	supplementStateNotDispatched = "not_dispatched"
	supplementStateUnknown       = "unknown"

	supplementBatchPrepared      = "prepared"
	supplementBatchStored        = "stored"
	supplementBatchAccepted      = "accepted"
	supplementBatchRejected      = "rejected"
	supplementBatchNotDispatched = "not_dispatched"
	supplementBatchUnknown       = "unknown"
	supplementBatchCanceled      = "canceled"
)

var (
	errSupplementSaveRequired = errors.New("supplement save is required before it can be received")
	errSupplementNoPending    = errors.New("no pending supplements for this issue")
)

type issueSupplementTarget struct {
	ContactID            string
	IssueID              string
	RequirementsRevision int
}

type issueSupplement struct {
	ID                   string
	ContactID            string
	IssueID              string
	Text                 string
	RequirementsRevision int
	CreatedAt            int64
	State                string
	Stored               bool
	BatchID              string
	Detail               string
	SessionID            string
	TaskID               string
	MessageID            string
	ExecutionID          string
}

type issueSupplementSnapshot struct {
	ID   string
	Text string
}

type issueSupplementBatch struct {
	ID          string
	Target      issueSupplementTarget
	Snapshot    []issueSupplementSnapshot
	Text        string
	CreatedAt   int64
	State       string
	Stored      bool
	SessionID   string
	TaskID      string
	MessageID   string
	ExecutionID string
}

type issueSupplementAssociation struct {
	SessionID   string
	TaskID      string
	MessageID   string
	ExecutionID string
}

// issueSupplementHistory is the exact input record the Runtime actually
// accepted. Matching requires the complete immutable batch text plus all
// association identities; it does not claim model adoption.
type issueSupplementHistory struct {
	Text        string
	SessionID   string
	TaskID      string
	MessageID   string
	ExecutionID string
}

type issueSupplementLedger struct {
	Items        []*issueSupplement
	AutoContinue bool
}

func newIssueSupplementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "supplement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("supplement-%d", time.Now().UnixNano())
}

func newIssueSupplementLedger() *issueSupplementLedger {
	return &issueSupplementLedger{AutoContinue: true}
}

func (l *issueSupplementLedger) pendingBytes() int {
	total := 0
	for _, item := range l.Items {
		if item != nil && (item.State == supplementStatePending || item.State == supplementStatePrepared) {
			total += len(item.Text)
		}
	}
	return total
}

func (l *issueSupplementLedger) pendingCount() int {
	count := 0
	for _, item := range l.Items {
		if item != nil && (item.State == supplementStatePending || item.State == supplementStatePrepared) {
			count++
		}
	}
	return count
}

// Add receives a supplement only after save succeeds. The callback is the
// app's existing one-shot store save; a nil callback is rejected rather than
// calling in-memory data received.
func (l *issueSupplementLedger) Add(target issueSupplementTarget, text string, save func() error) (*issueSupplement, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, errSupplementSaveRequired
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("supplement text is required")
	}
	if len([]byte(text)) > maxSupplementTextBytes {
		return nil, fmt.Errorf("supplement is too large: %d bytes (max %d)", len([]byte(text)), maxSupplementTextBytes)
	}
	if l.pendingCount() >= maxPendingSupplements {
		return nil, fmt.Errorf("too many pending supplements for this issue (max %d)", maxPendingSupplements)
	}
	if l.pendingBytes()+len([]byte(text)) > maxPendingSupplementBytes {
		return nil, fmt.Errorf("pending supplement text is too large (max %d bytes)", maxPendingSupplementBytes)
	}
	item := &issueSupplement{
		ID: newIssueSupplementID(), ContactID: target.ContactID, IssueID: target.IssueID,
		Text: text, RequirementsRevision: target.RequirementsRevision,
		CreatedAt: nowMillis(), State: supplementStatePending,
	}
	l.Items = append(l.Items, item)
	if err := save(); err != nil {
		l.Items = l.Items[:len(l.Items)-1]
		return nil, fmt.Errorf("could not save supplement: %w", err)
	}
	item.Stored = true
	return item, nil
}

func validateSupplementTarget(target issueSupplementTarget) error {
	if strings.TrimSpace(target.ContactID) == "" || strings.TrimSpace(target.IssueID) == "" {
		return errors.New("supplement target must include contact and issue")
	}
	if target.RequirementsRevision < 0 {
		return errors.New("supplement target revision must not be negative")
	}
	return nil
}

func supplementTargetsEqual(a, b issueSupplementTarget) bool {
	return a.ContactID == b.ContactID && a.IssueID == b.IssueID && a.RequirementsRevision == b.RequirementsRevision
}

func (l *issueSupplementLedger) pendingFor(target issueSupplementTarget) []*issueSupplement {
	var out []*issueSupplement
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending && item.Stored &&
			item.ContactID == target.ContactID && item.IssueID == target.IssueID &&
			item.RequirementsRevision == target.RequirementsRevision {
			out = append(out, item)
		}
	}
	return out
}

// Prepare creates an immutable batch from currently pending, already-saved
// supplements. It captures the exact target and requirements revision and does
// not later follow edits/additions to the original items.
func (l *issueSupplementLedger) Prepare(target issueSupplementTarget) (*issueSupplementBatch, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if !l.AutoContinue {
		return nil, errors.New("automatic supplement continuation is stopped")
	}
	items := l.pendingFor(target)
	if len(items) == 0 {
		return nil, errSupplementNoPending
	}
	batch := &issueSupplementBatch{
		ID: newIssueSupplementID(), Target: target, CreatedAt: nowMillis(),
		State: supplementBatchPrepared,
	}
	var texts []string
	for _, item := range items {
		batch.Snapshot = append(batch.Snapshot, issueSupplementSnapshot{ID: item.ID, Text: item.Text})
		texts = append(texts, item.Text)
		item.State = supplementStatePrepared
		item.BatchID = batch.ID
	}
	batch.Text = strings.Join(texts, "\n")
	return batch, nil
}

// MarkBatchStored must be called after the one-shot store save of the batch.
// Until then Accept rejects the batch.
func (l *issueSupplementLedger) MarkBatchStored(batch *issueSupplementBatch) bool {
	if batch == nil || batch.State != supplementBatchPrepared {
		return false
	}
	batch.Stored = true
	batch.State = supplementBatchStored
	return true
}

// AbortPrepared returns a not-yet-submitted batch to its pending items without
// deleting their text. It is for save/UI failure before dispatch.
func (l *issueSupplementLedger) AbortPrepared(batch *issueSupplementBatch) bool {
	if batch == nil || batch.State != supplementBatchPrepared {
		return false
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil && item.State == supplementStatePrepared && item.BatchID == batch.ID {
			item.State = supplementStatePending
			item.BatchID = ""
		}
	}
	batch.State = supplementBatchCanceled
	return true
}

// Accept records a submitted batch only when Runtime returned a real accepted
// task with all four association identities. It says nothing about adoption.
func (l *issueSupplementLedger) Accept(batch *issueSupplementBatch, association issueSupplementAssociation) bool {
	if batch == nil || batch.State != supplementBatchStored || !batch.Stored || !l.AutoContinue {
		return false
	}
	if strings.TrimSpace(association.SessionID) == "" || strings.TrimSpace(association.TaskID) == "" ||
		strings.TrimSpace(association.MessageID) == "" || strings.TrimSpace(association.ExecutionID) == "" {
		return false
	}
	batch.State = supplementBatchAccepted
	batch.SessionID, batch.TaskID, batch.MessageID, batch.ExecutionID = association.SessionID, association.TaskID, association.MessageID, association.ExecutionID
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateSubmitted
			item.SessionID, item.TaskID, item.MessageID, item.ExecutionID = batch.SessionID, batch.TaskID, batch.MessageID, batch.ExecutionID
		}
	}
	return true
}

func (l *issueSupplementLedger) Reject(batch *issueSupplementBatch, detail string) bool {
	if batch == nil || (batch.State != supplementBatchPrepared && batch.State != supplementBatchStored) {
		return false
	}
	batch.State = supplementBatchRejected
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateRejected
			item.Detail = detail
		}
	}
	return true
}

func (l *issueSupplementLedger) NotDispatched(batch *issueSupplementBatch, detail string) bool {
	if batch == nil || (batch.State != supplementBatchPrepared && batch.State != supplementBatchStored) {
		return false
	}
	batch.State = supplementBatchNotDispatched
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateNotDispatched
			item.Detail = detail
		}
	}
	return true
}

func (l *issueSupplementLedger) Unknown(batch *issueSupplementBatch, detail string) bool {
	if batch == nil || (batch.State != supplementBatchPrepared && batch.State != supplementBatchStored) {
		return false
	}
	batch.State = supplementBatchUnknown
	l.AutoContinue = false
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateUnknown
			item.Detail = detail
		}
	}
	return true
}

// StopAutoContinue revokes automatic continuation while preserving all text and
// the record of what happened. A prepared-but-not-dispatched batch is aborted.
func (l *issueSupplementLedger) StopAutoContinue(batch *issueSupplementBatch) {
	l.AutoContinue = false
	if batch == nil || (batch.State != supplementBatchPrepared && batch.State != supplementBatchStored) {
		return
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil && item.BatchID == batch.ID &&
			(item.State == supplementStatePrepared || item.State == supplementStatePending) {
			item.State = supplementStatePending
			item.BatchID = ""
		}
	}
	batch.State = supplementBatchCanceled
}

func (l *issueSupplementLedger) ResumeAutoContinue() {
	l.AutoContinue = true
}

func (l *issueSupplementLedger) itemByID(id string) *issueSupplement {
	for _, item := range l.Items {
		if item != nil && item.ID == id {
			return item
		}
	}
	return nil
}

func (b *issueSupplementBatch) MatchHistory(records []issueSupplementHistory) bool {
	if b == nil || b.State != supplementBatchAccepted || b.Text == "" ||
		b.SessionID == "" || b.TaskID == "" || b.MessageID == "" || b.ExecutionID == "" {
		return false
	}
	for _, record := range records {
		if record.Text != b.Text {
			continue
		}
		if b.SessionID != "" && record.SessionID != b.SessionID {
			continue
		}
		if b.TaskID != "" && record.TaskID != b.TaskID {
			continue
		}
		if b.MessageID != "" && record.MessageID != b.MessageID {
			continue
		}
		if b.ExecutionID != "" && record.ExecutionID != b.ExecutionID {
			continue
		}
		return true
	}
	return false
}
