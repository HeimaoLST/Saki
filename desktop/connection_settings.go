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
	Draft            connectionDraft
	Errors           map[string]string
	Source           string
	SavedSummary     string
	EffectiveSummary string
	Status           string
	Error            string
	ReloadBlocked    string
	Busy             bool
	HasSaved         bool
	focusField       string
}

type connectionSettingsActions struct {
	Save  func(connectionConfig)
	Apply func()
	Test  func(connectionConfig)
	Close func()
}

func newConnectionSettings(config *connectionConfig, workDir string) *connectionSettings {
	s := &connectionSettings{Source: "Not configured in Nagi", Draft: connectionDraft{
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
		s.Source = "Nagi connection settings"
	}
	return s
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
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Connection settings").FontSize(17).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Button(c, "Close").Disabled(s.Busy).OnClick(actions.Close)
		})
		ui.Text(c, "Connect a model to start working with your contacts.").FontSize(13).LineHeight(1.4).TextColor(t.TextMuted)
		ui.Text(c, "Source: "+s.Source).FontSize(12).TextColor(t.TextMuted)
		if s.Error != "" {
			ui.Text(c, s.Error).FontSize(13).LineHeight(1.4).TextColor(t.Danger)
		}
		ui.Text(c, "Protocol").FontSize(12).FontWeight(500).TextColor(t.Text)
		ui.Row(c).Gap(6).Children(func() {
			for _, choice := range []struct{ label, value string }{{"Chat completions", "openai-chat-completions"}, {"Responses", "openai-responses"}} {
				choice := choice
				button := ui.Button(c, choice.label).FontSize(12).Disabled(s.Busy).OnClick(func() { s.Draft.API = choice.value })
				if s.Draft.API == choice.value {
					button.Background(t.Accent).TextColor(t.AccentText)
				}
			}
		})
		if s.Errors["API"] != "" {
			ui.Text(c, s.Errors["API"]).FontSize(12).TextColor(t.Danger)
		}
		s.field(c, t, "Endpoint", "API endpoint", "https://api.example.com/v1", &s.Draft.Endpoint, false)
		ui.Text(c, "Use the API base URL. Nagi adds the protocol path.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		s.field(c, t, "APIKey", "API key", "Paste your API key", &s.Draft.APIKey, true)
		ui.Text(c, "Saved in a private local file, without encryption. Nagi does not add this key to contact settings or model prompts.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		s.field(c, t, "Model", "Default model", "Provider model ID", &s.Draft.Model, false)
		ui.Text(c, "Capacity · explicit limits").FontSize(12).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "Enter limits supported by this model. These values are not inferred from its name.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		s.field(c, t, "ContextTokens", "Context tokens", "131072", &s.Draft.ContextTokens, false)
		s.field(c, t, "OutputTokens", "Output tokens", "8192", &s.Draft.OutputTokens, false)
		s.field(c, t, "WorkingDirectory", "Default working directory", "/absolute/path/to/project", &s.Draft.WorkingDirectory, false)
		s.field(c, t, "MaxActiveTasks", "Maximum active tasks", "2", &s.Draft.Slots, false)
		ui.Text(c, "Defaults apply to new work. Existing sessions keep their model, limits and working directory.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			ui.PrimaryButton(c, "Save configuration").FontSize(13).Disabled(s.Busy).OnClick(func() {
				if config, ok := s.validated(); ok && actions.Save != nil {
					actions.Save(config)
				}
			})
			ui.Button(c, "Apply saved").FontSize(13).Disabled(s.Busy || !s.HasSaved || s.ReloadBlocked != "").OnClick(actions.Apply)
		})
		if s.ReloadBlocked != "" {
			ui.Text(c, s.ReloadBlocked).FontSize(12).LineHeight(1.4).TextColor(t.Warning)
		}
		ui.Text(c, "Saving does not interrupt work. When all work has finished, choose Apply saved to use the saved settings.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		if s.SavedSummary != "" {
			ui.Text(c, "Saved: "+s.SavedSummary).FontSize(12).LineHeight(1.4).TextColor(t.Text)
		}
		if s.EffectiveSummary != "" {
			ui.Text(c, "Effective: "+s.EffectiveSummary).FontSize(12).LineHeight(1.4).TextColor(t.Text)
		}
		ui.Button(c, "Test connection and model").FontSize(13).Disabled(s.Busy).OnClick(func() {
			if config, ok := s.validated(); ok && actions.Test != nil {
				actions.Test(config)
			}
		})
		ui.Text(c, "Test runs a short model check using the values above. It may incur provider charges. Saving and starting the connection do not test model access.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		if s.Status != "" {
			ui.Text(c, s.Status).FontSize(12).LineHeight(1.4).TextColor(t.Text)
		}
	})
}

func (s *connectionSettings) field(c *ui.Context, t *ui.Theme, key, label, placeholder string, value *string, secret bool) {
	ui.Text(c, label).FontSize(12).FontWeight(500).TextColor(t.Text)
	input := ui.TextInput(c, value).Label(label).FontSize(14).Placeholder(placeholder).Disabled(s.Busy)
	if secret {
		input.Password()
	}
	if s.focusField == key {
		input.Focus()
		s.focusField = ""
	}
	if message := s.Errors[key]; message != "" {
		ui.Text(c, message).FontSize(12).LineHeight(1.4).TextColor(t.Danger)
	}
}
