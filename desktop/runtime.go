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
	Reason    string `json:"reason,omitempty"`
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

type runtimeCapabilities struct {
	ProtocolVersion int      `json:"protocol_version"`
	StorageSchemas  []int    `json:"storage_schemas"`
	Methods         []string `json:"methods"`
	MaxActiveTasks  int      `json:"max_active_tasks"`
	AutomaticTitles bool     `json:"automatic_titles"`
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

type runtimeTaskState struct {
	SessionID       string `json:"session_id"`
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	Phase           string `json:"phase"`
	CancelRequested bool   `json:"cancel_requested"`
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
	OnStderr        func(string)
}

type runtimeOptions struct {
	Binary   string
	Args     []string
	Dir      string
	Env      []string
	Handlers runtimeHandlers
}

type runtimeClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdinOnce sync.Once
	handlers  runtimeHandlers

	// stateMu guards request bookkeeping only; blocking pipe I/O never holds it.
	stateMu     sync.Mutex
	nextID      int
	pending     map[string]chan runtimeResponse
	issued      map[string]bool
	closed      bool
	failed      *runtimeError
	failureOnce sync.Once

	// writeMu serializes stdin writes; a blocked write must not block the
	// reader from delivering responses/events.
	writeMu sync.Mutex

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
		issued:   make(map[string]bool),
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
		c.failProtocol("stdout read failed: " + err.Error())
		_, _ = io.Copy(io.Discard, r)
	}
}

func (c *runtimeClient) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		c.stderrMu.Lock()
		line := scanner.Text()
		c.stderr = append(c.stderr, line)
		if len(c.stderr) > 200 {
			c.stderr = c.stderr[len(c.stderr)-200:]
		}
		c.stderrMu.Unlock()
		if c.handlers.OnStderr != nil {
			c.handlers.OnStderr(line)
		}
	}
	if err := scanner.Err(); err != nil {
		c.failProtocol("stderr read failed: " + err.Error())
		_, _ = io.Copy(io.Discard, r)
	}
}

type wireEnvelope struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Result    json.RawMessage `json:"result"`
	Error     *runtimeError   `json:"error"`
	SessionID string          `json:"session_id"`
	TaskID    string          `json:"task_id"`
	MessageID string          `json:"message_id"`
	Event     *runtimeEvent   `json:"event"`
}

func (c *runtimeClient) handleLine(line []byte) {
	if c.isFailed() {
		return // keep reading to EOF so the core can drain
	}
	var env wireEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		c.failProtocol("malformed JSON from runtime: " + err.Error())
		return
	}
	if env.Version != runtimeProtocolVersion {
		c.failProtocol(fmt.Sprintf("unsupported runtime protocol version %d", env.Version))
		return
	}
	switch env.Type {
	case "ready":
		var ready runtimeReady
		if err := json.Unmarshal(line, &ready); err != nil {
			c.failProtocol("malformed ready message: " + err.Error())
			return
		}
		if c.handlers.OnReady != nil {
			c.handlers.OnReady(ready)
		}
	case "response":
		c.deliverResponse(env)
	case "event":
		if env.Event == nil {
			c.failProtocol("event envelope without event")
			return
		}
		if c.handlers.OnEvent != nil {
			c.handlers.OnEvent(runtimeEventEnvelope{
				Version: env.Version, Type: env.Type, SessionID: env.SessionID,
				TaskID: env.TaskID, MessageID: env.MessageID, Event: env.Event,
			})
		}
	default:
		c.failProtocol("unknown runtime message type " + strconv.Quote(env.Type))
	}
}

func (c *runtimeClient) deliverResponse(env wireEnvelope) {
	hasResult := len(env.Result) > 0
	if hasResult == (env.Error != nil) {
		c.failProtocol("response must contain exactly one of result or error")
		return
	}
	response := runtimeResponse{Version: env.Version, Type: env.Type, ID: env.ID, Result: env.Result, Error: env.Error}
	c.stateMu.Lock()
	ch, pending := c.pending[env.ID]
	if pending {
		delete(c.pending, env.ID)
	}
	issued := c.issued[env.ID]
	c.stateMu.Unlock()
	if pending {
		select {
		case ch <- response:
		default:
		}
		return
	}
	if issued {
		// A response for a request that already timed out/finished. Discard it;
		// it must not be mistaken for a new success nor break the connection.
		return
	}
	c.failProtocol("response for unknown request id " + strconv.Quote(env.ID))
}

func (c *runtimeClient) isFailed() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.failed != nil
}

// failProtocol is the single protocol-failure boundary: stop accepting new
// requests, fail pending calls with an observable error, close stdin so the
// core drains, and keep reading stdout/stderr to EOF before Wait.
func (c *runtimeClient) failProtocol(message string) {
	c.failureOnce.Do(func() {
		failure := &runtimeError{Code: "protocol_error", Message: message}
		c.stateMu.Lock()
		c.failed = failure
		pending := c.pending
		c.pending = make(map[string]chan runtimeResponse)
		c.stateMu.Unlock()
		for _, ch := range pending {
			select {
			case ch <- runtimeResponse{Error: failure}:
			default:
			}
		}
		c.closeStdin()
		if c.handlers.OnProtocolError != nil {
			c.handlers.OnProtocolError(message)
		}
	})
}

