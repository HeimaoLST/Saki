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
	"strconv"
	"strings"
	"time"
)

const (
	issueToolSourceID           = "nagi_issue_tools"
	issueToolScopeContextName   = "nagi.issue_tools.scope"
	issueToolWaitTimeout        = 25 * time.Second
	issueToolResultExcerptSize  = 4096
	issueToolResultFilesLimit   = 50
	issueToolQueryMaxBytes      = 16 * 1024
	issueToolMCPEnvelopeReserve = 512
	issueToolQueryTextLimit     = 512
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

func issueToolConversationExternal() []map[string]string {
	external := make([]map[string]string, 0, len(issueToolConversationDefinitions()))
	for _, definition := range issueToolConversationDefinitions() {
		external = append(external, map[string]string{
			"source_id": issueToolSourceID, "tool_name": definition.Name, "exposed_name": definition.Name,
		})
	}
	return external
}

func issueToolConversationDefinitions() []issueToolDefinition {
	all := issueToolDefinitions()
	out := make([]issueToolDefinition, 0, len(all))
	for _, definition := range all {
		if definition.Name != issueToolWorkToolName {
			out = append(out, definition)
		}
	}
	return out
}

func issueToolWorkDefinitions() []issueToolDefinition {
	for _, definition := range issueToolDefinitions() {
		if definition.Name == issueToolWorkToolName {
			return []issueToolDefinition{definition}
		}
	}
	return nil
}

const issueToolWorkToolName = "issue_wait"

func issueToolWorkExternal() []map[string]string {
	return []map[string]string{{
		"source_id": issueToolSourceID, "tool_name": issueToolWorkToolName, "exposed_name": issueToolWorkToolName,
	}}
}

func resolveBuiltinToolNames(rt *runtimeClient, selection *[]string) ([]string, error) {
	if selection != nil {
		return append([]string{}, (*selection)...), nil
	}
	catalog, err := rt.tools()
	if err != nil {
		return nil, fmt.Errorf("load Runtime built-in tools: %w", err)
	}
	return builtinToolNames(catalog.Builtin), nil
}

func runtimeConversationSelection(rt *runtimeClient, builtin *[]string, issueTools bool) (map[string]any, error) {
	if builtin == nil && !issueTools {
		return nil, nil
	}
	names, err := resolveBuiltinToolNames(rt, builtin)
	if err != nil {
		return nil, err
	}
	external := []map[string]string{}
	if issueTools {
		external = issueToolConversationExternal()
	}
	return map[string]any{"builtin": names, "external": external}, nil
}

func runtimeWorkSelection(rt *runtimeClient, builtin *[]string, issueTools bool) (map[string]any, error) {
	if builtin == nil && !issueTools {
		return nil, nil
	}
	names, err := resolveBuiltinToolNames(rt, builtin)
	if err != nil {
		return nil, err
	}
	external := []map[string]string{}
	if issueTools {
		external = issueToolWorkExternal()
	}
	return map[string]any{"builtin": names, "external": external}, nil
}

func runtimeSelectionHasWorkWait(selection runtimeToolSelection) bool {
	for _, binding := range selection.External {
		if binding.SourceID == issueToolSourceID && binding.ToolName == issueToolWorkToolName && binding.ExposedName == issueToolWorkToolName {
			return true
		}
	}
	return false
}

func (a *appModel) issueToolMergedWorkSelection(current runtimeToolSelection) (map[string]any, error) {
	if !a.issueToolEnabled() {
		return nil, nil
	}
	selection := map[string]any{"external": issueToolWorkExternal()}
	selection["builtin"] = append([]string{}, current.Builtin...)
	external := make([]map[string]string, 0, len(current.External)+1)
	exposedOwner := map[string]string{}
	found := false
	for _, binding := range current.External {
		external = append(external, map[string]string{
			"source_id": binding.SourceID, "tool_name": binding.ToolName, "exposed_name": binding.ExposedName,
		})
		if binding.ExposedName != "" {
			exposedOwner[binding.ExposedName] = binding.SourceID + "\x00" + binding.ToolName
		}
		if binding.SourceID == issueToolSourceID && binding.ToolName == issueToolWorkToolName && binding.ExposedName == issueToolWorkToolName {
			found = true
		}
	}
	if !found {
		owner := issueToolSourceID + "\x00" + issueToolWorkToolName
		if existing, used := exposedOwner[issueToolWorkToolName]; used && existing != owner {
			return nil, fmt.Errorf("issue wait exposed name is already used by %s", existing)
		}
		external = append(external, map[string]string{
			"source_id": issueToolSourceID, "tool_name": issueToolWorkToolName, "exposed_name": issueToolWorkToolName,
		})
	}
	selection["external"] = external
	return selection, nil
}

func (a *appModel) issueToolEnsureWorkSession(rt *runtimeClient, sessionID, roleGuidance string, created bool) (bool, error) {
	if created {
		return true, nil
	}
	if rt == nil || sessionID == "" {
		return false, errRuntimeDisconnected
	}
	config, err := rt.connectionConfig(sessionID)
	if err != nil {
		return false, fmt.Errorf("read work tool selection: %w", err)
	}
	if config.StorageSchema < 2 {
		return false, nil
	}
	toolsReady := runtimeSelectionHasWorkWait(config.EffectiveConfig.ToolSelection)
	desiredPrompt := addWorkflowGuidance(config.EffectiveConfig.SystemPrompt, roleGuidance)
	promptReady := desiredPrompt == config.EffectiveConfig.SystemPrompt
	refresh := a.issueToolRefreshRequired(rt, sessionID)
	if toolsReady && promptReady && !refresh {
		return true, nil
	}
	selection, mergeErr := a.issueToolMergedWorkSelection(config.EffectiveConfig.ToolSelection)
	if mergeErr != nil {
		return false, mergeErr
	}
	if selection == nil {
		return false, nil
	}
	if toolsReady && !refresh {
		selection = nil
	}
	promptPatch := ""
	if !promptReady {
		promptPatch = desiredPrompt
	}
	updated, err := rt.updateSessionConfiguration(sessionID, config.ConfigRevision, selection, promptPatch)
	if err != nil {
		return false, fmt.Errorf("enable or refresh issue work workflow: %w", err)
	}
	if updated.ReadbackError != nil {
		return false, fmt.Errorf("read back work configuration: %w", updated.ReadbackError)
	}
	if !runtimeSelectionHasWorkWait(updated.EffectiveConfig.ToolSelection) {
		return false, errors.New("Runtime did not confirm the work wait tool")
	}
	if updated.EffectiveConfig.SystemPrompt != desiredPrompt {
		return false, errors.New("Runtime did not confirm the work workflow prompt")
	}
	a.clearIssueToolRefresh(sessionID)
	return true, nil
}

func (a *appModel) releaseIssueWorkSession(ct *contact, issue *productIssue) {
	if a == nil || a.rt == nil || ct == nil || issue == nil || !a.issueToolEnabled() || a.shutdownStarted ||
		issue.WorkSessionID == "" || issue.Work.ActiveTaskID != "" || issue.Work.Releasing ||
		issue.Work.SendState != sendIdle || issue.Work.pendingExecutionID != "" || len(issue.Work.pendingEvents) > 0 {
		return
	}
	sessionID, contactID, issueID := issue.WorkSessionID, ct.ID, issue.ID
	rt, generation := a.rt, a.connectionGeneration
	issue.Work.Releasing = true
	a.issueToolReleaseInFlight++
	go func() {
		err := rt.releaseSession(sessionID)
		a.updateUI(func() {
			if a.issueToolReleaseInFlight > 0 {
				a.issueToolReleaseInFlight--
			}
			defer func() {
				if a.shutdownStarted {
					a.finishShutdown()
				}
			}()
			if a.rt != rt || a.connectionGeneration != generation {
				return
			}
			contactOwner := a.contactByID(contactID)
			if contactOwner == nil {
				return
			}
			owner := contactOwner.issueByID(issueID)
			if owner == nil || owner.WorkSessionID != sessionID {
				return
			}
			owner.Work.Releasing = false
			owner.Work.ReleasePending = false
			if err != nil {
				owner.Work.SendError = "Could not release idle work tool process: " + a.connectionRuntimeMessage(err)
			} else {
				owner.Work.Released = true
				owner.Work.Loaded = true
			}
			a.maybeAutoContinueIssue(contactOwner, owner)
		})
	}()
}

func (a *appModel) issueToolNewWorkScope(contactID, issueID, executionID, sessionID string) (*IssueToolScope, error) {
	ct := a.contactByID(contactID)
	if ct == nil || issueForContact(ct, issueID) == nil {
		return nil, errors.New("issue not found for work scope")
	}
	scope := IssueToolScope{
		Ref: newIssueToolScopeRef(), Mode: issueToolModeWork,
		ContactID: contactID, RuntimeSessionID: sessionID,
		TurnID: executionID, AllowedIssueIDs: []string{issueID},
		WorkIssueID: issueID, WorkExecutionID: executionID, CreatedAt: nowMillis(),
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
		return nil, fmt.Errorf("save work issue scope: %w", err)
	}
	return &scope, nil
}

func runtimeSelectionHasIssueTools(selection runtimeToolSelection) bool {
	found := map[string]bool{}
	for _, binding := range selection.External {
		if binding.SourceID == issueToolSourceID {
			found[binding.ToolName+"\x00"+binding.ExposedName] = true
		}
	}
	for _, definition := range issueToolConversationDefinitions() {
		if !found[definition.Name+"\x00"+definition.Name] {
			return false
		}
	}
	return true
}

func (a *appModel) issueToolMergedSelection(current runtimeToolSelection) (map[string]any, error) {
	if !a.issueToolEnabled() {
		return nil, nil
	}
	selection := map[string]any{"external": issueToolConversationExternal()}
	// An empty builtin list is an explicit "no built-in tools" choice. Preserve
	// it exactly; only a new conversation resolves the host default.
	selection["builtin"] = append([]string{}, current.Builtin...)
	external := make([]map[string]string, 0, len(current.External)+len(issueToolConversationDefinitions()))
	exposedOwner := map[string]string{}
	canonical := map[string]bool{}
	for _, binding := range current.External {
		external = append(external, map[string]string{
			"source_id": binding.SourceID, "tool_name": binding.ToolName, "exposed_name": binding.ExposedName,
		})
		if binding.ExposedName != "" {
			exposedOwner[binding.ExposedName] = binding.SourceID + "\x00" + binding.ToolName
		}
		if binding.SourceID == issueToolSourceID && binding.ToolName == binding.ExposedName {
			canonical[binding.ToolName] = true
		}
	}
	for _, definition := range issueToolDefinitions() {
		if canonical[definition.Name] {
			continue
		}
		owner := issueToolSourceID + "\x00" + definition.Name
		if existing, used := exposedOwner[definition.Name]; used && existing != owner {
			return nil, fmt.Errorf("issue tool exposed name %q is already used by %s", definition.Name, existing)
		}
		exposedOwner[definition.Name] = owner
		external = append(external, map[string]string{
			"source_id": issueToolSourceID, "tool_name": definition.Name, "exposed_name": definition.Name,
		})
	}
	selection["external"] = external
	return selection, nil
}

// issueToolEnsureConversationSession checks an existing schema-2 session and
// adds only the issue-tool bindings when absent. It deliberately does not
// migrate schema 1 or guessed legacy ownership; those paths remain explicit
// follow-ups.
type issueToolRefreshMark struct {
	runtime    *runtimeClient
	generation int
}

// issueToolRefreshRequired is the single app fact for a Runtime-issued
// tool_schema_changed error. It is not a cache of the current tool schema; it
// only authorizes the next explicit retry to refresh the persisted selection.
func (a *appModel) issueToolRefreshRequired(rt *runtimeClient, sessionID string) bool {
	a.issueToolMu.Lock()
	defer a.issueToolMu.Unlock()
	mark, ok := a.issueToolRefresh[sessionID]
	return ok && mark.runtime == rt && mark.generation == a.connectionGeneration
}

func (a *appModel) requireIssueToolRefresh(rt *runtimeClient, sessionID string) {
	a.issueToolMu.Lock()
	if a.issueToolRefresh == nil {
		a.issueToolRefresh = map[string]issueToolRefreshMark{}
	}
	a.issueToolRefresh[sessionID] = issueToolRefreshMark{runtime: rt, generation: a.connectionGeneration}
	a.issueToolMu.Unlock()
}

func (a *appModel) noteIssueToolTaskError(rt *runtimeClient, sessionID string, err error) {
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) && runtimeErr.Code == "tool_schema_changed" {
		a.requireIssueToolRefresh(rt, sessionID)
	}
}

