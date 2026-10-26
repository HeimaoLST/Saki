package main

// Application-side adapter for #154 conversation issue tools. It owns only
// scope resolution, the persistent operation ledger and projection of existing
// productIssue/issueExecution/history facts. It does not create a second issue
// model, Runtime client, dispatcher or history parser.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	issueToolSourceID          = "nagi_issue_tools"
	issueToolScopeContextName  = "nagi.issue_tools.scope"
	issueToolWaitTimeout       = 25 * time.Second
	issueToolResultExcerptSize = 4096
	issueToolResultFilesLimit  = 50
)

func newIssueToolTurnID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "turn-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("turn-%d", time.Now().UnixNano())
}

func newIssueToolScopeRef() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "scope-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("scope-%d", time.Now().UnixNano())
}

func (a *appModel) issueToolEnabled() bool {
	return a != nil && a.issueToolServer != nil && a.issueToolEndpoint != "" && a.issueToolHelper != ""
}

func (a *appModel) startIssueToolService() error {
	if a.issueToolEnabled() {
		return nil
	}
	helper := strings.TrimSpace(os.Getenv("SAKI_TEST_ISSUE_TOOL_HELPER"))
	if helper == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate issue tool helper: %w", err)
		}
		// The Go test binary is not a runnable helper entry point. Tests that
		// exercise the real chain explicitly provide a built helper path.
		if strings.HasSuffix(filepath.Base(exe), ".test") {
			return nil
		}
		helper = exe
	}
	helper, err := filepath.Abs(helper)
	if err != nil {
		return fmt.Errorf("resolve issue tool helper: %w", err)
	}
	if info, err := os.Stat(helper); err != nil || info.IsDir() {
		return fmt.Errorf("issue tool helper is not executable at %s", helper)
	}
	endpoint := filepath.Join(os.TempDir(), fmt.Sprintf("nagi-issue-%d-%s.sock", os.Getpid(), newIssueToolScopeRef()))
	server, err := startIssueToolIPCServer(endpoint, a.handleIssueToolForward)
	if err != nil {
		return err
	}
	a.issueToolServer = server
	a.issueToolEndpoint = endpoint
	a.issueToolHelper = helper
	if a.issueToolLedger == nil {
		a.issueToolLedger = &IssueToolOperationLedger{}
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	if a.issueToolWaiters == nil {
		a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	}
	return nil
}

func (a *appModel) closeIssueToolService() error {
	if a == nil || a.issueToolServer == nil {
		return nil
	}
	return a.issueToolServer.Close()
}

// issueToolLaunchSource is the single Runtime host-config source for this app's
// helper. It is passed only to the conversation Runtime; issue work sessions do
// not select external tools.
func (a *appModel) issueToolLaunchSource() *runtimeToolSource {
	if !a.issueToolEnabled() {
		return nil
	}
	return &runtimeToolSource{
		ID: issueToolSourceID, Transport: "stdio", Command: a.issueToolHelper,
		Args:    []string{"--issue-tool-helper"},
		EnvFrom: []string{issueToolIPCSocketEnv}, Protocol: "legacy",
		Endpoint:         a.issueToolEndpoint,
		StartupTimeoutMS: 10000, CallTimeoutMS: 120000, CancelGraceMS: 2000,
	}
}

func (a *appModel) issueToolConversationSelection() map[string]any {
	if !a.issueToolEnabled() {
		return nil
	}
	external := make([]map[string]string, 0, len(issueToolDefinitions()))
	for _, definition := range issueToolDefinitions() {
		external = append(external, map[string]string{
			"source_id": issueToolSourceID, "tool_name": definition.Name, "exposed_name": definition.Name,
		})
	}
	return map[string]any{
		"builtin":  []string{"read", "bash", "edit", "write"},
		"external": external,
	}
}

// issueToolNewScope issues one app-owned scope before a conversation task is
// sent. The caller must persist it before the task.send.
func (a *appModel) issueToolNewScope(contactID, sessionID, turnID string) (*IssueToolScope, error) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return nil, errors.New("contact not found for issue tool scope")
	}
	ids := make([]string, 0, len(ct.Issues))
	for _, issue := range ct.Issues {
		if issue != nil && issue.ID != "" {
			ids = append(ids, issue.ID)
		}
	}
	scope := IssueToolScope{
		Ref: newIssueToolScopeRef(), ContactID: contactID, ConversationSessionID: sessionID,
		TurnID: turnID, AllowedIssueIDs: ids, AllowCreate: true, CreatedAt: nowMillis(),
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	a.issueToolScopes[scope.Ref] = scope
	if err := a.persist(); err != nil {
		delete(a.issueToolScopes, scope.Ref)
		return nil, fmt.Errorf("save issue tool scope: %w", err)
	}
	return &scope, nil
}

