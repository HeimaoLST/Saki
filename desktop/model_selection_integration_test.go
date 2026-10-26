package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type modelProviderFixture struct {
	mu      sync.Mutex
	models  []string
	hold    <-chan struct{}
	arrived chan struct{}
	opaque  bool
}

func (p *modelProviderFixture) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.models = append(p.models, body.Model)
	first := len(p.models) == 1
	p.mu.Unlock()
	if first && p.arrived != nil {
		close(p.arrived)
	}
	if first && p.hold != nil {
		<-p.hold
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/chat/completions" {
		_, _ = w.Write([]byte(`{"id":"chat","choices":[{"index":0,"message":{"role":"assistant","content":"model fixture answer"},"finish_reason":"stop"}]}`))
	} else if p.opaque {
		_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"reasoning","id":"reasoning","status":"completed","content":[{"type":"reasoning_text","text":"opaque"}],"encrypted_content":"opaque-fixture"},{"type":"message","id":"answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"model fixture answer"}]}]}`))
	} else {
		_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"message","id":"answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"model fixture answer"}]}]}`))
	}
}

func (p *modelProviderFixture) requestedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func modelTestLaunch(t *testing.T, api, endpoint string) (*connectionLaunch, string) {
	t.Helper()
	root := t.TempDir()
	config := testConnectionConfig(t)
	config.API, config.Endpoint, config.Model = api, endpoint+"/v1", "model-A"
	launch, err := prepareConnectionLaunch(config, nil, filepath.Join(root, "sessions"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Cleanup() })
	return launch, filepath.Join(root, "sessions")
}

func modelTestCompaction() runtimeCompaction {
	return runtimeCompaction{ContextTokens: 32768, OutputTokens: 4096, KeepRecentTokens: 512, MaxSummaryTokens: 512}
}

func startModelTestRuntime(t *testing.T, binary, dir string, env []string, args ...string) *liveRuntime {
	t.Helper()
	live := &liveRuntime{ready: make(chan runtimeReady, 1), events: make(chan runtimeEventEnvelope, 64), exit: make(chan error, 1), protocol: make(chan string, 4)}
	client, err := startRuntime(runtimeOptions{Binary: binary, Args: args, Dir: dir, Env: env, Handlers: runtimeHandlers{
		OnReady: func(v runtimeReady) { live.ready <- v }, OnEvent: func(v runtimeEventEnvelope) { live.events <- v }, OnExit: func(err error) { live.exit <- err }, OnProtocolError: func(v string) { live.protocol <- v },
	}})
	if err != nil {
		t.Fatal(err)
	}
	live.client = client
	select {
	case <-live.ready:
	case err := <-live.protocol:
		t.Fatal(err)
	case <-client.done:
		t.Fatal("helper exited before ready")
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not become ready")
	}
	return live
}

