package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestRuntimeHelperMissingHasConfigHint(t *testing.T) {
	a := &appModel{store: &contactStore{}, runtimeStatus: runtimeStateStarting, workDir: t.TempDir()}
	t.Setenv("SAKI_RUNTIME_BIN", "")
	t.Setenv("PATH", "")
	a.startRuntime()
	if a.runtimeStage != "Runtime helper" {
		t.Fatalf("stage = %q, want Runtime helper", a.runtimeStage)
	}
	for _, want := range []string{"Saki-runtime", "SAKI_RUNTIME_BIN"} {
		if !strings.Contains(a.runtimeHint, want) {
			t.Fatalf("hint = %q, missing %q", a.runtimeHint, want)
		}
	}
	if a.runtimeErr == "" {
		t.Fatal("missing helper should set a visible error")
	}
}

func TestRuntimeExecFailureHasStartHint(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-Saki")
	a := &appModel{store: &contactStore{}, runtimeStatus: runtimeStateStarting, workDir: t.TempDir()}
	t.Setenv("SAKI_RUNTIME_BIN", missing)
	a.startRuntime()
	if a.runtimeStage != "Runtime start" {
		t.Fatalf("stage = %q, want Runtime start", a.runtimeStage)
	}
	if !strings.Contains(a.runtimeHint, missing) {
		t.Fatalf("hint = %q, want helper path", a.runtimeHint)
	}
	if a.runtimeBinary != missing {
		t.Fatalf("runtimeBinary = %q", a.runtimeBinary)
	}
}

func TestRuntimeExitClassifiesConfigAndRedacts(t *testing.T) {
	a := &appModel{
		runtimeStatus: runtimeStateStarting,
		workDir:       "/tmp/saki-112-work",
		rt:            &runtimeClient{stderr: []string{"Error: API_KEY is not set (checked the environment and an optional .env file)"}},
	}
	a.applyRuntimeExit(errors.New("exit status 1"))
	if a.runtimeStage != "Runtime configuration" {
		t.Fatalf("stage = %q", a.runtimeStage)
	}
	if !strings.Contains(a.runtimeErr, "API_KEY is not set") || !strings.Contains(a.runtimeHint, "Set API_KEY") {
		t.Fatalf("err=%q hint=%q", a.runtimeErr, a.runtimeHint)
	}

	b := &appModel{
		runtimeStatus: runtimeStateStarting,
		workDir:       "/tmp/saki-112-work",
		rt:            &runtimeClient{stderr: []string{"Authorization: Bearer sk-sentinel-112"}},
	}
	b.applyRuntimeExit(errors.New("exit status 1"))
	for _, value := range []string{b.runtimeErr, b.runtimeHint} {
		if strings.Contains(value, "sk-sentinel-112") {
			t.Fatalf("secret leaked in %q", value)
		}
	}
	if !strings.Contains(b.runtimeErr, "[redacted]") {
		t.Fatalf("unknown diagnostic was not bounded/redacted: %q", b.runtimeErr)
	}

	c := &appModel{
		runtimeStatus:    runtimeStateReady,
		runtimeHandshake: true,
		contacts: []*contact{{
			ID: "saki", Name: "Saki", ActiveTaskID: "task-1", Draft: "later draft",
		}},
	}
	c.applyRuntimeExit(errors.New("exit status 1"))
	if c.runtimeStage != "Runtime connection" || !strings.Contains(c.runtimeHint, "Restart Saki") {
		t.Fatalf("stage=%q hint=%q", c.runtimeStage, c.runtimeHint)
	}
	if c.contacts[0].ActiveTaskID != "" || c.contacts[0].SendState != sendUnknown {
		t.Fatalf("active task state = %+v", c.contacts[0])
	}
	if c.contacts[0].Draft != "later draft" {
		t.Fatalf("later draft changed: %q", c.contacts[0].Draft)
	}
}

func TestSafeDiagnosticRedactsSecrets(t *testing.T) {
	cases := []string{
		"API_KEY=sk-sentinel-112",
		"Authorization: Bearer sentinel-112",
		"token=token-sentinel-112",
		`{"api_key":"json-sentinel-112"}`,
		`{"token": "json-token-sentinel-112"}`,
		"Incorrect API key provided: sk-sentinel-112",
		"https://user:pass-sentinel-112@example.test/v1",
	}
	for _, input := range cases {
		got := safeDiagnostic(input)
		if strings.Contains(got, "sentinel-112") {
			t.Errorf("safeDiagnostic(%q) leaked a secret: %q", input, got)
		}
	}
	if got := safeDiagnostic("API_KEY is not set"); !strings.Contains(got, "API_KEY is not set") {
		t.Fatalf("safe non-value diagnostic was lost: %q", got)
	}
}

