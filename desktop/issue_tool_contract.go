package main

// #154 application-side contract for conversation-issued issue tools.
//
// This file deliberately contains no Runtime client, dispatcher, queue, or
// second issue model. It binds app-issued scopes to the existing productIssue
// and issueExecution facts, and reserves persistent operation receipts so a
// lost tool reply cannot become a second issue mutation.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	issueToolActionCreate        = "create"
	issueToolActionUpdate        = "update"
	issueToolActionRun           = "run"
	issueToolActionStop          = "stop"
	issueToolActionAddSupplement = "add_supplement"
	issueToolActionWait          = "wait"
	issueToolActionResume        = "resume"
)

const (
	issueToolReceiptSaved         = "saved"
	issueToolReceiptDispatching   = "dispatching"
	issueToolReceiptAccepted      = "accepted"
	issueToolReceiptStopRequested = "stop_requested"
	issueToolReceiptUnknown       = "unknown"
)

var (
	errIssueToolScope             = errors.New("issue tool scope is invalid")
	errIssueToolAction            = errors.New("issue tool action is invalid")
	errIssueToolIntentConflict    = errors.New("issue tool intent already has different payload")
	errIssueToolSaveRequired      = errors.New("issue tool operation save is required")
	errIssueToolReceiptIDsMissing = errors.New("accepted issue tool receipt lacks real Runtime IDs")
)

// IssueToolScope is issued before a Runtime task exists. TurnID is the stable
// app-issued user-turn/intent identity; RuntimeTaskID may be attached later and
// is never required to create the scope.
const (
	issueToolModeConversation = "conversation"
	issueToolModeWork         = "work"
)

type IssueToolScope struct {
	Ref                   string   `json:"ref"`
	Mode                  string   `json:"mode,omitempty"`
	ContactID             string   `json:"contact_id"`
	ConversationSessionID string   `json:"conversation_session_id,omitempty"` // legacy read fallback only
	RuntimeSessionID      string   `json:"runtime_session_id,omitempty"`
	TurnID                string   `json:"turn_id"`
	AllowedIssueIDs       []string `json:"allowed_issue_ids,omitempty"`
	AllowCreate           bool     `json:"allow_create,omitempty"`
	RuntimeTaskID         string   `json:"runtime_task_id,omitempty"`
	WorkIssueID           string   `json:"work_issue_id,omitempty"`
	WorkExecutionID       string   `json:"work_execution_id,omitempty"`
	CreatedAt             int64    `json:"created_at"`
}

func (s IssueToolScope) RuntimeSession() string {
	if strings.TrimSpace(s.RuntimeSessionID) != "" {
		return strings.TrimSpace(s.RuntimeSessionID)
	}
	return strings.TrimSpace(s.ConversationSessionID)
}

func (s IssueToolScope) Validate() error {
	if strings.TrimSpace(s.Ref) == "" || strings.TrimSpace(s.ContactID) == "" ||
		strings.TrimSpace(s.RuntimeSession()) == "" || strings.TrimSpace(s.TurnID) == "" {
		return errIssueToolScope
	}
	for _, id := range s.AllowedIssueIDs {
		if strings.TrimSpace(id) == "" {
			return errIssueToolScope
		}
	}
	return nil
}

func (s IssueToolScope) AllowsIssue(issue *productIssue) bool {
	if issue == nil || s.Validate() != nil || issue.ContactID != s.ContactID {
		return false
	}
	return s.AllowsIssueID(issue.ID)
}

func (s IssueToolScope) AllowsIssueID(issueID string) bool {
	for _, id := range s.AllowedIssueIDs {
		if id == issueID {
			return true
		}
	}
	return false
}

type IssueToolError struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	Candidates      []string `json:"candidates,omitempty"`
	CurrentRevision *int     `json:"current_revision,omitempty"`
}

