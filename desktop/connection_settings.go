package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type connectionDraft struct {
	API, Endpoint, APIKey, Model                         string
	ContextTokens, OutputTokens, WorkingDirectory, Slots string
}

type connectionSettings struct {
	Draft         connectionDraft
	initial       connectionDraft
	Errors        map[string]string
	Status        string
	Error         string
	ReloadBlocked string
	Busy          bool
	HasSaved      bool
	LimitsOpen    bool
	focusField    string
}

type connectionSettingsActions struct {
	Save    func(connectionConfig)
	Apply   func()
	Test    func(connectionConfig)
	Discard func()
}

func newConnectionSettings(config *connectionConfig, workDir string) *connectionSettings {
	s := &connectionSettings{Draft: connectionDraft{
		API: "openai-chat-completions", ContextTokens: "131072", OutputTokens: "8192",
		WorkingDirectory: workDir, Slots: "2",
	}, focusField: "Endpoint"}
	if config != nil {
		s.Draft = connectionDraft{
			API: config.API, Endpoint: config.Endpoint, APIKey: config.APIKey, Model: config.Model,
			ContextTokens: strconv.Itoa(config.ContextTokens), OutputTokens: strconv.Itoa(config.OutputTokens),
			WorkingDirectory: config.WorkingDirectory, Slots: strconv.Itoa(config.MaxActiveTasks),
		}
		s.HasSaved = true
	}
	s.initial = s.Draft
	return s
}

func (s *connectionSettings) Dirty() bool {
	return s != nil && s.Draft != s.initial
}

func (s *connectionSettings) ResetDraft() {
	if s == nil {
		return
	}
	s.Draft = s.initial
	s.Errors = nil
	s.Error = ""
	s.Status = ""
	s.ReloadBlocked = ""
	s.focusField = "Endpoint"
}

func (s *connectionSettings) validated() (connectionConfig, bool) {
	d := s.Draft
	config := connectionConfig{Version: 1, API: d.API, Endpoint: strings.TrimSpace(d.Endpoint),
		APIKey: d.APIKey, Model: strings.TrimSpace(d.Model), WorkingDirectory: strings.TrimSpace(d.WorkingDirectory)}
	numericErrors := map[string]string{}
	for _, f := range []struct {
		key, value string
		target     *int
	}{
		{"ContextTokens", d.ContextTokens, &config.ContextTokens},
		{"OutputTokens", d.OutputTokens, &config.OutputTokens},
		{"MaxActiveTasks", d.Slots, &config.MaxActiveTasks},
	} {
		value, err := strconv.Atoi(strings.TrimSpace(f.value))
		if err != nil || value < 1 {
			numericErrors[f.key] = "Enter a positive whole number."
		}
		*f.target = value
	}
	s.Errors = validateConnectionConfig(config)
	if s.Errors == nil {
		s.Errors = map[string]string{}
	}
	for key, message := range numericErrors {
		s.Errors[key] = message
	}
	if s.Errors["ContextTokens"] != "" || s.Errors["OutputTokens"] != "" || s.Errors["MaxActiveTasks"] != "" {
		s.LimitsOpen = true
	}
	s.Error = ""
	if len(s.Errors) > 0 {
		s.Error = "Check the highlighted fields. Your changes are still here."
		for _, key := range []string{"API", "Endpoint", "APIKey", "Model", "ContextTokens", "OutputTokens", "WorkingDirectory", "MaxActiveTasks"} {
			if s.Errors[key] != "" {
				s.focusField = key
				break
			}
		}
		return config, false
	}
	return config, true
}

