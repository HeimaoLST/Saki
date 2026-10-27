package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIssueToolCreateCarriesConversationSnapshotIntoIssueContext(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := newIssueToolChainProvider()
	h := startIssueToolTestHarness(t, binary, provider.handler())
	t.Cleanup(provider.releaseFixture)
	material := "SNAPSHOT-ONLY-CONTEXT-154\n项目代号：青岚\n负责人：林\n"
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("fixture-material", material); err != nil {
			sendErr = err.Error()
			return
		}
		ct.Draft = issueToolTestMarker
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	select {
	case <-provider.arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive the conversation request")
	}
	h.pump.wait(func() bool { return len(h.app.issueToolScopes) == 1 })
	var scopeRef, conversationTaskID string
	h.pump.do(func() {
		for ref, scope := range h.app.issueToolScopes {
			scopeRef = ref
			conversationTaskID = scope.RuntimeTaskID
		}
	})
	if scopeRef == "" {
		t.Fatal("no app-issued scope")
	}
	provider.mu.Lock()
	provider.scope = scopeRef
	provider.mu.Unlock()
	provider.releaseFixture()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct == nil || len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 1 {
			return false
		}
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" &&
			ct.Issues[0].Executions[0].Status == issueExecutionCompleted &&
			ct.Issues[0].Work.ActiveTaskID == ""
	})
	var issue *productIssue
	var userMessage *message
	var draftItems int
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue = ct.Issues[0]
		draftItems = len(ct.Context.items)
		for _, message := range ct.Messages {
			if message.Kind == msgUser && message.TaskID == conversationTaskID {
				userMessage = message
				break
			}
		}
	})
	if issue == nil {
		t.Fatal("issue was not created")
	}
	if draftItems != 0 {
		t.Fatalf("accepted conversation material remained in the next-message draft: %d", draftItems)
	}
	if userMessage == nil || len(userMessage.Context) != 1 || userMessage.Context[0].Name != "fixture-material" ||
		userMessage.Context[0].Text != material {
		t.Fatalf("conversation user message did not retain the submitted snapshot: %+v", userMessage)
	}
	if !strings.Contains(provider.body(0), issueToolConversationGuidance) {
		t.Fatal("first conversation request did not carry the application workflow guidance")
	}
	if len(issue.Context.items) != 0 {
		t.Fatalf("accepted issue context was not cleared from the next-run draft: %+v", issue.Context.items)
	}
	execution := issue.Executions[0]
	if strings.Contains(execution.Prompt, material) {
		t.Fatalf("material was hand-copied into the issue prompt: %q", execution.Prompt)
	}
	if len(execution.Context) != 1 || execution.Context[0].Name != "fixture-material" ||
		execution.Context[0].Text != material {
		t.Fatalf("execution context does not match the captured issue context: %+v", execution.Context)
	}
	var workBody string
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, issueToolTestIssueGoal) && !strings.Contains(body, issueToolTestMarker) {
			workBody = body
			break
		}
	}
	if workBody == "" {
		t.Fatal("provider did not receive the issue work request")
	}
	found := false
	for _, context := range taskContextCallerContexts(t, workBody) {
		if context.Name == "fixture-material" && context.Text == material {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("issue work request did not carry the captured caller context")
	}
	if !strings.Contains(workBody, issueToolWorkGuidance) {
		t.Fatal("issue work request did not carry the work workflow guidance")
	}
}

func issueToolContextIssueID(body, callID string) string {
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	for index := len(payload.Input) - 1; index >= 0; index-- {
		input := payload.Input[index]
		if input.Type != "function_call_output" || input.CallID != callID {
			continue
		}
		var output struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(input.Output), &output) != nil {
			return ""
		}
		for _, content := range output.Content {
			var receipt struct {
				IssueID string `json:"issue_id"`
			}
			if json.Unmarshal([]byte(content.Text), &receipt) == nil && receipt.IssueID != "" {
				return receipt.IssueID
			}
		}
	}
	return ""
}

type issueToolContextTwoTurnProvider struct {
	mu       sync.Mutex
	convStep map[string]int
	workStep map[string]int
	bodies   []string
}

func newIssueToolContextTwoTurnProvider() *issueToolContextTwoTurnProvider {
	return &issueToolContextTwoTurnProvider{convStep: map[string]int{}, workStep: map[string]int{}}
}

func (p *issueToolContextTwoTurnProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolContextTwoTurnProvider) body(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.bodies) {
		return ""
	}
	return p.bodies[index]
}

func (p *issueToolContextTwoTurnProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		turn := ""
		switch {
		case strings.Contains(text, "CONTEXT-TURN-B"):
			turn = "B"
		case strings.Contains(text, "CONTEXT-TURN-A"):
			turn = "A"
		}
		goal := ""
		if strings.Contains(text, "CONTEXT-GOAL-B") {
			goal = "CONTEXT-GOAL-B"
		} else if strings.Contains(text, "CONTEXT-GOAL-A") {
			goal = "CONTEXT-GOAL-A"
		}
		isWork := turn == "" && goal != ""
		scope := issueToolTestScope(text)
		p.mu.Lock()
		p.bodies = append(p.bodies, text)
		if isWork {
			step := p.workStep[goal]
			p.workStep[goal]++
			p.mu.Unlock()
			var response any
			if step == 0 {
				response = issueResultFunctionCall("write", "work-write-"+goal, map[string]any{
					"path": "work-" + goal + ".txt", "content": "work output",
				})
			} else {
				response = issueResultPageFinal("WORK-FINAL-" + goal)
			}
			data, _ := json.Marshal(response)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		if turn == "" {
			p.mu.Unlock()
			http.Error(w, "missing context turn", http.StatusBadRequest)
			return
		}
		step := p.convStep[turn]
		p.convStep[turn]++
		p.mu.Unlock()
		var response any
		switch step {
		case 0:
			response = issueResultFunctionCall("issue_action", "tool-create-"+turn, map[string]any{
				"scope": scope, "action": "create", "goal": "CONTEXT-GOAL-" + turn,
				"requirements": []string{"CONTEXT-REQ-" + turn},
			})
		case 1:
			issueID := issueToolContextIssueID(text, "tool-create-"+turn)
			response = issueResultFunctionCall("issue_action", "tool-run-"+turn, map[string]any{
				"scope": scope, "action": "run", "issue_id": issueID,
			})
		default:
			response = issueResultPageFinal("CONVERSATION-FINAL-" + turn)
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func waitIssueToolIssue(t *testing.T, h *guiHarness, goal string) *productIssue {
	t.Helper()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct == nil || ct.SendState != sendIdle || ct.ActiveTaskID != "" {
			return false
		}
		for _, issue := range ct.Issues {
			if issue != nil && issue.Goal == goal && len(issue.Executions) == 1 &&
				issue.Executions[0].Status == issueExecutionCompleted && issue.Work.ActiveTaskID == "" {
				return true
			}
		}
		return false
	})
	var out *productIssue
	h.pump.do(func() {
		for _, issue := range h.app.selectedContact().Issues {
			if issue != nil && issue.Goal == goal {
				out = issue
				break
			}
		}
	})
	return out
}

func TestIssueToolCreateContextIsTurnScopedAndRestartSafe(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := newIssueToolContextTwoTurnProvider()
	h := startIssueToolTestHarness(t, binary, provider.handler())
	materialA := "TURN-A-SNAPSHOT\n青岚-A\n"
	materialB := "TURN-B-SNAPSHOT\n青岚-B\n"
	var sendErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("material-A", materialA); err != nil {
			sendErr = err
			return
		}
		ct.Draft = "CONTEXT-TURN-A"
		if !h.app.sendCurrent() {
			sendErr = fmt.Errorf("A send: %s", ct.SendError)
		}
	})
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	issueA := waitIssueToolIssue(t, h, "CONTEXT-GOAL-A")
	if issueA == nil || len(issueA.Executions) != 1 {
		t.Fatal("issue A did not complete")
	}
	execA := issueA.Executions[0]
	if len(execA.Context) != 1 || execA.Context[0].Name != "material-A" || execA.Context[0].Text != materialA {
		t.Fatalf("A execution context = %+v", execA.Context)
	}
	var bodyA string
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, "CONTEXT-GOAL-A") && !strings.Contains(body, "CONTEXT-TURN-A") {
			bodyA = body
			break
		}
	}
	if bodyA == "" {
		t.Fatal("A work request missing")
	}
	contextsA := taskContextCallerContexts(t, bodyA)
	foundA := false
	for _, context := range contextsA {
		if context.Name == "material-A" && context.Text == materialA {
			foundA = true
		}
		if context.Name == "material-B" || context.Text == materialB {
			t.Fatalf("A work received B material: %+v", contextsA)
		}
	}
	if !foundA {
		t.Fatalf("A work contexts = %+v", contextsA)
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if len(ct.Context.items) != 0 {
			sendErr = fmt.Errorf("A left context in draft: %+v", ct.Context.items)
			return
		}
		if _, err := ct.Context.AddSnippet("material-B", materialB); err != nil {
			sendErr = err
			return
		}
		ct.Draft = "CONTEXT-TURN-B"
		if !h.app.sendCurrent() {
			sendErr = fmt.Errorf("B send: %s", ct.SendError)
		}
	})
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	issueB := waitIssueToolIssue(t, h, "CONTEXT-GOAL-B")
	if issueB == nil || len(issueB.Executions) != 1 {
		t.Fatal("issue B did not complete")
	}
	execB := issueB.Executions[0]
	if len(execB.Context) != 1 || execB.Context[0].Name != "material-B" || execB.Context[0].Text != materialB {
		t.Fatalf("B execution context = %+v", execB.Context)
	}
	var bodyB string
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, "CONTEXT-GOAL-B") && !strings.Contains(body, "CONTEXT-TURN-B") {
			bodyB = body
			break
		}
	}
	if bodyB == "" {
		t.Fatal("B work request missing")
	}
	contextsB := taskContextCallerContexts(t, bodyB)
	foundB := false
	for _, context := range contextsB {
		if context.Name == "material-B" && context.Text == materialB {
			foundB = true
		}
	}
	if !foundB {
		t.Fatalf("B work contexts = %+v", contextsB)
	}
	if strings.Contains(bodyB, materialA) {
		t.Fatal("A material was broadcast into B work")
	}
	var bDraftErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if len(ct.Context.items) != 0 {
			bDraftErr = fmt.Sprintf("B left context in draft: %+v", ct.Context.items)
		}
	})
	if bDraftErr != "" {
		t.Fatal(bDraftErr)
	}
	callsBeforeRestart := provider.count()
	h.shutdown(t)
	restarted := restartGUIHarness(t, binary)
	t.Cleanup(func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	})
	restarted.pump.wait(func() bool {
		ct := restarted.app.selectedContact()
		return ct != nil && ct.Loaded
	})
	var restartedA, restartedB *productIssue
	restarted.pump.do(func() {
		for _, issue := range restarted.app.selectedContact().Issues {
			switch issue.Goal {
			case "CONTEXT-GOAL-A":
				restartedA = issue
			case "CONTEXT-GOAL-B":
				restartedB = issue
			}
		}
	})
	if restartedA == nil || restartedB == nil || len(restartedA.Executions) != 1 || len(restartedB.Executions) != 1 {
		t.Fatalf("restart lost issues: A=%+v B=%+v", restartedA, restartedB)
	}
	if restartedA.Executions[0].Context[0].Text != materialA || restartedB.Executions[0].Context[0].Text != materialB {
		t.Fatal("restart changed execution context association")
	}
	if provider.count() != callsBeforeRestart {
		t.Fatal("restart dispatched provider work")
	}
}

