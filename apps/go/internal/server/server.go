// Package server owns durable fixture execution independently of attached clients.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
	"golang.org/x/sys/unix"
)

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type engine struct {
	mu          sync.Mutex
	snap        protocol.Snapshot
	store       *storage.Store
	subscribers map[chan protocol.Snapshot]bool
	stopping    bool
}

func clone(s protocol.Snapshot) protocol.Snapshot {
	b, _ := json.Marshal(s)
	var n protocol.Snapshot
	_ = json.Unmarshal(b, &n)
	return n
}
func (e *engine) publish() {
	for ch := range e.subscribers {
		select {
		case ch <- clone(e.snap):
		default:
			close(ch)
			delete(e.subscribers, ch)
		}
	}
}
func (e *engine) command(c protocol.Command) (protocol.Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.Version != protocol.Version {
		return protocol.Receipt{}, failure("version_mismatch", "protocol version 1 required")
	}
	if c.ID == "" || len(c.ID) > 128 {
		return protocol.Receipt{}, failure("invalid", "bounded command identity required")
	}
	if r, err := e.store.Lookup(c); err != nil {
		return protocol.Receipt{}, err
	} else if r != nil {
		return *r, nil
	}
	if e.stopping {
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	next := clone(e.snap)
	target, err := apply(&next, c)
	if err != nil {
		return protocol.Receipt{}, err
	}
	next.Revision++
	encoded, err := json.Marshal(next)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if len(encoded) > 4<<20 {
		return protocol.Receipt{}, failure("capacity", "fixture snapshot exceeds 4 MiB; remove queued content before submitting")
	}
	r := protocol.Receipt{ID: c.ID, State: "accepted", Revision: next.Revision, TargetID: target}
	if err = e.store.Save(next, &c, &r); err != nil {
		return protocol.Receipt{}, err
	}
	e.snap = next
	e.publish()
	return r, nil
}
func (e *engine) tick() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	changed := false
	for i := range next.Threads {
		t := &next.Threads[i]
		if t.NeedsResume || t.State != "running" {
			continue
		}
		changed = true
		if t.Tick >= fixtureTurnTicks {
			if fixtureTurnCompleted(t) {
				if len(t.Queue) == 0 {
					t.State = "idle"
					continue
				}
				startFixturePrompt(t)
			} else {
				// Older snapshots used an unbounded tick counter. Finish their
				// preserved turn before starting any queued prompt.
				t.Tick = fixtureTurnTicks - 1
			}
		}
		t.Tick++
		if t.Tick == fixtureTurnTicks/2 {
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "completed"
				}
			}
			for j := range t.Plan {
				if t.Plan[j].State == "active" {
					t.Plan[j].State = "completed"
					if j+1 < len(t.Plan) {
						t.Plan[j+1].State = "active"
					}
					break
				}
			}
		}
		if t.Tick >= fixtureTurnTicks {
			for j := range t.Plan {
				t.Plan[j].State = "completed"
			}
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "completed"
				}
			}
			promptID := t.ID + "-initial"
			for j := len(t.Activity) - 1; j >= 0; j-- {
				if t.Activity[j].Role == "user" {
					promptID = t.Activity[j].ID
					t.Activity[j].State = "completed"
					break
				}
			}
			result := protocol.Activity{ID: "result-" + promptID, Role: "agent", Title: "Fixture agent", Text: "Synthetic review complete. Captured settings and attachments were preserved; no agent or tool was executed.", State: "completed"}
			found := false
			for j := range t.Activity {
				if t.Activity[j].ID == result.ID {
					t.Activity[j] = result
					found = true
					break
				}
			}
			if !found {
				t.Activity = append(t.Activity, result)
			}
			if len(t.Queue) == 0 {
				t.State = "idle"
			}
		}
		trimFixtureActivity(t)
	}
	if !changed {
		return nil
	}
	next.Revision++
	if err := e.store.Save(next, nil, nil); err != nil {
		return err
	}
	e.snap = next
	e.publish()
	return nil
}

// Fixture turns use durable per-turn progress so Resume continues saved work.
const fixtureTurnTicks = 8

func fixtureTurnCompleted(t *protocol.Thread) bool {
	for _, step := range t.Plan {
		if step.State != "completed" {
			return false
		}
	}
	for _, child := range t.Children {
		if child.State == "running" || child.State == "interrupted" {
			return false
		}
	}
	promptID := t.ID + "-initial"
	for i := len(t.Activity) - 1; i >= 0; i-- {
		if t.Activity[i].Role == "user" {
			promptID = t.Activity[i].ID
			break
		}
	}
	for _, a := range t.Activity {
		if a.ID == "result-"+promptID && a.State == "completed" {
			return true
		}
	}
	return false
}

