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
	"testing"
	"time"
)

type issueToolTestResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *issueToolRPCError `json:"error,omitempty"`
}

func decodeIssueToolOutput(t *testing.T, output string) []issueToolTestResponse {
	t.Helper()
	var responses []issueToolTestResponse
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var response issueToolTestResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("decode helper stdout %q: %v", scanner.Text(), err)
		}
		responses = append(responses, response)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return responses
}

func runIssueToolHelper(t *testing.T, input string, forward issueToolForwardFunc) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := issueToolHelperServe(context.Background(), strings.NewReader(input), &out, forward, issueToolHelperOptions{ForwardTimeout: 2 * time.Second})
	return out.String(), err
}

func issueToolInitLine() string {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"
}

func TestIssueToolHelperInitializeListAndNotification(t *testing.T) {
	input := issueToolInitLine() +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":"list-1","method":"tools/list"}` + "\n"
	forwardCalls := 0
	out, err := runIssueToolHelper(t, input, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		forwardCalls++
		return issueToolForwardResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if forwardCalls != 0 {
		t.Fatalf("initialize/list forwarded %d business calls", forwardCalls)
	}
	responses := decodeIssueToolOutput(t, out)
	if len(responses) != 2 {
		t.Fatalf("responses = %+v", responses)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(responses[0].Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != issueToolMCPProtocolVersion || init.Capabilities.Tools == nil {
		t.Fatalf("initialize = %+v", init)
	}
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(responses[1].Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 3 {
		t.Fatalf("tool list = %+v", list.Tools)
	}
	if list.Tools[0].Name != "issue_list" || list.Tools[1].Name != "issue_get" || list.Tools[2].Name != "issue_action" {
		t.Fatalf("tool names = %+v", list.Tools)
	}
	if list.Tools[0].InputSchema["required"] == nil {
		t.Fatal("issue_list schema has no required fields")
	}
	if strings.TrimSpace(out) != strings.TrimSpace(strings.Join(strings.Split(out, "\n")[:len(strings.Split(out, "\n"))-1], "\n")) {
		t.Fatal("unexpected non-line stdout")
	}
}

func TestIssueToolHelperForwardsCallOnceAndPreservesID(t *testing.T) {
	var forwarded []issueToolForwardRequest
	input := issueToolInitLine() +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"issue_get","arguments":{"scope":"s1","issue_id":"i1"}}}` + "\n"
	out, err := runIssueToolHelper(t, input, func(_ context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
		forwarded = append(forwarded, request)
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: `{"issue_id":"i1"}`}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := decodeIssueToolOutput(t, out)
	if len(forwarded) != 1 || len(responses) != 2 {
		t.Fatalf("forwarded/responses = %d/%d", len(forwarded), len(responses))
	}
	if forwarded[0].Tool != "issue_get" || !strings.Contains(string(forwarded[0].Arguments), `"i1"`) {
		t.Fatalf("forwarded request = %+v", forwarded[0])
	}
	if string(responses[1].ID) != `"call-1"` {
		t.Fatalf("response id = %s", responses[1].ID)
	}
	var result issueToolForwardResponse
	if err := json.Unmarshal(responses[1].Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `{"issue_id":"i1"}` || result.IsError {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestIssueToolHelperBusinessErrorAndProtocolErrors(t *testing.T) {
	init := issueToolInitLine() + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	forwardCalls := 0
	input := init +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"issue_list","arguments":{"scope":"s1"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"unknown/method","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"issue_get","arguments":[]}}` + "\n" +
		`not-json` + "\n"
	out, err := runIssueToolHelper(t, input, func(_ context.Context, _ issueToolForwardRequest) (issueToolForwardResponse, error) {
		forwardCalls++
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: `{"code":"ambiguous_target"}`}}, IsError: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := decodeIssueToolOutput(t, out)
	if len(responses) != 5 || forwardCalls != 1 {
		t.Fatalf("responses/forward = %d/%d / %+v", len(responses), forwardCalls, responses)
	}
	var business issueToolForwardResponse
	if err := json.Unmarshal(responses[1].Result, &business); err != nil || !business.IsError || len(business.Content) != 1 {
		t.Fatalf("business error result = %+v / %v", business, err)
	}
	if responses[2].Error == nil || responses[2].Error.Code != -32601 {
		t.Fatalf("unknown method = %+v", responses[2])
	}
	if responses[3].Error == nil || responses[3].Error.Code != -32602 {
		t.Fatalf("invalid params = %+v", responses[3])
	}
	if responses[4].Error == nil || responses[4].Error.Code != -32700 || string(responses[4].ID) != "null" {
		t.Fatalf("parse error/null id = %+v", responses[4])
	}
}

func TestIssueToolHelperNotificationDoesNotInitializeOrForward(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"issue_list","arguments":{"scope":"s"}}}` + "\n"
	forwardCalls := 0
	out, err := runIssueToolHelper(t, input, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		forwardCalls++
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: "should-not-run"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if forwardCalls != 0 {
		t.Fatal("notification initialized the server without initialize")
	}
	responses := decodeIssueToolOutput(t, out)
	if len(responses) != 1 || responses[0].Error == nil || responses[0].Error.Code != -32002 {
		t.Fatalf("pre-init call = %+v", responses)
	}
}

func TestIssueToolHelperIPCUnknownAndNoRetry(t *testing.T) {
	path := fmt.Sprintf("/tmp/saki-154-%d.sock", time.Now().UnixNano())
	defer os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 2)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- struct{}{}
		_ = conn.Close()
	}()
	input := issueToolInitLine() + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":"unknown-1","method":"tools/call","params":{"name":"issue_action","arguments":{"scope":"s1","action":"run","issue_id":"i1"}}}` + "\n"
	out, err := runIssueToolHelper(t, input, issueToolIPCForwarder(path))
	if err != nil {
		t.Fatal(err)
	}
	responses := decodeIssueToolOutput(t, out)
	if len(responses) != 2 || responses[1].Error != nil {
		t.Fatalf("helper response = %+v", responses)
	}
	var result issueToolForwardResponse
	if err := json.Unmarshal(responses[1].Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "operation_unknown") {
		t.Fatalf("IPC unknown result = %+v", result)
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("IPC server did not observe the one connection")
	}
	// The helper does not dial a second time after the disconnect.
	select {
	case <-accepted:
		t.Fatal("helper retried after IPC disconnect")
	default:
	}
}

