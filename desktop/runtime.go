package main

// stdio v1 runtime client and DTOs for the confirmed #60 contract.
// The process boundary is the only integration point: no Rust journal parsing,
// no second model/tool loop, no SDK/plugin layer.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	runtimeProtocolVersion = 1
	runtimeMaxLine         = 8 * 1024 * 1024
	runtimeControlTimeout  = 20 * time.Second
	runtimeSendTimeout     = 30 * time.Second
	runtimeShutdownTimeout = 10 * time.Second
)

var errRuntimeDisconnected = errors.New("runtime disconnected")

// ---------- wire DTOs ----------

type runtimeError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

func (e *runtimeError) Error() string {
	if e == nil {
		return ""
	}
	if e.SessionID != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.SessionID)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type runtimeReady struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	API     string `json:"api"`
}

type runtimeMetadata struct {
	SchemaVersion    int             `json:"schema_version"`
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	TitleSource      string          `json:"title_source,omitempty"`
	CreatedAt        int64           `json:"created_at"`
	WorkingDirectory string          `json:"working_directory"`
	Model            string          `json:"model"`
	API              string          `json:"api"`
	Compaction       json.RawMessage `json:"compaction,omitempty"`
}

type runtimeActiveTask struct {
	TaskID          string `json:"task_id"`
	MessageID       string `json:"message_id"`
	CancelRequested bool   `json:"cancel_requested"`
}

type runtimeSession struct {
	SessionID  string             `json:"session_id"`
	Metadata   *runtimeMetadata   `json:"metadata,omitempty"`
	Loaded     bool               `json:"loaded"`
	State      string             `json:"state"`
	ActiveTask *runtimeActiveTask `json:"active_task"`
	Status     string             `json:"status"`
}

type runtimeSessionSummary struct {
	Metadata     runtimeMetadata `json:"metadata"`
	UpdatedAt    int64           `json:"updated_at"`
	Status       string          `json:"status"`
	InUse        bool            `json:"in_use"`
	Unavailable  *string         `json:"unavailable"`
	Loaded       bool            `json:"loaded"`
	State        string          `json:"state"`
	ActiveTaskID *string         `json:"active_task_id"`
}

type runtimeSessionList struct {
	Sessions []runtimeSessionSummary `json:"sessions"`
}

type runtimeSendResult struct {
	TaskID    string `json:"task_id"`
	MessageID string `json:"message_id"`
}

type runtimeCancelResult struct {
	Status string `json:"status"`
}

// historyRecord is one page entry of the semantic journal reader. The record
// keeps InputItem shapes; it is not the live OutputItem/id structure.
type historyRecord struct {
	Offset      int           `json:"offset"`
	TimestampMS int64         `json:"timestamp_ms"`
	Record      journalRecord `json:"record"`
}

type journalRecord struct {
	Type      string      `json:"type"`
	Items     []inputItem `json:"items"`
	TaskID    string      `json:"task_id"`
	MessageID string      `json:"message_id"`
	Status    string      `json:"status"`
	Error     *string     `json:"error"`
}

// inputItem mirrors the journal InputItem variants. Content is a string or an
// array of text/image parts; it is kept raw so old and new records both work.
type inputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    string          `json:"output"`
}

type runtimeHistory struct {
	SessionID    string          `json:"session_id"`
	Records      []historyRecord `json:"records"`
	NextOffset   *int            `json:"next_offset"`
	Status       string          `json:"status"`
	State        string          `json:"state"`
	ActiveTaskID *string         `json:"active_task_id"`
}

