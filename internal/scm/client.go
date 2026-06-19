package scm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Client calls the Optimize API endpoints directly, authenticating with the
// short-lived bearer JWT the SCM web app itself uses (passed in the
// x-auth-jwt header, not a cookie). The JWT expires ~15 minutes after
// login, so a Client is meant to be used immediately after Login, not
// persisted across runs.
type Client struct {
	// Host is the tenant-specific API host, e.g.
	// "paas-16.prod.panorama.paloaltonetworks.com".
	Host string
	// JWT is the value of the x-auth-jwt header.
	JWT string

	HTTPClient *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) get(path string, out interface{}) error {
	url := fmt.Sprintf("https://%s%s", c.Host, path)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-auth-jwt", c.JWT)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode response: %w", path, err)
	}
	return nil
}

func (c *Client) UnreferencedObjects() (*UnreferencedObjectsResponse, error) {
	var out UnreferencedObjectsResponse
	if err := c.get("/api/config/v9.2/object/unreferencedObjects", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ZeroHitObjects() ([]ZeroHitObjectsEntry, error) {
	var out []ZeroHitObjectsEntry
	if err := c.get("/api/config/v9.2/object/zeroHitObjects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) UnusedRules() (*RulesResponse, error) {
	var out RulesResponse
	if err := c.get("/api/config/v9.2/UnusedRules", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) AllSecurityRules() (*RulesResponse, error) {
	var out RulesResponse
	if err := c.get("/api/config/v9.2/Policies/AllSecurityRules", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
