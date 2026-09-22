package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func TestShutdownRejectsLateViewWrite(t *testing.T) {
	e := testEngine(t)
	original, err := e.putView("client", protocol.View{Data: json.RawMessage(`{"draft":"preserved"}`)})
	if err != nil {
		t.Fatal(err)
	}
	e.stopping = true
	_, err = e.putView("client", protocol.View{Revision: original.Revision, Data: json.RawMessage(`{"draft":"late"}`)})
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "stopping" {
		t.Fatalf("late write was not rejected: %v", err)
	}
	retained, err := e.store.LoadView("client")
	if err != nil || retained.Revision != original.Revision || string(retained.Data) != string(original.Data) {
		t.Fatalf("late handler overwrote accepted draft: %+v %v", retained, err)
	}
}

func TestHTTPDrainLetsAcceptedHandlerFinish(t *testing.T) {
	connections := &httpConnections{}
	read, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		close(read)
		<-release
		_, _ = w.Write(body)
	}))
	s.Config.ConnState = connections.changed
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	delivered := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, strings.NewReader("accepted view"))
		resp, err := s.Client().Do(req)
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if err == nil && string(body) != "accepted view" {
				err = fmt.Errorf("accepted response lost: %q", body)
			}
		}
		delivered <- err
	}()
	select {
	case <-read:
	case <-ctx.Done():
		close(release)
		t.Fatal("handler did not read request")
	}
	connections.drain()
	drained := make(chan error, 1)
	go func() { drained <- s.Config.Shutdown(ctx) }()
	close(release)
	if err := <-delivered; err != nil {
		t.Fatal(err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestStopWithUnfinishedHTTPConnections(t *testing.T) {
	for _, kind := range []string{"new", "partial-header", "partial-body"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			c, cleanup := startTestServer(t, home)
			defer cleanup()
			conn, err := net.Dial("tcp", strings.TrimPrefix(c.Discovery.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			switch kind {
			case "partial-header":
				_, err = fmt.Fprint(conn, "GET /v1/snapshot HTTP/1.1\r\nHost: localhost\r\n")
			case "partial-body":
				_, err = fmt.Fprintf(conn, "POST /v1/command HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nX-TUI-Protocol: 1\r\nContent-Length: 1000\r\nExpect: 100-continue\r\n\r\n", c.Discovery.Token)
				if err == nil {
					_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					var response *http.Response
					response, err = http.ReadResponse(bufio.NewReader(conn), nil)
					if err == nil && response.StatusCode != http.StatusContinue {
						t.Fatalf("expected body reader to start, got %s", response.Status)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			started := time.Now()
			if err := lifecycle.Stop(ctx, home); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("unfinished HTTP input delayed shutdown: %v", elapsed)
			}
			st, err := storage.Open(home + "/state.sqlite")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			snapshot, _, err := st.Load()
			if err != nil || len(snapshot.Threads) == 0 {
				t.Fatalf("shutdown state missing: %v", err)
			}
			for _, thread := range snapshot.Threads {
				if thread.State == "running" || thread.State == "waiting" {
					t.Fatal("shutdown did not persist interrupted work")
				}
			}
		})
	}
}