func TestUnavailableStoreFallbackKeepsPathsAndFile(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	sessionDir := filepath.Join(dir, "sessions")
	workDir := filepath.Join(dir, "work")
	original := []byte("{not json")
	if err := os.WriteFile(storePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_SESSION_DIR", sessionDir)
	t.Setenv("SAKI_GUI_WORKDIR", workDir)

	_, err := newRuntimeApp()
	if err == nil || !strings.Contains(err.Error(), storePath) {
		t.Fatalf("newRuntimeApp error = %v", err)
	}
	a := newUnavailableApp(err)
	if a.store != nil {
		t.Fatal("unavailable fallback must keep store=nil")
	}
	if a.runtimeStage != startupStageLocalStore {
		t.Fatalf("stage = %q", a.runtimeStage)
	}
	if a.storePath != storePath || a.sessionDir != sessionDir || a.workDir != workDir {
		t.Fatalf("fallback paths = store %q, session %q, work %q", a.storePath, a.sessionDir, a.workDir)
	}
	if !strings.Contains(a.runtimeErr, storePath) || a.contacts[0].Unavailable != err.Error() {
		t.Fatalf("original store error was not preserved: runtimeErr=%q unavailable=%q", a.runtimeErr, a.contacts[0].Unavailable)
	}
	for _, want := range []string{storePath, "untouched", "did not create a replacement store", "Drafts cannot be persisted"} {
		if !strings.Contains(a.runtimeHint, want) {
			t.Errorf("hint = %q, missing %q", a.runtimeHint, want)
		}
	}

	a.openAdvancedSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	tr.Frame()
	for _, want := range []string{
		"Startup & configuration",
		"Local contact store:",
		"Runtime: not started (local contact store unavailable)",
		"Store: " + storePath,
		"Session directory: " + sessionDir,
		"GUI work directory: " + workDir,
		"Helper: not started; fix the local contact store first",
	} {
		if !tr.HasText(want) {
			t.Errorf("fallback panel missing %q", want)
		}
	}

	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("fallback must not overwrite the corrupt store")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "contacts.json" {
		t.Fatalf("fallback created extra store/session/work files: %v", entries)
	}
}

func TestRuntimePanelShowsPathsAndMetadata(t *testing.T) {
	ct := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant",
		Workspace: "/tmp/contact-ws", SessionID: "session-1", Loaded: true,
		SessionWorkingDir: "/tmp/session-ws", SessionModel: "model-x",
		SessionAPI: "openai-chat-completions", SessionState: "running", Draft: "keep",
	}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady,
		runtimeBinary: "/tmp/Saki-runtime", runtimeAPI: "openai-responses",
		storePath: "/tmp/contacts.json", sessionDir: "/tmp/sessions", workDir: "/tmp/work",
		scrollTo: -1, contacts: []*contact{ct},
	}
	a.openAdvancedSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	tr.Frame()
	for _, want := range []string{
		"Startup & configuration",
		"Helper: /tmp/Saki-runtime",
		"Store: /tmp/contacts.json",
		"Session directory: /tmp/sessions",
		"GUI work directory: /tmp/work",
		"Contact workspace: /tmp/session-ws",
		"Session: session-1",
		"state: running",
		"Session API: openai-chat-completions",
		"Session model: model-x",
	} {
		if !tr.HasText(want) {
			t.Errorf("runtime panel missing %q; texts=%v", want, tr.Texts())
		}
	}
	if ct.Draft != "keep" {
		t.Fatalf("runtime status changed the draft: %q", ct.Draft)
	}
}

