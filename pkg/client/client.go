// Package client is a tiny Go client for submitting Elko Feedback, meant
// for MCP servers and CLIs. It has no dependencies beyond the stdlib.
//
//	c := client.New("https://feedback.elko.ai")
//	id, err := c.Submit(ctx, client.Feedback{Channel: "elko.mcp.news", Message: "great", AppVersion: "1.4.0"})
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Feedback struct {
	Channel    string            `json:"channel"`
	Message    string            `json:"message"`
	Rating     *int              `json:"rating,omitempty"`
	Category   string            `json:"category,omitempty"`
	Email      string            `json:"email,omitempty"`
	Custom     map[string]string `json:"custom,omitempty"`
	AppVersion string            `json:"app_version,omitempty"`
	Source     string            `json:"source,omitempty"` // defaults to "mcp"
}

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 10 * time.Second}, UserAgent: "elko-feedback-client/1"}
}

// FormURL returns the hosted form link for a channel (handy as an MCP
// tool result so humans can follow up in a browser).
func (c *Client) FormURL(channel string) string { return c.BaseURL + "/f/" + channel }

// Submit posts feedback and returns the new feedback id.
func (c *Client) Submit(ctx context.Context, f Feedback) (int64, error) {
	if f.Source == "" {
		f.Source = "mcp"
	}
	b, _ := json.Marshal(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/feedback", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("elko-feedback: %s (%d)", out.Error, resp.StatusCode)
	}
	return out.ID, nil
}
