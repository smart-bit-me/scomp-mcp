package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultServerURL = "https://link.scomp.me"
	maxResponseBytes = 2 << 20
)

type Scope string

const (
	ScopeHostsRead     Scope = "hosts:read"
	ScopeTerminalRead  Scope = "terminal:read"
	ScopeTerminalWrite Scope = "terminal:write"
)

type Session struct {
	ID      string    `json:"id"`
	Cmd     string    `json:"cmd"`
	Mode    string    `json:"mode"`
	Active  bool      `json:"active"`
	Created time.Time `json:"created"`
}

type Host struct {
	AgentUID            string    `json:"agent_uid"`
	Hostname            string    `json:"hostname"`
	Online              bool      `json:"online"`
	Sessions            []Session `json:"sessions"`
	RegisteredAt        time.Time `json:"registered_at"`
	AgentVersion        string    `json:"agent_version,omitempty"`
	MinimumAgentVersion string    `json:"minimum_agent_version,omitempty"`
	LatestAgentVersion  string    `json:"latest_agent_version,omitempty"`
	UpdateRequired      bool      `json:"update_required,omitempty"`
	UpdateAvailable     bool      `json:"update_available,omitempty"`
}

type Credential struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	Scopes       []Scope    `json:"scopes"`
	AllowedHosts []string   `json:"allowed_hosts"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

type DeviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceToken struct {
	AccessToken string     `json:"access_token"`
	TokenType   string     `json:"token_type"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Credential  Credential `json:"credential"`
}

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("scomp API: %s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("scomp API: HTTP %d", e.Status)
}

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func NewClient(rawURL, token string) (*Client, error) {
	baseURL, err := ParseServerURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				IdleConnTimeout:       60 * time.Second,
			},
		},
	}, nil
}

func ParseServerURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		rawURL = DefaultServerURL
	}
	parsed, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("server URL must be an HTTPS origin")
	}
	host := parsed.Hostname()
	loopback := host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return nil, errors.New("server URL must use HTTPS (HTTP is allowed only for loopback development)")
	}
	if parsed.Path != "" {
		return nil, errors.New("server URL must not include a path")
	}
	return parsed, nil
}

func (c *Client) ServerURL() string { return c.baseURL.String() }

func (c *Client) WebSocketURL(path string, query url.Values) string {
	u := *c.baseURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *Client) Hosts(ctx context.Context) ([]Host, error) {
	var result []Host
	if err := c.do(ctx, http.MethodGet, "/api/hosts", nil, true, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Host{}
	}
	return result, nil
}

func (c *Client) Sessions(ctx context.Context, hostUID string) ([]Session, error) {
	var result []Session
	path := "/api/sessions?uid=" + url.QueryEscape(hostUID)
	if err := c.do(ctx, http.MethodGet, path, nil, true, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Session{}
	}
	return result, nil
}

func (c *Client) WSTicket(ctx context.Context) (string, error) {
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/ws-ticket", struct{}{}, true, &result); err != nil {
		return "", err
	}
	if result.Ticket == "" {
		return "", errors.New("scomp API returned an empty WebSocket ticket")
	}
	return result.Ticket, nil
}

func (c *Client) StartDevice(ctx context.Context, label string, scopes []Scope, challenge string) (DeviceStart, error) {
	var result DeviceStart
	err := c.do(ctx, http.MethodPost, "/api/mcp/device/start", map[string]any{
		"label": label, "scopes": scopes, "code_challenge": challenge,
	}, false, &result)
	return result, err
}

func (c *Client) PollDevice(ctx context.Context, deviceCode, verifier string) (DeviceToken, error) {
	var result DeviceToken
	err := c.do(ctx, http.MethodPost, "/api/mcp/device/token", map[string]string{
		"device_code": deviceCode, "code_verifier": verifier,
	}, false, &result)
	return result, err
}

func (c *Client) do(ctx context.Context, method, path string, body any, authenticated bool, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if c.token == "" {
			return errors.New("not logged in; run scomp-mcp login")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("scomp API response exceeded size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Code string `json:"error"`
		}
		json.Unmarshal(data, &apiError)
		return &APIError{Status: response.StatusCode, Code: apiError.Code}
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode scomp API response: %w", err)
	}
	return nil
}
