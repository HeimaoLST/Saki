package main

// Minimal one-request Unix-socket transport between the MCP helper and the Nagi
// application handler. It carries no contact/issue/Runtime state; scope and
// action remain in the forwarded tool arguments. No retry occurs.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	issueToolIPCSocketEnv        = "NAGI_ISSUE_TOOL_SOCKET"
	issueToolIPCDefaultTimeout   = 30 * time.Second
	issueToolIPCEndpointMaxBytes = 4096
)

type issueToolIPCClient struct {
	Endpoint      string
	Timeout       time.Duration
	MaxFrameBytes int
}

func (c issueToolIPCClient) forward(ctx context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" || len(endpoint) > issueToolIPCEndpointMaxBytes || !filepath.IsAbs(endpoint) {
		return issueToolForwardResponse{}, errors.New("issue tool IPC endpoint is invalid")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = issueToolIPCDefaultTimeout
	}
	maxFrame := c.MaxFrameBytes
	if maxFrame <= 0 {
		maxFrame = issueToolMaxFrameBytes
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		return issueToolForwardResponse{}, fmt.Errorf("encode issue tool IPC request: %w", err)
	}
	if len(requestData) > maxFrame {
		return issueToolForwardResponse{}, errors.New("issue tool IPC request exceeds frame limit")
	}
	if err := ctx.Err(); err != nil {
		return issueToolForwardResponse{}, err
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return issueToolForwardResponse{}, fmt.Errorf("connect issue tool IPC: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return issueToolForwardResponse{}, err
	}
	payload := append(requestData, '\n')
	n, err := conn.Write(payload)
	if err != nil {
		return issueToolForwardResponse{}, fmt.Errorf("write issue tool IPC request: %w", err)
	}
	if n != len(payload) {
		return issueToolForwardResponse{}, io.ErrShortWrite
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 16*1024), maxFrame)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return issueToolForwardResponse{}, fmt.Errorf("read issue tool IPC response: %w", err)
		}
		return issueToolForwardResponse{}, errors.New("issue tool IPC closed without a response")
	}
	if len(scanner.Bytes()) == 0 {
		return issueToolForwardResponse{}, errors.New("issue tool IPC returned an empty response")
	}
	var response issueToolForwardResponse
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return issueToolForwardResponse{}, fmt.Errorf("decode issue tool IPC response: %w", err)
	}
	if len(response.Content) == 0 {
		return issueToolForwardResponse{}, errors.New("issue tool IPC response has no content")
	}
	return response, nil
}

func issueToolIPCEndpoint() string {
	return strings.TrimSpace(os.Getenv(issueToolIPCSocketEnv))
}

// issueToolIPCForwarder is the helper's only application bridge. Each call
// creates one connection and sends exactly one request; errors are returned to
// the MCP layer as unknown, never retried.
func issueToolIPCForwarder(endpoint string) issueToolForwardFunc {
	client := issueToolIPCClient{Endpoint: endpoint}
	return func(ctx context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
		return client.forward(ctx, request)
	}
}

type issueToolHandlerFunc func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error)

type issueToolIPCServer struct {
	endpoint   string
	listener   net.Listener
	socketInfo os.FileInfo
	handler    issueToolHandlerFunc
	done       chan struct{}
	closeDone  chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	closing    bool
	conns      map[net.Conn]struct{}
	wg         sync.WaitGroup
}

// startIssueToolIPCServer creates exactly one application-owned Unix socket.
// Startup failure returns without removing an existing colliding address.
func startIssueToolIPCServer(endpoint string, handler issueToolHandlerFunc) (*issueToolIPCServer, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || len(endpoint) > issueToolIPCEndpointMaxBytes || !filepath.IsAbs(endpoint) {
		return nil, errors.New("issue tool IPC server endpoint is invalid")
	}
	if handler == nil {
		return nil, errors.New("issue tool IPC server handler is required")
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen issue tool IPC: %w", err)
	}
	if unix, ok := listener.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, fmt.Errorf("set issue tool IPC permissions: %w", err)
	}
	info, err := os.Lstat(endpoint)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, fmt.Errorf("inspect issue tool IPC socket: %w", err)
	}
	server := &issueToolIPCServer{
		endpoint: endpoint, listener: listener, socketInfo: info, handler: handler,
		done: make(chan struct{}), closeDone: make(chan struct{}), conns: map[net.Conn]struct{}{},
	}
	go server.acceptLoop()
	return server, nil
}

func (s *issueToolIPCServer) acceptLoop() {
	defer close(s.done)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func issueToolIPCErrorResponse(code, message string) issueToolForwardResponse {
	data, _ := json.Marshal(map[string]string{"code": code, "message": message})
	return issueToolForwardResponse{
		Content: []issueToolContent{{Type: "text", Text: string(data)}},
		IsError: true,
	}
}

func (s *issueToolIPCServer) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 16*1024), issueToolMaxFrameBytes)
	if !scanner.Scan() {
		if scanner.Err() != nil {
			s.writeResponse(conn, issueToolIPCErrorResponse("request_invalid", "request exceeded the 4 MiB frame or was not readable"))
		}
		return
	}
	var request issueToolForwardRequest
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || strings.TrimSpace(request.Tool) == "" {
		s.writeResponse(conn, issueToolIPCErrorResponse("request_invalid", "request is not a valid issue tool request"))
		return
	}
	response, err := s.handler(context.Background(), request)
	if err != nil {
		s.writeResponse(conn, issueToolIPCErrorResponse("operation_unknown", "application handler failed; no automatic retry"))
		return
	}
	if len(response.Content) == 0 {
		s.writeResponse(conn, issueToolIPCErrorResponse("invalid_forward_result", "application handler returned no content"))
		return
	}
	s.writeResponse(conn, response)
}

func (s *issueToolIPCServer) writeResponse(conn net.Conn, response issueToolForwardResponse) {
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	if len(data) > issueToolMaxFrameBytes {
		data, _ = json.Marshal(issueToolIPCErrorResponse("response_too_large", "application response exceeded the 4 MiB frame"))
	}
	_, _ = conn.Write(append(data, '\n'))
}

// Close stops accepting, closes all currently accepted IO connections to
// unblock idle reads/writes, waits for accepted handlers to complete their
// side effects, and then removes only this server's socket. It is blocking and
// must be called off the UI thread when handlers may wait on UI callbacks.
func (s *issueToolIPCServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		s.mu.Unlock()
		_ = s.listener.Close()
		for _, conn := range conns {
			_ = conn.Close()
		}
		<-s.done
		s.wg.Wait()
		if current, err := os.Lstat(s.endpoint); err == nil && s.socketInfo != nil && os.SameFile(current, s.socketInfo) {
			_ = os.Remove(s.endpoint)
		}
		close(s.closeDone)
	})
	<-s.closeDone
	return nil
}
