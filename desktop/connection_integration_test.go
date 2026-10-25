package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionProcessProbeBothProtocolsAndRepair(t *testing.T) {
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN to verify settings against the real Rust helper")
	}
	for _, api := range []string{connectionAPIResponses, connectionAPIChatCompletions} {
		t.Run(api, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer valid-fixture-key" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":{"message":"rejected invalid-fixture-key"}}`))
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error("provider could not decode request")
				}
				if body["model"] != "settings-fixture" {
					t.Error("request did not use the saved default model")
				}
				if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
					t.Error("connection test exposed tools")
				}
				if api == connectionAPIResponses {
					if r.URL.Path != "/v1/responses" {
						t.Error("responses adapter used unexpected path")
					}
					_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"message","id":"answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`))
				} else {
					if r.URL.Path != "/v1/chat/completions" {
						t.Error("chat adapter used unexpected path")
					}
					_, _ = w.Write([]byte(`{"id":"chat","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
				}
			}))
			defer server.Close()
			config := testConnectionConfig(t)
			config.API, config.Endpoint, config.Model = api, server.URL+"/v1", "settings-fixture"
			config.APIKey = "invalid-fixture-key"
			path := filepath.Join(t.TempDir(), "connection.json")
			if err := saveConnectionConfig(path, config); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatal("saving configuration sent a model request")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := probeConnection(ctx, binary, config, nil, t.TempDir())
			if err == nil || strings.Contains(err.Error(), config.APIKey) {
				t.Fatal("authentication failure must be observable without exposing the key")
			}
			config.APIKey = "valid-fixture-key"
			if err := saveConnectionConfig(path, config); err != nil {
				t.Fatal(err)
			}
			restored, err := loadConnectionConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			// Conflicting inherited values must not win over the saved GUI source.
			parent := []string{"API_KEY=wrong", "SAKI_BASE_URL=http://127.0.0.1:1", "SAKI_MODEL=wrong", "SAKI_OUTPUT_TOKENS=1"}
			result, err := probeConnection(ctx, binary, restored, parent, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			actual := result.Config.EffectiveConfig
			if result.Connection.Endpoint != config.Endpoint || actual.Model != config.Model || actual.Compaction.ContextTokens != config.ContextTokens || actual.Compaction.OutputTokens != config.OutputTokens || actual.ConnectionBinding.State != "bound" {
				t.Fatal("runtime-reported effective configuration differs from saved configuration")
			}
			if requests.Load() != 2 {
				t.Fatalf("wanted one request for each explicit test; got %d", requests.Load())
			}
		})
	}
}

func TestConnectionProcessSavedChangeKeepsActiveSnapshotAndHistory(t *testing.T) {
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN to verify settings against the real Rust helper")
	}
	hold := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(hold) }) })
	providerA := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	serverA := httptest.NewServer(providerA.handler(t, "endpoint-A"))
	defer serverA.Close()
	providerB := &providerFixture{}
	serverB := httptest.NewServer(providerB.handler(t, "endpoint-B"))
	defer serverB.Close()
	defer release.Do(func() { close(hold) })
	root := t.TempDir()
	path := filepath.Join(root, "connection.json")
	sessions := filepath.Join(root, "sessions")
	configA := testConnectionConfig(t)
	configA.API, configA.Endpoint, configA.Model = connectionAPIResponses, serverA.URL, "model-A"
	if err := saveConnectionConfig(path, configA); err != nil {
		t.Fatal(err)
	}
	launchA, err := prepareConnectionLaunch(configA, nil, sessions, root)
	if err != nil {
		t.Fatal(err)
	}
	defer launchA.Cleanup()
	first := startIntegrationRuntime(t, binary, launchA.Dir, launchA.Env)
	t.Cleanup(func() { _ = first.client.shutdown() })
	var session runtimeSession
	if err := first.client.call("session.create", map[string]any{"storage_schema": 2, "config": map[string]any{"connection_id": connectionID}}, &session, runtimeControlTimeout); err != nil {
		t.Fatal(err)
	}
	task, err := first.client.sendTask(session.SessionID, "Continue on A while defaults are edited.")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-providerA.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("A did not start")
	}
	configB := configA
	configB.Endpoint, configB.Model = serverB.URL, "model-B"
	if err := saveConnectionConfig(path, configB); err != nil {
		t.Fatal(err)
	}
	actualA, err := first.client.connectionConfig(session.SessionID)
	if err != nil || actualA.EffectiveConfig.Model != "model-A" || providerB.count() != 0 {
		t.Fatal("saving changed a running helper or started B")
	}
	release.Do(func() { close(hold) })
	first.finishTask(t, task.TaskID)
	if err := first.client.shutdown(); err != nil {
		t.Fatal(err)
	}
	restored, err := loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	launchB, err := prepareConnectionLaunch(restored, nil, sessions, root)
	if err != nil {
		t.Fatal(err)
	}
	defer launchB.Cleanup()
	second := startIntegrationRuntime(t, binary, launchB.Dir, launchB.Env)
	defer second.client.shutdown()
	if _, err := second.client.loadSession(session.SessionID); err == nil {
		t.Fatal("a bound session silently changed its endpoint")
	}
	history, err := second.client.historyPage(session.SessionID, 0, 100)
	if err != nil || len(history.Records) == 0 {
		t.Fatal("old history became unreadable after changing the default endpoint")
	}
	oldConfig, err := second.client.connectionConfig(session.SessionID)
	if err != nil || oldConfig.EffectiveConfig.Model != "model-A" {
		t.Fatal("default change rewrote the old session configuration")
	}
	newSession, err := second.client.createSession(configB.WorkingDirectory, "new work")
	if err != nil {
		t.Fatal(err)
	}
	newTask, err := second.client.sendTask(newSession.SessionID, "Use the new default.")
	if err != nil {
		t.Fatal(err)
	}
	second.finishTask(t, newTask.TaskID)
	if providerA.count() != 1 || providerB.count() != 1 || !strings.Contains(providerB.body(0), `"model":"model-B"`) {
		t.Fatal("new work did not reach the new default endpoint/model exactly once")
	}
}
