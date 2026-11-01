package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func issueToolSocketPath(t *testing.T, label string) string {
	t.Helper()
	return fmt.Sprintf("/tmp/saki-154-%s-%d.sock", label, time.Now().UnixNano())
}

func issueToolTestRequest(scope string) issueToolForwardRequest {
	return issueToolForwardRequest{Tool: "issue_get", Arguments: json.RawMessage(`{"scope":"` + scope + `","issue_id":"issue-a"}`)}
}

func TestIssueToolServerOneRequestOneHandlerAndCleanup(t *testing.T) {
	path := issueToolSocketPath(t, "server")
	var handlerCalls atomic.Int32
	server, err := startIssueToolIPCServer(path, func(_ context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
		handlerCalls.Add(1)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: string(request.Arguments)}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := issueToolIPCForwarder(path)(context.Background(), issueToolTestRequest("scope-one"))
	if err != nil {
		t.Fatal(err)
	}
	if handlerCalls.Load() != 1 || len(response.Content) != 1 || !strings.Contains(response.Content[0].Text, "scope-one") {
		t.Fatalf("server response = %+v calls=%d", response, handlerCalls.Load())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned socket remained after Close: %v", err)
	}
	if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("closed listener still accepted a connection")
	}
}

func TestIssueToolServerParallelHelpersDoNotMixScope(t *testing.T) {
	path := issueToolSocketPath(t, "parallel")
	server, err := startIssueToolIPCServer(path, func(_ context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
		var args map[string]string
		if err := json.Unmarshal(request.Arguments, &args); err != nil {
			return issueToolForwardResponse{}, err
		}
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: args["scope"]}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	results := make([]string, 2)
	var wg sync.WaitGroup
	for index, scope := range []string{"scope-left", "scope-right"} {
		index, scope := index, scope
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := issueToolIPCForwarder(path)(context.Background(), issueToolTestRequest(scope))
			if err != nil || len(response.Content) != 1 {
				results[index] = "err:" + fmt.Sprint(err)
				return
			}
			results[index] = response.Content[0].Text
		}()
	}
	wg.Wait()
	if results[0] != "scope-left" || results[1] != "scope-right" {
		t.Fatalf("parallel helper results = %+v", results)
	}
}

func TestIssueToolServerCloseWaitsForActiveHandler(t *testing.T) {
	path := issueToolSocketPath(t, "close-wait")
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	var effects atomic.Int32
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	server, err := startIssueToolIPCServer(path, func(_ context.Context, _ issueToolForwardRequest) (issueToolForwardResponse, error) {
		close(started)
		<-release
		effects.Add(1)
		close(handlerDone)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "after-close"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = server.Close()
	})
	clientResult := make(chan string, 1)
	go func() {
		response, err := issueToolIPCForwarder(path)(context.Background(), issueToolTestRequest("scope-close"))
		if err != nil {
			clientResult <- "err:" + err.Error()
			return
		}
		clientResult <- response.Content[0].Text
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	closed := make(chan struct{})
	go func() {
		_ = server.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before active handler ended")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after handler release")
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not complete after release")
	}
	if effects.Load() != 1 {
		t.Fatalf("handler side effects = %d", effects.Load())
	}
	select {
	case result := <-clientResult:
		// Closing accepted IO may make the client see an unknown error before
		// the handler's response is written. Either that error or a delivered
		// receipt is acceptable; a second handler call is not.
		if result == "" {
			t.Fatal("client returned an empty result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not finish after Close")
	}
}

func TestIssueToolServerCollisionDoesNotDeleteExistingListener(t *testing.T) {
	path := issueToolSocketPath(t, "collision")
	existing, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	server, err := startIssueToolIPCServer(path, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "unexpected"}}}, nil
	})
	if err == nil || server != nil {
		t.Fatalf("collision unexpectedly started: server=%v err=%v", server, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("collision path was removed: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("existing listener was not preserved: %v", err)
	}
	_ = conn.Close()
}

func TestIssueToolServerInvalidAndOversizeRequestsDoNotCallHandler(t *testing.T) {
	path := issueToolSocketPath(t, "invalid")
	var calls atomic.Int32
	server, err := startIssueToolIPCServer(path, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		calls.Add(1)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "unexpected"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	readResponse := func(conn net.Conn) issueToolForwardResponse {
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 1024), issueToolMaxFrameBytes)
		if !scanner.Scan() {
			t.Fatalf("no error response; scanner=%v", scanner.Err())
		}
		var response issueToolForwardResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("not-json\n"))
	response := readResponse(conn)
	_ = conn.Close()
	if !response.IsError || len(response.Content) != 1 || !strings.Contains(response.Content[0].Text, "request_invalid") {
		t.Fatalf("invalid response = %+v", response)
	}

	conn, err = net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	oversize := bytes.Repeat([]byte("x"), issueToolMaxFrameBytes+1)
	_, _ = io.Copy(conn, bytes.NewReader(oversize))
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	oversizeResponse := readResponse(conn)
	_ = conn.Close()
	if !oversizeResponse.IsError || calls.Load() != 0 {
		t.Fatalf("invalid/oversize request = %+v calls=%d", oversizeResponse, calls.Load())
	}
}