func (a *appModel) clearIssueToolRefresh(sessionID string) {
	a.issueToolMu.Lock()
	delete(a.issueToolRefresh, sessionID)
	a.issueToolMu.Unlock()
}

func (a *appModel) issueToolEnsureConversationSession(rt *runtimeClient, sessionID, roleGuidance string, created bool) (bool, error) {
	if created {
		return true, nil
	}
	if rt == nil || sessionID == "" {
		return false, errRuntimeDisconnected
	}
	config, err := rt.connectionConfig(sessionID)
	if err != nil {
		return false, fmt.Errorf("read conversation tool selection: %w", err)
	}
	if config.StorageSchema < 2 {
		return false, nil
	}
	toolsReady := runtimeSelectionHasIssueTools(config.EffectiveConfig.ToolSelection)
	desiredPrompt := addWorkflowGuidance(config.EffectiveConfig.SystemPrompt, roleGuidance)
	promptReady := desiredPrompt == config.EffectiveConfig.SystemPrompt
	refresh := a.issueToolRefreshRequired(rt, sessionID)
	if toolsReady && promptReady && !refresh {
		// The session already has the exact selection and prompt. A no-op
		// config.update after restart can be rejected for native continuation,
		// so do not touch Runtime configuration.
		return true, nil
	}
	selection, mergeErr := a.issueToolMergedSelection(config.EffectiveConfig.ToolSelection)
	if mergeErr != nil {
		return false, mergeErr
	}
	if selection == nil {
		return false, nil
	}
	if toolsReady && !refresh {
		selection = nil
	}
	// Updating the full selection is only needed when it is not already present
	// or when Runtime explicitly requested a schema refresh. The combined prompt
	// preserves the user's saved profile; this changes no model/cwd/compaction
	// setting.
	promptPatch := ""
	if !promptReady {
		promptPatch = desiredPrompt
	}
	updated, err := rt.updateSessionConfiguration(sessionID, config.ConfigRevision, selection, promptPatch)
	if err != nil {
		return false, fmt.Errorf("enable or refresh conversation issue workflow: %w", err)
	}
	if updated.ReadbackError != nil {
		return false, fmt.Errorf("read back conversation configuration: %w", updated.ReadbackError)
	}
	if !runtimeSelectionHasIssueTools(updated.EffectiveConfig.ToolSelection) {
		return false, errors.New("Runtime did not confirm the conversation issue-tool selection")
	}
	if updated.EffectiveConfig.SystemPrompt != desiredPrompt {
		return false, errors.New("Runtime did not confirm the conversation workflow prompt")
	}
	a.clearIssueToolRefresh(sessionID)
	return true, nil
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
		Ref: newIssueToolScopeRef(), ContactID: contactID, RuntimeSessionID: sessionID,
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

// issueToolScopeToken is the only model-facing projection of an app-issued
// scope. The full object stays in the app map/store for authorization and
// source checking; the model receives only this opaque ref.
func issueToolScopeToken(scope *IssueToolScope) string {
	if scope == nil {
		return ""
	}
	return scope.Ref
}

func issueToolScopeInput(scope *IssueToolScope) (runtimeContextInput, bool) {
	token := issueToolScopeToken(scope)
	if token == "" {
		return runtimeContextInput{}, false
	}
	return runtimeContextInput{Name: issueToolScopeContextName, Text: token}, true
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

// issueToolScopeActive binds a scope to the one captured conversation turn
// that is currently submitting or executing. Scopes are not permanent
// authorization: a new turn, new session, archive, restart after terminal, or
// failed send cannot use an old scope for a new side effect.
func (a *appModel) issueToolScopeActive(captured IssueToolScope) bool {
	if a.shutdownStarted || captured.Validate() != nil || a.issueToolScopes == nil {
		return false
	}
	// The accepted task can bind after the handler captured its scope copy.
	// Read the latest binding from the app map, while requiring that the
	// immutable scope identity still matches the captured one.
	scope, ok := a.issueToolScopes[captured.Ref]
	if !ok || scope.Ref != captured.Ref || scope.ContactID != captured.ContactID ||
		scope.RuntimeSession() != captured.RuntimeSession() || scope.TurnID != captured.TurnID {
		return false
	}
	ct := a.contactByID(scope.ContactID)
	if ct == nil || ct.Archived || scope.Validate() != nil {
		return false
	}
	if scope.Mode == issueToolModeWork {
		issue := ct.issueByID(scope.WorkIssueID)
		if issue == nil || issue.Archived || issue.WorkSessionID != scope.RuntimeSession() {
			return false
		}
		_, execution := ct.executionByID(scope.WorkExecutionID)
		if issue.Work.ActiveTaskID != "" {
			return scope.RuntimeTaskID == issue.Work.ActiveTaskID ||
				execution != nil && execution.ToolScopeRef == scope.Ref && execution.TaskID == issue.Work.ActiveTaskID
		}
		return issue.Work.SendState == sendSubmitting && execution != nil &&
			execution.ToolScopeRef == scope.Ref && execution.SessionID == scope.RuntimeSession()
	}
	if ct.SessionID != scope.RuntimeSession() {
		return false
	}
	if ct.ActiveTaskID != "" {
		return scope.RuntimeTaskID != "" && scope.RuntimeTaskID == ct.ActiveTaskID
	}
	return ct.SendState == sendSubmitting && ct.pendingSend != nil && ct.pendingSend.ToolScopeRef == scope.Ref
}

func (a *appModel) issueToolActionAllowed(scope IssueToolScope) error {
	if !a.issueToolScopeActive(scope) {
		return &IssueToolError{Code: "scope_inactive", Message: "the conversation turn or issue tool owner is no longer active"}
	}
	return nil
}

func (a *appModel) issueToolAttachPendingScope(contactID, ref string) error {
	ct := a.contactByID(contactID)
	if ct == nil || ct.pendingSend == nil {
		return errors.New("conversation send intent is no longer present")
	}
	previous := ct.pendingSend.ToolScopeRef
	ct.pendingSend.ToolScopeRef = ref
	if err := a.persist(); err != nil {
		ct.pendingSend.ToolScopeRef = previous
		return err
	}
	return nil
}

func (a *appModel) issueToolBindTask(ref, taskID string) {
	if ref == "" || taskID == "" {
		return
	}
	scope, ok := a.issueToolScopes[ref]
	if !ok {
		return
	}
	scope.RuntimeTaskID = taskID
	a.issueToolScopes[ref] = scope
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			for _, question := range issue.Questions {
				if question != nil && question.SourceScopeRef == ref && question.SourceTaskID == "" {
					question.SourceTaskID = taskID
				}
			}
		}
	}
}