func (e *IssueToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type IssueToolActionRequest struct {
	ScopeRef         string   `json:"scope"`
	Action           string   `json:"action"`
	IssueID          string   `json:"issue_id,omitempty"`
	ExpectedRevision *int     `json:"expected_revision,omitempty"`
	Goal             string   `json:"goal,omitempty"`
	Requirements     []string `json:"requirements,omitempty"`
	Text             string   `json:"text,omitempty"`
	QuestionRef      string   `json:"question_ref,omitempty"`
}

func (r IssueToolActionRequest) Validate(scope IssueToolScope) error {
	if err := scope.Validate(); err != nil || r.ScopeRef != scope.Ref {
		return errIssueToolScope
	}
	switch r.Action {
	case issueToolActionCreate:
		// The first slice has one create intent slot per app-issued turn; a
		// turn needing multiple new issues must be split into distinct turns.
		if strings.TrimSpace(r.IssueID) != "" || strings.TrimSpace(r.Goal) == "" || !scope.AllowCreate {
			return errIssueToolAction
		}
	case issueToolActionUpdate:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Goal) == "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return errIssueToolAction
		}
	case issueToolActionRun, issueToolActionStop:
		if strings.TrimSpace(r.IssueID) == "" {
			return errIssueToolAction
		}
	case issueToolActionWait:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Text) == "" || r.ExpectedRevision == nil || *r.ExpectedRevision < 1 {
			return errIssueToolAction
		}
	case issueToolActionResume:
		if strings.TrimSpace(r.IssueID) == "" {
			return errIssueToolAction
		}
	case issueToolActionAddSupplement:
		if strings.TrimSpace(r.IssueID) == "" || strings.TrimSpace(r.Text) == "" {
			return errIssueToolAction
		}
	default:
		return errIssueToolAction
	}
	if r.Action != issueToolActionCreate && !scope.AllowsIssueID(r.IssueID) {
		return &IssueToolError{Code: "scope_denied", Message: "issue is outside the issued scope"}
	}
	return nil
}

func (r IssueToolActionRequest) targetIssueID() string {
	if r.Action == issueToolActionCreate {
		return ""
	}
	return r.IssueID
}

