package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/RCRaceHub/EventScreenDisplay/internal/jsonx"
)

// DefaultControlSocket is where eventscreen wifi listens for the agent.
const DefaultControlSocket = "/run/eventscreen-wifi/control.sock"

// WifiClient forwards /api/v1/wifi calls to the root eventscreen-wifi
// service over its Unix socket. The agent runs unprivileged, so it can't
// change NetworkManager itself.
type WifiClient struct {
	path   string
	client *http.Client
}

// NewWifiClient talks to the control socket at path.
func NewWifiClient(path string) *WifiClient {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	return &WifiClient{path: path, client: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// Call implements WifiCaller. If the service isn't running it returns a
// 503 API error.
func (c *WifiClient) Call(method, path string, body jsonx.Object) (int, json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://localhost"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return 0, nil, apiErr(503, "Wi-Fi service isn't running (systemctl status eventscreen-wifi)")
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	return resp.StatusCode, json.RawMessage(data), nil
}