// issueToolReserveForTurn performs existing-operation lookup, active-turn
// validation and Reserve in one UI queue callback. A repeated old operation can
// still return its receipt after the scope expires, but it can never reserve a
// new side effect outside the captured turn.
func (a *appModel) issueToolReserveForTurn(scope IssueToolScope, request IssueToolActionRequest) (*IssueToolOperation, bool, bool, *IssueToolReceipt, error) {
	var operation *IssueToolOperation
	var created, existing bool
	var receipt *IssueToolReceipt
	var resultErr error
	a.runOnUI(func() {
		if a.issueToolLedger == nil {
			a.issueToolLedger = &IssueToolOperationLedger{}
		}
		payload, err := issueToolCanonicalPayload(request)
		if err != nil {
			resultErr = err
			return
		}
		slot := issueToolIntentSlot(scope, request)
		if found, foundOK, err := a.issueToolLedger.operationFor(slot, payload); foundOK {
			existing = true
			operation = found
			if err != nil {
				resultErr = err
				return
			}
			receipt = cloneIssueToolReceipt(found.Receipt)
			return
		}
		if !a.issueToolScopeActive(scope) {
			resultErr = &IssueToolError{Code: "scope_inactive", Message: "the conversation turn that issued this scope is no longer active"}
			return
		}
		operation, created, resultErr = a.issueToolReserve(scope, request)
		if !created && resultErr == nil {
			existing = true
			receipt = cloneIssueToolReceipt(operation.Receipt)
		}
	})
	return operation, created, existing, receipt, resultErr
}

