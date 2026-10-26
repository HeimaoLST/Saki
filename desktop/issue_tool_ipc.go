package main

// Minimal one-request Unix-socket transport between the MCP helper and the
// future Nagi application handler. It carries no contact/issue/Runtime state;
// scope and action remain in the forwarded tool arguments. No retry occurs.

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