func TestIssueToolServerBusinessAndHandlerErrorsStayError(t *testing.T) {
	path := issueToolSocketPath(t, "errors")
	var calls atomic.Int32
	server, err := startIssueToolIPCServer(path, func(_ context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
		calls.Add(1)
		if strings.Contains(string(request.Arguments), "handler-error") {
			return issueToolForwardResponse{}, errors.New("handler failed")
		}
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "business rejection"}}, IsError: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	business, err := issueToolIPCForwarder(path)(context.Background(), issueToolTestRequest("s"))
	if err != nil || !business.IsError {
		t.Fatalf("business response = %+v / %v", business, err)
	}
	handler, err := issueToolIPCForwarder(path)(context.Background(), issueToolForwardRequest{Tool: "issue_action", Arguments: json.RawMessage(`{"scope":"s","action":"run","issue_id":"handler-error"}`)})
	if err != nil || !handler.IsError || !strings.Contains(handler.Content[0].Text, "operation_unknown") {
		t.Fatalf("handler error response = %+v / %v", handler, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d", calls.Load())
	}
}

func TestIssueToolServerOversizeResponseIsSmallErrorAndSingleHandler(t *testing.T) {
	path := issueToolSocketPath(t, "oversize")
	var calls atomic.Int32
	server, err := startIssueToolIPCServer(path, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		calls.Add(1)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: strings.Repeat("y", issueToolMaxFrameBytes+1)}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := issueToolIPCForwarder(path)(context.Background(), issueToolTestRequest("s"))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !response.IsError || len(response.Content) != 1 || !strings.Contains(response.Content[0].Text, "response_too_large") {
		t.Fatalf("oversize response = %+v / calls=%d", response, calls.Load())
	}
}

func TestIssueToolServerCloseUnblocksIdleAndPartialFrameClients(t *testing.T) {
	for _, partial := range []string{"", `{"tool":"issue_get"`} {
		name := "idle"
		if partial != "" {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			path := issueToolSocketPath(t, "idle-"+name)
			var calls atomic.Int32
			server, err := startIssueToolIPCServer(path, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
				calls.Add(1)
				return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "unexpected"}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			conn, err := net.Dial("unix", path)
			if err != nil {
				_ = server.Close()
				t.Fatal(err)
			}
			if partial != "" {
				if _, err := conn.Write([]byte(partial)); err != nil {
					t.Fatal(err)
				}
			}
			closed := make(chan struct{})
			go func() {
				_ = server.Close()
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("Close hung on idle/partial client")
			}
			_ = conn.Close()
			if calls.Load() != 0 {
				t.Fatal("idle/partial frame invoked handler")
			}
		})
	}
}

func TestIssueToolServerCloseUnblocksUnreadLargeResponse(t *testing.T) {
	path := issueToolSocketPath(t, "unread")
	started := make(chan struct{})
	server, err := startIssueToolIPCServer(path, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		close(started)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: strings.Repeat("z", 3*1024*1024)}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = server.Close()
	})
	if _, err := conn.Write([]byte(`{"tool":"issue_get","arguments":{"scope":"s"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = conn.Close()
		_ = server.Close()
		t.Fatal("handler did not start for unread response")
	}
	closed := make(chan struct{})
	go func() {
		_ = server.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung writing to an unread client")
	}
	_ = conn.Close()
}