func issueToolScopeInput(scope *IssueToolScope) (runtimeContextInput, bool) {
	if scope == nil {
		return runtimeContextInput{}, false
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return runtimeContextInput{}, false
	}
	return runtimeContextInput{Name: issueToolScopeContextName, Text: string(data)}, true
}

func (a *appModel) issueToolScopeByRef(ref string) (IssueToolScope, error) {
	if a.issueToolScopes == nil {
		return IssueToolScope{}, errors.New("issue tool scope is unknown")
	}
	scope, ok := a.issueToolScopes[ref]
	if !ok || scope.Validate() != nil {
		return IssueToolScope{}, errors.New("issue tool scope is unknown")
	}
	if a.contactByID(scope.ContactID) == nil {
		return IssueToolScope{}, errors.New("issue tool scope contact no longer exists")
	}
	return scope, nil
}

func issueToolErrorResponse(code, message string) (issueToolForwardResponse, error) {
	return issueToolDomainErrorResponse(&IssueToolError{Code: code, Message: message})
}

func issueToolDomainErrorResponse(toolErr *IssueToolError) (issueToolForwardResponse, error) {
	data, err := json.Marshal(toolErr)
	if err != nil {
		return issueToolForwardResponse{}, err
	}
	return issueToolForwardResponse{
		Content: []issueToolContent{{Type: "text", Text: string(data)}},
		IsError: true,
	}, nil
}

func issueToolConsumeError(err error) (issueToolForwardResponse, error) {
	if toolErr, ok := err.(*IssueToolError); ok {
		return issueToolDomainErrorResponse(toolErr)
	}
	return issueToolErrorResponse("operation_unknown", err.Error())
}

func issueToolJSONResponse(value any, isError bool) (issueToolForwardResponse, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return issueToolErrorResponse("encode_failed", "could not encode issue tool response")
	}
	return issueToolForwardResponse{
		Content: []issueToolContent{{Type: "text", Text: string(data)}},
		IsError: isError,
	}, nil
}

func (a *appModel) handleIssueToolForward(ctx context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
	switch request.Tool {
	case "issue_list", "issue_get", "issue_action":
	default:
		return issueToolErrorResponse("unknown_tool", "unknown issue tool")
	}
	if request.Tool == "issue_list" {
		return a.issueToolHandleList(ctx, request.Arguments)
	}
	if request.Tool == "issue_get" {
		return a.issueToolHandleGet(ctx, request.Arguments)
	}
	return a.issueToolHandleAction(ctx, request.Arguments)
}

func issueToolArgumentString(raw json.RawMessage, field string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", errors.New("tool arguments must be an object")
	}
	var value string
	if item, ok := fields[field]; ok {
		if err := json.Unmarshal(item, &value); err != nil {
			return "", fmt.Errorf("%s must be a string", field)
		}
	}
	return strings.TrimSpace(value), nil
}

