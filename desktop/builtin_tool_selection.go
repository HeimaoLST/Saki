package main

// Contact-profile defaults for Runtime built-in tools. The profile stores an
// optional selection so a missing field means "use the Runtime default" while a
// present, empty slice means "explicitly no built-in tools".

import (
	"fmt"
	"slices"
	"strings"
)

func cloneBuiltinTools(selection *[]string) *[]string {
	if selection == nil {
		return nil
	}
	names := append([]string(nil), (*selection)...)
	if names == nil {
		names = []string{}
	}
	return &names
}

func builtinToolsEqual(left, right *[]string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return slices.Equal(*left, *right)
}

func profileDraftEqual(left, right profileDraft) bool {
	return left.Name == right.Name &&
		left.Purpose == right.Purpose &&
		left.Instructions == right.Instructions &&
		left.Workspace == right.Workspace &&
		builtinToolsEqual(left.BuiltinTools, right.BuiltinTools)
}

func builtinToolNames(tools []runtimeBuiltinTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func builtinToolSelectionContains(selection *[]string, name string) bool {
	return selection != nil && slices.Contains(*selection, name)
}

func builtinToolSelectionLabel(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (a *appModel) validateBuiltinToolSelection(current, next *[]string) string {
	if !a.connectionManaged && !builtinToolsEqual(current, next) {
		if a.connectionLegacy {
			return "Built-in tool selection requires a managed Nagi connection; this Runtime uses legacy environment configuration."
		}
		return "Save and Apply a managed Nagi connection before customizing built-in tools."
	}
	if next == nil || len(*next) == 0 {
		return ""
	}
	if !a.runtimeBuiltinToolsLoaded {
		if current != nil && slices.Equal(*next, *current) {
			return ""
		}
		return "The built-in tool list is not available from Runtime yet. Retry before changing the selection."
	}
	available := make(map[string]bool, len(a.runtimeBuiltinTools))
	for _, tool := range a.runtimeBuiltinTools {
		available[tool.Name] = true
	}
	for _, name := range *next {
		if !available[name] {
			return fmt.Sprintf("Built-in tool %q is not available from Runtime.", name)
		}
	}
	return ""
}

func (a *appModel) capturedBuiltinSelection(ct *contact) *[]string {
	if ct == nil {
		return nil
	}
	if ct.BuiltinTools != nil {
		return cloneBuiltinTools(ct.BuiltinTools)
	}
	// A legacy environment connection has no schema-2 connection identity,
	// so the app must not turn its nil/default profile into an explicit
	// selection that the legacy create path cannot honor.
	if !a.connectionManaged {
		return nil
	}
	if a.runtimeBuiltinToolsLoaded {
		names := builtinToolNames(a.runtimeBuiltinTools)
		return &names
	}
	return nil
}
