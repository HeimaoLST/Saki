package main

// Issue supplement domain model. This is deliberately independent of the GUI
// store and Runtime client. The app will attach the ledger to productIssue and
// include it in the same atomic store save; this file has no dispatcher, queue
// worker, or second execution loop.

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
	supplementStateDispatching   = "dispatching"
	supplementStateSubmitted     = "submitted"
	supplementStateRejected      = "rejected"
	supplementStateNotDispatched = "not_dispatched"
	supplementStateUnknown       = "unknown"

	supplementBatchPrepared      = "prepared"
	supplementBatchDispatching   = "dispatching"
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
	ContactID            string `json:"contact_id"`
	IssueID              string `json:"issue_id"`
	RequirementsRevision int    `json:"requirements_revision"`
}

type issueSupplement struct {
	ID                   string `json:"id"`
	ContactID            string `json:"contact_id"`
	IssueID              string `json:"issue_id"`
	Text                 string `json:"text"`
	RequirementsRevision int    `json:"requirements_revision"`
	CreatedAt            int64  `json:"created_at"`
	State                string `json:"state"`
	BatchID              string `json:"batch_id,omitempty"`
	Detail               string `json:"detail,omitempty"`
	SessionID            string `json:"session_id,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	MessageID            string `json:"message_id,omitempty"`
	ExecutionID          string `json:"execution_id,omitempty"`
}

type issueSupplementSnapshot struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type issueSupplementBatch struct {
	ID          string                    `json:"id"`
	Target      issueSupplementTarget     `json:"target"`
	Snapshot    []issueSupplementSnapshot `json:"snapshot"`
	Text        string                    `json:"text"`
	CreatedAt   int64                     `json:"created_at"`
	State       string                    `json:"state"`
	SessionID   string                    `json:"session_id,omitempty"`
	TaskID      string                    `json:"task_id,omitempty"`
	MessageID   string                    `json:"message_id,omitempty"`
	ExecutionID string                    `json:"execution_id,omitempty"`
	SaveError   string                    `json:"save_error,omitempty"`
}

type issueSupplementAssociation struct {
	SessionID   string
	TaskID      string
	MessageID   string
	ExecutionID string
}

// issueSupplementHistory is a caller-provided record, not a claim that this
// package parsed Runtime history. Matching requires a complete input string and
// all association identities supplied by the caller.
type issueSupplementHistory struct {
	Text        string
	SessionID   string
	TaskID      string
	MessageID   string
	ExecutionID string
}

type issueSupplementLedger struct {
	Items        []*issueSupplement      `json:"items,omitempty"`
	Batches      []*issueSupplementBatch `json:"batches,omitempty"`
	AutoContinue bool                    `json:"auto_continue"`
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

func (l *issueSupplementLedger) pendingCount() int {
	count := 0
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending {
			count++
		}
	}
	return count
}

func (l *issueSupplementLedger) pendingBytes() int {
	total := 0
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending {
			total += len([]byte(item.Text))
		}
	}
	return total
}

// Add receives a supplement only after save succeeds. The callback is the app's
// existing one-shot store save; nil is rejected instead of calling in-memory
// data received.
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

func supplementTargetsSameIssue(a, b issueSupplementTarget) bool {
	return a.ContactID == b.ContactID && a.IssueID == b.IssueID
}

func (l *issueSupplementLedger) pendingFor(target issueSupplementTarget) []*issueSupplement {
	var out []*issueSupplement
	for _, item := range l.Items {
		if item != nil && item.State == supplementStatePending &&
			supplementTargetsSameIssue(issueSupplementTarget{
				ContactID: item.ContactID, IssueID: item.IssueID,
			}, target) &&
			item.RequirementsRevision <= target.RequirementsRevision {
			out = append(out, item)
		}
	}
	return out
}

func (l *issueSupplementLedger) activeBatch(target issueSupplementTarget) *issueSupplementBatch {
	for _, batch := range l.Batches {
		if batch == nil || !supplementTargetsSameIssue(batch.Target, target) {
			continue
		}
		switch batch.State {
		case supplementBatchPrepared, supplementBatchDispatching, supplementBatchUnknown:
			return batch
		}
	}
	return nil
}

// Prepare creates and durably stores an immutable batch from currently pending
// supplements. It captures the exact target/requirements revision and copies
// the text; later additions or edits cannot alter the batch.
func (l *issueSupplementLedger) Prepare(target issueSupplementTarget, save func() error) (*issueSupplementBatch, error) {
	if err := validateSupplementTarget(target); err != nil {
		return nil, err
	}
	if save == nil {
		return nil, errSupplementSaveRequired
	}
	if !l.AutoContinue {
		return nil, errors.New("automatic supplement continuation is stopped")
	}
	if active := l.activeBatch(target); active != nil {
		return nil, fmt.Errorf("a supplement batch is already %s for this issue", active.State)
	}
	items := l.pendingFor(target)
	if len(items) == 0 {
		return nil, errSupplementNoPending
	}
	batch := &issueSupplementBatch{
		ID: newIssueSupplementID(), Target: target, CreatedAt: nowMillis(), State: supplementBatchPrepared,
	}
	var texts []string
	for _, item := range items {
		batch.Snapshot = append(batch.Snapshot, issueSupplementSnapshot{ID: item.ID, Text: item.Text})
		texts = append(texts, item.Text)
		item.State = supplementStatePrepared
		item.BatchID = batch.ID
	}
	batch.Text = strings.Join(texts, "\n")
	l.Batches = append(l.Batches, batch)
	if err := save(); err != nil {
		l.Batches = l.Batches[:len(l.Batches)-1]
		for _, snapshot := range batch.Snapshot {
			if item := l.itemByID(snapshot.ID); item != nil {
				item.State = supplementStatePending
				item.BatchID = ""
			}
		}
		return nil, fmt.Errorf("could not save supplement batch: %w", err)
	}
	return batch, nil
}

func (l *issueSupplementLedger) snapshotBatchItems(batch *issueSupplementBatch) map[string]issueSupplement {
	items := map[string]issueSupplement{}
	if batch == nil {
		return items
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			items[item.ID] = *item
		}
	}
	return items
}

func (l *issueSupplementLedger) restoreBatchItems(batch *issueSupplementBatch, old map[string]issueSupplement) {
	if batch == nil {
		return
	}
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			if previous, ok := old[item.ID]; ok {
				*item = previous
			}
		}
	}
}

// BeginDispatch is the durable boundary before task.send. Only after this save
// may the app start the RPC. A stopped/unknown dispatching batch must never be
// folded back into pending.
func (l *issueSupplementLedger) BeginDispatch(batch *issueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil || canonical.State != supplementBatchPrepared || !l.AutoContinue {
		return false
	}
	batch = canonical
	old := batch.State
	oldItems := l.snapshotBatchItems(batch)
	batch.State = supplementBatchDispatching
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateDispatching
		}
	}
	if err := save(); err != nil {
		batch.State = old
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

// ReleasePrepared abandons only a local prepared batch that has not crossed the
// BeginDispatch boundary. It returns the exact snapshot items to pending, keeps
// AutoContinue unchanged, and leaves the canceled batch as audit history. It is
// deliberately narrower than StopAutoContinue, which revokes user intent.
func (l *issueSupplementLedger) ReleasePrepared(batch *issueSupplementBatch, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil || canonical.State != supplementBatchPrepared {
		return false
	}
	old := *canonical
	oldItems := l.snapshotBatchItems(canonical)
	for _, snapshot := range canonical.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStatePending
			item.BatchID = ""
		}
	}
	canonical.State = supplementBatchCanceled
	if err := save(); err != nil {
		*canonical = old
		l.restoreBatchItems(canonical, oldItems)
		return false
	}
	return true
}

// Accept records a real Runtime acceptance with all four identities. It remains
// valid even if auto-continuation was stopped after dispatch; submission does
// not claim adoption, understanding, or requirement satisfaction.
func (l *issueSupplementLedger) Accept(batch *issueSupplementBatch, association issueSupplementAssociation, save func() error) (bool, error) {
	canonical := l.canonicalBatch(batch)
	if canonical == nil || (canonical.State != supplementBatchDispatching && canonical.State != supplementBatchUnknown) {
		return false, nil
	}
	batch = canonical
	if save == nil {
		return false, errSupplementSaveRequired
	}
	if strings.TrimSpace(association.SessionID) == "" || strings.TrimSpace(association.TaskID) == "" ||
		strings.TrimSpace(association.MessageID) == "" || strings.TrimSpace(association.ExecutionID) == "" {
		return false, errors.New("accepted supplement requires session, task, message and execution ids")
	}
	batch.State = supplementBatchAccepted
	batch.SessionID, batch.TaskID, batch.MessageID, batch.ExecutionID = association.SessionID, association.TaskID, association.MessageID, association.ExecutionID
	batch.SaveError = ""
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateSubmitted
			item.SessionID, item.TaskID, item.MessageID, item.ExecutionID = batch.SessionID, batch.TaskID, batch.MessageID, batch.ExecutionID
		}
	}
	if err := save(); err != nil {
		// Runtime acceptance is an external fact and cannot be rolled back.
		// Keep the accepted snapshot and association, stop automatic
		// continuation, and expose the persistence failure to the app.
		batch.SaveError = err.Error()
		l.AutoContinue = false
		return true, fmt.Errorf("runtime accepted supplement, but saving the association failed: %w", err)
	}
	return true, nil
}

func (l *issueSupplementLedger) Reject(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != supplementBatchPrepared && canonical.State != supplementBatchDispatching) {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = supplementBatchRejected
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateRejected
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

func (l *issueSupplementLedger) NotDispatched(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != supplementBatchPrepared && canonical.State != supplementBatchDispatching) {
		return false
	}
	batch = canonical
	old := *batch
	oldItems := l.snapshotBatchItems(batch)
	batch.State = supplementBatchNotDispatched
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateNotDispatched
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

func (l *issueSupplementLedger) Unknown(batch *issueSupplementBatch, detail string, save func() error) bool {
	canonical := l.canonicalBatch(batch)
	if save == nil || canonical == nil ||
		(canonical.State != supplementBatchPrepared && canonical.State != supplementBatchDispatching) {
		return false
	}
	batch = canonical
	old := *batch
	oldAuto, oldItems := l.AutoContinue, l.snapshotBatchItems(batch)
	batch.State = supplementBatchUnknown
	l.AutoContinue = false
	for _, snapshot := range batch.Snapshot {
		if item := l.itemByID(snapshot.ID); item != nil {
			item.State = supplementStateUnknown
			item.Detail = detail
		}
	}
	if err := save(); err != nil {
		*batch = old
		l.AutoContinue = oldAuto
		l.restoreBatchItems(batch, oldItems)
		return false
	}
	return true
}

// StopAutoContinue revokes future scheduling only. Prepared, never-dispatched
// batches return to pending; dispatching/unknown batches retain their identity
// so a late accepted response can still be recorded.
func (l *issueSupplementLedger) StopAutoContinue(batch *issueSupplementBatch, save func() error) bool {
	if save == nil {
		return false
	}
	if batch != nil {
		batch = l.canonicalBatch(batch)
		if batch == nil {
			return false
		}
	}
	oldAuto := l.AutoContinue
	oldBatch := issueSupplementBatch{}
	var oldItems map[string]issueSupplement
	l.AutoContinue = false
	if batch != nil {
		oldBatch = *batch
		oldItems = l.snapshotBatchItems(batch)
		if batch.State == supplementBatchPrepared {
			for _, snapshot := range batch.Snapshot {
				if item := l.itemByID(snapshot.ID); item != nil {
					item.State = supplementStatePending
					item.BatchID = ""
				}
			}
			batch.State = supplementBatchCanceled
		}
	}
	if err := save(); err != nil {
		l.AutoContinue = oldAuto
		if batch != nil {
			*batch = oldBatch
			l.restoreBatchItems(batch, oldItems)
		}
		return false
	}
	return true
}

func (l *issueSupplementLedger) ResumeAutoContinue(save func() error) bool {
	if save == nil {
		return false
	}
	old := l.AutoContinue
	l.AutoContinue = true
	if err := save(); err != nil {
		l.AutoContinue = old
		return false
	}
	return true
}

func (l *issueSupplementLedger) itemByID(id string) *issueSupplement {
	for _, item := range l.Items {
		if item != nil && item.ID == id {
			return item
		}
	}
	return nil
}

func (l *issueSupplementLedger) batchByID(id string) *issueSupplementBatch {
	for _, batch := range l.Batches {
		if batch != nil && batch.ID == id {
			return batch
		}
	}
	return nil
}

func (l *issueSupplementLedger) canonicalBatch(batch *issueSupplementBatch) *issueSupplementBatch {
	if batch == nil {
		return nil
	}
	canonical := l.batchByID(batch.ID)
	if canonical != batch {
		return nil
	}
	return canonical
}

// MatchProvidedHistory matches a caller-supplied complete send input and
// association against an accepted batch. It does not parse Runtime history and
// batch.Text is not claimed to be the full model input.
func (b *issueSupplementBatch) MatchProvidedHistory(fullInput string, records []issueSupplementHistory) bool {
	if b == nil || b.State != supplementBatchAccepted || b.Text == "" ||
		b.SessionID == "" || b.TaskID == "" || b.MessageID == "" || b.ExecutionID == "" {
		return false
	}
	for _, record := range records {
		if record.Text != fullInput || record.SessionID != b.SessionID || record.TaskID != b.TaskID ||
			record.MessageID != b.MessageID || record.ExecutionID != b.ExecutionID {
			continue
		}
		return true
	}
	return false
}