func TestIssueToolCreateContextPreacceptAndRejectBoundary(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-context-boundary", "turn-context-boundary")
	if err != nil {
		t.Fatal(err)
	}
	ct.SessionID = scope.RuntimeSession()
	ct.Context.items = []taskContextItem{{ID: "draft-only", Name: "draft", Text: "must not be copied", Revision: 1}}
	pendingItem := taskContextItem{ID: "pending-context", Name: "snapshot", Text: "preaccept snapshot", Revision: 1}
	ct.pendingSend = &pendingSend{ToolScopeRef: scope.Ref, Context: []taskContextItem{pendingItem}}
	if got := a.issueToolTurnContext(ct, *scope); len(got) != 1 || got[0].Text != pendingItem.Text {
		t.Fatalf("preaccept context = %+v", got)
	}
	ct.pendingSend.ToolScopeRef = "different-scope"
	if got := a.issueToolTurnContext(ct, *scope); got != nil {
		t.Fatalf("unrelated pending context was copied: %+v", got)
	}
	ct.pendingSend = nil
	scope.RuntimeTaskID = "task-context-boundary"
	ct.Messages = []*message{{
		Kind: msgUser, TaskID: scope.RuntimeTaskID,
		Context: []taskContextItem{{ID: "live-context", Name: "snapshot", Text: "accepted snapshot", Revision: 1}},
	}}
	if got := a.issueToolTurnContext(ct, *scope); len(got) != 1 || got[0].Text != "accepted snapshot" {
		t.Fatalf("accepted msgUser context = %+v", got)
	}
	scope.RuntimeSessionID = "different-session"
	if got := a.issueToolTurnContext(ct, *scope); got != nil {
		t.Fatalf("cross-owner context was copied: %+v", got)
	}
}

