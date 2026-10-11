package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DesiredResponse is the body of GET /v1/deploy/desired.
type DesiredResponse struct {
	Release *Release `json:"release"`
}

// Client is the actuator's HTTP API to `line serve`, with the deployer token.
type Client struct {
	URL, Token string
	HTTP       *http.Client
}

func (c Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c Client) Desired(ctx context.Context) (*Release, error) {
	var d DesiredResponse
	if err := c.do(ctx, http.MethodGet, "/v1/deploy/desired", nil, &d); err != nil {
		return nil, err
	}
	return d.Release, nil
}

func (c Client) Report(ctx context.Context, s Status) error {
	return c.do(ctx, http.MethodPost, "/v1/deploy/status", s, nil)
}