func TestConfigurationEntryKeepsContactSettingsUsable(t *testing.T) {
	ct := &contact{ID: "saki", Name: "Saki", Role: "Assistant", Draft: "keep"}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, runtimeHandshake: true,
		runtimeBinary: "/tmp/Saki-runtime", storePath: "/tmp/contacts.json",
		sessionDir: "/tmp/sessions", workDir: "/tmp/work", scrollTo: -1,
		contacts: []*contact{ct},
	}
	a.openSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	for _, want := range []string{"Contact profile", "Contact name", "Save changes", "← Back to workbench"} {
		if !tr.HasText(want) {
			t.Fatalf("contact profile page missing %q", want)
		}
	}
	if err := tr.Click("Advanced"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced {
		t.Fatalf("advanced route = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	for _, want := range []string{"Startup & configuration", "Helper: /tmp/Saki-runtime"} {
		if !tr.HasText(want) {
			t.Fatalf("advanced page missing %q", want)
		}
	}
	if ct.Draft != "keep" {
		t.Fatalf("configuration view changed the draft: %q", ct.Draft)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	if a.settingsOpen {
		t.Fatal("back did not close the settings page")
	}
}

func TestStartupBannerKeepsComposerFocusAndDraft(t *testing.T) {
	ct := &contact{ID: "saki", Name: "Saki", Role: "Assistant", Draft: "keep"}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateFailed,
		runtimeStage: "Runtime helper", runtimeErr: "Runtime helper not found.",
		runtimeHint: "Place Saki-runtime next to the app or set SAKI_RUNTIME_BIN.",
		scrollTo:    -1,
		contacts:    []*contact{ct},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if !tr.HasText("Configuration details") || !tr.HasText("set SAKI_RUNTIME_BIN") {
		t.Fatal("startup failure should show an actionable banner")
	}
	if err := tr.Click("Composer for Saki"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, active := tr.TextCaret(); !active {
		t.Fatal("startup banner must not steal composer focus")
	}
	if ct.Draft != "keep" {
		t.Fatalf("startup banner changed the draft: %q", ct.Draft)
	}
	if err := tr.Click("Configuration details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAdvanced {
		t.Fatalf("banner opened settings=%v tab=%q, want Advanced", a.settingsOpen, a.settingsTab)
	}
	a.closeSettings()
}

func TestTaskFailureKeepsAcceptedHistoryAndLaterDraft(t *testing.T) {
	ct := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "session-1", Loaded: true,
		ActiveTaskID: "task-1", Draft: "later edit",
		Messages: []*message{{Key: "live:task-1:user", Kind: msgUser, Text: "sent task"}},
	}
	a := &appModel{demo: false, runtimeStatus: runtimeStateReady, scrollTo: -1, contacts: []*contact{ct}}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "session-1", TaskID: "task-1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed",
			Error: &runtimeError{Code: "model_error", Message: "upstream 401 Unauthorized"}},
	})
	if len(ct.Messages) != 1 || ct.Messages[0].Text != "sent task" {
		t.Fatalf("accepted history was changed: %+v", ct.Messages)
	}
	if ct.Draft != "later edit" {
		t.Fatalf("later unsent draft changed: %q", ct.Draft)
	}
	for _, want := range []string{"Model request failed", "401", "no automatic retry"} {
		if !strings.Contains(ct.StatusDetail, want) {
			t.Errorf("StatusDetail = %q, missing %q", ct.StatusDetail, want)
		}
	}
	if ct.ActiveTaskID != "" || ct.SendState != sendIdle || ct.SessionState != "unknown" {
		t.Fatalf("terminal state = %+v", ct)
	}
}

func TestProviderAuthFailureUsesKnownHTTPPrefix(t *testing.T) {
	for _, message := range []string{"HTTP 401 Unauthorized: bad key", "HTTP 403 Forbidden: no access"} {
		if !providerAuthFailure(message) {
			t.Errorf("providerAuthFailure(%q) = false", message)
		}
	}
	for _, message := range []string{"proxy returned 401 text", "HTTP 4010 Teapot", "HTTP 4030 Forbidden", "HTTP 500 Internal Server Error", "upstream request failed"} {
		if providerAuthFailure(message) {
			t.Errorf("providerAuthFailure(%q) guessed from arbitrary text", message)
		}
	}
}

func TestProviderAuthDetailDoesNotEchoBody(t *testing.T) {
	payload := &runtimeEvent{Status: "failed", Error: &runtimeError{
		Code: "model_error", Message: "HTTP 401 Unauthorized: Incorrect API key provided: sk-sentinel-112",
	}}
	detail := taskFinishedDetail(payload)
	if !strings.Contains(detail, "Provider authentication failed") || !strings.Contains(detail, "HTTP 401") {
		t.Fatalf("detail = %q", detail)
	}
	for _, leaked := range []string{"Incorrect API key", "sk-sentinel-112"} {
		if strings.Contains(detail, leaked) {
			t.Fatalf("auth detail echoed %q: %q", leaked, detail)
		}
	}
}

