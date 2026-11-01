package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	connectionAPIResponses       = "openai-responses"
	connectionAPIChatCompletions = "openai-chat-completions"
	connectionAPIKeyEnv          = "NAGI_CONNECTION_API_KEY"
	connectionID                 = "default"
	connectionHostConfigName     = "host.json"
)

// connectionConfig is the plaintext local GUI source; connection.json contains
// a private local value and must stay private to this user.
type connectionConfig struct {
	Version          int    `json:"version"`
	API              string `json:"api"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"`
	Model            string `json:"model"`
	ContextTokens    int    `json:"context_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	WorkingDirectory string `json:"working_directory"`
	MaxActiveTasks   int    `json:"max_active_tasks"`
}

type connectionLaunch struct {
	Dir            string
	Env            []string
	HostConfigPath string
	tempDir        string
}

func (l *connectionLaunch) Cleanup() error {
	if l == nil || l.tempDir == "" {
		return nil
	}
	if err := os.RemoveAll(l.tempDir); err != nil {
		return fmt.Errorf("remove connection launch directory: %w", err)
	}
	l.tempDir = ""
	return nil
}

func loadConnectionConfig(path string) (connectionConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return connectionConfig{}, fmt.Errorf("read connection config %s: %w", path, err)
	}
	var config connectionConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return connectionConfig{}, fmt.Errorf("parse connection config %s: %w", path, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return connectionConfig{}, fmt.Errorf("parse connection config %s: trailing data", path)
		}
		return connectionConfig{}, fmt.Errorf("parse connection config %s: %w", path, err)
	}
	if fields := validateConnectionConfig(config); len(fields) > 0 {
		return config, fmt.Errorf("invalid connection config %s: %s", path, connectionValidationSummary(fields))
	}
	return config, nil
}

func saveConnectionConfig(path string, config connectionConfig) error {
	if fields := validateConnectionConfig(config); len(fields) > 0 {
		return fmt.Errorf("invalid connection config: %s", connectionValidationSummary(fields))
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connection config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create connection config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("create connection config temp file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("set connection config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write connection config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync connection config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close connection config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace connection config: %w", err)
	}
	return nil
}

type runtimeToolSource struct {
	ID               string   `json:"id"`
	Transport        string   `json:"transport"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	EnvFrom          []string `json:"env_from,omitempty"`
	Protocol         string   `json:"protocol,omitempty"`
	StartupTimeoutMS int      `json:"startup_timeout_ms,omitempty"`
	CallTimeoutMS    int      `json:"call_timeout_ms,omitempty"`
	CancelGraceMS    int      `json:"cancel_grace_ms,omitempty"`
	Endpoint         string   `json:"-"`
}

func prepareConnectionLaunch(config connectionConfig, parentEnv []string, sessionDir, tempRoot string) (*connectionLaunch, error) {
	return prepareConnectionLaunchWithTools(config, parentEnv, sessionDir, tempRoot, nil)
}

func prepareConnectionLaunchWithTools(config connectionConfig, parentEnv []string, sessionDir, tempRoot string, toolSource *runtimeToolSource) (*connectionLaunch, error) {
	if fields := validateConnectionConfig(config); len(fields) > 0 {
		return nil, fmt.Errorf("invalid connection config: %s", connectionValidationSummary(fields))
	}
	if tempRoot == "" {
		tempRoot = os.TempDir()
	}
	absoluteRoot, err := filepath.Abs(tempRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve connection temp root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create connection temp root: %w", err)
	}
	tempDir, err := os.MkdirTemp(absoluteRoot, "nagi-connection-*")
	if err != nil {
		return nil, fmt.Errorf("create connection launch directory: %w", err)
	}
	fail := func(err error) (*connectionLaunch, error) {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	toolSources := []runtimeToolSource(nil)
	if toolSource != nil && strings.TrimSpace(toolSource.Command) != "" {
		toolSources = append(toolSources, *toolSource)
	}
	hostConfig := struct {
		DefaultConnectionID string               `json:"default_connection_id"`
		Connections         []connectionHostItem `json:"connections"`
		MaxActiveTasks      int                  `json:"max_active_tasks"`
		MaxToolProcesses    int                  `json:"max_tool_processes,omitempty"`
		ToolSources         []runtimeToolSource  `json:"tool_sources,omitempty"`
	}{
		DefaultConnectionID: connectionID,
		Connections: []connectionHostItem{{
			ID:           connectionID,
			API:          config.API,
			BaseURL:      config.Endpoint,
			APIKeyEnv:    connectionAPIKeyEnv,
			DefaultModel: config.Model,
		}},
		MaxActiveTasks: config.MaxActiveTasks,
		ToolSources:    toolSources,
	}
	hostData, err := json.MarshalIndent(hostConfig, "", "  ")
	if err != nil {
		return fail(fmt.Errorf("encode runtime host config: %w", err))
	}
	hostData = append(hostData, '\n')
	// The runtime host config is a derived snapshot and never contains that value;
	// it is injected only through the child environment.
	hostPath := filepath.Join(tempDir, connectionHostConfigName)
	hostFile, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(fmt.Errorf("create runtime host config: %w", err))
	}
	if err := hostFile.Chmod(0o600); err != nil {
		hostFile.Close()
		return fail(fmt.Errorf("set runtime host config permissions: %w", err))
	}
	if _, err := hostFile.Write(hostData); err != nil {
		hostFile.Close()
		return fail(fmt.Errorf("write runtime host config: %w", err))
	}
	if err := hostFile.Sync(); err != nil {
		hostFile.Close()
		return fail(fmt.Errorf("sync runtime host config: %w", err))
	}
	if err := hostFile.Close(); err != nil {
		return fail(fmt.Errorf("close runtime host config: %w", err))
	}

	// Keep both compaction limits explicit so an old environment or the model
	// catalog cannot change the configured budget at runtime.
	remaining := config.ContextTokens - config.OutputTokens
	keepRecent := min(8192, remaining/4)
	summaryTokens := min(4096, config.OutputTokens, remaining/4)
	env := filterConnectionEnv(parentEnv)
	env = append(env,
		"SAKI_RUNTIME_CONFIG="+hostPath,
		"SAKI_SESSION_DIR="+sessionDir,
		connectionAPIKeyEnv+"="+config.APIKey,
		"SAKI_CONTEXT_TOKENS="+strconv.Itoa(config.ContextTokens),
		"SAKI_OUTPUT_TOKENS="+strconv.Itoa(config.OutputTokens),
		"SAKI_COMPACT_KEEP_TOKENS="+strconv.Itoa(keepRecent),
		"SAKI_COMPACT_SUMMARY_TOKENS="+strconv.Itoa(summaryTokens),
	)
	if toolSource != nil && toolSource.Endpoint != "" {
		env = append(env, issueToolIPCSocketEnv+"="+toolSource.Endpoint)
	}
	return &connectionLaunch{
		Dir:            config.WorkingDirectory,
		Env:            env,
		HostConfigPath: hostPath,
		tempDir:        tempDir,
	}, nil
}

