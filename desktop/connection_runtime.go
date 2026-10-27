package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type runtimeConnection struct {
	ID           string `json:"id"`
	API          string `json:"api"`
	Endpoint     string `json:"endpoint"`
	DefaultModel string `json:"default_model"`
}

type runtimeExternalToolBinding struct {
	SourceID    string `json:"source_id"`
	ToolName    string `json:"tool_name"`
	ExposedName string `json:"exposed_name"`
}

type runtimeToolSelection struct {
	Builtin  []string                     `json:"builtin"`
	External []runtimeExternalToolBinding `json:"external"`
}

type runtimeConnectionConfig struct {
	SessionID       string   `json:"session_id"`
	ConfigRevision  uint64   `json:"config_revision"`
	StorageSchema   int      `json:"storage_schema"`
	ImmutableFields []string `json:"immutable_fields"`
	EffectiveConfig struct {
		ConnectionID          *string              `json:"connection_id"`
		ExecutionConnectionID *string              `json:"execution_connection_id"`
		Model                 string               `json:"model"`
		API                   string               `json:"api"`
		WorkingDirectory      string               `json:"working_directory"`
		SystemPrompt          string               `json:"system_prompt"`
		Compaction            runtimeCompaction    `json:"compaction"`
		ToolSelection         runtimeToolSelection `json:"tool_selection"`
		CapacitySource        struct {
			Kind      string `json:"kind"`
			CatalogID string `json:"catalog_id"`
		} `json:"capacity_source"`
		ConnectionBinding struct {
			State string `json:"state"`
		} `json:"connection_binding"`
	} `json:"effective_config"`
}

func (c *runtimeClient) connections() ([]runtimeConnection, error) {
	var result struct {
		Connections []runtimeConnection `json:"connections"`
	}
	err := c.call("connection.list", map[string]any{}, &result, runtimeControlTimeout)
	return result.Connections, err
}

func (c *runtimeClient) connectionConfig(sessionID string) (runtimeConnectionConfig, error) {
	var result runtimeConnectionConfig
	err := c.call("session.config.get", map[string]any{"session_id": sessionID}, &result, runtimeControlTimeout)
	return result, err
}

// updateSessionConfiguration changes only the explicitly supplied tool
// selection and/or system prompt. It does not rewrite model, connection, cwd or
// compaction.
func (c *runtimeClient) updateSessionConfiguration(sessionID string, revision uint64, selection map[string]any, systemPrompt string) (runtimeConfigUpdated, error) {
	patch := map[string]any{}
	if selection != nil {
		patch["tool_selection"] = selection
	}
	if systemPrompt != "" {
		patch["system_prompt"] = systemPrompt
	}
	var result runtimeConfigUpdated
	err := c.call("session.config.update", map[string]any{
		"session_id": sessionID, "expected_revision": revision,
		"patch": patch,
	}, &result, runtimeControlTimeout)
	if err != nil {
		// A lost reply is not proof of failure; read back the committed value
		// without submitting the patch a second time.
		result.runtimeConnectionConfig, result.ReadbackError = c.connectionConfig(sessionID)
	}
	return result, err
}

type connectionProbeResult struct {
	Connection runtimeConnection
	Config     runtimeConnectionConfig
}

// A connection test runs only on explicit user action, in its own temporary
// session store. No tools are enabled and no contact history is involved.
func probeConnection(ctx context.Context, binary string, config connectionConfig, parentEnv []string, tempRoot string) (result connectionProbeResult, err error) {
	root, err := os.MkdirTemp(tempRoot, "nagi-connection-test-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(root)
	launch, err := prepareConnectionLaunch(config, parentEnv, filepath.Join(root, "sessions"), root)
	if err != nil {
		return result, err
	}
	defer launch.Cleanup()
	defer func() {
		if err != nil {
			message := err.Error()
			if config.APIKey != "" {
				message = strings.ReplaceAll(message, config.APIKey, "[redacted]")
			}
			err = errors.New(safeDiagnostic(message))
		}
	}()
	ready := make(chan struct{}, 1)
	finished := make(chan runtimeEventEnvelope, 1)
	rt, err := startRuntime(runtimeOptions{Binary: binary, Dir: launch.Dir, Env: launch.Env, Handlers: runtimeHandlers{
		OnReady: func(runtimeReady) { ready <- struct{}{} },
		OnEvent: func(event runtimeEventEnvelope) {
			if event.Event != nil && event.Event.Type == "task_finished" {
				select {
				case finished <- event:
				default:
				}
			}
		},
	}})
	if err != nil {
		return result, err
	}
	defer func() {
		if shutdownErr := rt.shutdown(); err == nil && shutdownErr != nil {
			err = fmt.Errorf("test helper shutdown: %w", shutdownErr)
		}
	}()
	startup := time.NewTimer(runtimeControlTimeout)
	defer startup.Stop()
	select {
	case <-ready:
	case <-ctx.Done():
		return result, ctx.Err()
	case <-rt.done:
		return result, errors.New("test helper exited before ready; check the connection configuration")
	case <-startup.C:
		return result, errors.New("test helper did not become ready")
	}
	connections, err := rt.connections()
	if err != nil {
		return result, err
	}
	if len(connections) != 1 {
		return result, errors.New("test helper did not report the configured connection")
	}
	result.Connection = connections[0]
	var session runtimeSession
	err = rt.call("session.create", map[string]any{
		"storage_schema": 2,
		"config": map[string]any{
			"connection_id":  connectionID,
			"system_prompt":  "This is a connection test. Reply briefly with OK. Do not call tools.",
			"tool_selection": map[string]any{"builtin": []string{}, "external": []string{}},
		},
	}, &session, runtimeControlTimeout)
	if err != nil {
		return result, err
	}
	result.Config, err = rt.connectionConfig(session.SessionID)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	task, err := rt.sendTask(session.SessionID, "Reply with OK.")
	if err != nil {
		return result, err
	}
	select {
	case terminal := <-finished:
		if terminal.TaskID != task.TaskID || terminal.Event.Status != "completed" {
			if terminal.Event.Error != nil {
				return result, terminal.Event.Error
			}
			return result, fmt.Errorf("model test ended with status %s", terminal.Event.Status)
		}
		return result, nil
	case <-ctx.Done():
		_, _ = rt.cancelTask(session.SessionID, task.TaskID)
		return result, ctx.Err()
	case <-rt.done:
		return result, errors.New("test helper disconnected before a result was confirmed")
	}
}