func TestIssueToolCreateContextPersistsOnceAndDoesNotDispatch(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-duplicate-context", "turn-duplicate-context")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	item := taskContextItem{ID: "duplicate-context", Name: "snapshot", Text: "one captured snapshot", Revision: 1}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{item}}}
	args := issueToolActionArgs(t, *scope, issueToolActionCreate, map[string]any{"goal": "context duplicate goal"})
	first, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("first create = %+v err=%v", first, err)
	}
	if len(ct.Issues) != 1 || len(ct.Issues[0].Context.items) != 1 || ct.Issues[0].Context.items[0].Text != item.Text {
		t.Fatalf("first issue context = %+v", ct.Issues)
	}
	second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: args})
	if err != nil || second.IsError {
		t.Fatalf("duplicate create = %+v err=%v", second, err)
	}
	if len(ct.Issues) != 1 || len(a.issueToolLedger.Operations) != 1 || len(ct.Issues[0].Executions) != 0 ||
		len(ct.Issues[0].Context.items) != 1 || ct.Issues[0].Context.items[0].Text != item.Text {
		t.Fatalf("duplicate changed context/dispatch: issues=%d ops=%d executions=%d context=%+v",
			len(ct.Issues), len(a.issueToolLedger.Operations), len(ct.Issues[0].Executions), ct.Issues[0].Context.items)
	}
}

func TestIssueToolSupplementAndAnswerCarryTurnContext(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	issue, err := a.createIssueFor(ct, "context supplement goal", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.createIssueFor(ct, "unrelated issue", "")
	if err != nil {
		t.Fatal(err)
	}
	a.issueToolAddScopeIssue(scope.Ref, issue.ID)
	itemC := taskContextItem{ID: "material-C", Name: "supplement-material", Text: "supplement attachment", Revision: 1}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{itemC}}}
	addArgs := issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "use the supplement material",
	})
	addResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: addArgs})
	if err != nil || addResponse.IsError {
		t.Fatalf("add_supplement = %+v err=%v", addResponse, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != itemC.ID || issue.Context.items[0].Text != itemC.Text {
		t.Fatalf("supplement issue context = %+v", issue.Context.items)
	}
	if len(other.Context.items) != 0 {
		t.Fatalf("supplement context broadcast to another issue: %+v", other.Context.items)
	}
	if len(issue.Executions) != 0 {
		t.Fatal("supplement on a never-run issue auto-dispatched a run")
	}
	// A later turn carrying the same captured item must reuse the existing
	// context ID/revision rather than append it again.
	scope2, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context-2")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope2)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope2.RuntimeTaskID, Context: []taskContextItem{itemC}}}
	secondArgs := issueToolActionArgs(t, *scope2, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "text": "second text from the same turn",
	})
	if second, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: secondArgs}); err != nil || second.IsError {
		t.Fatalf("second add_supplement = %+v err=%v", second, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != itemC.ID {
		t.Fatalf("same-turn supplement duplicated context: %+v", issue.Context.items)
	}
	// A distinct answer turn with an attachment links the answer supplement and
	// the captured context in the existing AddWith save, without a first Run.
	question := &issueQuestion{Ref: "question-context-test", Text: "which material?", RequirementsRevision: issue.RequirementsRevision, State: issueQuestionOpen}
	issue.Questions = append(issue.Questions, question)
	itemD := taskContextItem{ID: "material-D", Name: "answer-material", Text: "answer attachment", Revision: 1}
	scope3, err := a.issueToolNewScope(ct.ID, "session-supplement-context", "turn-supplement-context-3")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope3)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope3.RuntimeTaskID, Context: []taskContextItem{itemD}}}
	answerArgs := issueToolActionArgs(t, *scope3, issueToolActionAddSupplement, map[string]any{
		"issue_id": issue.ID, "question_ref": question.Ref, "text": "answer with attachment",
	})
	answerResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: answerArgs})
	if err != nil || answerResponse.IsError {
		t.Fatalf("answer = %+v err=%v", answerResponse, err)
	}
	if question.State != issueQuestionAnswered || question.AnswerSupplementID == "" {
		t.Fatalf("answer state = %+v", question)
	}
	if len(issue.Context.items) != 2 || issue.Context.items[1].ID != itemD.ID || issue.Context.items[1].Text != itemD.Text {
		t.Fatalf("answer issue context = %+v", issue.Context.items)
	}
	if len(issue.Executions) != 0 {
		t.Fatal("answer on a never-run issue auto-dispatched a run")
	}
}

