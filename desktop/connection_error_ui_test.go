package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKnownConnectionFailureIsConciseAndKeepsOriginalDetails(t *testing.T) {
	a, ct := workspaceTestApp(t)
	raw := "connection_mismatch: session was created for another connection; credential details omitted"
	ct.LoadErr = raw
	ct.SendError = raw
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection unavailable or changed") ||
		!tr.HasText("Connection settings") || !tr.HasText("Details") {
		t.Fatalf("known connection failure summary missing: %v", tr.Texts())
	}
	if tr.HasText(raw) {
		t.Fatal("raw connection diagnostic was shown without Details")
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(raw) || !tr.HasText("Copy error details") {
		t.Fatalf("connection detail was not accessible: %v", tr.Texts())
	}
	priorAux := a.aux
	if err := tr.Click("Connection settings"); err != nil {
		t.Fatal(err)
	}
	if a.aux == priorAux {
		t.Fatal("connection settings entry did not open a settings surface")
	}
}

func TestUnknownContactLoadErrorIsNotSwallowed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	raw := "opaque load failure without a known connection code"
	ct.LoadErr = raw
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText(raw) {
		t.Fatalf("unknown contact load error was swallowed: %v", tr.Texts())
	}
}

func TestMixedConnectionDiagnosticsStayIndependent(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.LoadErr = "connection_mismatch: session is bound elsewhere"
	ct.SendError = "send pipeline unavailable; inspect the task record"
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection unavailable or changed") || !tr.HasText(ct.SendError) {
		t.Fatalf("mixed known/unknown diagnostics were not both visible: %v", tr.Texts())
	}
	if tr.HasText(ct.LoadErr) {
		t.Fatal("known load diagnostic leaked before Details")
	}
	if _, known := knownConnectionFailure("wrapped: connection_mismatch: still unknown"); known {
		t.Fatal("unknown diagnostic containing the code was treated as known")
	}
}
