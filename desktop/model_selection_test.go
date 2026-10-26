package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func modelSelectionTester(s *modelSelection, actions modelSelectionActions, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(c.Theme().Dark).Theme
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Surface).Children(func() {
			s.view(c, theme, actions)
		})
	}, width, height)
}

func selectionConfig(model string, contextTokens, outputTokens int, revision uint64) runtimeConnectionConfig {
	var config runtimeConnectionConfig
	config.SessionID = "session-a"
	config.ConfigRevision = revision
	config.EffectiveConfig.Model = model
	config.EffectiveConfig.API = connectionAPIChatCompletions
	maxInput := contextTokens - outputTokens
	config.EffectiveConfig.Compaction = runtimeCompaction{
		ContextTokens: contextTokens, MaxInputTokens: &maxInput, OutputTokens: outputTokens,
		KeepRecentTokens: 4096, MaxSummaryTokens: 2048,
	}
	config.EffectiveConfig.CapacitySource.Kind = "stored"
	id := "default"
	config.EffectiveConfig.ConnectionID = &id
	return config
}

func selectionTarget() modelSelectionTarget {
	return modelSelectionTarget{
		Kind: "conversation", Label: "Writer",
		Config: selectionConfig("provider-model-a", 131072, 8192, 4),
	}
}

func selectionCatalog() runtimeModelCatalog {
	outB, inputB := 16384, 80000
	outC := 8192
	return runtimeModelCatalog{
		ConnectionID: "default",
		Models: []runtimeCatalogModel{
			{CatalogID: "catalog-b", Name: "Model B", Limits: runtimeModelLimits{Context: 262144, Input: &inputB, Output: &outB}},
			{CatalogID: "catalog-c", Name: "Model C", Limits: runtimeModelLimits{Context: 65536, Output: &outC}},
		},
		ConfiguredModels: []runtimeConfiguredModel{
			{Model: "provider-model-z", Compaction: runtimeCompaction{ContextTokens: 65536, OutputTokens: 4096}},
		},
	}
}

func TestModelSelectionCancelKeepsEffectiveAndDoesNotUpdate(t *testing.T) {
	target := selectionTarget()
	s := newModelSelection(target, selectionCatalog())
	s.SelectCatalog(0)
	updated, cancelled := 0, 0
	actions := modelSelectionActions{
		Update: func(modelSelectionRequest) { updated++ },
		Cancel: func() { cancelled++ },
	}
	tr := modelSelectionTester(s, actions, 320, 720)
	tr.Frame()
	if err := tr.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if updated != 0 || cancelled != 1 {
		t.Fatalf("cancel performed update=%d cancel=%d", updated, cancelled)
	}
	if s.Draft.Model != "provider-model-a" || s.Draft.CatalogID != "" || s.Effective.Config.EffectiveConfig.Model != "provider-model-a" {
		t.Fatal("cancel did not restore the effective draft")
	}
	if s.Busy || s.Error != "" {
		t.Fatal("cancel left transient state")
	}
}

func TestModelSelectionEditsProviderIDAndExplicitCompaction(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.Draft.Model = "provider-model-custom"
	s.Draft.CapacityMode = modelCapacityExplicit
	s.Draft.ContextTokens = "200000"
	s.Draft.OutputTokens = "12000"

	var got modelSelectionRequest
	actions := modelSelectionActions{Update: func(request modelSelectionRequest) { got = request }}
	tr := modelSelectionTester(s, actions, 560, 720)
	tr.Frame()
	if err := tr.Click("Update model"); err != nil {
		t.Fatal(err)
	}
	if got.Patch.Model != "provider-model-custom" || got.Patch.CatalogID != "" || got.Patch.Compaction == nil {
		t.Fatalf("patch = %+v", got.Patch)
	}
	if got.Patch.Compaction.ContextTokens != 200000 || got.Patch.Compaction.OutputTokens != 12000 ||
		got.Patch.Compaction.KeepRecentTokens != 4096 || got.Patch.Compaction.MaxSummaryTokens != 2048 ||
		got.Patch.Compaction.MaxInputTokens == nil || *got.Patch.Compaction.MaxInputTokens != 122880 {
		t.Fatalf("compaction policy was not preserved: %+v", got.Patch.Compaction)
	}
	if got.Target.Config.SessionID != "session-a" || got.Target.Config.ConfigRevision != 4 {
		t.Fatal("request lost the target identity/revision")
	}
}

func TestModelSelectionCatalogChoiceSuppliesCapacityOnly(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.SelectCatalog(0)
	request, ok := s.Request()
	if !ok {
		t.Fatal(s.Error)
	}
	if request.Patch.Model != "provider-model-a" || request.Patch.CatalogID != "catalog-b" || request.Patch.Compaction != nil {
		t.Fatalf("catalog patch = %+v", request.Patch)
	}
	if s.Draft.ContextTokens != "262144" || s.Draft.OutputTokens != "16384" {
		t.Fatal("catalog choice did not expose its limits")
	}
}