func TestIssueToolSupplementAndAnswerSaveFailureRestoreContext(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	ct := a.selectedContact()
	scope, err := a.issueToolNewScope(ct.ID, "session-save-failure", "turn-save-failure")
	if err != nil {
		t.Fatal(err)
	}
	activateIssueToolTestScope(t, a, scope)
	issue, err := a.createIssueFor(ct, "save failure context", "")
	if err != nil {
		t.Fatal(err)
	}
	a.issueToolAddScopeIssue(scope.Ref, issue.ID)
	initial := taskContextItem{ID: "initial-context", Name: "initial", Text: "keep me", Revision: 1}
	issue.Context.items = []taskContextItem{initial}
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{{ID: "new-supplement", Name: "new", Text: "must rollback", Revision: 1}}}}
	a.store.path = t.TempDir()
	addResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{"issue_id": issue.ID, "text": "will not save"}),
	})
	if err != nil || !addResponse.IsError {
		t.Fatalf("supplement save failure = %+v err=%v", addResponse, err)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != initial.ID {
		t.Fatalf("supplement context was not restored: %+v", issue.Context.items)
	}
	if issue.Supplements.pendingCount() != 0 {
		t.Fatalf("supplement item survived failed save: %d", issue.Supplements.pendingCount())
	}
	question := &issueQuestion{Ref: "question-save-failure", Text: "needs answer", RequirementsRevision: issue.RequirementsRevision, State: issueQuestionOpen}
	issue.Questions = append(issue.Questions, question)
	ct.Messages = []*message{{Kind: msgUser, TaskID: scope.RuntimeTaskID, Context: []taskContextItem{{ID: "answer-context", Name: "answer", Text: "must rollback too", Revision: 1}}}}
	answerResponse, err := a.handleIssueToolForward(context.Background(), issueToolForwardRequest{
		Tool: "issue_action", Arguments: issueToolActionArgs(t, *scope, issueToolActionAddSupplement, map[string]any{
			"issue_id": issue.ID, "question_ref": question.Ref, "text": "will not save answer",
		}),
	})
	if err != nil || !answerResponse.IsError {
		t.Fatalf("answer save failure = %+v err=%v", answerResponse, err)
	}
	if question.State != issueQuestionOpen || question.AnswerSupplementID != "" {
		t.Fatalf("answer state was not restored: %+v", question)
	}
	if len(issue.Context.items) != 1 || issue.Context.items[0].ID != initial.ID {
		t.Fatalf("answer context was not restored: %+v", issue.Context.items)
	}
	if issue.Supplements.pendingCount() != 0 {
		t.Fatalf("answer supplement survived failed save: %d", issue.Supplements.pendingCount())
	}
}

type issueToolAnswerContextProvider struct {
	mu          sync.Mutex
	createStep  int
	answerStep  int
	workCalls   int
	bodies      []string
	questionRef string
}

