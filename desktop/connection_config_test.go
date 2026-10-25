package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testConnectionConfig(t *testing.T) connectionConfig {
	return connectionConfig{
		Version:          1,
		API:              connectionAPIResponses,
		Endpoint:         "https://example.com/v1",
		APIKey:           "test-connection-key",
		Model:            "test-model",
		ContextTokens:    65536,
		OutputTokens:     8192,
		WorkingDirectory: t.TempDir(),
		MaxActiveTasks:   2,
	}
}

func TestConnectionConfigSaveLoadPermissionsAndRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "connection.json")
	config := testConnectionConfig(t)
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("connection file mode = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("connection directory mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	loaded, err := loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Endpoint != config.Endpoint || loaded.Model != config.Model || loaded.APIKey != config.APIKey ||
		loaded.ContextTokens != config.ContextTokens || loaded.OutputTokens != config.OutputTokens ||
		loaded.WorkingDirectory != config.WorkingDirectory || loaded.MaxActiveTasks != config.MaxActiveTasks {
		t.Fatal("saved connection config did not round-trip")
	}

	config.Model = "edited-model"
	config.Endpoint = "https://edited.example/v1"
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Model != "edited-model" || loaded.Endpoint != "https://edited.example/v1" {
		t.Fatal("edited connection config did not round-trip")
	}
}

func TestConnectionConfigSaveFailureKeepsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	original := testConnectionConfig(t)
	if err := saveConnectionConfig(path, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	edited := original
	edited.Model = "must-not-be-saved"
	edited.Endpoint = "https://must-not-be-saved.example/v1"
	if err := saveConnectionConfig(path, edited); err == nil {
		t.Fatal("save unexpectedly succeeded into a read-only directory")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed the previous connection file")
	}
	loaded, err := loadConnectionConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Model != original.Model || loaded.Endpoint != original.Endpoint {
		t.Fatal("previous connection values were not retained")
	}
}

