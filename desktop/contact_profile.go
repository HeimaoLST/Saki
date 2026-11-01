package main

// Contact profile draft editor and validation.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

type contactProfileActions struct {
	Save              func(profileDraft)
	SaveAndNew        func(profileDraft)
	Archive           func()
	Restore           func()
	Discard           func()
	RetryBuiltinTools func()
}

type contactProfileEditor struct {
	Draft                 profileDraft
	Archived              bool
	HasConversation       bool
	ActiveWork            bool
	UnknownWork           bool
	Busy                  bool
	Errors                map[string]string
	Error                 string
	Status                string
	BuiltinTools          []runtimeBuiltinTool
	BuiltinToolsLoaded    bool
	BuiltinToolsLoading   bool
	BuiltinToolsError     string
	BuiltinToolsSupported bool
	CanRetryBuiltinTools  bool
	focusField            string
	initial               profileDraft
}

func newContactProfileEditor(draft profileDraft) *contactProfileEditor {
	draft = cloneProfileDraft(draft)
	return &contactProfileEditor{Draft: draft, initial: cloneProfileDraft(draft), focusField: "Name"}
}

func cloneProfileDraft(draft profileDraft) profileDraft {
	draft.BuiltinTools = cloneBuiltinTools(draft.BuiltinTools)
	return draft
}

// SetProfile replaces the host's saved profile and resets the draft. It is
// used after a successful host save, never to optimistically claim success.
func (e *contactProfileEditor) SetProfile(draft profileDraft) {
	e.Busy = false
	e.initial = cloneProfileDraft(draft)
	e.Draft = cloneProfileDraft(draft)
	e.Errors = nil
	e.Error = ""
	e.Status = ""
}

func (e *contactProfileEditor) SetBusy(busy bool, status string) {
	e.Busy = busy
	e.Status = status
}

func (e *contactProfileEditor) SetError(message string) {
	e.Busy = false
	e.Error = message
	e.Status = ""
}

func (e *contactProfileEditor) ResetDraft() {
	e.Draft = cloneProfileDraft(e.initial)
	e.Errors = nil
	e.Error = ""
	e.Status = ""
	e.focusField = "Name"
}

func validateProfileDraft(draft profileDraft) (profileDraft, map[string]string) {
	problems := map[string]string{}
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Workspace = strings.TrimSpace(draft.Workspace)
	if draft.Name == "" {
		problems["Name"] = "Enter a contact name."
	}
	if draft.Workspace != "" {
		if !filepath.IsAbs(draft.Workspace) {
			problems["Workspace"] = "Workspace must be an absolute path, or empty to use the applied default."
		} else if info, err := os.Stat(draft.Workspace); err != nil || !info.IsDir() {
			problems["Workspace"] = "Workspace must be an existing directory."
		}
	}
	return draft, problems
}

func (e *contactProfileEditor) Request(saveAndNew bool) (profileDraft, bool) {
	if e.Busy {
		e.Error = "Saving is already in progress."
		return profileDraft{}, false
	}
	if e.Archived {
		e.Error = "Restore this contact before saving its profile."
		return profileDraft{}, false
	}
	if saveAndNew {
		if !e.HasConversation {
			e.Error = "A new conversation can be started only when an existing conversation exists."
			return profileDraft{}, false
		}
		if e.ActiveWork || e.UnknownWork {
			e.Error = e.activityReason()
			return profileDraft{}, false
		}
	}
	draft, errors := validateProfileDraft(e.Draft)
	e.Errors = errors
	e.Error = ""
	if len(e.Errors) > 0 {
		e.Status = ""
		if e.Errors["Name"] != "" {
			e.focusField = "Name"
		} else {
			e.focusField = "Workspace"
		}
		return profileDraft{}, false
	}
	return draft, true
}

func (e *contactProfileEditor) activityReason() string {
	switch {
	case e.Archived:
		return "This contact is archived. Restore it before editing or starting new work."
	case e.UnknownWork:
		return "Work is still unresolved. Reload to confirm its status before archiving or starting a new conversation."
	case e.ActiveWork:
		return "Active work must finish or stop before archiving or starting a new conversation."
	default:
		return ""
	}
}

func (e *contactProfileEditor) builtinDefaultNames() []string {
	return builtinToolNames(e.BuiltinTools)
}

func (e *contactProfileEditor) setBuiltinDefault(enabled bool) {
	if enabled {
		e.Draft.BuiltinTools = nil
		return
	}
	names := e.builtinDefaultNames()
	e.Draft.BuiltinTools = &names
}

func (e *contactProfileEditor) toggleBuiltinTool(name string, enabled bool) {
	if e.Draft.BuiltinTools == nil {
		names := e.builtinDefaultNames()
		e.Draft.BuiltinTools = &names
	}
	names := append([]string(nil), (*e.Draft.BuiltinTools)...)
	if enabled {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	} else {
		for index, current := range names {
			if current == name {
				names = append(names[:index], names[index+1:]...)
				break
			}
		}
	}
	if names == nil {
		names = []string{}
	}
	e.Draft.BuiltinTools = &names
}