func TestProviderAuthFailureUIAndRecovery(t *testing.T) {
	ct := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "session-1", Loaded: true,
		ActiveTaskID: "task-1", SendState: sendSubmitting, Draft: "later edit",
		Messages: []*message{{Key: "live:task-1:user", Kind: msgUser, Text: "sent task"}},
	}
	a := &appModel{demo: false, runtimeStatus: runtimeStateReady, runtimeHandshake: true, scrollTo: -1, contacts: []*contact{ct}}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "session-1", TaskID: "task-1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed",
			Error: &runtimeError{Code: "model_error", Message: "HTTP 401 Unauthorized: Incorrect API key provided: sk-sentinel-112"}},
	})
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" || ct.Draft != "later edit" {
		t.Fatalf("failed auth task did not return to idle with draft: %+v", ct)
	}
	for _, leaked := range []string{"Incorrect API key", "sk-sentinel-112"} {
		if strings.Contains(ct.StatusDetail, leaked) {
			t.Fatalf("UI detail echoed %q: %q", leaked, ct.StatusDetail)
		}
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Provider authentication failed") || tr.HasText("sk-sentinel-112") {
		t.Fatal("provider auth UI must show the bounded classification without the provider body")
	}

	// A later accepted task can start from the same contact; no automatic
	// retry of the rejected task is queued.
	ct.ActiveTaskID = "task-2"
	ct.SendState = sendSubmitting
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "session-1", TaskID: "task-2",
		Event: &runtimeEvent{Type: "task_started"},
	})
	if ct.ActiveTaskID != "task-2" || ct.SendState != sendAccepted || ct.SessionState != "running" {
		t.Fatalf("recovery task state = %+v", ct)
	}
	if len(ct.Messages) != 1 || ct.Messages[0].Text != "sent task" {
		t.Fatalf("auth retry changed accepted history: %+v", ct.Messages)
	}
}

func TestProtocolFailureExitClearsActiveTaskAndKeepsDraft(t *testing.T) {
	ct := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "session-1", Loaded: true,
		ActiveTaskID: "task-1", SendState: sendSubmitting, Status: statusWorking,
		SessionState: "running", Draft: "later edit",
		Messages: []*message{{Key: "live:task-1:user", Kind: msgUser, Text: "sent task"}},
	}
	a := &appModel{
		runtimeStatus: runtimeStateReady, runtimeHandshake: true, scrollTo: -1,
		contacts: []*contact{ct}, rt: &runtimeClient{stderr: []string{"API_KEY is not set"}},
	}
	a.protocolFailed("unsupported runtime protocol version 2")
	a.applyRuntimeExit(errors.New("exit status 1"))

	if a.runtimeStage != "Runtime protocol" || !strings.Contains(a.runtimeErr, "unsupported runtime protocol version 2") {
		t.Fatalf("protocol diagnosis overwritten: stage=%q err=%q", a.runtimeStage, a.runtimeErr)
	}
	if a.runtimeStatus != runtimeStateExited || ct.ActiveTaskID != "" {
		t.Fatalf("protocol exit did not clear active task: status=%q contact=%+v", a.runtimeStatus, ct)
	}
	if ct.SendState != sendUnknown || ct.SessionState != "unknown" || ct.Status != statusAttention {
		t.Fatalf("protocol exit did not preserve unknown delivery: %+v", ct)
	}
	if ct.Draft != "later edit" || len(ct.Messages) != 1 || ct.Messages[0].Text != "sent task" {
		t.Fatalf("protocol exit changed history/draft: %+v", ct)
	}
	if !strings.Contains(ct.SendError, "Delivery/result not confirmed") || !strings.Contains(ct.StatusDetail, "reload to verify") {
		t.Fatalf("protocol exit lost unknown-result cleanup: %+v", ct)
	}
	if strings.Contains(a.runtimeHint, "API_KEY") {
		t.Fatalf("protocol exit fell back to configuration hint: %q", a.runtimeHint)
	}
}

func TestRuntimeExitKeepsProtocolAndDrain(t *testing.T) {
	p := &appModel{
		runtimeStatus: runtimeStateFailed,
		runtimeStage:  "Runtime protocol",
		runtimeErr:    "Runtime protocol error: malformed event",
		rt:            &runtimeClient{stderr: []string{"API_KEY is not set"}},
	}
	p.applyRuntimeExit(errors.New("exit status 1"))
	if p.runtimeStage != "Runtime protocol" || !strings.Contains(p.runtimeErr, "malformed event") {
		t.Fatalf("protocol failure overwritten: stage=%q err=%q", p.runtimeStage, p.runtimeErr)
	}

	d := &appModel{runtimeStatus: runtimeStateDraining, rt: &runtimeClient{stderr: []string{"API_KEY is not set"}}}
	d.applyRuntimeExit(nil)
	if d.runtimeErr != "" || strings.Contains(d.runtimeStage, "configuration") {
		t.Fatalf("normal drain got a startup error: stage=%q err=%q", d.runtimeStage, d.runtimeErr)
	}
}

