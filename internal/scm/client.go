package scm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// panoramaHost is fixed (not tenant-specific like Client.Host) -- every
// tenant's Cloud Manager talks to the same host for Panorama-sourced data.
const panoramaHost = "api-prod.us.secure-policy.cloudmgmt.paloaltonetworks.com"

// PanoramaClient calls the same kind of Config Cleanup data as Client, but
// for data sourced from the Config Cleanup page's "Panorama" dropdown
// option(s) instead of Cloud Manager itself -- a different host and a
// different bearer token (the OIDC access token, not the short-lived SCM
// JWT).
type PanoramaClient struct {
	// IAMToken is the OAuth access token (the x-auth-iamtoken header),
	// captured by auth.Login alongside the regular JWT.
	IAMToken string

	HTTPClient *http.Client
}

func (c *PanoramaClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// get performs a GET against the spiffy/v1 API and decodes a 200 response
// into out. ok is false (with no error) on a 204 -- not every manager has
// analysis results for every view yet, e.g. a freshly connected Panorama.
func (c *PanoramaClient) get(path string, m Manager, out interface{}) (ok bool, err error) {
	reqURL := fmt.Sprintf("https://%s%s", panoramaHost, path)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("x-auth-iamtoken", c.IAMToken)
	req.Header.Set("x-source-app", "buckbeak")
	if m.SecondarySerialID != "" {
		req.Header.Set("x-passive-peer-serial", m.SecondarySerialID)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return false, nil
	default:
		return false, fmt.Errorf("GET %s: unexpected status %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("GET %s: decode response: %w", path, err)
	}
	return true, nil
}

// Managers lists Cloud Manager and every Panorama appliance connected to
// this tenant -- the options behind the Config Cleanup page's source
// dropdown.
func (c *PanoramaClient) Managers() ([]Manager, error) {
	var out []Manager
	if _, err := c.get("/spiffy/v1/panmetadata", Manager{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func managerQuery(m Manager) string {
	return fmt.Sprintf("?instance_id=%s&pan_id=%d", url.QueryEscape(m.SerialID), m.ID)
}

// UnreferencedObjects fetches m's "Unused Objects" data. m must be a
// Panorama manager, not Cloud Manager (use Client for that).
func (c *PanoramaClient) UnreferencedObjects(m Manager) (*UnreferencedObjectsResponse, error) {
	var out UnreferencedObjectsResponse
	if _, err := c.get("/spiffy/v1/unreferencedObjects"+managerQuery(m), m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ZeroHitObjects fetches m's "Zero Hit Objects" data. m must be a Panorama
// manager, not Cloud Manager (use Client for that).
func (c *PanoramaClient) ZeroHitObjects(m Manager) ([]ZeroHitObjectsEntry, error) {
	var wrapped struct {
		Data []ZeroHitObjectsEntry `json:"data"`
	}
	if _, err := c.get("/spiffy/v1/zeroHitObjects"+managerQuery(m), m, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Data, nil
}

// UnusedPolicies fetches m's "Zero Hit Policy Rules" data, if any has been
// computed yet -- ok is false (nil, false, nil) if this manager has none
// yet (HTTP 204), which is common right after a Panorama is first connected.
func (c *PanoramaClient) UnusedPolicies(m Manager) (*UnusedPoliciesResponse, bool, error) {
	var out UnusedPoliciesResponse
	ok, err := c.get("/spiffy/v1/unusedPolicies"+managerQuery(m), m, &out)
	if !ok {
		return nil, false, err
	}
	return &out, true, err
}