// The proxy drops exactly one config-update response from the real Rust
// process. The subsequent config.get still travels over the same stdio link.
func TestModelSelectionProxyHelper(t *testing.T) {
	if os.Getenv("SAKI_MODEL_PROXY") != "1" {
		return
	}
	cmd := exec.Command(os.Getenv("SAKI_MODEL_PROXY_BINARY"), "--stdio")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var updates sync.Map
	var reads sync.Map
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 65536), runtimeMaxLine)
		for scanner.Scan() {
			var request struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &request)
			if request.Method == "session.config.update" {
				updates.Store(request.ID, true)
				file, err := os.OpenFile(os.Getenv("SAKI_MODEL_PROXY_COUNT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return
				}
				_, _ = file.WriteString("update\n")
				_ = file.Close()
			}
			if request.Method == "session.config.get" {
				reads.Store(request.ID, true)
			}
			if _, err := input.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
				break
			}
		}
		_ = input.Close()
	}()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 65536), runtimeMaxLine)
	dropped := false
	failedRead := false
	mode := os.Getenv("SAKI_MODEL_PROXY_MODE")
	writeError := func(id, code string) {
		data, _ := json.Marshal(map[string]any{"version": runtimeProtocolVersion, "type": "response", "id": id, "error": map[string]string{"code": code, "message": "controlled reply failure after the real update"}})
		_, _ = os.Stdout.Write(append(data, '\n'))
	}
	for scanner.Scan() {
		var response struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &response)
		if _, ok := updates.Load(response.ID); ok && !dropped {
			dropped = true
			if mode == "commit_unknown" {
				writeError(response.ID, "commit_unknown")
			}
			continue
		}
		if _, ok := reads.Load(response.ID); ok && dropped && !failedRead && mode == "fail_readback" {
			failedRead = true
			writeError(response.ID, "io_error")
			continue
		}
		_, _ = os.Stdout.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
	}
	if err := cmd.Wait(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestModelSelectionProcessLostReplyReadsCommittedValue(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &modelProviderFixture{}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	launch, sessions := modelTestLaunch(t, connectionAPIResponses, server.URL)
	countPath := filepath.Join(t.TempDir(), "updates")
	env := append(launch.Env, "SAKI_MODEL_PROXY=1", "SAKI_MODEL_PROXY_BINARY="+binary, "SAKI_MODEL_PROXY_COUNT="+countPath)
	live := startModelTestRuntime(t, os.Args[0], launch.Dir, env, "-test.run=^TestModelSelectionProxyHelper$")
	t.Cleanup(func() { _ = live.client.shutdown() })
	session, err := live.client.createConfiguredSession(launch.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	sendModelTask(t, live, session.SessionID)
	initial, err := live.client.connectionConfig(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(sessions, session.SessionID, "history.jsonl")
	before, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	capacity := modelTestCompaction()
	result, err := live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "model-B", Compaction: &capacity})
	if err == nil || !strings.Contains(err.Error(), "timed out") || result.ReadbackError != nil {
		t.Fatalf("expected a lost reply and successful readback: update=%v readback=%v", err, result.ReadbackError)
	}
	if result.EffectiveConfig.Model != "model-B" || result.ConfigRevision != initial.ConfigRevision+1 {
		t.Fatal("lost reply did not return the actual committed model")
	}
	requests, err := os.ReadFile(countPath)
	if err != nil || string(requests) != "update\n" {
		t.Fatal("an uncertain update was submitted again")
	}
	after, err := os.ReadFile(historyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("reconciliation altered history")
	}
	if err := live.client.releaseSession(session.SessionID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := live.client.loadSession(session.SessionID)
	if err != nil || reloaded.Metadata == nil || reloaded.Metadata.Model != "model-B" {
		t.Fatalf("explicit release/load lost the committed configuration: %v", err)
	}
	after, err = os.ReadFile(historyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("explicit release/load altered history")
	}
	sendModelTask(t, live, session.SessionID)
	if !reflect.DeepEqual(provider.requestedModels(), []string{"model-A", "model-B"}) {
		t.Fatal("subsequent request did not use the reconciled model")
	}
}

func TestModelSelectionProcessCatalogIsCapacityNotEndpointIdentity(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &modelProviderFixture{}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	launch, _ := modelTestLaunch(t, connectionAPIResponses, server.URL)
	cache := filepath.Join(launch.Dir, ".saki", "cache", "models.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"models":{"model-A":{"limit":{"context":65536,"input":60000,"output":8192}},"catalog/B":{"name":"Capacity B","limit":{"context":32768,"input":30000,"output":4096}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, entry := range launch.Env {
		if !strings.HasPrefix(entry, "SAKI_CONTEXT_TOKENS=") {
			env = append(env, entry)
		}
	}
	live := startModelTestRuntime(t, binary, launch.Dir, env)
	t.Cleanup(func() { _ = live.client.shutdown() })
	session, err := live.client.createConfiguredSession(launch.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := live.client.models(connectionID)
	if err != nil || len(catalog.Models) != 2 {
		t.Fatal("runtime catalog was not loaded")
	}
	initial, err := live.client.connectionConfig(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "provider-specific-B", CatalogID: "catalog/B"})
	if err != nil || changed.EffectiveConfig.Model != "provider-specific-B" || changed.EffectiveConfig.CapacitySource.CatalogID != "catalog/B" || changed.EffectiveConfig.Compaction.ContextTokens != 32768 {
		t.Fatalf("catalog selection was not reflected by Runtime: %v", err)
	}
	if len(provider.requestedModels()) != 0 {
		t.Fatal("catalog selection performed a model request")
	}
	sendModelTask(t, live, session.SessionID)
	if !reflect.DeepEqual(provider.requestedModels(), []string{"provider-specific-B"}) {
		t.Fatal("catalog ID was incorrectly used as the provider model ID")
	}
}

func sendModelTask(t *testing.T, live *liveRuntime, sessionID string) {
	t.Helper()
	task, err := live.client.sendTask(sessionID, "Continue the same work.")
	if err != nil {
		t.Fatal(err)
	}
	events := live.finishTask(t, task.TaskID)
	if len(events) == 0 || events[len(events)-1].Event.Status != "completed" {
		t.Fatal("fixture task did not complete")
	}
}

func assertModelError(t *testing.T, err error, code string) {
	t.Helper()
	var protocol *runtimeError
	if !errors.As(err, &protocol) || protocol.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func TestModelSelectionProcessSwitchRestartAndFailureKeepsHistory(t *testing.T) {
	binary := requireRuntimeBinary(t)
	for _, api := range []string{connectionAPIChatCompletions, connectionAPIResponses} {
		t.Run(api, func(t *testing.T) {
			provider := &modelProviderFixture{}
			server := httptest.NewServer(http.HandlerFunc(provider.handler))
			defer server.Close()
			launch, sessions := modelTestLaunch(t, api, server.URL)
			live := startModelTestRuntime(t, binary, launch.Dir, launch.Env)
			t.Cleanup(func() { _ = live.client.shutdown() })
			session, err := live.client.createConfiguredSession(launch.Dir, "Keep the same context.")
			if err != nil {
				t.Fatal(err)
			}
			other, err := live.client.createConfiguredSession(launch.Dir, "An independent work session.")
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := live.client.models(connectionID)
			if err != nil || len(catalog.ConfiguredModels) != 1 || catalog.ConfiguredModels[0].Model != "model-A" || len(provider.requestedModels()) != 0 {
				t.Fatal("model directory did not expose host facts without inference")
			}
			sendModelTask(t, live, session.SessionID)
			historyPath := filepath.Join(sessions, session.SessionID, "history.jsonl")
			before, err := os.ReadFile(historyPath)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := live.client.connectionConfig(session.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "unknown-limits"})
			assertModelError(t, err, "model_limits_required")
			invalid := modelTestCompaction()
			invalid.ContextTokens = 1
			_, err = live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "model-B", Compaction: &invalid})
			assertModelError(t, err, "invalid_model_limits")
			retained, err := live.client.connectionConfig(session.SessionID)
			if err != nil || retained.ConfigRevision != initial.ConfigRevision || retained.EffectiveConfig.Model != "model-A" {
				t.Fatal("rejected change altered the original config")
			}
			capacity := modelTestCompaction()
			changed, err := live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "model-B", Compaction: &capacity})
			if err != nil || changed.EffectiveConfig.Model != "model-B" || changed.EffectiveFrom != "next_task" {
				t.Fatalf("B was not applied: %v", err)
			}
			_, err = live.client.updateSessionModel(session.SessionID, initial.ConfigRevision, runtimeModelPatch{Model: "model-A"})
			assertModelError(t, err, "config_conflict")
			otherConfig, err := live.client.connectionConfig(other.SessionID)
			if err != nil || otherConfig.EffectiveConfig.Model != "model-A" {
				t.Fatal("selection changed another session")
			}
			after, err := os.ReadFile(historyPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("configuration updates rewrote history")
			}
			sendModelTask(t, live, session.SessionID)
			if err := live.client.shutdown(); err != nil {
				t.Fatal(err)
			}
			live = startModelTestRuntime(t, binary, launch.Dir, launch.Env)
			if _, err := live.client.loadSession(session.SessionID); err != nil {
				t.Fatal(err)
			}
			restored, err := live.client.connectionConfig(session.SessionID)
			if err != nil || restored.EffectiveConfig.Model != "model-B" || restored.ConfigRevision != changed.ConfigRevision || restored.EffectiveConfig.Compaction.ContextTokens != capacity.ContextTokens {
				t.Fatal("restart did not retain model B and its capacity")
			}
			sendModelTask(t, live, session.SessionID)
			back, err := live.client.updateSessionModel(session.SessionID, restored.ConfigRevision, runtimeModelPatch{Model: "model-A"})
			if err != nil || back.EffectiveConfig.Model != "model-A" {
				t.Fatalf("could not return to A: %v", err)
			}
			sendModelTask(t, live, session.SessionID)
			if !reflect.DeepEqual(provider.requestedModels(), []string{"model-A", "model-B", "model-B", "model-A"}) {
				t.Fatal("actual requests differed from effective model")
			}
		})
	}
}