func (e *contactProfileEditor) builtinToolsView(c *ui.Context, t *ui.Theme, editable bool, actions contactProfileActions) {
	if !e.BuiltinToolsSupported {
		if e.BuiltinToolsError != "" {
			ui.Text(c, "Built-in tools").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
			ui.Text(c, e.BuiltinToolsError).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		}
		return
	}
	ui.Text(c, "Built-in tools").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
	if e.BuiltinToolsLoading {
		ui.Text(c, "Loading built-in tools from Runtime…").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		return
	}
	if !e.BuiltinToolsLoaded {
		message := e.BuiltinToolsError
		if message == "" {
			message = "Built-in tools have not been loaded from Runtime yet."
		}
		ui.Text(c, message).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		if e.CanRetryBuiltinTools {
			secondaryButton(c, "Retry built-in tools").Disabled(e.Busy).OnClick(func() {
				if actions.RetryBuiltinTools != nil {
					actions.RetryBuiltinTools()
				}
			})
		}
		return
	}

	defaultNames := e.builtinDefaultNames()
	defaultSelected := e.Draft.BuiltinTools == nil
	defaultLabel := "Use host defaults"
	if len(defaultNames) > 0 {
		defaultLabel += " (" + strings.Join(defaultNames, ", ") + ")"
	}
	defaultValue := defaultSelected
	defaultBox := appTooltip(c, ui.Checkbox(c, &defaultValue, defaultLabel), "Use the built-in tools supplied by Runtime's host default.").FillWidth().MinHeight(32).Disabled(!editable)
	defaultBox.OnChange(func() { e.setBuiltinDefault(defaultValue) })

	for _, tool := range e.BuiltinTools {
		name := tool.Name
		checked := slices.Contains(defaultNames, name)
		if !defaultSelected {
			checked = builtinToolSelectionContains(e.Draft.BuiltinTools, name)
		}
		value := checked
		box := ui.Checkbox(c, &value, name).FillWidth().MinHeight(32).Disabled(!editable)
		if tool.Description != "" {
			box = appTooltip(c, box, tool.Description)
		}
		box.OnChange(func() { e.toggleBuiltinTool(name, value) })
	}
	if e.Draft.BuiltinTools != nil && len(*e.Draft.BuiltinTools) == 0 {
		ui.Text(c, "No built-in tools are enabled. Nagi issue tools remain available for their own workflows.").FontSize(14).LineHeight(1.5).TextColor(t.Warning)
	}
	if e.BuiltinToolsError != "" {
		ui.Text(c, e.BuiltinToolsError).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
	}
}

func (e *contactProfileEditor) view(c *ui.Context, t *ui.Theme, actions contactProfileActions) {
	editable := !e.Busy && !e.Archived
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, "Saving updates this contact's name. Other settings apply when a conversation or issue work session is first created; existing sessions keep their configuration.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		if reason := e.activityReason(); reason != "" {
			ui.Text(c, reason).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		}
		if e.Archived {
			ui.Text(c, "Archived").FontSize(typeAction).FontWeight(500).TextColor(t.Warning)
		}

		ui.Text(c, "Name").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		name := ui.TextInput(c, &e.Draft.Name).Label("Contact name").FontSize(14).Placeholder("Name").Disabled(!editable)
		if e.focusField == "Name" {
			name.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Name"]; message != "" {
			ui.Text(c, message).FontSize(14).TextColor(t.Danger)
		}
		ui.Text(c, "Purpose").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.TextArea(c, &e.Draft.Purpose).Label("Contact purpose").FontSize(14).Placeholder("Purpose").Disabled(!editable)
		ui.Text(c, "Instructions").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.TextArea(c, &e.Draft.Instructions).Label("Contact instructions").FontSize(14).Placeholder("Instructions").Disabled(!editable)
		ui.Text(c, "Workspace").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		workspace := ui.TextInput(c, &e.Draft.Workspace).Label("Contact workspace").FontSize(14).Placeholder("/absolute/path/to/workspace (empty uses default)").Disabled(!editable)
		if e.focusField == "Workspace" {
			workspace.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Workspace"]; message != "" {
			ui.Text(c, message).FontSize(14).TextColor(t.Danger)
		}
		ui.Text(c, "Leave Workspace empty to use the applied default directory.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)

		e.builtinToolsView(c, t, editable, actions)

		ui.Column(c).Gap(6).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.PrimaryButton(c, "Save changes").Label("Save changes").FontSize(typeAction).MinHeight(32).Disabled(!editable).OnClick(func() {
					if draft, ok := e.Request(false); ok && actions.Save != nil {
						actions.Save(draft)
					}
				})
				if e.HasConversation && !e.Archived {
					secondaryButton(c, "Save and start new conversation").Disabled(!editable || e.ActiveWork || e.UnknownWork).OnClick(func() {
						if draft, ok := e.Request(true); ok && actions.SaveAndNew != nil {
							actions.SaveAndNew(draft)
						}
					})
				}
				if !profileDraftEqual(e.Draft, e.initial) && !e.Busy && actions.Discard != nil {
					secondaryButton(c, "Discard changes").OnClick(actions.Discard)
				}
			})
			ui.Text(c, "Contact management").FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if e.Archived {
					secondaryButton(c, "Restore").Disabled(e.Busy).OnClick(func() {
						if actions.Restore != nil {
							actions.Restore()
						}
					})
				} else {
					secondaryButton(c, "Archive").Disabled(e.Busy || e.ActiveWork || e.UnknownWork).OnClick(func() {
						if actions.Archive != nil {
							actions.Archive()
						}
					})
				}
			})
		})
		if e.Busy {
			ui.Text(c, "Saving…").FontSize(14).TextColor(t.TextMuted)
		}
		if e.Error != "" {
			ui.Text(c, e.Error).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
		}
		if e.Status != "" {
			ui.Text(c, e.Status).FontSize(14).LineHeight(1.5).TextColor(t.Text)
		}
	})
}