// The host owns persistence, process lifetime and UI-thread updates. Rendering
// and editing this form never starts a model request or writes configuration.
func (s *connectionSettings) view(c *ui.Context, t *ui.Theme, actions connectionSettingsActions) {
	ui.Column(c).FillWidth().AlignItems(ui.Start).Gap(24).Children(func() {
		if s.Error != "" {
			ui.Text(c, s.Error).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
		}
		ui.Column(c).FillWidth().Gap(16).Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				ui.Text(c, "Protocol").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
				ui.Row(c).Gap(6).Wrap().Children(func() {
					for _, choice := range []struct{ label, value string }{{"Chat completions", "openai-chat-completions"}, {"Responses", "openai-responses"}} {
						choice := choice
						button := secondaryButton(c, choice.label).Disabled(s.Busy).OnClick(func() { s.Draft.API = choice.value })
						if s.Draft.API == choice.value {
							button.BorderWidth(2).BorderColor(t.Accent)
						}
					}
				})
				if s.Errors["API"] != "" {
					ui.Text(c, s.Errors["API"]).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
				}
			})
			s.field(c, t, "Endpoint", "API endpoint", "https://api.example.com/v1", &s.Draft.Endpoint, false,
				"Use the API base URL. Nagi adds the protocol path.")
			s.field(c, t, "APIKey", "API key", "Paste your API key", &s.Draft.APIKey, true,
				"Saved in a private local file without encryption; not added to contact settings or model prompts.")
			s.field(c, t, "Model", "Default model", "Provider model ID", &s.Draft.Model, false, "")
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				limitsLabel := "Capacity & execution limits"
				limitsTooltip := "Show capacity and execution limits"
				if s.LimitsOpen {
					limitsLabel = "Hide capacity & execution limits"
					limitsTooltip = "Hide capacity and execution limits"
				}
				ui.Toolbar(c, func() {
					limits := appTooltip(c, ui.Button(c, "").Label(limitsLabel), limitsTooltip).
						MinHeight(32).Padding(4, 8).Gap(6)
					limits.Children(func() {
						arrow := float32(180)
						if s.LimitsOpen {
							arrow = -90
						}
						ui.Icon(c, navBackIcon).Width(16).Height(16).Rotate(arrow).TextColor(t.TextMuted)
						ui.Text(c, limitsLabel).FontSize(typeAction).TextColor(t.Text)
					})
					limits.OnClick(func() { s.LimitsOpen = !s.LimitsOpen })
				}).Label("Capacity disclosure").Padding(0)
				if s.LimitsOpen {
					ui.Text(c, "Enter limits supported by this model; they are not inferred from its name.").
						FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
					ui.Column(c).FillWidth().Gap(12).Children(func() {
						s.field(c, t, "ContextTokens", "Context tokens", "131072", &s.Draft.ContextTokens, false, "")
						s.field(c, t, "OutputTokens", "Output tokens", "8192", &s.Draft.OutputTokens, false, "")
						s.field(c, t, "MaxActiveTasks", "Maximum active tasks", "2", &s.Draft.Slots, false, "")
					})
				}
			})
			s.field(c, t, "WorkingDirectory", "Default working directory", "/absolute/path/to/project", &s.Draft.WorkingDirectory, false,
				"New work uses these defaults; existing sessions keep their model, limits and working directory.")
		})
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.PrimaryButton(c, "Save configuration").Label("Save configuration").FontSize(typeAction).MinHeight(32).Disabled(s.Busy).OnClick(func() {
					if config, ok := s.validated(); ok && actions.Save != nil {
						actions.Save(config)
					}
				})
				secondaryButton(c, "Apply saved").Disabled(s.Busy || !s.HasSaved || s.ReloadBlocked != "").OnClick(actions.Apply)
				if s.Dirty() && actions.Discard != nil {
					secondaryButton(c, "Discard changes").Disabled(s.Busy).OnClick(actions.Discard)
				}
			})
			if s.ReloadBlocked != "" {
				ui.Text(c, s.ReloadBlocked).FontSize(typeBody).LineHeight(1.5).TextColor(t.Warning)
			}
			ui.Text(c, "Save does not interrupt work. Apply saved after all work finishes.").
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				secondaryButton(c, "Test connection and model").Disabled(s.Busy).OnClick(func() {
					if config, ok := s.validated(); ok && actions.Test != nil {
						actions.Test(config)
					}
				})
			})
			ui.Text(c, "Tests the values above and may incur provider charges. It does not save or apply; Save and Apply do not test model access.").
				FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
			if s.Status != "" {
				ui.Text(c, s.Status).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
			}
		})
	})
}

func (s *connectionSettings) field(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool, helper string) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, label).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		input := ui.TextInput(c, value).Label(label).FontSize(typeBody).Placeholder(placeholder).Disabled(s.Busy)
		if secret {
			input.Password()
		}
		if s.focusField == key {
			input.Focus()
			s.focusField = ""
		}
		if message := s.Errors[key]; message != "" {
			ui.Text(c, message).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
		}
		if helper != "" {
			ui.Text(c, helper).FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
		}
	})
}
