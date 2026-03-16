package cloudapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const baseURL = "https://grafana.com"

type Client struct {
	token string
	http  *http.Client
}

type AccessPolicy struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName"`
	Scopes      []string            `json:"scopes"`
	Realms      []AccessPolicyRealm `json:"realms"`
}

type AccessPolicyRealm struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

type CreateAccessPolicyRequest struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName"`
	Scopes      []string            `json:"scopes"`
	Realms      []AccessPolicyRealm `json:"realms"`
}

type Token struct {
	ID             string `json:"id"`
	AccessPolicyID string `json:"accessPolicyId"`
	Name           string `json:"name"`
	DisplayName    string `json:"displayName"`
	Key            string `json:"token,omitempty"`
}

type CreateTokenRequest struct {
	AccessPolicyID string `json:"accessPolicyId,omitempty"`
	Name           string `json:"name"`
	DisplayName    string `json:"displayName"`
}

type listResponse[T any] struct {
	Items []T `json:"items"`
}

func (t *Token) UnmarshalJSON(data []byte) error {
	type rawToken struct {
		ID             string `json:"id"`
		AccessPolicyID string `json:"accessPolicyId"`
		Name           string `json:"name"`
		DisplayName    string `json:"displayName"`
		Token          string `json:"token"`
		Key            string `json:"key"`
	}
	var raw rawToken
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.ID = raw.ID
	t.AccessPolicyID = raw.AccessPolicyID
	t.Name = raw.Name
	t.DisplayName = raw.DisplayName
	t.Key = strings.TrimSpace(raw.Token)
	if t.Key == "" {
		t.Key = strings.TrimSpace(raw.Key)
	}
	return nil
}

func New(token string) *Client {
	return &Client{
		token: strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Validate(ctx context.Context, region, stackID string) error {
	_, err := c.ListAccessPolicies(ctx, region, stackID)
	return err
}

func (c *Client) ListAccessPolicies(ctx context.Context, region, stackID string) ([]AccessPolicy, error) {
	params := url.Values{}
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		params.Set("region", trimmed)
	}
	if trimmed := strings.TrimSpace(stackID); trimmed != "" {
		params.Set("realmType", "stack")
		params.Set("realmIdentifier", trimmed)
	}
	resp, err := c.do(ctx, http.MethodGet, "/api/v1/accesspolicies", params, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("grafana cloud api failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var wrapped listResponse[AccessPolicy]
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Items != nil {
		return wrapped.Items, nil
	}
	var out []AccessPolicy
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateAccessPolicy(ctx context.Context, region string, req CreateAccessPolicyRequest) (*AccessPolicy, error) {
	params := url.Values{}
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		params.Set("region", trimmed)
	}
	out := &AccessPolicy{}
	if err := c.postJSON(ctx, "/api/v1/accesspolicies", params, req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeleteAccessPolicy(ctx context.Context, region, accessPolicyID string) error {
	endpoint := fmt.Sprintf("/api/v1/accesspolicies/%s", url.PathEscape(strings.TrimSpace(accessPolicyID)))
	params := url.Values{}
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		params.Set("region", trimmed)
	}
	resp, err := c.do(ctx, http.MethodDelete, endpoint, params, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("grafana cloud api failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) CreateToken(ctx context.Context, region, accessPolicyID string, req CreateTokenRequest) (*Token, error) {
	out := &Token{}
	params := url.Values{}
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		params.Set("region", trimmed)
	}
	req.AccessPolicyID = strings.TrimSpace(accessPolicyID)
	if err := c.postJSON(ctx, "/api/v1/tokens", params, req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeleteToken(ctx context.Context, region, tokenID string) error {
	endpoint := fmt.Sprintf("/api/v1/tokens/%s", url.PathEscape(strings.TrimSpace(tokenID)))
	params := url.Values{}
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		params.Set("region", trimmed)
	}
	resp, err := c.do(ctx, http.MethodDelete, endpoint, params, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("grafana cloud api failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, params url.Values, out any) error {
	resp, err := c.do(ctx, http.MethodGet, endpoint, params, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("grafana cloud api failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, endpoint string, params url.Values, reqBody any, out any) error {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, endpoint, params, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("grafana cloud api failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) do(ctx context.Context, method, endpoint string, params url.Values, body []byte) (*http.Response, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, endpoint)
	if len(params) > 0 {
		u.RawQuery = params.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}