type issueToolScopeIdentity struct {
	Ref              string
	ContactID        string
	RuntimeSessionID string
	TurnID           string
	CreatedAt        int64
}

// issueToolScopeIndexSnapshot copies only immutable issued-scope identity. It
// must be called on the UI actor before handing history parsing to a goroutine.
func (a *appModel) issueToolScopeIndexSnapshot() map[string]issueToolScopeIdentity {
	index := make(map[string]issueToolScopeIdentity, len(a.issueToolScopes))
	for ref, scope := range a.issueToolScopes {
		index[ref] = issueToolScopeIdentity{
			Ref: ref, ContactID: scope.ContactID,
			RuntimeSessionID: scope.RuntimeSession(),
			TurnID:           scope.TurnID, CreatedAt: scope.CreatedAt,
		}
	}
	return index
}

func filterIssueToolContextWithIndex(index map[string]issueToolScopeIdentity, sessionID string, items []taskContextItem) []taskContextItem {
	if len(items) == 0 {
		return items
	}
	last := items[len(items)-1]
	if last.Name != issueToolScopeContextName {
		return items
	}
	text := strings.TrimSpace(last.Text)
	var input IssueToolScope
	if json.Unmarshal([]byte(text), &input) == nil && input.Validate() == nil {
		identity, ok := index[input.Ref]
		if !ok || identity.ContactID != input.ContactID || identity.RuntimeSessionID != sessionID ||
			identity.TurnID != input.TurnID || identity.CreatedAt != input.CreatedAt {
			return items
		}
		return append([]taskContextItem(nil), items[:len(items)-1]...)
	}
	// New wire form: the model-facing scope value is only the opaque ref.
	// Legacy persisted JSON above is still recognized; unknown/same-named user
	// material is never filtered merely by its name.
	identity, ok := index[text]
	if !ok || identity.ContactID == "" || identity.RuntimeSessionID != sessionID {
		return items
	}
	return append([]taskContextItem(nil), items[:len(items)-1]...)
}

func parseHistoryWithIssueToolScopeFilter(sessionID string, records []historyRecord, stopRequested map[string]bool, index map[string]issueToolScopeIdentity) historyProjection {
	return parseHistoryWithContextFilter(sessionID, records, stopRequested, func(recordSession string, items []taskContextItem) []taskContextItem {
		return filterIssueToolContextWithIndex(index, recordSession, items)
	})
}

