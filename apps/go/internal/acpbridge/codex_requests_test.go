package acpbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestCodexNativeRequestResolutionAfterResponse(t *testing.T) {
	for _, response := range []string{`{"id":7,"result":{"answers":{}}}`, `{"id":7,"error":{"code":-32602,"message":"unsupported"}}`} {
		b := newCodex(&host{ctx: context.Background()}).(*codexBridge)
		request := `{"id":7,"method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"turn","itemId":"item"}}`
		reader := newCodexNativeReader(strings.NewReader(request+"\n"), b)
		got, err := io.ReadAll(reader)
		if err != nil || string(got) != request+"\n" {
			t.Fatalf("request altered: %q %v", got, err)
		}
		var output bytes.Buffer
		if _, err = b.writeNativeResponse(&output, []byte(response)); err != nil {
			t.Fatal(err)
		}
		// Duplicate SDK/native response never writes twice.
		_, _ = b.writeNativeResponse(&output, []byte(response))
		if output.String() != response {
			t.Fatalf("duplicate response: %s", output.String())
		}
		resolved := []byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":7}}`)
		if err = b.observeNativeRequest(resolved); err != nil {
			t.Fatal(err)
		}
		if b.retired {
			t.Fatal("normal resolution retired the bridge")
		}
		if err = b.observeNativeRequest(resolved); err != nil || b.retired {
			t.Fatal("duplicate resolution retired bridge")
		}
		_, _ = b.writeNativeResponse(&output, []byte(response))
		if output.String() != response {
			t.Fatal("resolved response replayed")
		}
	}
}

func TestCodexNativeWithdrawalBeforeCallback(t *testing.T) {
	b := newCodex(&host{ctx: context.Background()}).(*codexBridge)
	if err := b.observeNativeRequest([]byte(`{"id":"7","method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"turn","itemId":"item"}}`)); err != nil {
		t.Fatal(err)
	}
	// A numeric ID or another thread must not resolve this request.
	for _, params := range []string{`{"threadId":"thread","requestId":7}`, `{"threadId":"other","requestId":"7"}`} {
		_ = b.observeNativeRequest([]byte(`{"method":"serverRequest/resolved","params":` + params + `}`))
		if b.retired {
			t.Fatal("uncorrelated resolution retired bridge")
		}
	}
	_ = b.observeNativeRequest([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":"7"}}`))
	b.mu.Lock()
	retired := b.retired
	b.mu.Unlock()
	if !retired {
		t.Fatal("withdrawal before callback registration was lost")
	}
	var output bytes.Buffer
	_, _ = b.writeNativeResponse(&output, []byte(`{"id":"7","result":{"answers":{}}}`))
	if output.Len() != 0 {
		t.Fatal("withdrawn answer reached native writer")
	}
}

func TestCodexNativeRequestBounds(t *testing.T) {
	b := newCodex(&host{}).(*codexBridge)
	for i := 0; i < 1024; i++ {
		if err := b.observeNativeRequest([]byte(fmt.Sprintf(`{"id":%d,"method":"test","params":{}}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.observeNativeRequest([]byte(`{"id":1024,"method":"test","params":{}}`)); err == nil {
		t.Fatal("unbounded request map")
	}
	if _, err := io.ReadAll(newCodexNativeReader(strings.NewReader(strings.Repeat("x", maxFrame+1)), b)); err == nil {
		t.Fatal("unbounded native frame")
	}
}