func (a *appModel) issueToolHandleList(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	scopeRef, err := issueToolArgumentString(raw, "scope")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	var args struct {
		IncludeArchived bool   `json:"include_archived"`
		Limit           int    `json:"limit"`
		Cursor          string `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return issueToolErrorResponse("invalid_arguments", "invalid issue_list arguments")
	}
	scopeRef = strings.TrimSpace(scopeRef)
	var result IssueToolIssueListResult
	var callErr error
	a.runOnUI(func() {
		scope, err := a.issueToolScopeByRef(scopeRef)
		if err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		all := make([]*productIssue, 0, len(ct.Issues))
		all = append(all, ct.Issues...)
		result, callErr = issueToolList(scope, all, args.IncludeArchived, args.Limit, strings.TrimSpace(args.Cursor))
	})
	if callErr != nil {
		return issueToolConsumeError(callErr)
	}
	return issueToolJSONResponse(result, false)
}

func (a *appModel) issueToolHandleGet(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	scopeRef, err := issueToolArgumentString(raw, "scope")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	issueID, err := issueToolArgumentString(raw, "issue_id")
	if err != nil || issueID == "" {
		return issueToolErrorResponse("invalid_arguments", "issue_id is required")
	}
	var detail *IssueToolIssueDetail
	var source issueResultSource
	var rt *runtimeClient
	var generation int
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		scope, err := a.issueToolScopeByRef(scopeRef)
		if err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		detail, callErr = issueToolGet(scope, ct.Issues, issueID)
		if callErr != nil || detail == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil {
			callErr = &IssueToolError{Code: "not_found", Message: "issue was not found"}
			return
		}
		for index := len(issue.Executions) - 1; index >= 0; index-- {
			execution := issue.Executions[index]
			if execution == nil || execution.SessionID == "" || execution.TaskID == "" {
				continue
			}
			source = issueResultSource{
				ExecutionID: execution.ID, SessionID: execution.SessionID,
				TaskID: execution.TaskID, MessageID: execution.MessageID,
			}
			detail.ResultExecutionID = execution.ID
			rt, generation = a.rt, a.connectionGeneration
			break
		}
	})
	if callErr != nil {
		return issueToolConsumeError(callErr)
	}
	if detail == nil {
		return issueToolErrorResponse("not_found", "issue was not found")
	}
	if source.ExecutionID == "" {
		detail.ResultUnavailable = "The latest execution has no recorded Runtime session/task; results cannot be inspected."
		return issueToolJSONResponse(detail, false)
	}
	stillCurrent := false
	a.runOnUI(func() { stillCurrent = rt != nil && a.rt == rt && a.connectionGeneration == generation })
	if !stillCurrent {
		detail.ResultUnavailable = "Runtime is not available for the recorded execution; results cannot be inspected."
		return issueToolJSONResponse(detail, false)
	}
	history, historyErr := a.historyAllForSession(rt, source.SessionID)
	if historyErr != nil {
		var historyErrText string
		a.runOnUI(func() { historyErrText = a.connectionRuntimeMessage(historyErr) })
		detail.ResultUnavailable = "Execution history is unavailable: " + historyErrText
		return issueToolJSONResponse(detail, false)
	}
	workingDirectory := ""
	if config, err := rt.connectionConfig(source.SessionID); err == nil {
		workingDirectory = config.EffectiveConfig.WorkingDirectory
	} else if session, loadErr := rt.loadSession(source.SessionID); loadErr == nil && session.Metadata != nil {
		workingDirectory = session.Metadata.WorkingDirectory
	}
	source.WorkingDirectory = workingDirectory
	derived := deriveIssueResultFiles(source, history)
	if derived.Unavailable != "" {
		detail.ResultUnavailable = derived.Unavailable
	} else {
		detail.ResultFileCount = len(derived.Files)
		if len(derived.Files) > issueToolResultFilesLimit {
			derived.Files = derived.Files[:issueToolResultFilesLimit]
			detail.ResultFilesHasMore = true
		}
		for _, file := range derived.Files {
			detail.ResultFiles = append(detail.ResultFiles, IssueToolResultFileView{
				ExecutionID: file.ExecutionID, CallID: file.CallID,
				ToolName: file.ToolName, Path: file.Path,
			})
		}
	}
	if excerpt := strings.TrimSpace(strings.Join(issueResultResponseSegments(history, source.TaskID), "\n\n")); excerpt != "" {
		detail.ResultExcerpt = truncateIssueToolText(excerpt, issueToolResultExcerptSize)
	}
	return issueToolJSONResponse(detail, false)
}

func truncateIssueToolText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n…[truncated]"
}

func issueToolRevisionPointer(value int) *int { return &value }

func (a *appModel) issueToolReserve(scope IssueToolScope, request IssueToolActionRequest) (*IssueToolOperation, bool, error) {
	if a.issueToolLedger == nil {
		a.issueToolLedger = &IssueToolOperationLedger{}
	}
	return a.issueToolLedger.Reserve(scope, request, a.persist)
}

func (a *appModel) issueToolSetReceipt(operation *IssueToolOperation, receipt *IssueToolReceipt) error {
	if operation == nil || a.issueToolLedger == nil {
		return errors.New("issue tool operation is unavailable")
	}
	return a.issueToolLedger.SetReceipt(operation.ID, receipt, a.persist)
}

func (a *appModel) issueToolAddScopeIssue(ref, issueID string) {
	scope, ok := a.issueToolScopes[ref]
	if !ok || issueID == "" {
		return
	}
	for _, existing := range scope.AllowedIssueIDs {
		if existing == issueID {
			return
		}
	}
	scope.AllowedIssueIDs = append(scope.AllowedIssueIDs, issueID)
	a.issueToolScopes[ref] = scope
}

func (a *appModel) issueToolNotify(operationID string, receipt IssueToolReceipt) {
	if operationID == "" || a.issueToolWaiters == nil {
		return
	}
	if waiter, ok := a.issueToolWaiters[operationID]; ok {
		select {
		case waiter <- receipt:
		default:
		}
	}
}

func (a *appModel) issueToolCompleteExecution(execution *issueExecution, accepted bool, detail string) {
	if execution == nil || execution.ToolOperationID == "" || a.issueToolLedger == nil {
		return
	}
	operation := a.issueToolLedger.operationByID(execution.ToolOperationID)
	if operation == nil {
		return
	}
	issueID := operation.TargetIssueID
	if issueID == "" {
		for _, ct := range a.contacts {
			if owner, _ := ct.executionByID(execution.ID); owner != nil {
				issueID = owner.ID
				break
			}
		}
	}
	receipt := &IssueToolReceipt{
		State: issueToolReceiptUnknown, IssueID: issueID, ExecutionID: execution.ID,
		SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID,
	}
	if accepted {
		receipt.State = issueToolReceiptAccepted
	}
	if detail != "" {
		receipt.Error = &IssueToolError{Code: "operation_unknown", Message: detail}
	}
	if err := a.issueToolSetReceipt(operation, receipt); err != nil {
		receipt = &IssueToolReceipt{State: issueToolReceiptUnknown, Error: &IssueToolError{
			Code: "operation_unknown", Message: "task acceptance was not saved with its operation receipt; reload before retrying",
		}}
	}
	a.issueToolNotify(operation.ID, *receipt)
}

func (a *appModel) issueToolHandleAction(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	var request IssueToolActionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return issueToolErrorResponse("invalid_arguments", "invalid issue_action arguments")
	}
	request.ScopeRef = strings.TrimSpace(request.ScopeRef)
	var scope IssueToolScope
	var scopeErr error
	a.runOnUI(func() { scope, scopeErr = a.issueToolScopeByRef(request.ScopeRef) })
	if scopeErr != nil {
		return issueToolErrorResponse("scope_denied", scopeErr.Error())
	}
	if err := request.Validate(scope); err != nil {
		if toolErr, ok := err.(*IssueToolError); ok {
			return issueToolJSONResponse(toolErr, true)
		}
		return issueToolErrorResponse("invalid_action", err.Error())
	}
	var operation *IssueToolOperation
	var created bool
	var reserveErr error
	a.runOnUI(func() { operation, created, reserveErr = a.issueToolReserve(scope, request) })
	if reserveErr != nil {
		return issueToolErrorResponse("operation_conflict", reserveErr.Error())
	}
	if !created {
		if operation.Receipt == nil {
			return issueToolErrorResponse("operation_unknown", "this turn already recorded an operation without a receipt; not replaying it")
		}
		return issueToolJSONResponse(operation.Receipt, operation.Receipt.State == issueToolReceiptUnknown)
	}
	var response issueToolForwardResponse
	switch request.Action {
	case issueToolActionCreate:
		response, _ = a.issueToolCreate(scope, request, operation)
	case issueToolActionUpdate:
		response, _ = a.issueToolUpdate(scope, request, operation)
	case issueToolActionAddSupplement:
		response, _ = a.issueToolSupplement(scope, request, operation)
	case issueToolActionRun:
		response, _ = a.issueToolRun(scope, request, operation)
	case issueToolActionStop:
		response, _ = a.issueToolStop(scope, request, operation)
	default:
		return issueToolErrorResponse("invalid_action", "unsupported issue action")
	}
	if response.IsError && operation.Receipt == nil {
		toolErr := issueToolResponseError(response)
		if toolErr == nil {
			toolErr = &IssueToolError{Code: "operation_unknown", Message: "issue action failed without a receipt; no automatic retry"}
		}
		a.runOnUI(func() {
			receipt := &IssueToolReceipt{
				State: issueToolReceiptUnknown, IssueID: operation.TargetIssueID, Error: toolErr,
			}
			if err := a.issueToolSetReceipt(operation, receipt); err == nil {
				a.issueToolNotify(operation.ID, *receipt)
			}
		})
	}
	return response, nil
}

func issueToolResponseError(response issueToolForwardResponse) *IssueToolError {
	if len(response.Content) != 1 {
		return nil
	}
	var toolErr IssueToolError
	if err := json.Unmarshal([]byte(response.Content[0].Text), &toolErr); err != nil {
		return nil
	}
	return &toolErr
}

func (a *appModel) issueToolCreate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	a.runOnUI(func() {
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		previousAllowed := append([]string(nil), scope.AllowedIssueIDs...)
		_, callErr = a.createIssueForWithSave(ct, request.Goal, strings.Join(request.Requirements, "\n"), func(created *productIssue) error {
			receipt := &IssueToolReceipt{
				State: issueToolReceiptSaved, IssueID: created.ID,
				RequirementsRevision: issueToolRevisionPointer(created.RequirementsRevision),
			}
			operation.Receipt = receipt
			a.issueToolAddScopeIssue(scope.Ref, created.ID)
			if err := a.persist(); err != nil {
				operation.Receipt = nil
				scope.AllowedIssueIDs = previousAllowed
				a.issueToolScopes[scope.Ref] = scope
				return err
			}
			return nil
		})
	})
	if callErr != nil {
		return issueToolErrorResponse("saved_failed", callErr.Error())
	}
	return issueToolJSONResponse(operation.Receipt, false)
}

func (a *appModel) issueToolUpdate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	var revision int
	a.runOnUI(func() {
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if request.ExpectedRevision == nil || *request.ExpectedRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "issue requirements changed; reload before updating", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		if !a.editIssueFor(ct, issue.ID, request.Goal, strings.Join(request.Requirements, "\n")) {
			callErr = errors.New(valueOr(a.issueFormError, "issue update was rejected"))
			return
		}
		revision = issue.RequirementsRevision
		callErr = a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID,
			RequirementsRevision: issueToolRevisionPointer(revision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(operation.Receipt, false)
}

func (a *appModel) issueToolSupplement(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	a.runOnUI(func() {
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		if err := a.addIssueSupplement(ct, request.IssueID, request.Text); err != nil {
			callErr = err
			return
		}
		issue := issueForContact(ct, request.IssueID)
		callErr = a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: request.IssueID,
			RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(operation.Receipt, false)
}

func (a *appModel) issueToolRun(scope IssueToolScope, _ IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	waiter := make(chan IssueToolReceipt, 1)
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, operation.TargetIssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if a.issueToolWaiters == nil {
			a.issueToolWaiters = map[string]chan IssueToolReceipt{}
		}
		a.issueToolWaiters[operation.ID] = waiter
		if !a.issueRunCapturedWithOperation(ct, issue, operation.ID) {
			callErr = errors.New(valueOr(a.issueFormError, "issue run was rejected"))
			return
		}
		execution := issue.Executions[len(issue.Executions)-1]
		if execution == nil || execution.ID == "" {
			callErr = errors.New("issue run did not record an execution")
			return
		}
		if err := a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptDispatching, IssueID: issue.ID, ExecutionID: execution.ID,
		}); err != nil {
			callErr = err
		}
	})
	if callErr != nil {
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueActionErrorResponse(callErr)
	}
	select {
	case receipt := <-waiter:
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolJSONResponse(receipt, receipt.State == issueToolReceiptUnknown)
	case <-time.After(issueToolWaitTimeout):
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolErrorResponse("operation_unknown", "issue task acceptance was not confirmed; no automatic retry")
	case <-a.issueToolShutdown:
		a.runOnUI(func() { delete(a.issueToolWaiters, operation.ID) })
		return issueToolErrorResponse("operation_unknown", "the app is shutting down before task acceptance was confirmed; no automatic retry")
	}
}

func (a *appModel) issueToolStop(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		taskID := issue.Work.ActiveTaskID
		execution := ct.executionByTask(taskID)
		if taskID == "" || execution == nil || execution.SessionID == "" || !issueHasTask(issue, taskID) {
			callErr = errors.New("issue has no active execution to stop")
			return
		}
		if !a.cancelIssueFor(ct, issue.ID) {
			callErr = errors.New("stop request was rejected")
			return
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptStopRequested, IssueID: issue.ID, ExecutionID: execution.ID,
			SessionID: execution.SessionID, TaskID: execution.TaskID,
		}
		callErr = a.issueToolSetReceipt(operation, receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(receipt, false)
}

func issueActionErrorResponse(err error) (issueToolForwardResponse, error) {
	if toolErr, ok := err.(*IssueToolError); ok {
		return issueToolJSONResponse(toolErr, true)
	}
	return issueToolErrorResponse("operation_unknown", err.Error())
}