func TestRuntimeExitUnknownBeforeReadyIsStartup(t *testing.T) {
	a := &appModel{
		runtimeStatus: runtimeStateStarting,
		workDir:       "/tmp/saki-112-work",
		rt:            &runtimeClient{stderr: []string{"panic: unexpected handshake"}},
	}
	a.applyRuntimeExit(errors.New("exit status 2"))
	if a.runtimeStage != "Runtime startup" {
		t.Fatalf("stage = %q, want Runtime startup", a.runtimeStage)
	}
	if !strings.Contains(a.runtimeErr, "exited before ready") || strings.Contains(a.runtimeHint, "API_KEY") {
		t.Fatalf("unknown startup classified as configuration: err=%q hint=%q", a.runtimeErr, a.runtimeHint)
	}
}

func TestRealRuntimeStartupConfigurationFailure(t *testing.T) {
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN for the real startup failure check")
	}
	root := t.TempDir()
	env := withoutEnv(os.Environ(), "API_KEY")
	env = append(env,
		"SAKI_API=openai-responses",
		"SAKI_BASE_URL=http://127.0.0.1:1/v1",
		"SAKI_MODEL=fixture",
		"SAKI_SESSION_DIR="+filepath.Join(root, "sessions"),
	)
	ready := make(chan runtimeReady, 1)
	exited := make(chan error, 1)
	client, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: root, Env: env,
		Handlers: runtimeHandlers{
			OnReady: func(value runtimeReady) { ready <- value },
			OnExit:  func(err error) { exited <- err },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.shutdown() }()
	select {
	case <-ready:
		t.Fatal("runtime without API_KEY must not report ready")
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatal("runtime did not exit after configuration failure")
	}
	diagnostic := strings.Join(client.diagnostics(), "\n")
	if !strings.Contains(diagnostic, "API_KEY is not set") {
		t.Fatalf("missing safe configuration diagnostic: %q", diagnostic)
	}
	if strings.Contains(diagnostic, "sk-") {
		t.Fatalf("diagnostic leaked a secret: %q", diagnostic)
	}
}

func TestRealProviderAuthFailureClassification(t *testing.T) {
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN for the real provider auth check")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-sentinel-112"}}`))
	}))
	defer server.Close()
	root := t.TempDir()
	env := withoutEnv(os.Environ(), "API_KEY")
	env = append(env,
		"API_KEY=offline-fixture",
		"SAKI_API=openai-responses",
		"SAKI_BASE_URL="+server.URL+"/v1",
		"SAKI_MODEL=fixture",
		"SAKI_CONTEXT_TOKENS=65536",
		"SAKI_SESSION_DIR="+filepath.Join(root, "sessions"),
	)
	ready := make(chan runtimeReady, 1)
	events := make(chan runtimeEventEnvelope, 8)
	exited := make(chan error, 1)
	client, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: root, Env: env,
		Handlers: runtimeHandlers{
			OnReady: func(value runtimeReady) { ready <- value },
			OnEvent: func(event runtimeEventEnvelope) { events <- event },
			OnExit:  func(err error) { exited <- err },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.shutdown() }()
	select {
	case <-ready:
	case err := <-exited:
		t.Fatalf("runtime exited before ready: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	session, err := client.createSession(root, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.sendTask(session.SessionID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case event := <-events:
			if event.TaskID == task.TaskID && event.Event.Type == "task_finished" {
				if event.Event.Status != "failed" || event.Event.Error == nil || event.Event.Error.Code != "model_error" {
					t.Fatalf("auth task terminal = %+v", event.Event)
				}
				if !providerAuthFailure(event.Event.Error.Message) {
					t.Fatalf("auth message = %q", event.Event.Error.Message)
				}
				detail := taskFinishedDetail(event.Event)
				if !strings.Contains(detail, "Provider authentication failed") {
					t.Fatalf("auth detail = %q", detail)
				}
				if strings.Contains(detail, "sk-sentinel-112") || strings.Contains(detail, "Incorrect API key") {
					t.Fatalf("auth detail echoed provider body: %q", detail)
				}
				return
			}
		case <-exited:
			t.Fatal("runtime exited before task terminal")
		case <-deadline:
			t.Fatal("auth task did not finish")
		}
	}
}

func withoutEnv(env []string, name string) []string {
	out := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, name+"=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
