package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestShutdownFailureIsNotReportedAsUnreachable(t *testing.T) {
	cause := errors.New("server shutdown failed: drain HTTP requests: context deadline exceeded")
	err := describeStopError("/test/home", cause)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "not reachable") || !strings.Contains(err.Error(), "drain HTTP requests") {
		t.Fatalf("misleading shutdown diagnostic: %v", err)
	}
}
