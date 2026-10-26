package main

// Standalone session-model view. Runtime catalog/config DTOs stay in
// runtime.go; this file only holds the editable UI state for one target.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type modelSelectionTarget struct {
	Kind   string
	Label  string
	Config runtimeConnectionConfig
}

type modelSelectionDraft struct {
	Model         string
	CatalogID     string
	CapacityMode  string
	ContextTokens string
	OutputTokens  string
}

type modelSelectionRequest struct {
	Target modelSelectionTarget
	Patch  runtimeModelPatch
}

type modelSelectionActions struct {
	Refresh func(modelSelectionTarget)
	Update  func(modelSelectionRequest)
	Cancel  func()
}

type modelSelection struct {
	Target      modelSelectionTarget
	Effective   modelSelectionTarget
	Catalog     runtimeModelCatalog
	Draft       modelSelectionDraft
	Filter      string
	Unconfirmed bool
	Busy        bool
	Error       string
	Status      string
	focusField  string
}

const (
	modelCapacityCatalog  = "catalog"
	modelCapacityExplicit = "explicit"
)

func newModelSelection(target modelSelectionTarget, catalog runtimeModelCatalog) *modelSelection {
	s := &modelSelection{Target: target, Effective: target, Catalog: catalog, focusField: "Model"}
	s.ResetDraft()
	return s
}

func (s *modelSelection) effectiveCompaction() runtimeCompaction {
	return s.Effective.Config.EffectiveConfig.Compaction
}

func (s *modelSelection) effectiveCatalogID() string {
	effective := s.Effective.Config.EffectiveConfig
	if effective.CapacitySource.Kind != "catalog" {
		return ""
	}
	return effective.CapacitySource.CatalogID
}

func (s *modelSelection) ResetDraft() {
	compaction := s.effectiveCompaction()
	s.Draft = modelSelectionDraft{
		Model:         s.Effective.Config.EffectiveConfig.Model,
		CapacityMode:  modelCapacityExplicit,
		ContextTokens: strconv.Itoa(compaction.ContextTokens),
		OutputTokens:  strconv.Itoa(compaction.OutputTokens),
	}
	if catalogID := s.effectiveCatalogID(); catalogID != "" {
		s.Draft.CatalogID = catalogID
		s.Draft.CapacityMode = modelCapacityCatalog
	}
	s.Error = ""
	s.Status = ""
	s.focusField = "Model"
}

func (s *modelSelection) SetTarget(target modelSelectionTarget) {
	wasDirty := s.dirty()
	changedTarget := s.Target.Config.SessionID != target.Config.SessionID || s.Target.Kind != target.Kind
	s.Target = target
	s.Effective = target
	if changedTarget {
		s.Filter = ""
		s.Unconfirmed = false
	}
	if changedTarget || !wasDirty {
		s.ResetDraft()
	}
}

func (s *modelSelection) SetCatalog(catalog runtimeModelCatalog) {
	s.Catalog = catalog
}

// SetEffective applies a readback snapshot without claiming a successful
// update. A dirty draft is retained so an uncertain update can be corrected.
func (s *modelSelection) SetEffective(config runtimeConnectionConfig) {
	wasDirty := s.dirty()
	changedTarget := s.Target.Config.SessionID != config.SessionID
	s.Effective.Config = config
	s.Target.Config = config
	if changedTarget || !wasDirty {
		s.ResetDraft()
	}
	s.Unconfirmed = false
	s.Error = ""
	s.Status = "Runtime configuration read back."
}

// SetResult applies a confirmed config update and discards the draft.
func (s *modelSelection) SetResult(config runtimeConnectionConfig) {
	s.Target.Config = config
	s.Effective.Config = config
	s.Busy = false
	s.Unconfirmed = false
	s.ResetDraft()
	s.Status = "Model configuration applied."
}

func (s *modelSelection) SetUnconfirmed(message string) {
	s.Unconfirmed = true
	s.Busy = false
	s.Error = ""
	if strings.TrimSpace(message) == "" {
		message = "Runtime did not confirm the current configuration. Refresh to read it again."
	}
	s.Status = message
}

func (s *modelSelection) SetBusy(busy bool, status string) {
	s.Busy = busy
	s.Status = status
	if !busy {
		s.focusField = "Model"
	}
}

func (s *modelSelection) SetError(message string) {
	s.Busy = false
	s.Error = message
}

func (s *modelSelection) SetStatus(message string) {
	s.Status = message
}