func TestModelSelectionProcessBusyAndOpaqueRefusal(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	provider := &modelProviderFixture{hold: hold, arrived: make(chan struct{}), opaque: true}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	defer release()
	launch, sessions := modelTestLaunch(t, connectionAPIResponses, server.URL)
	live := startModelTestRuntime(t, binary, launch.Dir, launch.Env)
	t.Cleanup(func() { _ = live.client.shutdown() })
	session, err := live.client.createConfiguredSession(launch.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	config, err := live.client.connectionConfig(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := live.client.sendTask(session.SessionID, "Create native continuation.")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("task did not reach the endpoint")
	}
	capacity := modelTestCompaction()
	_, err = live.client.updateSessionModel(session.SessionID, config.ConfigRevision, runtimeModelPatch{Model: "model-B", Compaction: &capacity})
	assertModelError(t, err, "busy")
	release()
	live.finishTask(t, task.TaskID)
	historyPath := filepath.Join(sessions, session.SessionID, "history.jsonl")
	before, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = live.client.updateSessionModel(session.SessionID, config.ConfigRevision, runtimeModelPatch{Model: "model-B", Compaction: &capacity})
	assertModelError(t, err, "continuation_not_supported")
	after, err := os.ReadFile(historyPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("refused continuation change altered history")
	}
	actual, err := live.client.connectionConfig(session.SessionID)
	if err != nil || actual.ConfigRevision != config.ConfigRevision || actual.EffectiveConfig.Model != "model-A" {
		t.Fatal("refusal changed the effective model")
	}
}