func startFixturePrompt(t *protocol.Thread) {
	p := t.Queue[0]
	t.Queue = t.Queue[1:]
	t.QueueRevision++
	t.Effective = p.Settings
	t.State, t.Tick = "running", 0
	// Keep the accepted capture after it leaves the editable queue. Activity
	// retention applies to this fixture history just as it does to other detail.
	capture, _ := json.Marshal(p)
	t.Activity = append(t.Activity, protocol.Activity{ID: p.ID, Role: "user", Text: p.Text, State: "running", Detail: string(capture)})
	t.Plan = []protocol.PlanStep{{Title: "Review the captured prompt", State: "active"}, {Title: "Complete the synthetic review", State: "pending"}}
	// Bound the fixture inspector while preserving older child detail in the
	// ordinary, explicitly retained activity history.
	if len(t.Children) >= 32 {
		child := t.Children[0]
		detail, _ := json.Marshal(child)
		t.Activity = append(t.Activity, protocol.Activity{ID: "archive-" + child.ID, Role: "agent", Title: child.Name, State: child.State, Text: "Earlier synthetic child history archived from the Agents inspector.", Detail: string(detail)})
		t.Children = t.Children[1:]
	}
	t.Children = append(t.Children, protocol.Child{ID: "child-" + p.ID, ParentID: t.ID, Name: "Prompt review", State: "running", Activity: []protocol.Activity{{ID: "review-" + p.ID, Role: "agent", Text: "Reviewing the captured prompt. This child and its history are synthetic."}}})
	trimFixtureActivity(t)
}

func trimFixtureActivity(t *protocol.Thread) {
	if len(t.Activity) <= 128 {
		return
	}
	// The notice owns a slot; never replace the detail of a retained capture.
	notice := protocol.Activity{ID: "history-limit-" + t.ID, Role: "tool", Title: "History retention", State: "completed", Text: "Earlier fixture activity was truncated by the 128-item retention limit."}
	t.Activity = append([]protocol.Activity{notice}, t.Activity[len(t.Activity)-127:]...)
}

