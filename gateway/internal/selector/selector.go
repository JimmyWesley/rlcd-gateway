// Package selector talks to the economy model: any System One endpoint
// (open-rlcd self-hosted, open-rlcd hosted, or TypeSafe's Jev). They share
// POST /v1/systemone, so the backend is just a base URL, a token and a model.
//
// In F0 it is only used by the dashboard's connection test. F1 uses it to
// score context blocks; it only ever returns decisions per key, never text.
package selector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// Question follows the System One schema: choice/score take criteria
// (a map for choice, an ordered list for score); noul takes none.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Result struct {
	Answers map[string]Answer `json:"answers"`
	Usage   json.RawMessage   `json:"usage,omitempty"`
	// WallMs is end to end from the gateway; ForwardMs is the model's own
	// compute when the server reports it (open-rlcd sends x-rlcd-forward-ms).
	WallMs    int64    `json:"wall_ms"`
	ForwardMs *float64 `json:"forward_ms,omitempty"`
}

type Client struct {
	cfg  config.Selector
	http *http.Client
}

func New(cfg config.Selector) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (*Result, error) {
	if c.cfg.BaseURL == "" {
		return nil, fmt.Errorf("selector has no base_url configured")
	}
	body, _ := json.Marshal(map[string]any{"state": state, "questions": questions, "model": c.cfg.Model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.cfg.ResolvedToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	// One retry on a failure before any response (DNS, refused connection):
	// .local names resolve through mDNS on macOS and miss now and then. The
	// caller's context still bounds the total time.
	if err != nil && ctx.Err() == nil {
		retry, rerr := http.NewRequestWithContext(ctx, http.MethodPost, req.URL.String(), bytes.NewReader(body))
		if rerr == nil {
			retry.Header = req.Header.Clone()
			resp, err = c.http.Do(retry)
		}
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		s := string(raw)
		if len(s) > 300 {
			s = s[:300]
		}
		return nil, fmt.Errorf("selector %d: %s", resp.StatusCode, s)
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("selector response: %w", err)
	}
	res.WallMs = time.Since(start).Milliseconds()
	// open-rlcd reports fractional milliseconds ("122.8").
	if v, err := strconv.ParseFloat(resp.Header.Get("X-Rlcd-Forward-Ms"), 64); err == nil {
		res.ForwardMs = &v
	}
	return &res, nil
}

// Probe is the dashboard's "test connection": one tiny, obvious question.
func (c *Client) Probe(ctx context.Context) (*Result, error) {
	return c.Ask(ctx,
		map[string]any{"goal": "fix the login bug in auth.go",
			"block": "npm install finished: added 812 packages in 14s"},
		map[string]Question{"keep": {Type: "noul",
			Instructions: "Is this block of context needed to accomplish the goal?"}})
}