func cloneIssueToolReceipt(receipt *IssueToolReceipt) *IssueToolReceipt {
	if receipt == nil {
		return nil
	}
	copy := *receipt
	if receipt.RequirementsRevision != nil {
		value := *receipt.RequirementsRevision
		copy.RequirementsRevision = &value
	}
	if receipt.Error != nil {
		errorCopy := *receipt.Error
		errorCopy.Candidates = append([]string(nil), receipt.Error.Candidates...)
		if receipt.Error.CurrentRevision != nil {
			value := *receipt.Error.CurrentRevision
			errorCopy.CurrentRevision = &value
		}
		copy.Error = &errorCopy
	}
	return &copy
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
	case "issue_list", "issue_get", "issue_action", "issue_wait":
	default:
		return issueToolErrorResponse("unknown_tool", "unknown issue tool")
	}
	if request.Tool == "issue_list" {
		return a.issueToolHandleList(ctx, request.Arguments)
	}
	if request.Tool == "issue_get" {
		return a.issueToolHandleGet(ctx, request.Arguments)
	}
	if request.Tool == "issue_wait" {
		return a.issueToolHandleWorkWait(ctx, request.Arguments)
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

func issueToolResponseBytes(value any) int {
	response, err := issueToolJSONResponse(value, false)
	if err != nil {
		return issueToolQueryMaxBytes + 1
	}
	data, err := json.Marshal(response)
	if err != nil {
		return issueToolQueryMaxBytes + 1
	}
	return len(data) + issueToolMCPEnvelopeReserve
}

func boundIssueToolList(result IssueToolIssueListResult) IssueToolIssueListResult {
	for index := range result.Issues {
		result.Issues[index].Goal = truncateIssueToolText(result.Issues[index].Goal, issueToolQueryTextLimit)
		result.Issues[index].Question = truncateIssueToolText(result.Issues[index].Question, issueToolQueryTextLimit)
	}
	for len(result.Issues) > 0 && issueToolResponseBytes(result) > issueToolQueryMaxBytes {
		result.Issues = result.Issues[:len(result.Issues)-1]
		result.HasMore = true
	}
	if result.HasMore && len(result.Issues) > 0 {
		result.NextCursor = result.Issues[len(result.Issues)-1].ID
	} else if len(result.Issues) == 0 {
		result.HasMore = false
		result.NextCursor = ""
	}
	return result
}

func boundIssueToolDetail(detail *IssueToolIssueDetail) *IssueToolIssueDetail {
	if detail == nil {
		return nil
	}
	detail.Issue.Goal = truncateIssueToolText(detail.Issue.Goal, issueToolQueryTextLimit)
	detail.Issue.Question = truncateIssueToolText(detail.Issue.Question, issueToolQueryTextLimit)
	for index := range detail.Requirements {
		detail.Requirements[index].Text = truncateIssueToolText(detail.Requirements[index].Text, issueToolQueryTextLimit)
		detail.Requirements[index].Evidence = truncateIssueToolText(detail.Requirements[index].Evidence, issueToolQueryTextLimit)
	}
	for index := range detail.Executions {
		detail.Executions[index].Detail = truncateIssueToolText(detail.Executions[index].Detail, issueToolQueryTextLimit)
	}
	detail.ResultExcerpt = truncateIssueToolText(detail.ResultExcerpt, issueToolResultExcerptSize)
	detail.ResultUnavailable = truncateIssueToolText(detail.ResultUnavailable, issueToolQueryTextLimit)
	if detail.RequirementCount == 0 {
		detail.RequirementCount = len(detail.Requirements)
	}
	for len(detail.Requirements) > 0 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.Requirements = detail.Requirements[:len(detail.Requirements)-1]
		detail.RequirementsHasMore = true
	}
	for len(detail.Executions) > 1 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.Executions = detail.Executions[1:]
		detail.ExecutionsHasMore = true
	}
	for len(detail.ResultFiles) > 0 && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		detail.ResultFiles = detail.ResultFiles[:len(detail.ResultFiles)-1]
		detail.ResultFilesHasMore = true
	}
	if detail.ResultExcerpt != "" && issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
		original := detail.ResultExcerpt
		for _, limit := range []int{2048, 1024, 512, 256, 128} {
			detail.ResultExcerpt = truncateIssueToolText(original, limit)
			detail.ResultExcerptHasMore = true
			if issueToolResponseBytes(detail) <= issueToolQueryMaxBytes {
				break
			}
		}
		if issueToolResponseBytes(detail) > issueToolQueryMaxBytes {
			detail.ResultExcerpt = ""
			detail.ResultExcerptHasMore = true
		}
	}
	return detail
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
	result = boundIssueToolList(result)
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
	var scope IssueToolScope
	var rt *runtimeClient
	var generation int
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		var err error
		scope, err = a.issueToolScopeByRef(scopeRef)
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
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	stillCurrent := false
	a.runOnUI(func() { stillCurrent = rt != nil && a.rt == rt && a.connectionGeneration == generation })
	if !stillCurrent {
		detail.ResultUnavailable = "Runtime is not available for the recorded execution; results cannot be inspected."
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
	}
	history, historyErr := a.historyAllForSession(rt, source.SessionID)
	if historyErr != nil {
		var historyErrText string
		a.runOnUI(func() { historyErrText = a.connectionRuntimeMessage(historyErr) })
		detail.ResultUnavailable = "Execution history is unavailable: " + historyErrText
		return issueToolJSONResponse(boundIssueToolDetail(detail), false)
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
	stillCurrent = false
	a.runOnUI(func() {
		ct := a.contactByID(scope.ContactID)
		owner, execution := (*productIssue)(nil), (*issueExecution)(nil)
		if ct != nil {
			owner, execution = ct.executionByID(source.ExecutionID)
		}
		stillCurrent = rt != nil && a.rt == rt && a.connectionGeneration == generation &&
			ct != nil && owner != nil && owner.ID == issueID && execution != nil &&
			execution.SessionID == source.SessionID && execution.TaskID == source.TaskID
	})
	if !stillCurrent {
		detail = &IssueToolIssueDetail{Issue: detail.Issue, ResultExecutionID: source.ExecutionID, ResultUnavailable: "The conversation turn or Runtime owner changed while history was being read; results were discarded."}
	}
	return issueToolJSONResponse(boundIssueToolDetail(detail), false)
}

func truncateIssueToolText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n…[truncated]"
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
		receipt = &IssueToolReceipt{
			State: issueToolReceiptUnknown, IssueID: issueID, ExecutionID: execution.ID,
			SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID,
			Error: &IssueToolError{Code: "operation_unknown", Message: "task acceptance was not saved with its operation receipt; reload before retrying"},
		}
	}
	a.issueToolNotify(operation.ID, *cloneIssueToolReceipt(receipt))
}