func TestModelSelectionErrorKeepsDraftAndReadbackKeepsEffective(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.Draft.Model = "provider-model-unconfirmed"
	s.Draft.CapacityMode = modelCapacityExplicit
	s.Draft.ContextTokens = "not-a-number"
	if _, ok := s.Request(); ok {
		t.Fatal("invalid capacity was accepted")
	}
	if !strings.Contains(s.Error, "Context tokens") || s.Draft.Model != "provider-model-unconfirmed" {
		t.Fatal("validation did not preserve the draft or explain the field")
	}

	s.SetBusy(true, "Updating…")
	s.SetError("Runtime busy; the previous model is still effective.")
	if s.Busy || s.Draft.Model != "provider-model-unconfirmed" || s.Effective.Config.EffectiveConfig.Model != "provider-model-a" {
		t.Fatal("error state lost the draft or effective value")
	}

	readback := selectionConfig("provider-model-a", 131072, 8192, 5)
	s.SetEffective(readback)
	if s.Effective.Config.EffectiveConfig.Model != "provider-model-a" || s.Target.Config.ConfigRevision != 5 || s.Draft.Model != "provider-model-unconfirmed" {
		t.Fatal("readback did not update effective/target while preserving the uncertain draft")
	}

	confirmed := selectionConfig("provider-model-unconfirmed", 131072, 8192, 6)
	s.SetResult(confirmed)
	if s.Draft.Model != "provider-model-unconfirmed" || s.Effective.Config.EffectiveConfig.Model != "provider-model-unconfirmed" || s.Status == "" {
		t.Fatal("confirmed result did not replace the draft with the effective value")
	}
}

func TestModelSelectionTargetSwitchResetsDraft(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.Draft.Model = "draft-for-a"
	next := selectionTarget()
	next.Kind = "work"
	next.Label = "Issue work"
	next.Config.SessionID = "session-b"
	next.Config.EffectiveConfig.Model = "provider-model-z"
	next.Config.ConfigRevision = 1
	s.SetTarget(next)
	if s.Draft.Model != "provider-model-z" || s.Effective.Config.EffectiveConfig.Model != "provider-model-z" || s.Target.Config.SessionID != "session-b" {
		t.Fatal("target switch retained the previous target draft")
	}
}

func TestModelSelectionLowerOutputKeepsFullPolicyAndDoesNotFakeAppliedResult(t *testing.T) {
	target := selectionTarget()
	s := newModelSelection(target, selectionCatalog())
	s.Draft.CapacityMode = modelCapacityExplicit
	s.Draft.ContextTokens = "131072"
	s.Draft.OutputTokens = "4096"
	request, ok := s.Request()
	if !ok {
		t.Fatal(s.Error)
	}
	if request.Patch.Compaction == nil || request.Patch.Compaction.OutputTokens != 4096 ||
		request.Patch.Compaction.ContextTokens != 131072 ||
		request.Patch.Compaction.KeepRecentTokens != 4096 ||
		request.Patch.Compaction.MaxSummaryTokens != 2048 {
		t.Fatalf("lower-output patch lost policy: %+v", request.Patch.Compaction)
	}

	s.SetBusy(true, "Updating…")
	s.SetError("Runtime rejected the lower output reserve; the previous model is still effective.")
	if s.Busy || s.Draft.OutputTokens != "4096" || s.Effective.Config.EffectiveConfig.Compaction.OutputTokens != 8192 {
		t.Fatal("failed update did not keep the correction draft or old effective output")
	}

	s.Draft.OutputTokens = "8192"
	retry, ok := s.Request()
	if !ok {
		t.Fatal(s.Error)
	}
	if retry.Patch.Compaction == nil || retry.Patch.Compaction.OutputTokens != 8192 {
		t.Fatalf("corrected output patch = %+v", retry.Patch.Compaction)
	}
}