func TestConnectionConfigLoadRejectsBadFilesWithoutReplacingThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")
	original := []byte(`{"version":1,`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConnectionConfig(path); err == nil {
		t.Fatal("malformed connection file was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("bad connection file was replaced")
	}

	secret := "bad-file-secret-value"
	invalid := `{"version":1,"api":"chat","endpoint":"https://example.com/v1","api_key":"` + secret + `","model":"m","context_tokens":32768,"output_tokens":8192,"working_directory":"` + dir + `","max_active_tasks":1}`
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	repairable, err := loadConnectionConfig(path)
	if err == nil {
		t.Fatal("invalid connection config was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("load error exposed sensitive data")
	}
	if repairable.Model != "m" || repairable.APIKey != secret {
		t.Fatal("invalid saved values must remain available for correction in the form")
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != invalid {
		t.Fatal("invalid connection file was replaced")
	}

	if _, err := loadConnectionConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing connection file was silently created")
	}
}

func TestConnectionConfigValidationFields(t *testing.T) {
	valid := testConnectionConfig(t)
	if fields := validateConnectionConfig(valid); len(fields) != 0 {
		t.Fatalf("valid config rejected with fields %v", sortedKeys(fields))
	}
	valid.API = connectionAPIChatCompletions
	if fields := validateConnectionConfig(valid); len(fields) != 0 {
		t.Fatal("chat completions API was rejected")
	}

	check := func(name string, mutate func(*connectionConfig), key string) {
		t.Helper()
		config := testConnectionConfig(t)
		mutate(&config)
		fields := validateConnectionConfig(config)
		if fields[key] == "" {
			t.Fatalf("%s: missing error for %s", name, key)
		}
	}
	check("version", func(c *connectionConfig) { c.Version = 2 }, "Version")
	check("api shorthand", func(c *connectionConfig) { c.API = "chat" }, "API")
	check("blank key", func(c *connectionConfig) { c.APIKey = " \t " }, "APIKey")
	check("blank model", func(c *connectionConfig) { c.Model = " \n" }, "Model")
	check("zero context", func(c *connectionConfig) { c.ContextTokens = 0 }, "ContextTokens")
	check("zero output", func(c *connectionConfig) { c.OutputTokens = 0 }, "OutputTokens")
	check("output exceeds context", func(c *connectionConfig) { c.ContextTokens = 100; c.OutputTokens = 100 }, "OutputTokens")
	check("context leaves no compaction room", func(c *connectionConfig) { c.ContextTokens = 100; c.OutputTokens = 97 }, "ContextTokens")
	check("relative cwd", func(c *connectionConfig) { c.WorkingDirectory = "relative/path" }, "WorkingDirectory")
	check("missing cwd", func(c *connectionConfig) { c.WorkingDirectory = filepath.Join(t.TempDir(), "missing") }, "WorkingDirectory")
	check("zero capacity", func(c *connectionConfig) { c.MaxActiveTasks = 0 }, "MaxActiveTasks")

	for _, endpoint := range []string{
		"",
		"ftp://example.com/v1",
		"https://user@example.com/v1",
		"https://user:pw@example.com/v1",
		"https://example.com/v1?x=1",
		"https://example.com/v1?",
		"https://example.com/v1#fragment",
		"https://example.com/v1#",
		"relative/path",
		" https://example.com/v1",
	} {
		config := testConnectionConfig(t)
		config.Endpoint = endpoint
		fields := validateConnectionConfig(config)
		if fields["Endpoint"] == "" {
			t.Fatalf("endpoint %q: missing Endpoint error", endpoint)
		}
	}
}

func TestPrepareConnectionLaunchFiltersEnvAndWritesDerivedHostConfig(t *testing.T) {
	config := testConnectionConfig(t)
	tempRoot := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	parentEnv := []string{
		"PATH=/usr/bin",
		"KEEP_ME=yes",
		"API_KEY=legacy-key",
		"SAKI_API=openai-chat-completions",
		"SAKI_BASE_URL=https://legacy.example/v1",
		"SAKI_MODEL=legacy-model",
		"SAKI_MODEL_ID=legacy-model-id",
		"SAKI_CONTEXT_TOKENS=999",
		"SAKI_OUTPUT_TOKENS=111",
		"SAKI_COMPACT_KEEP_TOKENS=222",
		"SAKI_COMPACT_SUMMARY_TOKENS=333",
		"SAKI_SESSION_DIR=/legacy/sessions",
		"SAKI_RUNTIME_CONFIG=/legacy/runtime.json",
		"NAGI_CONNECTION_API_KEY=legacy-nagi-key",
	}
	originalParent := append([]string(nil), parentEnv...)
	launch, err := prepareConnectionLaunch(config, parentEnv, sessionDir, tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	if !reflect.DeepEqual(parentEnv, originalParent) {
		t.Fatal("prepare mutated the parent environment")
	}
	if launch.Dir != config.WorkingDirectory {
		t.Fatal("launch working directory mismatch")
	}
	if filepath.Dir(launch.HostConfigPath) == tempRoot {
		t.Fatal("launch host config must be inside its own directory")
	}
	hostInfo, err := os.Stat(launch.HostConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if hostInfo.Mode().Perm() != 0o600 {
		t.Fatalf("host config mode = %o, want 0600", hostInfo.Mode().Perm())
	}
	launchDirInfo, err := os.Stat(filepath.Dir(launch.HostConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if launchDirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("launch directory mode = %o, want 0700", launchDirInfo.Mode().Perm())
	}
	hostBytes, err := os.ReadFile(launch.HostConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hostBytes), config.APIKey) {
		t.Fatal("host config contains sensitive data")
	}
	var host struct {
		DefaultConnectionID string `json:"default_connection_id"`
		Connections         []struct {
			ID           string `json:"id"`
			API          string `json:"api"`
			BaseURL      string `json:"base_url"`
			APIKeyEnv    string `json:"api_key_env"`
			DefaultModel string `json:"default_model"`
		} `json:"connections"`
		MaxActiveTasks int `json:"max_active_tasks"`
	}
	if err := json.Unmarshal(hostBytes, &host); err != nil {
		t.Fatal(err)
	}
	if host.DefaultConnectionID != "default" || host.MaxActiveTasks != config.MaxActiveTasks || len(host.Connections) != 1 {
		t.Fatal("host config defaults/capacity mismatch")
	}
	connection := host.Connections[0]
	if connection.ID != "default" || connection.API != config.API || connection.BaseURL != config.Endpoint ||
		connection.APIKeyEnv != connectionAPIKeyEnv || connection.DefaultModel != config.Model {
		t.Fatal("host config connection mismatch")
	}

	env := environmentMap(launch.Env)
	for _, key := range []string{"API_KEY", "SAKI_API", "SAKI_BASE_URL", "SAKI_MODEL", "SAKI_MODEL_ID"} {
		if env[key] != "" {
			t.Fatalf("old environment key %s was not filtered", key)
		}
	}
	if env["KEEP_ME"] != "yes" || env["PATH"] == "" {
		t.Fatal("unrelated parent environment was not preserved")
	}
	if env["SAKI_RUNTIME_CONFIG"] != launch.HostConfigPath {
		t.Fatal("runtime host config path mismatch")
	}
	if env["SAKI_SESSION_DIR"] != sessionDir {
		t.Fatal("session directory env mismatch")
	}
	if env[connectionAPIKeyEnv] != config.APIKey {
		t.Fatal("connection API key env mismatch")
	}
	if env["SAKI_CONTEXT_TOKENS"] != "65536" || env["SAKI_OUTPUT_TOKENS"] != "8192" ||
		env["SAKI_COMPACT_KEEP_TOKENS"] != "8192" || env["SAKI_COMPACT_SUMMARY_TOKENS"] != "4096" {
		t.Fatal("explicit or compaction environment values mismatch")
	}
}

func TestPrepareConnectionLaunchAlwaysSetsCompactionEnvAndCleansOwnDirectory(t *testing.T) {
	config := testConnectionConfig(t)
	config.ContextTokens = 1000
	config.OutputTokens = 100
	tempRoot := t.TempDir()
	keep := filepath.Join(tempRoot, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	workingMarker := filepath.Join(config.WorkingDirectory, "working-dir-marker")
	if err := os.WriteFile(workingMarker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch, err := prepareConnectionLaunch(config, nil, filepath.Join(t.TempDir(), "sessions"), tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	env := environmentMap(launch.Env)
	if env["SAKI_COMPACT_KEEP_TOKENS"] != "225" || env["SAKI_COMPACT_SUMMARY_TOKENS"] != "100" {
		t.Fatal("small-budget compaction env values mismatch")
	}
	hostDir := filepath.Dir(launch.HostConfigPath)
	if err := launch.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hostDir); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove its own launch directory")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("cleanup removed a sibling in the temp root")
	}
	if _, err := os.Stat(workingMarker); err != nil {
		t.Fatal("cleanup removed a file in the working directory")
	}
	if err := launch.Cleanup(); err != nil {
		t.Fatal("second cleanup failed")
	}
}

func TestPrepareConnectionLaunchRejectsInvalidConfigWithoutCreatingTempData(t *testing.T) {
	config := testConnectionConfig(t)
	config.APIKey = " "
	tempRoot := t.TempDir()
	if _, err := prepareConnectionLaunch(config, nil, t.TempDir(), tempRoot); err == nil {
		t.Fatal("invalid config was accepted")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("invalid config created temp data")
	}
}

func environmentMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