func (a *appModel) issueToolHandleAction(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	var request IssueToolActionRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		message := "invalid issue_action arguments"
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field == "expected_revision" {
			message += ": " + issueToolExpectedRevisionCorrection
		}
		return issueToolErrorResponse("invalid_arguments", message)
	}
	request.ScopeRef = strings.TrimSpace(request.ScopeRef)
	var scope IssueToolScope
	var scopeErr error
	a.runOnUI(func() { scope, scopeErr = a.issueToolScopeByRef(request.ScopeRef) })
	if scopeErr != nil {
		return issueToolErrorResponse("scope_denied", scopeErr.Error())
	}
	if scope.Mode == issueToolModeWork {
		return issueToolErrorResponse("scope_denied", "work scope allows only issue_wait")
	}
	if err := request.Validate(scope); err != nil {
		if toolErr, ok := err.(*IssueToolError); ok {
			return issueToolJSONResponse(toolErr, true)
		}
		return issueToolErrorResponse("invalid_action", err.Error())
	}
	operation, created, existing, receipt, reserveErr := a.issueToolReserveForTurn(scope, request)
	if reserveErr != nil {
		return issueToolConsumeError(reserveErr)
	}
	if existing {
		if receipt == nil {
			return issueToolErrorResponse("operation_unknown", "this turn already recorded an operation without a receipt; not replaying it")
		}
		return issueToolJSONResponse(receipt, receipt.State == issueToolReceiptUnknown)
	}
	if !created {
		return issueToolErrorResponse("operation_unknown", "operation was not reserved; no side effect was performed")
	}
	var response issueToolForwardResponse
	switch request.Action {
	case issueToolActionCreate:
		response, _ = a.issueToolCreate(scope, request, operation)
	case issueToolActionUpdate:
		response, _ = a.issueToolUpdate(scope, request, operation)
	case issueToolActionAddSupplement:
		response, _ = a.issueToolSupplement(scope, request, operation)
	case issueToolActionWait:
		response, _ = a.issueToolWait(scope, request, operation)
	case issueToolActionResume:
		response, _ = a.issueToolResume(scope, request, operation)
	case issueToolActionRun:
		response, _ = a.issueToolRun(scope, request, operation)
	case issueToolActionStop:
		response, _ = a.issueToolStop(scope, request, operation)
	default:
		return issueToolErrorResponse("invalid_action", "unsupported issue action")
	}
	if response.IsError {
		toolErr := issueToolResponseError(response)
		if toolErr == nil {
			toolErr = &IssueToolError{Code: "operation_unknown", Message: "issue action failed without a receipt; no automatic retry"}
		}
		a.runOnUI(func() {
			if operation.Receipt != nil {
				return
			}
			receipt := &IssueToolReceipt{
				State: issueToolReceiptUnknown, IssueID: operation.TargetIssueID, Error: toolErr,
			}
			if err := a.issueToolSetReceipt(operation, receipt); err == nil {
				a.issueToolNotify(operation.ID, *cloneIssueToolReceipt(receipt))
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

// issueToolCreateContext returns only the user context snapshot belonging to
// the exact conversation scope/turn that issued create. It prefers the
// pre-accept pendingSend snapshot and otherwise uses the live msgUser record
// with the scope's real Runtime task. It never reads the current draft, scans
// chat history, or treats the scope token as user material.
func (a *appModel) issueToolTurnContext(ct *contact, scope IssueToolScope) []taskContextItem {
	if ct == nil || ct.ID != scope.ContactID {
		return nil
	}
	sessionID := scope.RuntimeSession()
	if sessionID == "" || ct.SessionID != sessionID {
		return nil
	}
	if ct.pendingSend != nil && ct.pendingSend.ToolScopeRef == scope.Ref {
		return cloneTaskContextItems(ct.pendingSend.Context)
	}
	if scope.RuntimeTaskID == "" {
		return nil
	}
	for index := len(ct.Messages) - 1; index >= 0; index-- {
		message := ct.Messages[index]
		if message != nil && message.Kind == msgUser && message.TaskID == scope.RuntimeTaskID {
			return cloneTaskContextItems(message.Context)
		}
	}
	return nil
}

func mergeIssueToolTurnContext(existing, captured []taskContextItem) []taskContextItem {
	merged := cloneTaskContextItems(existing)
	index := make(map[string]taskContextItem, len(merged))
	for _, item := range merged {
		index[item.ID+"\x00"+strconv.FormatUint(item.Revision, 10)] = item
	}
	for _, item := range captured {
		key := item.ID + "\x00" + strconv.FormatUint(item.Revision, 10)
		if _, ok := index[key]; ok {
			continue
		}
		merged = append(merged, item)
		index[key] = item
	}
	return merged
}

func (a *appModel) issueToolCaptureTurnContext(ct *contact, scope IssueToolScope) ([]taskContextItem, error) {
	captured := a.issueToolTurnContext(ct, scope)
	if err := validateTaskContext(captured); err != nil {
		return nil, err
	}
	return captured, nil
}

func (a *appModel) issueToolCreate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	var receipt *IssueToolReceipt
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		previousAllowed := append([]string(nil), scope.AllowedIssueIDs...)
		latestScope, latestErr := a.issueToolScopeByRef(scope.Ref)
		if latestErr != nil {
			callErr = latestErr
			return
		}
		capturedContext := a.issueToolTurnContext(ct, latestScope)
		if err := validateTaskContext(capturedContext); err != nil {
			callErr = err
			return
		}
		_, callErr = a.createIssueForWithSave(ct, request.Goal, strings.Join(request.Requirements, "\n"), func(created *productIssue) error {
			created.Context.items = capturedContext
			receipt = &IssueToolReceipt{
				State: issueToolReceiptSaved, IssueID: created.ID, IssueStatus: created.Status,
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
			receipt = cloneIssueToolReceipt(operation.Receipt)
			return nil
		})
	})
	if callErr != nil {
		return issueToolErrorResponse("saved_failed", callErr.Error())
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolUpdate(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var callErr error
	var revision int
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
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
			State: issueToolReceiptSaved, IssueID: issue.ID, IssueStatus: issue.Status,
			RequirementsRevision: issueToolRevisionPointer(revision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	var receipt *IssueToolReceipt
	a.runOnUI(func() { receipt = cloneIssueToolReceipt(operation.Receipt) })
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolSupplement(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	if strings.TrimSpace(request.QuestionRef) != "" {
		return a.issueToolAnswerQuestion(scope, request, operation)
	}
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		if ct == nil {
			callErr = errors.New("issue tool scope contact no longer exists")
			return
		}
		issue := issueForContact(ct, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		latestScope, latestErr := a.issueToolScopeByRef(scope.Ref)
		if latestErr != nil {
			callErr = latestErr
			return
		}
		captured, captureErr := a.issueToolCaptureTurnContext(ct, latestScope)
		if captureErr != nil {
			callErr = captureErr
			return
		}
		previousContext := issue.Context.snapshot()
		mergedContext := mergeIssueToolTurnContext(issue.Context.items, captured)
		contextChanged := len(mergedContext) != len(issue.Context.items)
		if contextChanged {
			if err := validateTaskContext(mergedContext); err != nil {
				callErr = err
				return
			}
		}
		if err := a.addIssueSupplementBefore(ct, request.IssueID, request.Text, func(*issueSupplement) {
			if contextChanged {
				issue.Context.items = mergedContext
			}
		}); err != nil {
			issue.Context.items = previousContext
			callErr = err
			return
		}
		callErr = a.issueToolSetReceipt(operation, &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: request.IssueID,
			RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		})
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	var receipt *IssueToolReceipt
	a.runOnUI(func() { receipt = cloneIssueToolReceipt(operation.Receipt) })
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolHandleWorkWait(_ context.Context, raw json.RawMessage) (issueToolForwardResponse, error) {
	scopeRef, err := issueToolArgumentString(raw, "scope")
	if err != nil {
		return issueToolErrorResponse("invalid_arguments", err.Error())
	}
	text, err := issueToolArgumentString(raw, "text")
	if err != nil || text == "" {
		return issueToolErrorResponse("invalid_arguments", "text is required")
	}
	var scope IssueToolScope
	var revision int
	var resolveErr error
	a.runOnUI(func() {
		scope, resolveErr = a.issueToolScopeByRef(scopeRef)
		if resolveErr != nil {
			return
		}
		ct := a.contactByID(scope.ContactID)
		_, execution := ct.executionByID(scope.WorkExecutionID)
		if execution != nil {
			revision = execution.RequirementsRevision
		}
	})
	if resolveErr != nil {
		return issueToolErrorResponse("scope_denied", resolveErr.Error())
	}
	if scope.Mode != issueToolModeWork || scope.WorkIssueID == "" {
		return issueToolErrorResponse("scope_denied", "issue_wait requires an app-issued work scope")
	}
	request := IssueToolActionRequest{
		ScopeRef: scope.Ref, Action: issueToolActionWait, IssueID: scope.WorkIssueID,
		ExpectedRevision: issueToolRevisionPointer(revision), Text: text,
	}
	if revision < 1 {
		return issueToolErrorResponse("revision_unavailable", "the captured work execution has no requirements revision")
	}
	if err := request.Validate(scope); err != nil {
		if toolErr, ok := err.(*IssueToolError); ok {
			return issueToolJSONResponse(toolErr, true)
		}
		return issueToolErrorResponse("invalid_action", err.Error())
	}
	operation, created, existing, receipt, reserveErr := a.issueToolReserveForTurn(scope, request)
	if reserveErr != nil {
		return issueToolConsumeError(reserveErr)
	}
	if existing {
		if receipt == nil {
			return issueToolErrorResponse("operation_unknown", "work wait already recorded without a receipt; not replaying it")
		}
		return issueToolJSONResponse(receipt, receipt.State == issueToolReceiptUnknown)
	}
	if !created {
		return issueToolErrorResponse("operation_unknown", "work wait was not reserved")
	}
	return a.issueToolWorkWait(scope, request, operation)
}

func (a *appModel) issueToolWorkWait(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	var cancel bool
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, scope.WorkIssueID)
		if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
			callErr = errors.New("work issue cannot wait in its current state")
			return
		}
		_, execution := ct.executionByID(scope.WorkExecutionID)
		if execution == nil {
			callErr = errors.New("work execution is not available")
			return
		}
		if execution.TaskID == "" {
			if issue.Work.SendState != sendSubmitting || issue.Work.ActiveTaskID != "" {
				callErr = errors.New("work execution is not awaiting acceptance")
				return
			}
		} else if issue.Work.ActiveTaskID != execution.TaskID {
			callErr = errors.New("work execution is not the active captured task")
			return
		}
		if request.ExpectedRevision == nil || *request.ExpectedRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "the captured work requirement revision is stale; do not retry", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		text := strings.TrimSpace(request.Text)
		supersedeRef := ""
		if open := issue.openQuestion(); open != nil {
			if open.RequirementsRevision == issue.RequirementsRevision && strings.TrimSpace(open.Text) == text {
				receipt = &IssueToolReceipt{
					State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: open.Ref,
					QuestionState: open.State, IssueStatus: issue.Status,
					RequirementsRevision: issueToolRevisionPointer(open.RequirementsRevision),
				}
				callErr = a.issueToolSetReceipt(operation, receipt)
				return
			}
			supersedeRef = open.Ref
		}
		question := &issueQuestion{
			Ref: newIssueQuestionRef(), Text: text,
			RequirementsRevision: issue.RequirementsRevision, SourceTaskID: scope.RuntimeTaskID,
			SourceScopeRef: scope.Ref, SourceTurnID: scope.TurnID,
			State: issueQuestionOpen, CreatedAt: nowMillis(),
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: issueQuestionOpen, IssueStatus: issueStatusWaitingUser,
			RequirementsRevision: issueToolRevisionPointer(question.RequirementsRevision),
		}
		previousWait := execution.WaitRequested
		execution.WaitRequested = true
		if !a.markIssueWaitingForQuestionActive(ct, issue.ID, supersedeRef, question, operation, receipt, true) {
			execution.WaitRequested = previousWait
			callErr = errors.New("work wait could not be saved")
			return
		}
		receipt = cloneIssueToolReceipt(receipt)
		cancel = execution.TaskID != ""
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	if cancel {
		a.runOnUI(func() {
			ct := a.contactByID(scope.ContactID)
			issue := issueForContact(ct, scope.WorkIssueID)
			_, execution := ct.executionByID(scope.WorkExecutionID)
			if issue == nil || execution == nil {
				return
			}
			a.requestIssueWaitStop(ct, issue, execution.ID, execution.SessionID, execution.TaskID)
		})
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolWait(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
			callErr = errors.New("issue cannot wait in its current state")
			return
		}
		if request.ExpectedRevision == nil || *request.ExpectedRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "issue requirements changed; reload before waiting", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "" {
			callErr = errors.New("issue has active or unresolved work; wait cannot be recorded")
			return
		}
		text := strings.TrimSpace(request.Text)
		supersedeRef := ""
		if open := issue.openQuestion(); open != nil {
			if open.RequirementsRevision == issue.RequirementsRevision && strings.TrimSpace(open.Text) == text {
				receipt = &IssueToolReceipt{
					State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: open.Ref,
					QuestionState: open.State, IssueStatus: issue.Status,
					RequirementsRevision: issueToolRevisionPointer(open.RequirementsRevision),
				}
				callErr = a.issueToolSetReceipt(operation, receipt)
				return
			}
			supersedeRef = open.Ref
		}
		question := &issueQuestion{
			Ref: newIssueQuestionRef(), Text: text,
			RequirementsRevision: issue.RequirementsRevision, SourceTaskID: scope.RuntimeTaskID,
			SourceScopeRef: scope.Ref, SourceTurnID: scope.TurnID,
			State: issueQuestionOpen, CreatedAt: nowMillis(),
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
			QuestionState: issueQuestionOpen, IssueStatus: issueStatusWaitingUser,
			RequirementsRevision: issueToolRevisionPointer(question.RequirementsRevision),
		}
		if !a.markIssueWaitingForQuestion(ct, issue.ID, supersedeRef, question, operation, receipt) {
			callErr = errors.New("issue could not be marked waiting and saved")
			return
		}
		receipt = cloneIssueToolReceipt(receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolResume(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil {
			callErr = errors.New("issue not found")
			return
		}
		if !a.resumeIssueFor(ct, issue.ID) {
			callErr = errors.New(valueOr(a.issueActionError, "issue cannot resume in its current state"))
			return
		}
		receipt = &IssueToolReceipt{
			State: issueToolReceiptSaved, IssueID: issue.ID, IssueStatus: issue.Status,
			RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
		}
		if question := issue.openQuestion(); question != nil {
			receipt.QuestionRef, receipt.QuestionState = question.Ref, question.State
		}
		callErr = a.issueToolSetReceipt(operation, receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(cloneIssueToolReceipt(receipt), false)
}

func (a *appModel) issueToolAnswerQuestion(scope IssueToolScope, request IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	var receipt *IssueToolReceipt
	var callErr error
	a.runOnUI(func() {
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
		ct := a.contactByID(scope.ContactID)
		issue := issueForContact(ct, request.IssueID)
		if issue == nil {
			callErr = &IssueToolError{Code: "not_found", Message: "issue not found for this question"}
			return
		}
		question := issue.questionByRef(request.QuestionRef)
		if question == nil {
			candidates := make([]string, 0)
			if open := issue.openQuestion(); open != nil {
				candidates = append(candidates, open.Ref)
			}
			callErr = &IssueToolError{Code: "question_not_found", Message: "question_ref is not attached to this issue", Candidates: candidates}
			return
		}
		if question.State == issueQuestionAnswered {
			answer := issueSupplementLedgerFor(issue).itemByID(question.AnswerSupplementID)
			if answer == nil {
				callErr = &IssueToolError{Code: "operation_unknown", Message: "the saved answer supplement is missing; reload before revising"}
				return
			}
			if strings.TrimSpace(request.Text) != strings.TrimSpace(answer.Text) {
				callErr = &IssueToolError{Code: "question_answer_conflict", Message: "this question already has a different answer; reload before revising"}
				return
			}
			receipt = &IssueToolReceipt{
				State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
				QuestionState: question.State, IssueStatus: issue.Status,
				SupplementID:         question.AnswerSupplementID,
				RequirementsRevision: issueToolRevisionPointer(question.RequirementsRevision),
			}
			callErr = a.issueToolSetReceipt(operation, receipt)
			return
		}
		if request.ExpectedRevision != nil && *request.ExpectedRevision != issue.RequirementsRevision ||
			question.RequirementsRevision != issue.RequirementsRevision {
			callErr = &IssueToolError{Code: "revision_mismatch", Message: "question or issue was changed; reload before answering", CurrentRevision: issueToolRevisionPointer(issue.RequirementsRevision)}
			return
		}
		if issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
			callErr = errors.New("issue cannot accept this answer in its current state")
			return
		}
		latestScope, latestErr := a.issueToolScopeByRef(scope.Ref)
		if latestErr != nil {
			callErr = latestErr
			return
		}
		captured, captureErr := a.issueToolCaptureTurnContext(ct, latestScope)
		if captureErr != nil {
			callErr = captureErr
			return
		}
		ledger := issueSupplementLedgerFor(issue)
		previousQuestion := *question
		previousStatus, previousUpdated, previousAuto := issue.Status, issue.UpdatedAt, ledger.AutoContinue
		previousContext := issue.Context.snapshot()
		mergedContext := mergeIssueToolTurnContext(issue.Context.items, captured)
		contextChanged := len(mergedContext) != len(issue.Context.items)
		if contextChanged {
			if err := validateTaskContext(mergedContext); err != nil {
				callErr = err
				return
			}
		}
		if err := a.addIssueSupplementBefore(ct, issue.ID, request.Text, func(item *issueSupplement) {
			if contextChanged {
				issue.Context.items = mergedContext
			}
			question.State, question.AnsweredAt, question.AnswerSupplementID = issueQuestionAnswered, nowMillis(), item.ID
			if len(issue.Executions) > 0 {
				issue.Status, ledger.AutoContinue = issueStatusInProgress, true
			} else {
				issue.Status, ledger.AutoContinue = issueStatusNotStarted, false
			}
			issue.UpdatedAt = nowMillis()
			receipt = &IssueToolReceipt{
				State: issueToolReceiptSaved, IssueID: issue.ID, QuestionRef: question.Ref,
				QuestionState: issueQuestionAnswered, IssueStatus: issue.Status,
				SupplementID: item.ID, RequirementsRevision: issueToolRevisionPointer(issue.RequirementsRevision),
			}
			operation.Receipt = receipt
		}); err != nil {
			*question = previousQuestion
			issue.Status, issue.UpdatedAt, ledger.AutoContinue = previousStatus, previousUpdated, previousAuto
			issue.Context.items = previousContext
			operation.Receipt = nil
			callErr = err
			return
		}
		receipt = cloneIssueToolReceipt(receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(receipt, false)
}

func (a *appModel) issueToolRun(scope IssueToolScope, _ IssueToolActionRequest, operation *IssueToolOperation) (issueToolForwardResponse, error) {
	waiter := make(chan IssueToolReceipt, 1)
	var callErr error
	a.runOnUI(func() {
		if a.shutdownStarted {
			callErr = errors.New("app is shutting down")
			return
		}
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
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
			State: issueToolReceiptDispatching, IssueID: issue.ID, IssueStatus: issue.Status, ExecutionID: execution.ID,
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
		if err := a.issueToolActionAllowed(scope); err != nil {
			callErr = err
			return
		}
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
			State: issueToolReceiptStopRequested, IssueID: issue.ID, IssueStatus: issue.Status,
			ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
		}
		callErr = a.issueToolSetReceipt(operation, receipt)
	})
	if callErr != nil {
		return issueActionErrorResponse(callErr)
	}
	return issueToolJSONResponse(cloneIssueToolReceipt(receipt), false)
}

func (a *appModel) requestIssueWaitStop(ct *contact, issue *productIssue, executionID, sessionID, taskID string) bool {
	if a.rt == nil || ct == nil || issue == nil || executionID == "" || taskID == "" || sessionID == "" ||
		issue.Work.ActiveTaskID != taskID || issue.WorkSessionID != sessionID {
		return false
	}
	owner, execution := ct.executionByID(executionID)
	if owner != issue || execution == nil || execution.TaskID != taskID || execution.SessionID != sessionID {
		return false
	}
	if w := workFor(ct, taskID); w != nil {
		w.State = workStopping
		w.StopRequested = true
	}
	issue.Work.SendError = "Waiting for your answer; stopping the current execution."
	rt, generation := a.rt, a.connectionGeneration
	go func() {
		status, err := rt.cancelTask(sessionID, taskID)
		a.updateUI(func() {
			if a.rt != rt || a.connectionGeneration != generation {
				return
			}
			a.applyIssueCancelResult(ct.ID, issue.ID, taskID, status, err)
		})
	}()
	return true
}

func issueActionErrorResponse(err error) (issueToolForwardResponse, error) {
	if toolErr, ok := err.(*IssueToolError); ok {
		return issueToolJSONResponse(toolErr, true)
	}
	return issueToolErrorResponse("operation_unknown", err.Error())
}
