package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

type Client struct {
	Discovery protocol.Discovery
	HTTP      *http.Client
}

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func New(d protocol.Discovery) *Client {
	return &Client{Discovery: d, HTTP: &http.Client{Timeout: 5 * time.Second}}
}
func Discover(home string) (protocol.Discovery, error) {
	var d protocol.Discovery
	b, err := os.ReadFile(filepath.Join(home, "discovery.json"))
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return d, err
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || d.Home != abs || d.Version != protocol.Version || d.Token == "" {
		return d, fmt.Errorf("invalid discovery for selected application home")
	}
	return d, nil
}
func (c *Client) request(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.Discovery.URL+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.Discovery.Token)
	r.Header.Set("X-TUI-Protocol", "1")
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var pe protocol.Error
		if json.NewDecoder(resp.Body).Decode(&pe) == nil && pe.Code != "" {
			return &pe
		}
		return fmt.Errorf("server HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return nil
}
func (c *Client) Snapshot(ctx context.Context) (protocol.Snapshot, error) {
	var s protocol.Snapshot
	err := c.request(ctx, "GET", "/v1/snapshot", nil, &s)
	if err == nil && (s.Version != protocol.Version || s.InstanceID != c.Discovery.InstanceID) {
		err = fmt.Errorf("server identity or protocol mismatch")
	}
	return s, err
}
func (c *Client) Command(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error) {
	var r protocol.Receipt
	if cmd.ID == "" {
		return r, &protocol.Error{Code: "invalid", Message: "assign client.ID() once and retain it across retries"}
	}
	if cmd.Version == 0 {
		cmd.Version = protocol.Version
	}
	err := c.request(ctx, "POST", "/v1/command", cmd, &r)
	return r, err
}
func (c *Client) Watch(ctx context.Context, receive func(protocol.Snapshot)) error {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.Discovery.Token)
	headers.Set("X-TUI-Protocol", "1")
	conn, _, err := websocket.Dial(ctx, strings.Replace(c.Discovery.URL, "http://", "ws://", 1)+"/v1/events", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)
	var previous int64
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var s protocol.Snapshot
		if err = json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s.Version != protocol.Version || s.InstanceID != c.Discovery.InstanceID {
			return fmt.Errorf("stream identity/version mismatch")
		}
		if s.Revision <= previous {
			return fmt.Errorf("stream revision did not advance")
		}
		previous = s.Revision
		receive(s)
	}
}
func (c *Client) LoadView(ctx context.Context, id string) (protocol.View, error) {
	var v protocol.View
	err := c.request(ctx, "GET", "/v1/views/"+url.PathEscape(id), nil, &v)
	return v, err
}
func (c *Client) PutView(ctx context.Context, id string, data json.RawMessage, expectedRevision int64) (protocol.View, error) {
	var v protocol.View
	err := c.request(ctx, "PUT", "/v1/views/"+url.PathEscape(id), protocol.View{Data: data, Revision: expectedRevision}, &v)
	return v, err
}
func (c *Client) View(ctx context.Context, id string) (json.RawMessage, error) {
	v, err := c.LoadView(ctx, id)
	return v.Data, err
}
func (c *Client) SaveView(ctx context.Context, id string, data json.RawMessage) error {
	v, err := c.LoadView(ctx, id)
	if err != nil {
		return err
	}
	_, err = c.PutView(ctx, id, data, v.Revision)
	return err
}
func (c *Client) Stop(ctx context.Context) error { return c.request(ctx, "POST", "/v1/stop", nil, nil) }
