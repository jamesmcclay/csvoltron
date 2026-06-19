// Package scm talks to Strata Cloud Manager's internal Optimize API -- the
// one behind https://stratacloudmanager.paloaltonetworks.com/manage/operation/optimize.
// It isn't part of the public PAN-OS / SCM API; the shapes here were reverse
// engineered from the browser's own network traffic (see cmd/discover).
package scm

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
}

// ZeroHitObject is one element of the top-level array returned by
// GET /api/config/v9.2/object/zeroHitObjects -- backs the "Zero Hit
// Objects" view. Each element groups the zero-hit objects referenced by one
// policy rule.
type ZeroHitObjectsEntry struct {
	RuleUUID string               `json:"rule_uuid"`
	Count    string               `json:"count"`
	Objects  []ZeroHitObjectEntry `json:"objects"`
}

type ZeroHitObjectEntry struct {
	ObjectID     string `json:"object_id"`
	ObjectName   string `json:"object_name"`
	ObjectType   string `json:"object_type"`
	DaysSinceHit int    `json:"days_since_hit"`
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
	Position       string     `json:"@position"`
	Default        string     `json:"@default"`
	CreatedTime    string     `json:"@createdTime"`
	UpdatedTime    string     `json:"@updatedTime"`
	Disabled       string     `json:"disabled"`
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
