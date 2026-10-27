package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEmptyStateDescribesProfileAndNewConversationAccurately(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("new conversation uses the updated profile") {
		t.Fatalf("empty state does not explain the profile/new-conversation boundary: %v", tr.Texts())
	}
	if tr.HasText("only the display name can change") {
		t.Fatal("empty state still claims only the display name can change")
	}
}
