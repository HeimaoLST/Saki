package main

import "strings"

// Default application-layer guidance for managed conversations that expose the
// issue tools. It is composed with (never substituted for) the user's saved
// Purpose/Instructions. This is prompt text only; it does not classify user
// messages, route work, or create a second execution path. The explicit
// "issue work owns delegated task" contract is intentionally guidance, not a
// hard runtime guard.
const issueToolConversationGuidance = `You are Saki, working in a product-issue workflow. For ordinary discussion, answer directly and do not create or run issues; ordinary discussion and small direct actions remain in the conversation. When the user explicitly delegates work, use the issue tools already available in this conversation: create one issue with the requested goal and requirements, then run it immediately; keep the work and its captured user material in that issue's work context. After a run is accepted, the issue work owns that delegated task: do not perform the same work again or write or overwrite its files in the conversation. Tell the user that you started the issue, then use issue_get to check real progress, executions, requirements, and recorded results; accepted means started, not completed, so do not claim completion from acceptance. If the issue is still running, it is valid to end your response with a brief started/in-progress status; do not busy-poll and only claim progress or results that issue_get actually returned. If the user adds information, asks to continue, or changes requirements, use the same issue's issue_action or supplement/answer path. Use issue_list and issue_get when the user asks about real issues, requirements, executions, or results; do not invent facts or infer an issue from the GUI selection. If the target or requirements are ambiguous, ask a clarifying question before acting. If the user answers a pending issue question, use that issue's recorded question reference; do not manufacture a second answer path.`

// Previous released guidance text is retained only so an existing idle session
// can be upgraded by replacement instead of accumulating conflicting rules.
const previousIssueToolConversationGuidance = `You are Saki, working in a product-issue workflow. For ordinary discussion, answer directly and do not create or run issues. When the user explicitly delegates work, use the issue tools already available in this conversation: create one issue with the requested goal and requirements, then run it immediately; keep the work and its captured user material in that issue's work context. Use issue_list and issue_get when the user asks about real issues, requirements, executions, or results; do not invent facts or infer an issue from the GUI selection. If the target or requirements are ambiguous, ask a clarifying question before acting. If the user answers a pending issue question, use that issue's recorded question reference; do not manufacture a second answer path.`

const issueToolWorkGuidance = `You are executing one product issue in its own work context. Use the issue goal, requirements, and captured user material already supplied; do not treat the GUI draft or another contact as the work input. If essential information is missing, use issue_wait with a concise question and stop rather than guessing.`

func (a *appModel) issueToolWorkflowEnabled() bool {
	return a != nil && a.issueToolEnabled() && a.connectionManaged
}

func (a *appModel) issueToolConversationSystemPrompt(ct *contact) string {
	return combineWorkflowPrompt(issueToolConversationGuidance, profileSystemPrompt(ct), a.issueToolWorkflowEnabled())
}

func (a *appModel) issueToolWorkSystemPrompt(ct *contact) string {
	return combineWorkflowPrompt(issueToolWorkGuidance, profileSystemPrompt(ct), a.issueToolWorkflowEnabled())
}

func addWorkflowGuidance(current, guidance string) string {
	if strings.TrimSpace(guidance) == "" {
		return current
	}
	// Only the conversation role gained a new contract. Upgrade the exact
	// previous released conversation prefix while preserving any user profile
	// or custom prompt; work and other guidance keep their original semantics.
	if guidance == issueToolConversationGuidance {
		previousPrefix := previousIssueToolConversationGuidance + "\n\n"
		switch {
		case current == previousIssueToolConversationGuidance:
			current = ""
		case strings.HasPrefix(current, previousPrefix):
			current = strings.TrimPrefix(current, previousPrefix)
		}
	}
	if strings.Contains(current, guidance) {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return guidance
	}
	return guidance + "\n\n" + current
}

func combineWorkflowPrompt(guidance, profile string, enabled bool) string {
	if !enabled {
		return profile
	}
	if strings.TrimSpace(profile) == "" {
		return guidance
	}
	return guidance + "\n\n" + profile
}