// issueToolCanonicalPayload is the persisted payload tied to one intent slot.
// It is intentionally independent from the operation ID.
func issueToolCanonicalPayload(r IssueToolActionRequest) (string, error) {
	payload := struct {
		Action           string   `json:"action"`
		IssueID          string   `json:"issue_id,omitempty"`
		ExpectedRevision *int     `json:"expected_revision,omitempty"`
		Goal             string   `json:"goal,omitempty"`
		Requirements     []string `json:"requirements,omitempty"`
		Text             string   `json:"text,omitempty"`
		QuestionRef      string   `json:"question_ref,omitempty"`
	}{
		Action: r.Action, IssueID: r.IssueID, ExpectedRevision: r.ExpectedRevision,
		Goal: strings.TrimSpace(r.Goal), Requirements: append([]string(nil), r.Requirements...),
		Text: strings.TrimSpace(r.Text), QuestionRef: strings.TrimSpace(r.QuestionRef),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func issueToolIntentSlot(scope IssueToolScope, r IssueToolActionRequest) string {
	return scope.Ref + "\x00" + scope.TurnID + "\x00" + r.Action + "\x00" + r.targetIssueID()
}

// IssueToolReceipt is a projection of the existing issue/execution/ledger
// facts. It is not a second business state machine.
type IssueToolReceipt struct {
	State                string          `json:"state"`
	IssueID              string          `json:"issue_id,omitempty"`
	ExecutionID          string          `json:"execution_id,omitempty"`
	SessionID            string          `json:"session_id,omitempty"`
	TaskID               string          `json:"task_id,omitempty"`
	MessageID            string          `json:"message_id,omitempty"`
	RequirementsRevision *int            `json:"requirements_revision,omitempty"`
	QuestionRef          string          `json:"question_ref,omitempty"`
	QuestionState        string          `json:"question_state,omitempty"`
	IssueStatus          string          `json:"issue_status,omitempty"`
	SupplementID         string          `json:"supplement_id,omitempty"`
	Error                *IssueToolError `json:"error,omitempty"`
}

func ValidateIssueToolReceipt(receipt *IssueToolReceipt) error {
	if receipt == nil {
		return nil
	}
	switch receipt.State {
	case issueToolReceiptSaved:
		if strings.TrimSpace(receipt.IssueID) == "" {
			return errors.New("saved receipt requires issue_id")
		}
	case issueToolReceiptDispatching:
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" {
			return errors.New("dispatching receipt requires issue_id and execution_id")
		}
	case issueToolReceiptAccepted:
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" || strings.TrimSpace(receipt.SessionID) == "" ||
			strings.TrimSpace(receipt.TaskID) == "" || strings.TrimSpace(receipt.MessageID) == "" {
			return errIssueToolReceiptIDsMissing
		}
	case issueToolReceiptStopRequested:
		if strings.TrimSpace(receipt.IssueID) == "" || strings.TrimSpace(receipt.ExecutionID) == "" || strings.TrimSpace(receipt.SessionID) == "" ||
			strings.TrimSpace(receipt.TaskID) == "" {
			return errors.New("stop_requested receipt requires real execution/session/task ids")
		}
	case issueToolReceiptUnknown:
		// Unknown may be intentionally incomplete; it is never accepted.
	default:
		return errors.New("unknown issue tool receipt state")
	}
	return nil
}

// IssueToolOperation is an application-issued operation record. It stores only
// the canonical payload and a receipt snapshot; issue/execution facts remain
// authoritative.
type IssueToolOperation struct {
	ID            string            `json:"id"`
	ScopeRef      string            `json:"scope_ref"`
	TurnID        string            `json:"turn_id"`
	Action        string            `json:"action"`
	TargetIssueID string            `json:"target_issue_id,omitempty"`
	Payload       string            `json:"payload"`
	Receipt       *IssueToolReceipt `json:"receipt,omitempty"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
}

type IssueToolOperationLedger struct {
	Operations []*IssueToolOperation `json:"operations,omitempty"`
}

func newIssueToolOperationID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "issue-op-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("issue-op-%d", time.Now().UnixNano())
}

func (l *IssueToolOperationLedger) operationFor(slot, payload string) (*IssueToolOperation, bool, error) {
	for _, operation := range l.Operations {
		if operation == nil || issueToolOperationSlot(operation) != slot {
			continue
		}
		if operation.Payload != payload {
			return nil, true, errIssueToolIntentConflict
		}
		return operation, true, nil
	}
	return nil, false, nil
}

func issueToolOperationSlot(operation *IssueToolOperation) string {
	if operation == nil {
		return ""
	}
	return operation.ScopeRef + "\x00" + operation.TurnID + "\x00" + operation.Action + "\x00" + operation.TargetIssueID
}

// Reserve atomically records the app-issued operation before the caller runs a
// business side effect. A duplicate same slot/payload returns the existing
// operation; a different payload is a conflict. Save failure removes the new
// reservation, so the caller must not execute the side effect.
func (l *IssueToolOperationLedger) Reserve(scope IssueToolScope, request IssueToolActionRequest, save func() error) (*IssueToolOperation, bool, error) {
	if err := request.Validate(scope); err != nil {
		return nil, false, err
	}
	if save == nil {
		return nil, false, errIssueToolSaveRequired
	}
	payload, err := issueToolCanonicalPayload(request)
	if err != nil {
		return nil, false, err
	}
	slot := issueToolIntentSlot(scope, request)
	if existing, found, err := l.operationFor(slot, payload); found {
		return existing, false, err
	}
	now := time.Now().UnixMilli()
	operation := &IssueToolOperation{
		ID: newIssueToolOperationID(), ScopeRef: scope.Ref, TurnID: scope.TurnID,
		Action: request.Action, TargetIssueID: request.targetIssueID(),
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	}
	l.Operations = append(l.Operations, operation)
	if err := save(); err != nil {
		l.Operations = l.Operations[:len(l.Operations)-1]
		return nil, false, fmt.Errorf("save issue tool operation: %w", err)
	}
	return operation, true, nil
}

func (l *IssueToolOperationLedger) operationByID(id string) *IssueToolOperation {
	for _, operation := range l.Operations {
		if operation != nil && operation.ID == id {
			return operation
		}
	}
	return nil
}

func (l *IssueToolOperationLedger) SetReceipt(operationID string, receipt *IssueToolReceipt, save func() error) error {
	operation := l.operationByID(operationID)
	if operation == nil || save == nil {
		return errors.New("issue tool operation is not in this ledger or save is missing")
	}
	if err := ValidateIssueToolReceipt(receipt); err != nil {
		return err
	}
	if operation.Action != issueToolActionCreate && receipt != nil &&
		receipt.IssueID != operation.TargetIssueID {
		return &IssueToolError{Code: "target_mismatch", Message: "receipt issue does not match the operation target"}
	}
	old := operation.Receipt
	oldUpdated := operation.UpdatedAt
	operation.Receipt = receipt
	operation.UpdatedAt = time.Now().UnixMilli()
	if err := save(); err != nil {
		operation.Receipt = old
		operation.UpdatedAt = oldUpdated
		return fmt.Errorf("save issue tool receipt: %w", err)
	}
	return nil
}

type IssueToolIssueListResult struct {
	Issues     []IssueToolIssueSummary `json:"issues"`
	HasMore    bool                    `json:"has_more"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type IssueToolIssueSummary struct {
	ID                   string `json:"id"`
	Goal                 string `json:"goal"`
	Status               string `json:"status"`
	Archived             bool   `json:"archived"`
	RequirementsRevision int    `json:"requirements_revision"`
	ActiveExecutionID    string `json:"active_execution_id,omitempty"`
	LastExecutionID      string `json:"last_execution_id,omitempty"`
	LastExecutionState   string `json:"last_execution_state,omitempty"`
	PendingSupplements   int    `json:"pending_supplements"`
	QuestionRef          string `json:"question_ref,omitempty"`
	Question             string `json:"question,omitempty"`
	QuestionRevision     int    `json:"question_revision,omitempty"`
}

type IssueToolRequirementView struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	State    string `json:"state"`
	Evidence string `json:"evidence,omitempty"`
}

type IssueToolExecutionView struct {
	ID                   string `json:"id"`
	SessionID            string `json:"session_id,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	MessageID            string `json:"message_id,omitempty"`
	Status               string `json:"status"`
	Detail               string `json:"detail,omitempty"`
	RequirementsRevision int    `json:"requirements_revision"`
}

const issueToolExecutionPageLimit = 20

type IssueToolResultFileView struct {
	ExecutionID string `json:"execution_id"`
	CallID      string `json:"call_id"`
	ToolName    string `json:"tool_name"`
	Path        string `json:"path"`
}

type IssueToolIssueDetail struct {
	Issue               IssueToolIssueSummary      `json:"issue"`
	Requirements        []IssueToolRequirementView `json:"requirements,omitempty"`
	RequirementCount    int                        `json:"requirement_count"`
	RequirementsHasMore bool                       `json:"requirements_has_more,omitempty"`
	Executions          []IssueToolExecutionView   `json:"executions,omitempty"`
	ExecutionCount      int                        `json:"execution_count"`
	ExecutionsHasMore   bool                       `json:"executions_has_more"`

	// Result facts are derived from the latest recorded execution's typed
	// history. They are never fabricated from assistant prose or tool logs.
	ResultExecutionID  string                    `json:"result_execution_id,omitempty"`
	ResultFileCount    int                       `json:"result_file_count"`
	ResultFiles        []IssueToolResultFileView `json:"result_files,omitempty"`
	ResultFilesHasMore bool                      `json:"result_files_has_more,omitempty"`
	ResultExcerpt      string                    `json:"result_excerpt,omitempty"`
	ResultUnavailable  string                    `json:"result_unavailable,omitempty"`
}

func issueToolSummary(issue *productIssue) IssueToolIssueSummary {
	summary := IssueToolIssueSummary{}
	if issue == nil {
		return summary
	}
	summary.ID, summary.Goal, summary.Status = issue.ID, issue.Goal, issue.Status
	summary.Archived, summary.RequirementsRevision = issue.Archived, issue.RequirementsRevision
	if issue.Supplements != nil {
		summary.PendingSupplements = issue.Supplements.pendingCount()
	}
	if question := issue.openQuestion(); question != nil {
		summary.QuestionRef, summary.Question, summary.QuestionRevision = question.Ref, question.Text, question.RequirementsRevision
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if execution == nil {
			continue
		}
		if summary.LastExecutionID == "" {
			summary.LastExecutionID, summary.LastExecutionState = execution.ID, execution.Status
		}
		if execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning || execution.Status == issueExecutionPending {
			summary.ActiveExecutionID = execution.ID
			break
		}
	}
	return summary
}

// issueToolList reads the existing product facts. It never creates, resumes, or
// mutates an issue, and never infers an issue from the selected GUI item.
func issueToolList(scope IssueToolScope, issues []*productIssue, includeArchived bool, limit int, cursor string) (IssueToolIssueListResult, error) {
	if err := scope.Validate(); err != nil {
		return IssueToolIssueListResult{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	all := make([]IssueToolIssueSummary, 0)
	for _, issue := range issues {
		if !scope.AllowsIssue(issue) || (!includeArchived && issue.Archived) {
			continue
		}
		all = append(all, issueToolSummary(issue))
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Status != all[j].Status {
			return all[i].Status < all[j].Status
		}
		return all[i].ID < all[j].ID
	})
	start := 0
	if cursor != "" {
		for i := range all {
			if all[i].ID == cursor {
				start = i + 1
				break
			}
		}
		if start == 0 {
			return IssueToolIssueListResult{}, &IssueToolError{Code: "invalid_cursor", Message: "issue list cursor is not in this scope"}
		}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	result := IssueToolIssueListResult{Issues: all[start:end]}
	if end < len(all) {
		result.HasMore = true
		result.NextCursor = all[end-1].ID
	}
	return result, nil
}

func issueToolGet(scope IssueToolScope, issues []*productIssue, issueID string) (*IssueToolIssueDetail, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var issue *productIssue
	for _, candidate := range issues {
		if candidate != nil && candidate.ID == issueID {
			issue = candidate
			break
		}
	}
	if issue == nil {
		return nil, &IssueToolError{Code: "not_found", Message: "issue was not found in this scope"}
	}
	if !scope.AllowsIssue(issue) {
		return nil, &IssueToolError{Code: "scope_denied", Message: "issue is outside the issued scope"}
	}
	detail := &IssueToolIssueDetail{Issue: issueToolSummary(issue)}
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		detail.Requirements = append(detail.Requirements, IssueToolRequirementView{
			ID: requirement.ID, Text: requirement.Text, State: requirement.State, Evidence: requirement.Evidence,
		})
	}
	executionStart := 0
	if len(issue.Executions) > issueToolExecutionPageLimit {
		executionStart = len(issue.Executions) - issueToolExecutionPageLimit
		detail.ExecutionsHasMore = true
	}
	for _, execution := range issue.Executions[executionStart:] {
		if execution == nil {
			continue
		}
		detail.Executions = append(detail.Executions, IssueToolExecutionView{
			ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
			MessageID: execution.MessageID, Status: execution.Status, Detail: execution.Detail,
			RequirementsRevision: execution.RequirementsRevision,
		})
	}
	detail.ExecutionCount = len(issue.Executions)
	return detail, nil
}
