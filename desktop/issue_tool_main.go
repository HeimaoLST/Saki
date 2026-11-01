package main

import (
	"context"
	"errors"
	"io"
)

// runIssueToolHelper is the same-binary stdio entry point. It never starts the
// GUI, a window, a Runtime client, or a second application store.
func runIssueToolHelperCLI(in io.Reader, out, logger io.Writer) error {
	endpoint := issueToolIPCEndpoint()
	if endpoint == "" {
		return errors.New("issue tool IPC endpoint is not configured")
	}
	return issueToolHelperServe(context.Background(), in, out, issueToolIPCForwarder(endpoint), issueToolHelperOptions{Logger: logger})
}

func runIssueToolHelperMain(args []string, in io.Reader, out, logger io.Writer) error {
	if len(args) != 1 || args[0] != "--issue-tool-helper" {
		return errors.New("not an issue tool helper invocation")
	}
	return runIssueToolHelperCLI(in, out, logger)
}
