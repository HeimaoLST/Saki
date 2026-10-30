package main

import (
	"errors"
	"strings"
)

const (
	appearanceSystem = "system"
	appearanceLight  = "light"
	appearanceDark   = "dark"
)

// normalizeAppearanceMode treats every absent or unknown stored value as the
// legacy system-following default. It never writes a new preference file.
func normalizeAppearanceMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case appearanceLight:
		return appearanceLight
	case appearanceDark:
		return appearanceDark
	default:
		return appearanceSystem
	}
}

func resolveAppearanceDark(mode string, systemDark bool) bool {
	switch normalizeAppearanceMode(mode) {
	case appearanceLight:
		return false
	case appearanceDark:
		return true
	default:
		return systemDark
	}
}

func appearanceModeLabel(mode string) string {
	switch normalizeAppearanceMode(mode) {
	case appearanceLight:
		return "Light"
	case appearanceDark:
		return "Dark"
	default:
		return "System"
	}
}

func appearanceModeFromLabel(label string) string {
	switch strings.TrimSpace(label) {
	case "Light":
		return appearanceLight
	case "Dark":
		return appearanceDark
	default:
		return appearanceSystem
	}
}

func (a *appModel) openAppearance() {
	a.appearanceStatus = ""
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
}

// setAppearanceMode applies the mode immediately, then persists it in the same
// existing GUI contact store. A failed write keeps the applied mode and reports
// the gap explicitly; it never falls back to the previous page or a new prefs
// file.
func (a *appModel) setAppearanceMode(mode string) {
	mode = normalizeAppearanceMode(mode)
	if a.appearanceMode == mode {
		a.appearanceChoice = appearanceModeLabel(mode)
		if a.appearanceError != "" {
			a.saveAppearance()
		}
		return
	}
	a.appearanceMode = mode
	a.appearanceChoice = appearanceModeLabel(mode)
	a.appearanceStatus = "Applied."
	a.appearanceError = ""
	if a.demo {
		a.appearanceStatus = "Applied for this preview; the fixture is not persisted."
		return
	}
	a.saveAppearance()
}

// saveAppearance uses the one existing GUI contact store. If the first launch
// has no store yet, it lazily writes the current contacts (including draft and
// ID) through persist; it does not start Runtime or create a preference file.
func (a *appModel) saveAppearance() {
	a.appearanceStatus = ""
	a.appearanceError = ""
	if err := a.ensureAppearanceStore(); err != nil {
		a.appearanceError = "Applied but not saved. Retry save to try again."
		return
	}
	if err := a.persist(); err != nil {
		a.appearanceError = "Applied but not saved. Retry save to try again."
		return
	}
	a.appearanceStatus = "Applied and saved."
}

func (a *appModel) ensureAppearanceStore() error {
	if a.store != nil {
		return nil
	}
	if !a.storeInitAllowed || strings.TrimSpace(a.storePath) == "" {
		return errors.New("the local contact store is unavailable")
	}
	if len(a.contacts) == 0 {
		return errors.New("there is no current contact to save")
	}
	store := &contactStore{path: a.storePath}
	store.setIssueToolState(a.issueToolLedger, a.issueToolScopes)
	a.store = store
	return nil
}

func (a *appModel) effectiveAppearanceLabel() string {
	dark := a.workTheme.Theme != nil && a.workTheme.Theme.Dark
	appearance := "Paper"
	if dark {
		appearance = "Night"
	}
	if normalizeAppearanceMode(a.appearanceMode) == appearanceSystem {
		system := "Light"
		if a.systemDark {
			system = "Dark"
		}
		return "System (" + system + ") · " + appearance
	}
	return appearanceModeLabel(a.appearanceMode) + " · " + appearance
}

func (a *appModel) connectionDefaultModel() string {
	for _, config := range []*connectionConfig{a.connectionEffective, a.connectionSaved} {
		if config != nil && strings.TrimSpace(config.Model) != "" {
			return strings.TrimSpace(config.Model)
		}
	}
	return ""
}

func (a *appModel) conversationModelShortName(ct *contact) string {
	if ct != nil {
		if model := strings.TrimSpace(ct.SessionModel); model != "" {
			return truncate(model, 30)
		}
		if ct.SessionID != "" {
			return "Model not loaded"
		}
	}
	if model := a.connectionDefaultModel(); model != "" {
		return truncate(model, 30)
	}
	return "Model not set"
}

func (a *appModel) conversationModelSummary(ct *contact) string {
	if ct == nil {
		return "Connection default not configured"
	}
	if ct.SessionID != "" {
		if strings.TrimSpace(ct.SessionModel) != "" {
			return "Session · " + truncate(ct.SessionModel, 42)
		}
		return "Session model not loaded"
	}
	if model := a.connectionDefaultModel(); model != "" {
		return "Connection default · " + truncate(model, 42)
	}
	return "Connection default not configured"
}
