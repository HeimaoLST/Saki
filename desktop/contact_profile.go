package main

// Contact profile draft editor and validation.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

type contactProfileActions struct {
	Save       func(profileDraft)
	SaveAndNew func(profileDraft)
	Archive    func()
	Restore    func()
	Cancel     func()
}

type contactProfileEditor struct {
	Draft           profileDraft
	Archived        bool
	HasConversation bool
	ActiveWork      bool
	UnknownWork     bool
	Busy            bool
	Errors          map[string]string
	Error           string
	Status          string
	focusField      string
	initial         profileDraft
}

func newContactProfileEditor(draft profileDraft) *contactProfileEditor {
	return &contactProfileEditor{Draft: draft, initial: draft, focusField: "Name"}
}

// SetProfile replaces the host's saved profile and resets the draft. It is
// used after a successful host save, never to optimistically claim success.
func (e *contactProfileEditor) SetProfile(draft profileDraft) {
	e.Busy = false
	e.initial = draft
	e.Draft = draft
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
	e.Draft = e.initial
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

func (e *contactProfileEditor) view(c *ui.Context, t *ui.Theme, actions contactProfileActions) {
	editable := !e.Busy && !e.Archived
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Contact profile").FontSize(17).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Button(c, "Cancel").Disabled(e.Busy).OnClick(func() {
				e.ResetDraft()
				if actions.Cancel != nil {
					actions.Cancel()
				}
			})
		})
		ui.Text(c, "Saving updates this contact's name. Purpose, instructions and workspace are defaults for future conversations and issues; existing conversations and issue work keep the snapshots they were created with.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		if reason := e.activityReason(); reason != "" {
			ui.Text(c, reason).FontSize(12).LineHeight(1.4).TextColor(t.Warning)
		}
		if e.Archived {
			ui.Text(c, "Archived").FontSize(12).FontWeight(500).TextColor(t.Warning)
		}

		ui.Text(c, "Name").FontSize(12).FontWeight(500).TextColor(t.Text)
		name := ui.TextInput(c, &e.Draft.Name).Label("Contact name").FontSize(14).Placeholder("Name").Disabled(!editable)
		if e.focusField == "Name" {
			name.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Name"]; message != "" {
			ui.Text(c, message).FontSize(12).TextColor(t.Danger)
		}
		ui.Text(c, "Purpose").FontSize(12).FontWeight(500).TextColor(t.Text)
		ui.TextArea(c, &e.Draft.Purpose).Label("Contact purpose").FontSize(14).Placeholder("Purpose").Disabled(!editable)
		ui.Text(c, "Instructions").FontSize(12).FontWeight(500).TextColor(t.Text)
		ui.TextArea(c, &e.Draft.Instructions).Label("Contact instructions").FontSize(14).Placeholder("Instructions").Disabled(!editable)
		ui.Text(c, "Workspace").FontSize(12).FontWeight(500).TextColor(t.Text)
		workspace := ui.TextInput(c, &e.Draft.Workspace).Label("Contact workspace").FontSize(14).Placeholder("/absolute/path/to/workspace (empty uses default)").Disabled(!editable)
		if e.focusField == "Workspace" {
			workspace.Focus()
			e.focusField = ""
		}
		if message := e.Errors["Workspace"]; message != "" {
			ui.Text(c, message).FontSize(12).TextColor(t.Danger)
		}
		ui.Text(c, "Leave Workspace empty to use the applied default directory.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)

		ui.Column(c).Gap(6).Children(func() {
			ui.PrimaryButton(c, "Save changes").FontSize(13).Disabled(!editable).OnClick(func() {
				if draft, ok := e.Request(false); ok && actions.Save != nil {
					actions.Save(draft)
				}
			})
			if e.HasConversation && !e.Archived {
				ui.Button(c, "Save and start new conversation").FontSize(13).Disabled(!editable || e.ActiveWork || e.UnknownWork).OnClick(func() {
					if draft, ok := e.Request(true); ok && actions.SaveAndNew != nil {
						actions.SaveAndNew(draft)
					}
				})
			}
			if e.Archived {
				ui.Button(c, "Restore").FontSize(13).Disabled(e.Busy).OnClick(func() {
					if actions.Restore != nil {
						actions.Restore()
					}
				})
			} else {
				ui.Button(c, "Archive").FontSize(13).Disabled(e.Busy || e.ActiveWork || e.UnknownWork).OnClick(func() {
					if actions.Archive != nil {
						actions.Archive()
					}
				})
			}
		})
		if e.Busy {
			ui.Text(c, "Saving…").FontSize(12).TextColor(t.TextMuted)
		}
		if e.Error != "" {
			ui.Text(c, e.Error).FontSize(12).LineHeight(1.4).TextColor(t.Danger)
		}
		if e.Status != "" {
			ui.Text(c, e.Status).FontSize(12).LineHeight(1.4).TextColor(t.Text)
		}
	})
}