func TestModelSelectionUnconfirmedRequiresRefreshAndPreservesDraft(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.Draft.Model = "provider-model-needs-confirmation"
	s.Draft.CapacityMode = modelCapacityExplicit
	updated, refreshed := 0, 0
	actions := modelSelectionActions{
		Update: func(modelSelectionRequest) { updated++ },
		Refresh: func(modelSelectionTarget) {
			refreshed++
			s.SetEffective(selectionConfig("provider-model-a", 131072, 8192, 5))
		},
	}
	if _, ok := s.Request(); !ok {
		// The valid draft is requestable before the readback becomes uncertain.
		t.Fatal(s.Error)
	}
	s.SetUnconfirmed("Runtime could not confirm the current model configuration.")
	if !s.Unconfirmed || s.Effective.Config.EffectiveConfig.Model != "provider-model-a" {
		t.Fatal("unconfirmed state lost the last read effective value")
	}
	if request, ok := s.Request(); ok {
		t.Fatalf("unconfirmed state submitted a request: %+v", request)
	}

	tr := modelSelectionTester(s, actions, 560, 720)
	tr.Frame()
	for _, text := range []string{"Last read model: provider-model-a", "Last read limits:", "Runtime could not confirm"} {
		if !tr.HasText(text) {
			t.Fatalf("unconfirmed view missing %q", text)
		}
	}
	if err := tr.Click("Update model"); err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		t.Fatal("Update was actionable while the effective config was unconfirmed")
	}
	if err := tr.Click("Refresh"); err != nil {
		t.Fatal(err)
	}
	if refreshed != 1 || s.Unconfirmed || s.Effective.Config.ConfigRevision != 5 {
		t.Fatal("Refresh did not recover a confirmed effective config")
	}
	if s.Draft.Model != "provider-model-needs-confirmation" {
		t.Fatal("Refresh discarded the uncertain draft")
	}
	tr.Frame()
	if err := tr.Click("Update model"); err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatal("Update remained disabled after a confirmed refresh")
	}
}

func TestModelSelectionCleanDraftAdoptsReadbackAndFiltersCandidates(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	readback := selectionConfig("provider-model-b", 131072, 8192, 5)
	s.SetEffective(readback)
	if s.Draft.Model != "provider-model-b" || s.Effective.Config.EffectiveConfig.Model != "provider-model-b" {
		t.Fatal("a clean draft did not adopt the readback value")
	}

	for i := 0; i < 12; i++ {
		s.Catalog.Models = append(s.Catalog.Models, runtimeCatalogModel{
			CatalogID: "catalog-extra-" + strconv.Itoa(i),
			Name:      "Extra " + strconv.Itoa(i),
			Limits:    runtimeModelLimits{Context: 100000 + i},
		})
	}
	if visible := s.visibleCatalogModels(); len(visible) != maxVisibleModelChoices {
		t.Fatalf("visible catalog entries = %d, want %d", len(visible), maxVisibleModelChoices)
	}
	s.Filter = "catalog-extra-10"
	visible := s.visibleCatalogModels()
	if len(visible) != 1 || visible[0].CatalogID != "catalog-extra-10" {
		t.Fatalf("filtered catalog = %+v", visible)
	}
	s.Filter = ""
}

func TestPreviewModelSelectionStates(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write model-selection previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := selectionTarget()
	target.Label = "A very long conversation target label that must remain readable"
	target.Config.SessionID = "session-with-a-long-identifier-for-preview"
	target.Config.EffectiveConfig.Model = "provider/model-with-a-long-version-name-2026"
	configured := make([]runtimeConfiguredModel, 0, 10)
	for i := 0; i < 10; i++ {
		configured = append(configured, runtimeConfiguredModel{
			Model: fmt.Sprintf("provider/model-%02d", i),
			Compaction: runtimeCompaction{
				ContextTokens: 131072 + i*1024, OutputTokens: 8192,
				KeepRecentTokens: 4096, MaxSummaryTokens: 2048,
			},
		})
	}
	catalog := selectionCatalog()
	catalog.ConfiguredModels = configured
	for i := 0; i < 20; i++ {
		catalog.Models = append(catalog.Models, runtimeCatalogModel{
			CatalogID: fmt.Sprintf("catalog-with-a-long-capacity-id-%02d", i),
			Name:      fmt.Sprintf("Capacity entry %02d", i),
			Limits:    runtimeModelLimits{Context: 131072 + i*2048},
		})
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			width, height int
			label         string
		}{{560, 720, "560x720"}, {320, 720, "320x720"}} {
			s := newModelSelection(target, catalog)
			s.SetError("Runtime rejected the lower output reserve; the previous model is still effective. Choose a valid limit to retry.")
			tr := modelSelectionTester(s, modelSelectionActions{}, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("model-selection-%s-%s-top.png", size.label, theme)), tr.Image())
			tr.Scroll(100, 500, 0, 1000)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("model-selection-%s-%s-scrolled.png", size.label, theme)), tr.Image())
		}
	}
}

func TestModelSelectionRendersAtMinimumWindow(t *testing.T) {
	s := newModelSelection(selectionTarget(), selectionCatalog())
	s.SetError("Choose a supported model.")
	tr := modelSelectionTester(s, modelSelectionActions{}, 320, 720)
	tr.Frame()
	for _, text := range []string{"Session model", "Effective model: provider-model-a", "Provider model ID", "Update model", "Cancel"} {
		if !tr.HasText(text) {
			t.Fatalf("minimum-window form missing %q", text)
		}
	}
}