func (s *modelSelection) dirty() bool {
	compaction := s.effectiveCompaction()
	if s.Draft.Model != s.Effective.Config.EffectiveConfig.Model {
		return true
	}
	if s.Draft.CapacityMode != modelCapacityExplicit {
		return s.Draft.CatalogID != s.effectiveCatalogID()
	}
	return s.Draft.ContextTokens != strconv.Itoa(compaction.ContextTokens) ||
		s.Draft.OutputTokens != strconv.Itoa(compaction.OutputTokens)
}

func (s *modelSelection) SelectCatalog(index int) {
	if index < 0 || index >= len(s.Catalog.Models) {
		return
	}
	entry := s.Catalog.Models[index]
	s.Draft.CatalogID = entry.CatalogID
	s.Draft.CapacityMode = modelCapacityCatalog
	s.Draft.ContextTokens = strconv.Itoa(entry.Limits.Context)
	if entry.Limits.Output != nil {
		s.Draft.OutputTokens = strconv.Itoa(*entry.Limits.Output)
	}
	s.Error = ""
}

func (s *modelSelection) SelectConfigured(index int) {
	if index < 0 || index >= len(s.Catalog.ConfiguredModels) {
		return
	}
	entry := s.Catalog.ConfiguredModels[index]
	s.Draft.Model = entry.Model
	s.Draft.CatalogID = ""
	s.Draft.CapacityMode = modelCapacityExplicit
	s.Draft.ContextTokens = strconv.Itoa(entry.Compaction.ContextTokens)
	s.Draft.OutputTokens = strconv.Itoa(entry.Compaction.OutputTokens)
	s.Error = ""
	s.focusField = ""
}

func (s *modelSelection) SelectCapacityMode(mode string) {
	if mode != modelCapacityCatalog && mode != modelCapacityExplicit {
		return
	}
	s.Draft.CapacityMode = mode
	if mode == modelCapacityExplicit {
		s.Draft.CatalogID = ""
	}
	s.Error = ""
}

func (s *modelSelection) Request() (modelSelectionRequest, bool) {
	if s.Unconfirmed {
		s.Error = "Refresh the Runtime configuration before updating the model. The current effective value is not confirmed."
		return modelSelectionRequest{}, false
	}
	modelID := strings.TrimSpace(s.Draft.Model)
	if modelID == "" {
		s.Error = "Enter the provider model ID."
		s.focusField = "Model"
		return modelSelectionRequest{}, false
	}
	if s.Target.Config.SessionID == "" {
		s.Error = "Choose a session target first."
		return modelSelectionRequest{}, false
	}
	request := modelSelectionRequest{
		Target: s.Target,
		Patch:  runtimeModelPatch{Model: modelID},
	}
	switch s.Draft.CapacityMode {
	case modelCapacityCatalog:
		catalogID := strings.TrimSpace(s.Draft.CatalogID)
		if catalogID == "" {
			s.Error = "Choose a Runtime catalog entry or set explicit limits."
			return modelSelectionRequest{}, false
		}
		request.Patch.CatalogID = catalogID
	default:
		contextTokens, err := positiveInt(s.Draft.ContextTokens)
		if err != nil {
			s.Error = "Context tokens must be a positive whole number."
			return modelSelectionRequest{}, false
		}
		outputTokens, err := positiveInt(s.Draft.OutputTokens)
		if err != nil {
			s.Error = "Output tokens must be a positive whole number."
			return modelSelectionRequest{}, false
		}
		if contextTokens <= outputTokens {
			s.Error = "Context tokens must be greater than output tokens."
			return modelSelectionRequest{}, false
		}
		compaction := s.effectiveCompaction()
		compaction.ContextTokens = contextTokens
		compaction.OutputTokens = outputTokens
		request.Patch.Compaction = &compaction
	}
	s.Error = ""
	return request, true
}

func positiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return parsed, nil
}

const maxVisibleModelChoices = 6

func (s *modelSelection) matchesFilter(text string) bool {
	query := strings.ToLower(strings.TrimSpace(s.Filter))
	return query == "" || strings.Contains(strings.ToLower(text), query)
}

func (s *modelSelection) visibleConfiguredModels() []runtimeConfiguredModel {
	visible := make([]runtimeConfiguredModel, 0, maxVisibleModelChoices)
	for _, entry := range s.Catalog.ConfiguredModels {
		if !s.matchesFilter(entry.Model) {
			continue
		}
		visible = append(visible, entry)
		if len(visible) == maxVisibleModelChoices {
			break
		}
	}
	return visible
}

