// Package scm talks to Strata Cloud Manager's internal Optimize API -- the
// one behind https://stratacloudmanager.paloaltonetworks.com/manage/operation/optimize.
// It isn't part of the public PAN-OS / SCM API; the shapes here were reverse
// engineered from the browser's own network traffic (see cmd/discover).
package scm

import "encoding/json"

// UnreferencedObjectsResponse is the body of
// GET /api/config/v9.2/object/unreferencedObjects -- backs the "Unused
// Objects" view.
type UnreferencedObjectsResponse struct {
	UnreferencedObjects          []UnreferencedObject `json:"unreferencedObjects"`
	TotalUnreferencedObjects     int                  `json:"totalUnreferencedObjects"`
	Last7daysUnreferencedObjects int                  `json:"last7daysUnreferencedObjects"`
	// CurrentTime is the server's clock at analysis time (RFC1123, e.g.
	// "Fri, 19 Jun 2026 06:41:36 GMT"), used to compute "days unused" the
	// same way the SCM UI does: relative to this, not to our own clock.
	CurrentTime string `json:"currentTime"`
}

type UnreferencedObject struct {
	ID                    string `json:"id"`
	Xpath                 string `json:"xpath"`
	Type                  string `json:"type"`
	HitCount              int    `json:"hitCount"`
	UnreferencedTimestamp string `json:"unreferencedTimestamp"`
	CreatedTimestamp      string `json:"createdTimestamp"`
	UpdatedTimestamp      string `json:"updatedTimestamp"`
	// Status is present on Panorama-sourced objects; Cloud Manager omits it.
	Status string `json:"status"`
}

// ZeroHitObject is one element of the top-level array returned by
// GET /api/config/v9.2/object/zeroHitObjects -- backs the "Zero Hit
// Objects" view. Each element groups the zero-hit objects referenced by one
// policy rule.
//
// Panorama-sourced data (see PanoramaClient) reuses this same struct: it
// adds RuleDefinition (the rule embedded inline, instead of a separate
// rule lookup) and gives each object HitTimestamp/TimeStamp instead of
// ObjectType/DaysSinceHit. Fields the current source doesn't provide are
// just left zero-valued.
type ZeroHitObjectsEntry struct {
	RuleUUID string               `json:"rule_uuid"`
	Count    flexString           `json:"count"`
	Objects  []ZeroHitObjectEntry `json:"objects"`
	// RuleDefinition is the rule this entry belongs to, JSON-encoded as a
	// string in the same shape as RuleEntry (Panorama-sourced data only).
	RuleDefinition string `json:"rule_definition"`
}

type ZeroHitObjectEntry struct {
	ObjectID     string `json:"object_id"`
	ObjectName   string `json:"object_name"`
	ObjectType   string `json:"object_type"`
	DaysSinceHit int    `json:"days_since_hit"`
	// HitTimestamp/TimeStamp are what Panorama-sourced data gives instead
	// of ObjectType/DaysSinceHit.
	HitTimestamp string `json:"hit_timestamp"`
	TimeStamp    string `json:"time_stamp"`
}

// flexString unmarshals from either a JSON string or a JSON number into a
// Go string -- Cloud Manager's zeroHitObjects API returns "count" as a
// quoted string, Panorama's returns it as a bare number.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = flexString(str)
		return nil
	}
	*s = flexString(b)
	return nil
}

// RulesResponse is the body of both GET /api/config/v9.2/Policies/AllSecurityRules
// and GET /api/config/v9.2/UnusedRules (the latter backs the "Zero Hit
// Policy Rules" view; it's the same PAN-OS rule schema, server-side
// pre-filtered to rules with zero hits).
type RulesResponse struct {
	Result struct {
		Result struct {
			Entry []RuleEntry `json:"entry"`
		} `json:"result"`
	} `json:"result"`
}