func (c *runtimeClient) closeStdin() {
	c.stdinOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
	})
}

func (c *runtimeClient) finish(err error) {
	c.doneOnce.Do(func() {
		c.stateMu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[string]chan runtimeResponse)
		failed := c.failed
		c.stateMu.Unlock()
		for _, ch := range pending {
			if failed != nil {
				select {
				case ch <- runtimeResponse{Error: failed}:
				default:
				}
			} else {
				close(ch)
			}
		}
		c.exitErr = err
		close(c.done)
		if c.handlers.OnExit != nil {
			c.handlers.OnExit(err)
		}
	})
}

func (c *runtimeClient) call(method string, params any, result any, timeout time.Duration) error {
	c.stateMu.Lock()
	if c.failed != nil {
		err := c.failed
		c.stateMu.Unlock()
		return err
	}
	if c.closed {
		c.stateMu.Unlock()
		return errRuntimeDisconnected
	}
	c.nextID++
	id := "g" + strconv.Itoa(c.nextID)
	ch := make(chan runtimeResponse, 1)
	c.pending[id] = ch
	c.issued[id] = true
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		delete(c.pending, id)
		c.stateMu.Unlock()
	}()

	request, err := json.Marshal(map[string]any{
		"version": runtimeProtocolVersion,
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(request, '\n'))
	c.writeMu.Unlock()
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
	if err := c.call("session.create", params, &session, runtimeControlTimeout); err != nil {
		return runtimeSession{}, err
	}
	if session.SessionID == "" {
		return runtimeSession{}, errors.New("runtime returned a session without session_id")
	}
	return session, nil
}

func (c *runtimeClient) loadSession(sessionID string) (runtimeSession, error) {
	var session runtimeSession
	if err := c.call("session.load", map[string]any{"session_id": sessionID}, &session, runtimeControlTimeout); err != nil {
		return runtimeSession{}, err
	}
	if session.SessionID == "" {
		return runtimeSession{}, errors.New("runtime returned a session without session_id")
	}
	return session, nil
}

func (c *runtimeClient) capabilities() (runtimeCapabilities, error) {
	var result runtimeCapabilities
	if err := c.call("runtime.capabilities", map[string]any{}, &result, runtimeControlTimeout); err != nil {
		return runtimeCapabilities{}, err
	}
	return result, nil
}

func (c *runtimeClient) sessionStatus(sessionID string) (runtimeSession, error) {
	var session runtimeSession
	if err := c.call("session.status", map[string]any{"session_id": sessionID}, &session, runtimeControlTimeout); err != nil {
		return runtimeSession{}, err
	}
	if session.SessionID == "" {
		return runtimeSession{}, errors.New("runtime returned a session without session_id")
	}
	return session, nil
}

func (c *runtimeClient) historyPage(sessionID string, offset, limit int) (runtimeHistory, error) {
	var page runtimeHistory
	if err := c.call("session.history", map[string]any{
		"session_id": sessionID,
		"offset":     offset,
		"limit":      limit,
	}, &page, runtimeControlTimeout); err != nil {
		return runtimeHistory{}, err
	}
	if page.SessionID == "" {
		return runtimeHistory{}, errors.New("runtime returned history without session_id")
	}
	return page, nil
}

func (c *runtimeClient) sendTask(sessionID, text string) (runtimeSendResult, error) {
	var result runtimeSendResult
	if err := c.call("task.send", map[string]any{
		"session_id": sessionID,
		"message":    map[string]any{"text": text},
	}, &result, runtimeSendTimeout); err != nil {
		return runtimeSendResult{}, err
	}
	if result.TaskID == "" || result.MessageID == "" {
		return runtimeSendResult{}, errors.New("runtime accepted task without task_id/message_id")
	}
	return result, nil
}

func (c *runtimeClient) taskStatus(sessionID, taskID string) (runtimeTaskState, error) {
	var state runtimeTaskState
	if err := c.call("task.status", map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
	}, &state, runtimeControlTimeout); err != nil {
		return runtimeTaskState{}, err
	}
	return state, nil
}

func (c *runtimeClient) cancelTask(sessionID, taskID string) (string, error) {
	var result runtimeCancelResult
	if err := c.call("task.cancel", map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
	}, &result, runtimeControlTimeout); err != nil {
		return "", err
	}
	return result.Status, nil
}

// shutdown asks the core to drain accepted work, closes stdin, and waits for
// the process to exit. It never kills the process; callers run it off the UI
// thread and can show a draining state while it waits.
func (c *runtimeClient) shutdown() error {
	_ = c.call("runtime.shutdown", map[string]any{}, nil, runtimeControlTimeout)
	c.closeStdin()
	<-c.done
	if c.exitErr != nil {
		return c.exitErr
	}
	return nil
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
