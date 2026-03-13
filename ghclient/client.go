package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

type webhookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
	InsecureSSL string `json:"insecure_ssl"`
}

type webhookRequest struct {
	Name   string        `json:"name"`
	Active bool          `json:"active"`
	Events []string      `json:"events"`
	Config webhookConfig `json:"config"`
}

type webhookResponse struct {
	ID int64 `json:"id"`
}

// RegisterWebhook creates a push webhook on the repo and returns its ID.
// repoURL is https://github.com/owner/repo.
func (c *Client) RegisterWebhook(ctx context.Context, repoURL, token, webhookURL, secret string) (int64, error) {
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return 0, err
	}

	body, _ := json.Marshal(webhookRequest{
		Name:   "web",
		Active: true,
		Events: []string{"push"},
		Config: webhookConfig{
			URL:         webhookURL,
			ContentType: "json",
			Secret:      secret,
			InsecureSSL: "0",
		},
	})

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var result webhookResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.ID, nil
}

// DeleteWebhook removes a webhook from the repo.
func (c *Client) DeleteWebhook(ctx context.Context, repoURL, token string, webhookID int64) error {
	owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return err
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", owner, repo, webhookID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	return nil
}

// parseRepoURL extracts owner and repo from https://github.com/owner/repo[.git].
func parseRepoURL(repoURL string) (owner, repo string, err error) {
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid GitHub repo URL: %s", repoURL)
	}
	return parts[0], parts[1], nil
}