func (p *issueToolAnswerContextProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolAnswerContextProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueToolAnswerContextProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		scope := issueToolTestScope(text)
		isCreateTurn := strings.Contains(text, "ANSWER-CREATE")
		isAnswerTurn := strings.Contains(text, "ANSWER-ANSWER")
		isWork := !isCreateTurn && !isAnswerTurn && strings.Contains(text, "ANSWER-GOAL")
		var response any
		p.mu.Lock()
		p.bodies = append(p.bodies, text)
		if isWork {
			p.workCalls++
			call := p.workCalls
			p.mu.Unlock()
			if call == 1 {
				response = issueResultFunctionCall("issue_wait", "answer-wait", map[string]any{
					"scope": scope, "text": "ANSWER-MISSING-INFORMATION",
				})
			} else {
				response = issueResultPageFinal("ANSWER-WORK-FINAL")
			}
			data, _ := json.Marshal(response)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		if isAnswerTurn {
			step := p.answerStep
			p.answerStep++
			issueID := issueToolContextIssueID(text, "answer-create")
			ref := p.questionRef
			p.mu.Unlock()
			if step == 0 && issueID != "" && ref != "" {
				response = issueResultFunctionCall("issue_action", "answer-supplement", map[string]any{
					"scope": scope, "action": "add_supplement", "issue_id": issueID,
					"question_ref": ref, "text": "ANSWER-WITH-ATTACHMENT",
				})
			} else {
				response = issueResultPageFinal("ANSWER-CONVERSATION-FINAL")
			}
		} else if isCreateTurn {
			step := p.createStep
			p.createStep++
			p.mu.Unlock()
			switch step {
			case 0:
				response = issueResultFunctionCall("issue_action", "answer-create", map[string]any{
					"scope": scope, "action": "create", "goal": "ANSWER-GOAL",
					"requirements": []string{"ANSWER-REQUIREMENT"},
				})
			case 1:
				issueID := issueToolContextIssueID(text, "answer-create")
				response = issueResultFunctionCall("issue_action", "answer-run", map[string]any{
					"scope": scope, "action": "run", "issue_id": issueID,
				})
			default:
				response = issueResultPageFinal("ANSWER-CONVERSATION-FINAL")
			}
		} else {
			p.mu.Unlock()
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func TestIssueToolAnswerQuestionCarriesAttachmentThroughRealChain(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolAnswerContextProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	materialA := "ANSWER-TURN-A-MATERIAL\n青岚-A\n"
	materialB := "ANSWER-TURN-B-MATERIAL\n青岚-B\n"
	var sendErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("answer-material-A", materialA); err != nil {
			sendErr = err
			return
		}
		ct.Draft = "ANSWER-CREATE"
		if !h.app.sendCurrent() {
			sendErr = fmt.Errorf("create send: %s", ct.SendError)
		}
	})
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct == nil || ct.ActiveTaskID != "" || ct.SendState != sendIdle {
			return false
		}
		for _, issue := range ct.Issues {
			if issue != nil && issue.Goal == "ANSWER-GOAL" && issue.openQuestion() != nil &&
				issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
				len(issue.Executions) > 0 && (issue.Executions[0].Status == issueExecutionInterrupted ||
				issue.Executions[0].Status == issueExecutionCompleted || issue.Executions[0].Status == issueExecutionFailed) {
				return true
			}
		}
		return false
	})
	var issueID, questionRef string
	h.pump.do(func() {
		for _, issue := range h.app.selectedContact().Issues {
			if issue != nil && issue.Goal == "ANSWER-GOAL" {
				issueID = issue.ID
				if question := issue.openQuestion(); question != nil {
					questionRef = question.Ref
				}
			}
		}
	})
	if issueID == "" || questionRef == "" {
		t.Fatal("issue question was not created")
	}
	provider.mu.Lock()
	provider.questionRef = questionRef
	provider.mu.Unlock()
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("answer-material-B", materialB); err != nil {
			sendErr = err
			return
		}
		ct.Draft = "ANSWER-ANSWER"
		if !h.app.sendCurrent() {
			sendErr = fmt.Errorf("answer send: %s", ct.SendError)
		}
	})
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct == nil || ct.SendState != sendIdle || ct.ActiveTaskID != "" {
			return false
		}
		for _, issue := range ct.Issues {
			if issue != nil && issue.ID == issueID && questionRef != "" && issue.openQuestion() == nil &&
				len(issue.Executions) >= 2 && issue.Work.ActiveTaskID == "" &&
				issue.Executions[len(issue.Executions)-1].Status == issueExecutionCompleted {
				return true
			}
		}
		return false
	})
	var issue *productIssue
	h.pump.do(func() {
		issue = h.app.contactByID(h.app.selectedContact().ID).issueByID(issueID)
	})
	if issue == nil || len(issue.Executions) < 2 {
		t.Fatalf("answer workflow did not run: %+v", issue)
	}
	execution := issue.Executions[len(issue.Executions)-1]
	if len(execution.Context) != 1 || execution.Context[0].Name != "answer-material-B" || execution.Context[0].Text != materialB {
		t.Fatalf("answer execution context = %+v", execution.Context)
	}
	if !strings.Contains(execution.Prompt, "ANSWER-WITH-ATTACHMENT") {
		t.Fatalf("answer text missing from execution prompt: %q", execution.Prompt)
	}
	var workBody string
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, "ANSWER-GOAL") && !strings.Contains(body, "ANSWER-CREATE") && !strings.Contains(body, "ANSWER-ANSWER") {
			contexts := taskContextCallerContexts(t, body)

			for _, context := range contexts {
				if context.Name == "answer-material-B" && context.Text == materialB {
					workBody = body
				}
			}
		}
	}
	if workBody == "" {
		t.Fatal("answer continuation work request did not carry the new attachment")
	}
}