func Serve(ctx context.Context, home string) error {
	abs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	home = abs
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err = os.Chmod(home, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(home, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return failure("already_running", "application home is locked by another server")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	dbPath := filepath.Join(home, "state.sqlite")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	if err = os.Chmod(dbPath, 0600); err != nil {
		return err
	}
	st, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	snap, exists, err := st.Load()
	if err != nil {
		return err
	}
	if exists && snap.Version != protocol.Version {
		return failure("version_mismatch", "stored snapshot version is unsupported; state was not modified")
	}
	if !exists {
		snap = fixture.Initial()
	} else {
		for i := range snap.Threads {
			t := &snap.Threads[i]
			if t.State != "idle" {
				t.NeedsResume = true
				t.State = "interrupted"
			}
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "interrupted"
				}
			}
			for j := range t.Requests {
				if t.Requests[j].State == "pending" {
					t.Requests[j].Revision++
					t.Requests[j].Delivery = "revalidation-required"
				}
			}
		}
		for i := range snap.Terminals {
			snap.Terminals[i].State = "ended"
			snap.Terminals[i].Revision++
		}
		snap.Revision++
	}
	ensureProjects(&snap)
	if !slices.Contains(snap.Capabilities, "project-management") {
		snap.Capabilities = append(snap.Capabilities, "project-management")
	}
	if !slices.Contains(snap.Capabilities, "thread-lifecycle") {
		snap.Capabilities = append(snap.Capabilities, "thread-lifecycle")
	}
	snap.InstanceID = ID()
	if err = st.Save(snap, nil, nil); err != nil {
		return err
	}
	e := &engine{snap: snap, store: st, subscribers: map[chan protocol.Snapshot]bool{}}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	discovery := protocol.Discovery{Version: protocol.Version, URL: "http://" + listener.Addr().String(), Token: ID() + ID(), InstanceID: snap.InstanceID, Home: home, PID: os.Getpid()}
	b, _ := json.Marshal(discovery)
	tmp := filepath.Join(home, "discovery.tmp")
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(home, "discovery.json")); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(home, "discovery.json"))
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		s := clone(e.snap)
		e.mu.Unlock()
		respond(w, s)
	})
	mux.HandleFunc("POST /v1/command", func(w http.ResponseWriter, r *http.Request) {
		var c protocol.Command
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 768*1024)).Decode(&c); err != nil {
			w.WriteHeader(400)
			respond(w, protocol.Error{Code: "invalid", Message: "invalid command JSON"})
			return
		}
		receipt, err := e.command(c)
		if err != nil {
			w.WriteHeader(409)
			var pe *protocol.Error
			if errors.As(err, &pe) {
				respond(w, pe)
			} else {
				respond(w, protocol.Error{Code: "storage", Message: "command could not be persisted"})
			}
			return
		}
		respond(w, receipt)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		watchctx := conn.CloseRead(r.Context())
		ch := make(chan protocol.Snapshot, 8)
		e.mu.Lock()
		ch <- clone(e.snap)
		e.subscribers[ch] = true
		e.mu.Unlock()
		defer func() { e.mu.Lock(); delete(e.subscribers, ch); e.mu.Unlock() }()
		for {
			select {
			case <-runctx.Done():
				conn.Close(websocket.StatusGoingAway, "server stopped")
				return
			case <-watchctx.Done():
				return
			case s, ok := <-ch:
				if !ok {
					conn.Close(websocket.StatusPolicyViolation, "resync required: slow client")
					return
				}
				b, _ := json.Marshal(s)
				writeCtx, cancel := context.WithTimeout(watchctx, 3*time.Second)
				err = conn.Write(writeCtx, websocket.MessageText, b)
				cancel()
				if err != nil {
					return
				}
			}
		}
	})
	mux.HandleFunc("GET /v1/views/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := st.LoadView(r.PathValue("id"))
		if err != nil {
			http.Error(w, "view unavailable", 500)
			return
		}
		respond(w, v)
	})
	mux.HandleFunc("PUT /v1/views/{id}", func(w http.ResponseWriter, r *http.Request) {
		var v protocol.View
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&v); err != nil {
			http.Error(w, "invalid view", 400)
			return
		}
		saved, err := st.PutView(r.PathValue("id"), v.Data, v.Revision)
		if err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				w.WriteHeader(http.StatusConflict)
				respond(w, pe)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				respond(w, protocol.Error{Code: "storage", Message: "view not persisted"})
			}
			return
		}
		respond(w, saved)
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.stopping = true
		e.mu.Unlock()
		respond(w, map[string]bool{"stopping": true})
		cancel()
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins unsupported", 403)
			return
		}
		want := "Bearer " + discovery.Token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Header.Get("X-TUI-Protocol") != "1" {
			http.Error(w, "protocol version 1 required", 426)
			return
		}
		mux.ServeHTTP(w, r)
	})
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var runErr error
loop:
	for {
		select {
		case <-runctx.Done():
			break loop
		case err := <-errs:
			if !errors.Is(err, http.ErrServerClosed) {
				runErr = err
			}
			break loop
		case <-ticker.C:
			if err := e.tick(); err != nil {
				runErr = fmt.Errorf("persist fixture tick: %w", err)
				break loop
			}
		}
	}
	cancel()
	e.mu.Lock()
	e.stopping = true
	final := clone(e.snap)
	for i := range final.Threads {
		t := &final.Threads[i]
		if t.State == "running" || t.State == "waiting" {
			t.State = "interrupted"
			t.NeedsResume = true
		}
		for j := range t.Children {
			if t.Children[j].State == "running" {
				t.Children[j].State = "interrupted"
			}
		}
	}
	for i := range final.Terminals {
		final.Terminals[i].State = "ended"
	}
	final.Revision++
	saveErr := st.Save(final, nil, nil)
	e.mu.Unlock()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	stopErr := httpServer.Shutdown(stopCtx)
	closeErr := st.Close()
	outcomeErr := errors.Join(runErr, saveErr, stopErr, closeErr)
	outcome := protocol.ShutdownOutcome{InstanceID: discovery.InstanceID, Success: outcomeErr == nil}
	if outcomeErr != nil {
		outcome.Error = outcomeErr.Error()
	}
	recordErr := writeShutdown(home, outcome)
	return errors.Join(outcomeErr, recordErr)
}

func writeShutdown(home string, outcome protocol.ShutdownOutcome) error {
	b, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, "shutdown-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(home, "shutdown-"+outcome.InstanceID+".json")); err != nil {
		return err
	}
	dir, err := os.Open(home)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