func validateConnectionConfig(config connectionConfig) map[string]string {
	fields := map[string]string{}
	if config.Version != 1 {
		fields["Version"] = "must be 1"
	}
	if config.API != connectionAPIResponses && config.API != connectionAPIChatCompletions {
		fields["API"] = "must be openai-responses or openai-chat-completions"
	}
	validateConnectionEndpoint(config.Endpoint, fields)
	if strings.TrimSpace(config.APIKey) == "" {
		fields["APIKey"] = "must not be blank"
	}
	if strings.TrimSpace(config.Model) == "" {
		fields["Model"] = "must not be blank"
	}
	validateConnectionWorkingDirectory(config.WorkingDirectory, fields)
	if config.ContextTokens <= 0 {
		fields["ContextTokens"] = "must be positive"
	}
	if config.OutputTokens <= 0 {
		fields["OutputTokens"] = "must be positive"
	} else if config.ContextTokens > 0 {
		if config.ContextTokens <= config.OutputTokens {
			fields["OutputTokens"] = "must be less than ContextTokens"
		} else if config.ContextTokens-config.OutputTokens < 4 {
			fields["ContextTokens"] = "must leave at least 4 tokens after OutputTokens"
		}
	}
	if config.MaxActiveTasks <= 0 {
		fields["MaxActiveTasks"] = "must be positive"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func validateConnectionEndpoint(raw string, fields map[string]string) {
	if raw == "" {
		fields["Endpoint"] = "must not be blank"
		return
	}
	if raw != strings.TrimSpace(raw) {
		fields["Endpoint"] = "must not contain surrounding whitespace"
		return
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		fields["Endpoint"] = "must be an absolute http(s) URL with a host"
		return
	}
	if parsed.User != nil {
		fields["Endpoint"] = "must not contain userinfo"
		return
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "?") {
		fields["Endpoint"] = "must not contain a query"
		return
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		fields["Endpoint"] = "must not contain a fragment"
	}
}

func validateConnectionWorkingDirectory(path string, fields map[string]string) {
	if path == "" || !filepath.IsAbs(path) {
		fields["WorkingDirectory"] = "must be an absolute path"
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fields["WorkingDirectory"] = "must exist and be a directory"
	}
}

func connectionValidationSummary(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+fields[key])
	}
	return strings.Join(parts, "; ")
}

type connectionHostItem struct {
	ID           string `json:"id"`
	API          string `json:"api"`
	BaseURL      string `json:"base_url"`
	APIKeyEnv    string `json:"api_key_env"`
	DefaultModel string `json:"default_model"`
}

var filteredConnectionEnv = map[string]bool{
	"API_KEY":                     true,
	"SAKI_API":                    true,
	"SAKI_BASE_URL":               true,
	"SAKI_MODEL":                  true,
	"SAKI_MODEL_ID":               true,
	"SAKI_CONTEXT_TOKENS":         true,
	"SAKI_OUTPUT_TOKENS":          true,
	"SAKI_COMPACT_KEEP_TOKENS":    true,
	"SAKI_COMPACT_SUMMARY_TOKENS": true,
	"SAKI_SESSION_DIR":            true,
	"SAKI_RUNTIME_CONFIG":         true,
	issueToolIPCSocketEnv:         true,
	connectionAPIKeyEnv:           true,
}

func filterConnectionEnv(parent []string) []string {
	filtered := make([]string, 0, len(parent))
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if ok && filteredConnectionEnv[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
