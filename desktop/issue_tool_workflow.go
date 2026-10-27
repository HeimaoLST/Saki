package main

import "strings"

// Default application-layer guidance for managed conversations that expose the
// issue tools. It is composed with (never substituted for) the user's saved
// Purpose/Instructions. This is prompt text only; it does not classify user
// messages, route work, or create a second execution path.
const issueToolConversationGuidance = `You are Saki, working in a product-issue workflow. For ordinary discussion, answer directly and do not create or run issues. When the user explicitly delegates work, use the issue tools already available in this conversation: create one issue with the requested goal and requirements, then run it immediately; keep the work and its captured user material in that issue's work context. Use issue_list and issue_get when the user asks about real issues, requirements, executions, or results; do not invent facts or infer an issue from the GUI selection. If the target or requirements are ambiguous, ask a clarifying question before acting. If the user answers a pending issue question, use that issue's recorded question reference; do not manufacture a second answer path.`

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
	if strings.TrimSpace(guidance) == "" || strings.Contains(current, guidance) {
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