func TestIssueToolParallelHelpersDoNotShareScopeResults(t *testing.T) {
	init := issueToolInitLine() + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	inputs := []string{
		init + `{"jsonrpc":"2.0","id":"one","method":"tools/call","params":{"name":"issue_list","arguments":{"scope":"scope-one"}}}` + "\n",
		init + `{"jsonrpc":"2.0","id":"two","method":"tools/call","params":{"name":"issue_list","arguments":{"scope":"scope-two"}}}` + "\n",
	}
	outputs := make([]string, len(inputs))
	var wg sync.WaitGroup
	for index := range inputs {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			outputs[index], _ = runIssueToolHelper(t, inputs[index], func(_ context.Context, request issueToolForwardRequest) (issueToolForwardResponse, error) {
				scope := "missing"
				var args map[string]string
				_ = json.Unmarshal(request.Arguments, &args)
				if args["scope"] != "" {
					scope = args["scope"]
				}
				return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: scope}}}, nil
			})
		}()
	}
	wg.Wait()
	for index, id := range []string{"one", "two"} {
		responses := decodeIssueToolOutput(t, outputs[index])
		if len(responses) != 2 || string(responses[1].ID) != `"`+id+`"` {
			t.Fatalf("parallel helper %d = %+v", index, responses)
		}
		var result issueToolForwardResponse
		if err := json.Unmarshal(responses[1].Result, &result); err != nil || len(result.Content) != 1 ||
			result.Content[0].Text != []string{"scope-one", "scope-two"}[index] {
			t.Fatalf("parallel helper result %d = %+v / %v", index, result, err)
		}
	}
}

