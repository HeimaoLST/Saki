package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContactHistoryPreviewKeepsUnicodeMatchAndBounds(t *testing.T) {
	text := strings.Repeat("前文İ", 120) + "预算β结论" + strings.Repeat("后文", 200)
	preview := contactHistoryPreview(text, "预算β")
	if !strings.Contains(preview, "预算β结论") || !utf8.ValidString(preview) || utf8.RuneCountInString(preview) > 242 {
		t.Fatal("search preview lost the match, split UTF-8 or kept the entire long message")
	}
	if !strings.HasPrefix(preview, "…") || !strings.HasSuffix(preview, "…") {
		t.Fatal("the shortened preview did not indicate omitted text")
	}
}

func TestContactHistoryProcessScopesLocatesAndReportsMissingContext(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &modelProviderFixture{}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	launch, _ := modelTestLaunch(t, connectionAPIResponses, server.URL)
	live := startModelTestRuntime(t, binary, launch.Dir, launch.Env)
	t.Cleanup(func() { _ = live.client.shutdown() })
	var ids []string
	for _, text := range []string{"First BUDGET target — α", "An unrelated saved conversation", "Hidden budget belonging to a different contact"} {
		session, err := live.client.createConfiguredSession(launch.Dir, "Do not index this system budget instruction.")
		if err != nil {
			t.Fatal(err)
		}
		task, err := live.client.sendTask(session.SessionID, text)
		if err != nil {
			t.Fatal(err)
		}
		live.finishTask(t, task.TaskID)
		ids = append(ids, session.SessionID)
	}
	if err := live.client.shutdown(); err != nil {
		t.Fatal(err)
	}
	live = startModelTestRuntime(t, binary, launch.Dir, launch.Env)
	a := &appModel{}
	result := a.searchContactHistory(live.client, []string{ids[1], "missing-session", ids[0], ids[0]}, "budget", nil)
	if result.Limited || len(result.Failures) != 1 || result.Failures[0].SessionID != "missing-session" || len(result.Matches) != 1 {
		t.Fatalf("saved-conversation search did not retain partial results and explicit scope: %+v", result)
	}
	match := result.Matches[0]
	if match.SessionID != ids[0] || match.From != "me" || !strings.Contains(match.Preview, "BUDGET target") {
		t.Fatal("search included an unrelated context/system prompt or lost the message owner")
	}
	records, _, err := a.historyAllFor(live.client, match.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	located := false
	for _, entry := range parseHistory(match.SessionID, records, nil).entries {
		if entry.ID == match.EntryID && entry.Text == "First BUDGET target — α" {
			located = true
		}
	}
	if !located {
		t.Fatal("search result cannot locate its full message after restart")
	}
	if len(provider.requestedModels()) != 3 {
		t.Fatal("history search dispatched a model request")
	}
}