type RuleEntry struct {
	Loc  string `json:"@loc"`
	Name string `json:"@name"`
	Type string `json:"@type"`
	UUID string `json:"@uuid"`
	// Position is "pre" or "post" (pre-rulebase / post-rulebase), present
	// on rules belonging to a specific site/location. Default (a rule that
	// ships with a snippet) and Position are mutually exclusive in
	// practice -- a rule has one or the other, never both.
	Position      string `json:"@position"`
	Default       string `json:"@default"`
	CreatedTime   string `json:"@createdTime"`
	UpdatedTime   string `json:"@updatedTime"`
	HitTimestamp  string `json:"@hitTimestamp"`
	Disabled      string `json:"disabled"`
	Description    string     `json:"description"`
	From           memberList `json:"from"`
	Source         memberList `json:"source"`
	SourceUser     memberList `json:"source-user"`
	SourceHIP      memberList `json:"source-hip"` // Source "Device" column in the UI
	To             memberList `json:"to"`
	Destination    memberList `json:"destination"`
	DestinationHIP memberList `json:"destination-hip"` // Destination "Device" column in the UI
	Application    memberList `json:"application"`
	Service        memberList `json:"service"`
	Category       memberList `json:"category"` // URL category
	Tag            memberList `json:"tag"`
	Action         string     `json:"action"`
	ProfileSetting struct {
		Group memberList `json:"group"`
	} `json:"profile-setting"` // "Security Profiles" column in the UI

	// SWG (Secure Web Gateway / Explicit Proxy) rules use these allow/block
	// lists instead of Application+Category -- the UI's "Access Control"
	// column. Regular NGFW rules don't set these.
	AllowWebApplication namedEntryList `json:"allow-web-application"`
	BlockWebApplication namedEntryList `json:"block-web-application"`
	AllowURLCategory    namedEntryList `json:"allow-url-category"`
	BlockURLCategory    namedEntryList `json:"block-url-category"`

	// SD-WAN source matching criteria (omitted by non-SD-WAN rules).
	Subscriber  memberList `json:"subscriber"`
	Equipment   memberList `json:"equipment"`
	NwContainer memberList `json:"nw-container"` // "Network Container" column in the UI

	// SaaS matching criteria (omitted by non-SaaS rules).
	SaasTenantList memberList `json:"saas-tenant-list"`
	SaasUserList   memberList `json:"saas-user-list"`

	// ReviewStatus is the Config Cleanup review-workflow state shown in the
	// "Status" column for Panorama spiffy/v1 unusedPolicies data
	// (stored as "@status"). Empty string means the UI shows "Pending Review".
	ReviewStatus string `json:"@status"`

	// These back the "Options" column in the UI. Like everything else here,
	// PAN-OS omits the field entirely rather than including a default value
	// when it hasn't been explicitly set on the rule.
	LogStart   string `json:"log-start"`
	LogEnd     string `json:"log-end"`
	LogSetting string `json:"log-setting"` // Log Forwarding profile name
	Option     struct {
		DisableServerResponseInspection string `json:"disable-server-response-inspection"`
	} `json:"option"`
}

// Manager is one entry of the array returned by
// GET https://api-prod.us.secure-policy.cloudmgmt.paloaltonetworks.com/spiffy/v1/panmetadata
// -- the data source(s) behind the "Cloud Manager" / "Panorama" dropdown
// next to the Config Cleanup page title. Cloud Manager itself is always
// included as one entry (ModelNo "cloud_mgmt"); every Panorama appliance
// connected to this tenant is another entry (ModelNo "Panorama").
type Manager struct {
	// ID is the "pan_id" query param used on PanoramaClient's spiffy/v1
	// calls for this manager.
	ID int `json:"id"`
	// SerialID is the "instance_id" query param used on PanoramaClient's
	// spiffy/v1 calls for this manager.
	SerialID string `json:"serialid"`
	// SecondarySerialID, if set, is the HA peer's serial number -- sent as
	// the x-passive-peer-serial header on PanoramaClient requests.
	SecondarySerialID string `json:"secondary_serialid"`
	ModelNo           string `json:"modelno"`
	Hostname          string `json:"hostname"`
}

// IsCloudManager reports whether this entry is Cloud Manager itself, as
// opposed to a connected Panorama appliance.
func (m Manager) IsCloudManager() bool {
	return m.ModelNo == "cloud_mgmt"
}

// UnusedPoliciesResponse is the body of
// GET .../spiffy/v1/unusedPolicies -- backs the "Zero Hit Policy Rules"
// view for Panorama-sourced data.
type UnusedPoliciesResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		CurrentTime                    string `json:"@currentTime"`
		LastAnalysisTime               string `json:"@lastAnalysisTime"`
		PreviousSuccessfulAnalysisTime string `json:"@previousSuccessfulAnalysisTime"`
		Result                         struct {
			Entry []RuleEntry `json:"entry"`
		} `json:"result"`
		Total int `json:"total"`
	} `json:"result"`
	ConfigID string `json:"configId"`
}

type memberList struct {
	Member []string `json:"member"`
}

type namedEntryList struct {
	Entry []struct {
		Name string `json:"@name"`
	} `json:"entry"`
}

func (l namedEntryList) names() []string {
	names := make([]string, len(l.Entry))
	for i, e := range l.Entry {
		names[i] = e.Name
	}
	return names
}