func TestIssueToolIPCRealSocketRoundTrip(t *testing.T) {
	path := fmt.Sprintf("/tmp/saki-154-ok-%d.sock", time.Now().UnixNano())
	defer os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan issueToolForwardRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 16*1024), issueToolMaxFrameBytes)
		if !scanner.Scan() {
			return
		}
		var request issueToolForwardRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		received <- request
		_, _ = conn.Write([]byte(`{"content":[{"type":"text","text":"socket-ok"}]}` + "\n"))
	}()
	response, err := issueToolIPCForwarder(path)(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: json.RawMessage(`{"scope":"scope-real","issue_id":"issue-a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Content) != 1 || response.Content[0].Text != "socket-ok" {
		t.Fatalf("socket response = %+v", response)
	}
	select {
	case request := <-received:
		if request.Tool != "issue_get" || !strings.Contains(string(request.Arguments), "scope-real") {
			t.Fatalf("socket request = %+v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket server did not receive request")
	}
}

func TestIssueToolHelperLogsOnlyToConfiguredSink(t *testing.T) {
	path := fmt.Sprintf("/tmp/saki-154-log-%d.sock", time.Now().UnixNano())
	defer os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	input := issueToolInitLine() + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"issue_list","arguments":{"scope":"s"}}}` + "\n"
	var stdout, stderr bytes.Buffer
	err = issueToolHelperServe(context.Background(), strings.NewReader(input), &stdout, issueToolIPCForwarder(path), issueToolHelperOptions{ForwardTimeout: 2 * time.Second, Logger: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "issue tool forward failed") {
		t.Fatalf("stderr log = %q", stderr.String())
	}
	responses := decodeIssueToolOutput(t, stdout.String())
	if len(responses) != 2 || responses[1].Error != nil {
		t.Fatalf("stdout protocol = %+v", responses)
	}
	var result issueToolForwardResponse
	if err := json.Unmarshal(responses[1].Result, &result); err != nil || !result.IsError {
		t.Fatalf("stdout unknown result = %+v / %v", result, err)
	}
	if strings.Contains(stdout.String(), "forward failed") {
		t.Fatal("log leaked to stdout")
	}
}

func TestIssueToolIPCReturnsFirstResponseWithoutWaitingForEOF(t *testing.T) {
	path := fmt.Sprintf("/tmp/saki-154-hold-%d.sock", time.Now().UnixNano())
	defer os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	released := make(chan struct{})
	clientClosed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 16*1024), issueToolMaxFrameBytes)
		if !scanner.Scan() {
			return
		}
		_, _ = conn.Write([]byte(`{"content":[{"type":"text","text":"held-response"}]}` + "\n"))
		<-released
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var one [1]byte
		if _, err := conn.Read(one[:]); errors.Is(err, io.EOF) {
			close(clientClosed)
		}
	}()
	result := make(chan issueToolForwardResponse, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := issueToolIPCForwarder(path)(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: json.RawMessage(`{"scope":"s"}`)})
		if err != nil {
			errs <- err
			return
		}
		result <- response
	}()
	select {
	case response := <-result:
		if len(response.Content) != 1 || response.Content[0].Text != "held-response" {
			t.Fatalf("held response = %+v", response)
		}
	case err := <-errs:
		t.Fatalf("client waited/errored before EOF: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("client did not return after first complete response")
	}
	close(released)
	select {
	case <-clientClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not close after first response")
	}
}

func TestIssueToolHelperResponseFrameTooLargeIsSmallError(t *testing.T) {
	init := issueToolInitLine() + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	input := init + `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"issue_get","arguments":{"scope":"s"}}}` + "\n"
	forwardCalls := 0
	var out bytes.Buffer
	err := issueToolHelperServe(context.Background(), strings.NewReader(input), &out, func(context.Context, issueToolForwardRequest) (issueToolForwardResponse, error) {
		forwardCalls++
		return issueToolForwardResponse{Content: []issueToolContent{{Type: "text", Text: strings.Repeat("x", 600)}}}, nil
	}, issueToolHelperOptions{ForwardTimeout: time.Second, MaxFrameBytes: 400})
	if err != nil {
		t.Fatal(err)
	}
	if forwardCalls != 1 {
		t.Fatalf("oversize response forwarded %d times", forwardCalls)
	}
	responses := decodeIssueToolOutput(t, out.String())
	if len(responses) != 2 || responses[1].Error != nil {
		t.Fatalf("oversize protocol output = %+v", responses)
	}
	var result issueToolForwardResponse
	if err := json.Unmarshal(responses[1].Result, &result); err != nil || !result.IsError ||
		len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "response_too_large") {
		t.Fatalf("oversize fallback = %+v / %v", result, err)
	}
}