func (s *modelSelection) visibleCatalogModels() []runtimeCatalogModel {
	visible := make([]runtimeCatalogModel, 0, maxVisibleModelChoices)
	for _, entry := range s.Catalog.Models {
		if !s.matchesFilter(entry.CatalogID + " " + entry.Name) {
			continue
		}
		visible = append(visible, entry)
		if len(visible) == maxVisibleModelChoices {
			break
		}
	}
	return visible
}

func (s *modelSelection) SelectCatalogID(catalogID string) {
	for i, entry := range s.Catalog.Models {
		if entry.CatalogID == catalogID {
			s.SelectCatalog(i)
			return
		}
	}
}

func (s *modelSelection) SelectConfiguredModel(model string) {
	for i, entry := range s.Catalog.ConfiguredModels {
		if entry.Model == model {
			s.SelectConfigured(i)
			return
		}
	}
}

func (s *modelSelection) view(c *ui.Context, t *ui.Theme, actions modelSelectionActions) {
	effective := s.Effective.Config.EffectiveConfig
	compaction := effective.Compaction
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Session model").FontSize(17).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Button(c, "Cancel").Disabled(s.Busy).OnClick(func() {
				s.ResetDraft()
				if actions.Cancel != nil {
					actions.Cancel()
				}
			})
		})
		ui.Text(c, "Target: "+s.targetLabel()).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		ui.Text(c, "Session: "+valueOr(s.Target.Config.SessionID, "not created")).FontSize(11).TextColor(t.TextMuted)
		if id := connectionIdentity(effective.ConnectionID); id != "" {
			ui.Text(c, "Connection: "+id).FontSize(11).TextColor(t.TextMuted)
		}
		modelLabel := "Effective model: "
		limitsLabel := "Effective limits: "
		if s.Unconfirmed {
			modelLabel = "Last read model: "
			limitsLabel = "Last read limits: "
		}
		ui.Text(c, modelLabel+valueOr(effective.Model, "unknown")).FontSize(13).FontWeight(500).TextColor(t.Text)
		source := capacitySourceLabel(s.Effective)
		if catalogID := s.effectiveCatalogID(); catalogID != "" {
			source += " (" + catalogID + ")"
		}
		ui.Text(c, fmt.Sprintf("%scontext %d / output %d · %s · revision %d",
			limitsLabel, compaction.ContextTokens, compaction.OutputTokens, source, s.Target.Config.ConfigRevision)).
			FontSize(11).TextColor(t.TextMuted)
		if s.Unconfirmed {
			ui.Text(c, "Runtime has not confirmed the current configuration; these are the last read values. Refresh is available.").FontSize(12).LineHeight(1.4).TextColor(t.Warning)
		}
		if s.Error != "" {
			ui.Text(c, s.Error).FontSize(12).LineHeight(1.4).TextColor(t.Danger)
		}
		ui.Text(c, "Provider model ID").FontSize(12).FontWeight(500).TextColor(t.Text)
		input := ui.TextInput(c, &s.Draft.Model).Label("Provider model ID").FontSize(14).Placeholder("Provider model ID").Disabled(s.Busy)
		if s.focusField == "Model" {
			input.Focus()
			s.focusField = ""
		}
		ui.Text(c, "Catalog entries supply capacity only; they do not prove that the provider endpoint supports a model.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		ui.Row(c).Gap(6).Children(func() {
			catalogButton := ui.Button(c, "Use Runtime catalog").FontSize(12).Disabled(s.Busy || len(s.Catalog.Models) == 0).OnClick(func() { s.SelectCapacityMode(modelCapacityCatalog) })
			if s.Draft.CapacityMode == modelCapacityCatalog {
				catalogButton.Background(t.Accent).TextColor(t.AccentText)
			}
			explicitButton := ui.Button(c, "Set explicit limits").FontSize(12).Disabled(s.Busy).OnClick(func() { s.SelectCapacityMode(modelCapacityExplicit) })
			if s.Draft.CapacityMode != modelCapacityCatalog {
				explicitButton.Background(t.Accent).TextColor(t.AccentText)
			}
		})
		if s.Draft.CapacityMode == modelCapacityCatalog {
			ui.Text(c, "Capacity comes from the selected Runtime catalog entry. Endpoint/model availability is not implied.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		} else {
			ui.Text(c, "Context tokens").FontSize(12).FontWeight(500).TextColor(t.Text)
			ui.TextInput(c, &s.Draft.ContextTokens).Label("Context tokens").FontSize(14).Placeholder("131072").Disabled(s.Busy)
			ui.Text(c, "Output tokens").FontSize(12).FontWeight(500).TextColor(t.Text)
			ui.TextInput(c, &s.Draft.OutputTokens).Label("Output tokens").FontSize(14).Placeholder("8192").Disabled(s.Busy)
			ui.Text(c, "Other compaction strategy values are preserved from the effective config.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "Refresh").FontSize(13).Disabled(s.Busy).OnClick(func() {
				if actions.Refresh != nil {
					actions.Refresh(s.Target)
				}
			})
			ui.PrimaryButton(c, "Update model").FontSize(13).Disabled(s.Busy || s.Unconfirmed).OnClick(func() {
				if request, ok := s.Request(); ok && actions.Update != nil {
					actions.Update(request)
				}
			})
		})
		if s.Busy {
			ui.Text(c, "Waiting for Runtime…").FontSize(12).TextColor(t.TextMuted)
		}
		if s.Status != "" {
			ui.Text(c, s.Status).FontSize(12).LineHeight(1.4).TextColor(t.Text)
		}
		ui.Text(c, "Filter candidates").FontSize(12).FontWeight(500).TextColor(t.Text)
		ui.TextInput(c, &s.Filter).Label("Filter").FontSize(13).Placeholder("Model or catalog ID").Disabled(s.Busy)
		configured := s.visibleConfiguredModels()
		if len(configured) > 0 {
			ui.Text(c, "Configured models").FontSize(12).FontWeight(500).TextColor(t.Text)
			ui.Text(c, "Host-provided declarations, not verified availability.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			for _, entry := range configured {
				entry := entry
				button := ui.Button(c, entry.Model+" · context "+strconv.Itoa(entry.Compaction.ContextTokens)).FontSize(12).Disabled(s.Busy).OnClick(func() { s.SelectConfiguredModel(entry.Model) })
				if s.Draft.Model == entry.Model && s.Draft.CapacityMode == modelCapacityExplicit {
					button.Background(t.Accent).TextColor(t.AccentText)
				}
			}
			if len(configured) < len(s.Catalog.ConfiguredModels) {
				ui.Text(c, fmt.Sprintf("Showing %d of %d configured models; refine Filter to see more.", len(configured), len(s.Catalog.ConfiguredModels))).FontSize(11).TextColor(t.TextMuted)
			}
		}
		catalog := s.visibleCatalogModels()
		if len(catalog) > 0 {
			ui.Text(c, "Runtime catalog capacity").FontSize(12).FontWeight(500).TextColor(t.Text)
			for _, entry := range catalog {
				entry := entry
				label := entry.Name
				if label == "" {
					label = entry.CatalogID
				}
				button := ui.Button(c, label+" · context "+strconv.Itoa(entry.Limits.Context)).FontSize(12).Disabled(s.Busy).OnClick(func() { s.SelectCatalogID(entry.CatalogID) })
				if s.Draft.CatalogID != "" && s.Draft.CatalogID == entry.CatalogID {
					button.Background(t.Accent).TextColor(t.AccentText)
				}
			}
			if len(catalog) < len(s.Catalog.Models) {
				ui.Text(c, fmt.Sprintf("Showing %d of %d catalog entries; refine Filter to see more.", len(catalog), len(s.Catalog.Models))).FontSize(11).TextColor(t.TextMuted)
			}
		} else if len(s.Catalog.Models) == 0 {
			ui.Text(c, "No Runtime catalog entries are available; enter explicit limits.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		}
	})
}

func (s *modelSelection) targetLabel() string {
	kind := s.Target.Kind
	switch kind {
	case "work", "issue":
		kind = "Issue work"
	case "conversation":
		kind = "Conversation"
	default:
		kind = valueOr(kind, "Session")
	}
	return kind + " · " + valueOr(s.Target.Label, s.Target.Config.SessionID)
}

func capacitySourceLabel(target modelSelectionTarget) string {
	switch target.Config.EffectiveConfig.CapacitySource.Kind {
	case "catalog":
		return "Runtime catalog"
	case "explicit":
		return "explicit limits"
	case "stored":
		return "stored"
	default:
		return valueOr(target.Config.EffectiveConfig.CapacitySource.Kind, "unknown source")
	}
}

func connectionIdentity(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