type runtimeEvent struct {
	Type      string          `json:"type"`
	ItemID    string          `json:"item_id"`
	Delta     string          `json:"delta"`
	Text      string          `json:"text"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Status    string          `json:"status"`
	Error     *runtimeError   `json:"error"`
	Purpose   string          `json:"purpose"`
}

type runtimeEventEnvelope struct {
	Version   int           `json:"version"`
	Type      string        `json:"type"`
	SessionID string        `json:"session_id"`
	TaskID    string        `json:"task_id"`
	MessageID string        `json:"message_id"`
	Event     *runtimeEvent `json:"event"`
}

type runtimeResponse struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *runtimeError   `json:"error"`
}

// ---------- client ----------

type runtimeHandlers struct {
	OnReady         func(runtimeReady)
	OnEvent         func(runtimeEventEnvelope)
	OnExit          func(error)
	OnProtocolError func(string)
}

type runtimeOptions struct {
	Binary   string
	Args     []string
	Dir      string
	Env      []string
	Handlers runtimeHandlers
}

type runtimeClient struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	handlers runtimeHandlers

	mu      sync.Mutex
	nextID  int
	pending map[string]chan runtimeResponse
	closed  bool

	done     chan struct{}
	doneOnce sync.Once
	exitErr  error

	stderrMu sync.Mutex
	stderr   []string
}

func startRuntime(opts runtimeOptions) (*runtimeClient, error) {
	if opts.Binary == "" {
		return nil, errors.New("runtime binary is not configured")
	}
	args := opts.Args
	if args == nil {
		args = []string{"--stdio"}
	}
	cmd := exec.Command(opts.Binary, args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	c := &runtimeClient{
		cmd:      cmd,
		stdin:    stdin,
		handlers: opts.Handlers,
		pending:  make(map[string]chan runtimeResponse),
		done:     make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		c.readStdout(stdout)
	}()
	go func() {
		defer readers.Done()
		c.readStderr(stderr)
	}()
	// Wait only after both pipes have been read to EOF, as StdoutPipe requires.
	go func() {
		readers.Wait()
		c.finish(cmd.Wait())
	}()
	return c, nil
}

func (c *runtimeClient) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), runtimeMaxLine)
	for scanner.Scan() {
		c.handleLine(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		c.protocolError("stdout read failed: " + err.Error())
	}
}

func (c *runtimeClient) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		c.stderrMu.Lock()
		c.stderr = append(c.stderr, scanner.Text())
		if len(c.stderr) > 200 {
			c.stderr = c.stderr[len(c.stderr)-200:]
		}
		c.stderrMu.Unlock()
	}
}

func (c *runtimeClient) handleLine(line []byte) {
	var env runtimeEventEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		c.protocolError("malformed JSON from runtime: " + err.Error())
		return
	}
	if env.Version != runtimeProtocolVersion {
		c.protocolError(fmt.Sprintf("unsupported runtime protocol version %d", env.Version))
		return
	}
	switch env.Type {
	case "ready":
		var ready runtimeReady
		if err := json.Unmarshal(line, &ready); err != nil {
			c.protocolError("malformed ready message: " + err.Error())
			return
		}
		if c.handlers.OnReady != nil {
			c.handlers.OnReady(ready)
		}
	case "response":
		var resp runtimeResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			c.protocolError("malformed response: " + err.Error())
			return
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		c.mu.Unlock()
		if ch == nil {
			c.protocolError("response for unknown request id " + strconv.Quote(resp.ID))
			return
		}
		ch <- resp
	case "event":
		if env.Event == nil {
			c.protocolError("event envelope without event")
			return
		}
		if c.handlers.OnEvent != nil {
			c.handlers.OnEvent(env)
		}
	default:
		c.protocolError("unknown runtime message type " + strconv.Quote(env.Type))
	}
}

func (c *runtimeClient) protocolError(message string) {
	if c.handlers.OnProtocolError != nil {
		c.handlers.OnProtocolError(message)
	}
}

func (c *runtimeClient) finish(err error) {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[string]chan runtimeResponse)
		c.mu.Unlock()
		for _, ch := range pending {
			close(ch)
		}
		c.exitErr = err
		close(c.done)
		if c.handlers.OnExit != nil {
			c.handlers.OnExit(err)
		}
	})
}

func (c *runtimeClient) call(method string, params any, result any, timeout time.Duration) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errRuntimeDisconnected
	}
	c.nextID++
	id := "g" + strconv.Itoa(c.nextID)
	ch := make(chan runtimeResponse, 1)
	c.pending[id] = ch
	request, err := json.Marshal(map[string]any{
		"version": runtimeProtocolVersion,
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err == nil {
		_, err = c.stdin.Write(append(request, '\n'))
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err != nil {
		return err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return errRuntimeDisconnected
			}
			if resp.Error != nil {
				return resp.Error
			}
			if result != nil && len(resp.Result) > 0 {
				if err := json.Unmarshal(resp.Result, result); err != nil {
					return fmt.Errorf("decode %s result: %w", method, err)
				}
			}
			return nil
		case <-c.done:
			select {
			case resp, ok := <-ch:
				if ok && resp.Error == nil {
					if result != nil && len(resp.Result) > 0 {
						return json.Unmarshal(resp.Result, result)
					}
					return nil
				}
			default:
			}
			if c.exitErr != nil {
				return fmt.Errorf("%w: %v", errRuntimeDisconnected, c.exitErr)
			}
			return errRuntimeDisconnected
		case <-timer.C:
			return fmt.Errorf("runtime request %s timed out", method)
		}
	}
}

func (c *runtimeClient) createSession(workingDir, systemPrompt string) (runtimeSession, error) {
	params := map[string]any{}
	if workingDir != "" {
		params["working_directory"] = workingDir
	}
	if systemPrompt != "" {
		params["system_prompt"] = systemPrompt
	}
	var session runtimeSession
	err := c.call("session.create", params, &session, runtimeControlTimeout)
	return session, err
}

func (c *runtimeClient) loadSession(sessionID string) (runtimeSession, error) {
	var session runtimeSession
	err := c.call("session.load", map[string]any{"session_id": sessionID}, &session, runtimeControlTimeout)
	return session, err
}

func (c *runtimeClient) sessionStatus(sessionID string) (runtimeSession, error) {
	var session runtimeSession
	err := c.call("session.status", map[string]any{"session_id": sessionID}, &session, runtimeControlTimeout)
	return session, err
}

func (c *runtimeClient) historyPage(sessionID string, offset, limit int) (runtimeHistory, error) {
	var page runtimeHistory
	err := c.call("session.history", map[string]any{
		"session_id": sessionID,
		"offset":     offset,
		"limit":      limit,
	}, &page, runtimeControlTimeout)
	return page, err
}

func (c *runtimeClient) sendTask(sessionID, text string) (runtimeSendResult, error) {
	var result runtimeSendResult
	err := c.call("task.send", map[string]any{
		"session_id": sessionID,
		"message":    map[string]any{"text": text},
	}, &result, runtimeSendTimeout)
	return result, err
}

func (c *runtimeClient) cancelTask(sessionID, taskID string) (string, error) {
	var result runtimeCancelResult
	err := c.call("task.cancel", map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
	}, &result, runtimeControlTimeout)
	return result.Status, err
}

func (c *runtimeClient) shutdown() error {
	err := c.call("runtime.shutdown", map[string]any{}, nil, runtimeControlTimeout)
	c.mu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		if err != nil && !errors.Is(err, errRuntimeDisconnected) {
			return err
		}
		return nil
	case <-time.After(runtimeShutdownTimeout):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
		return errors.New("runtime did not shut down; killed")
	}
}

func (c *runtimeClient) diagnostics() []string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	out := make([]string, len(c.stderr))
	copy(out, c.stderr)
	return out
}

// inputText flattens InputMessageContent: a string, or an array of text/image
// parts. Images are shown as a placeholder; raw reasoning is never displayed.
func inputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	out := ""
	for _, part := range parts {
		switch part.Type {
		case "text":
			out += part.Text
		case "image":
			out += "[image]"
		}
	}
	return out
}

func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}
