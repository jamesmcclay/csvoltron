package scm

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var entryNameRe = regexp.MustCompile(`entry\[@name='([^']*)'\]`)

// nameAndLocation pulls the object's own name and the name of the
// container/cloud area it lives in out of an xpath like:
//
//	/config/devices/entry[@name='localhost.localdomain']/container/entry[@name='All']/certificate/entry[@name='Forward-UnTrust-CA']
//
// giving name="Forward-UnTrust-CA", location="All". There's no separate
// "name" or "location" field in the unreferencedObjects API response, so
// this is the only way to get them. The first entry[@name=...] is always
// the root device ("localhost.localdomain"); the second is always the
// container/cloud entry the SCM UI groups the table by -- confirmed against
// a live "Unused Objects" screenshot, e.g. ".../device/cloud/entry[@name='Mobile
// Users']/..." groups under "Access Agent" (see humanizeLocation).
func nameAndLocation(xpath string) (name, location string) {
	matches := entryNameRe.FindAllStringSubmatch(xpath, -1)
	if len(matches) == 0 {
		return "", ""
	}
	name = matches[len(matches)-1][1]
	if len(matches) >= 2 {
		location = matches[1][1]
	}
	return name, location
}

// locationDisplayNames maps the raw container/cloud entry name (as it
// appears in xpaths) to the label the SCM UI actually shows for it.
// Anything not listed here is shown as-is (e.g. "Prisma Access", a specific
// remote network site name, etc. already match the raw name).
var locationDisplayNames = map[string]string{
	"All":                         "Global",
	"Mobile Users":                "Access Agent",
	"Mobile Users Explicit Proxy": "Explicit Proxy",
}

func humanizeLocation(location string) string {
	if display, ok := locationDisplayNames[location]; ok {
		return display
	}
	return location
}

// typeDisplayNames maps the raw "type" field to the UI's label, for the
// cases that aren't just title-casing the hyphenated raw value.
var typeDisplayNames = map[string]string{
	"ipsec": "IPSec Tunnel",
}

// wordDisplayNames overrides the casing of individual words when
// title-casing a hyphenated raw type, for acronyms the UI capitalizes in
// full (e.g. "custom-url-category" -> "Custom URL Category", not "Custom
// Url Category").
var wordDisplayNames = map[string]string{
	"url": "URL",
	"dns": "DNS",
}

// humanizeType turns a raw object "type" (e.g. "custom-url-category") into
// the label the SCM UI shows (e.g. "Custom URL Category"). xpath is needed
// because the API's "type" is sometimes too generic on its own -- e.g.
// type "profile" under .../network/qos/profile/... is shown as "QoS
// Profile", not just "Profile".
func humanizeType(objType, xpath string) string {
	if objType == "profile" && strings.Contains(xpath, "/qos/profile/") {
		return "QoS Profile"
	}
	if display, ok := typeDisplayNames[objType]; ok {
		return display
	}

	words := strings.Split(objType, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		if display, ok := wordDisplayNames[w]; ok {
			words[i] = display
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// daysSince computes whole days between an RFC1123 "currentTime" (as the
// unreferencedObjects API gives it, e.g. "Fri, 19 Jun 2026 06:41:36 GMT")
// and an RFC3339 timestamp, matching the SCM UI's "Days Unused" column.
// Returns "" if either can't be parsed.
func daysSince(currentTime, timestamp string) string {
	cur, err := time.Parse(time.RFC1123, currentTime)
	if err != nil {
		return ""
	}
	ts, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return ""
	}
	// Ceiling, not floor/truncation: any partial day counts as a full day,
	// confirmed against the SCM UI's own "Days Unused" values.
	diff := cur.Sub(ts)
	days := int(diff / (24 * time.Hour))
	if diff%(24*time.Hour) > 0 {
		days++
	}
	if days < 0 {
		days = 0
	}
	return strconv.Itoa(days)
}

func writeCSV(path string, header []string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// daysUnused computes the "Days Unused" value for an unreferenced object.
// Cloud Manager objects carry unreferencedTimestamp in RFC3339Nano format;
// spiffy/v1 Panorama objects omit it and carry only createdTimestamp in
// "2006-01-02 15:04:05" format, so we fall back to that.
func daysUnused(currentTime, unreferencedTimestamp, createdTimestamp string) string {
	if unreferencedTimestamp != "" {
		return daysSince(currentTime, unreferencedTimestamp)
	}
	return daysSinceCreated(currentTime, createdTimestamp)
}

// WriteUnusedObjectsCSV exports the "Unused Objects" view. panorama controls
// whether to include the Status column, which only appears in the UI for
// Panorama-sourced data.
func WriteUnusedObjectsCSV(path string, resp *UnreferencedObjectsResponse, panorama bool) error {
	header := []string{"Name", "Object Type", "Location", "Days Unused", "Unreferenced Since", "Created", "Updated"}
	if panorama {
		header = []string{"Name", "Status", "Object Type", "Location", "Days Unused", "Unreferenced Since", "Created", "Updated"}
	}
	rows := make([][]string, 0, len(resp.UnreferencedObjects))
	for _, o := range resp.UnreferencedObjects {
		name, location := nameAndLocation(o.Xpath)
		row := []string{
			name,
			humanizeType(o.Type, o.Xpath),
			humanizeLocation(location),
			daysUnused(resp.CurrentTime, o.UnreferencedTimestamp, o.CreatedTimestamp),
			o.UnreferencedTimestamp,
			o.CreatedTimestamp,
			o.UpdatedTimestamp,
		}
		if panorama {
			row = append([]string{name, panoramaRuleStatus(o.Status)}, row[1:]...)
		}
		rows = append(rows, row)
	}
	return writeCSV(path, header, rows)
}

// RulesFromZeroHitObjects builds a RulesResponse from each entry's embedded
// RuleDefinition, for Panorama-sourced ZeroHitObjects data -- which embeds
// the full rule inline instead of requiring a separate rule lookup the way
// Cloud Manager's AllSecurityRules does. Entries without a RuleDefinition
// (Cloud Manager's) are skipped; callers with Cloud Manager data already
// have a real RulesResponse from AllSecurityRules to pass to
// WriteZeroHitObjectsCSV instead of this.
func RulesFromZeroHitObjects(entries []ZeroHitObjectsEntry) *RulesResponse {
	var resp RulesResponse
	for _, e := range entries {
		if e.RuleDefinition == "" {
			continue
		}
		var r RuleEntry
		if err := json.Unmarshal([]byte(e.RuleDefinition), &r); err != nil {
			continue
		}
		resp.Result.Result.Entry = append(resp.Result.Result.Entry, r)
	}
	return &resp
}

// WriteZeroHitObjectsCSV exports the "Zero Hit Objects" view. rules is used
// to resolve each entry's rule_uuid to a human-readable rule name (the
// zeroHitObjects API only gives you the UUID).
func WriteZeroHitObjectsCSV(path string, entries []ZeroHitObjectsEntry, rules *RulesResponse) error {
	ruleByUUID := make(map[string]RuleEntry, len(rules.Result.Result.Entry))
	for _, r := range rules.Result.Result.Entry {
		ruleByUUID[r.UUID] = r
	}

	// One row per rule, matching the SCM UI's "Security Policy Rules (N)
	// with Zero Hit Objects" table -- not one row per object, which reads
	// nothing like what's on screen.
	header := []string{
		"Name", "Zero Hit Objects",
		// SOURCE group
		"Source Zone", "Source Address", "Source User",
		// DESTINATION group
		"Destination Zone", "Destination Address",
		"URL Category", "Tags", "Application", "Service",
		// Extra metadata not shown in UI but useful for auditing
		"Zero Hit Object Count", "Rule UUID", "Location", "Action",
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		r, ok := ruleByUUID[e.RuleUUID]
		if !ok {
			r = RuleEntry{Name: e.RuleUUID}
		}

		objects := make([]string, len(e.Objects))
		for i, o := range e.Objects {
			// Panorama-sourced objects don't have an ObjectType.
			if o.ObjectType == "" {
				objects[i] = o.ObjectName
			} else {
				objects[i] = fmt.Sprintf("%s (%s)", o.ObjectName, o.ObjectType)
			}
		}

		rows = append(rows, []string{
			r.Name,
			strings.Join(objects, "; "),
			strings.Join(r.From.Member, ";"),
			strings.Join(r.Source.Member, ";"),
			strings.Join(r.SourceUser.Member, ";"),
			strings.Join(r.To.Member, ";"),
			strings.Join(r.Destination.Member, ";"),
			strings.Join(r.Category.Member, ";"),
			strings.Join(r.Tag.Member, ";"),
			strings.Join(r.Application.Member, ";"),
			strings.Join(r.Service.Member, ";"),
			strconv.Itoa(len(e.Objects)),
			e.RuleUUID,
			r.Loc,
			r.Action,
		})
	}
	return writeCSV(path, header, rows)
}

// rulebase describes a rule's "Pre Rules" / "Post Rules" / "Default"
// grouping in the SCM UI: a rule has @position ("pre"/"post") if it
// belongs to a specific site/location's rulebase, or @default="yes" if
// it's a default rule shipped with a snippet -- never both, per observed
// data.
func rulebase(r RuleEntry) string {
	switch r.Position {
	case "pre":
		return "Pre"
	case "post":
		return "Post"
	}
	if r.Default == "yes" {
		return "Default"
	}
	return ""
}

// daysSinceCreated computes whole days (ceiling) between now (RFC1123) and
// a rule's @createdTime ("2026-05-11 21:03:24", implicitly UTC), matching
// the SCM UI's "Days with Zero Hits" column. The UI's own label says this
// is technically relative to SCM Pro activation, not rule creation, but
// creation time is the only bound we have visibility into, and it matched
// the UI exactly for every rule checked (i.e. these rules were all created
// after activation). Returns "" if either can't be parsed.
func daysSinceCreated(now, createdTime string) string {
	cur, err := time.Parse(time.RFC1123, now)
	if err != nil {
		return ""
	}
	ts, err := time.Parse("2006-01-02 15:04:05", createdTime)
	if err != nil {
		return ""
	}
	diff := cur.Sub(ts)
	days := int(diff / (24 * time.Hour))
	if diff%(24*time.Hour) > 0 {
		days++
	}
	if days < 0 {
		days = 0
	}
	return strconv.Itoa(days)
}

// ruleStatus converts the PAN-OS "disabled" field ("yes"/"no") to the
// human-readable label the SCM UI shows in the Status column.
func ruleStatus(disabled string) string {
	switch disabled {
	case "yes":
		return "Disabled"
	case "no":
		return "Enabled"
	default:
		return disabled
	}
}

// yesNoLabel turns PAN-OS's "yes"/"no" rule attribute values into the UI's
// "Yes"/"No" capitalization.
func yesNoLabel(v string) string {
	switch v {
	case "yes":
		return "Yes"
	case "no":
		return "No"
	default:
		return v
	}
}

// options summarizes the rule attributes the SCM UI groups under its
// "Options" column: Log Forwarding, Log at Session Start/End, and Disable
// Server Response Inspection. Only attributes actually present on the rule
// are included, matching how the API itself omits unset ones.
func options(r RuleEntry) string {
	var parts []string
	if r.LogSetting != "" {
		parts = append(parts, "Log Forwarding: "+r.LogSetting)
	}
	if r.LogStart != "" {
		parts = append(parts, "Log at Session Start: "+yesNoLabel(r.LogStart))
	}
	if r.LogEnd != "" {
		parts = append(parts, "Log at Session End: "+yesNoLabel(r.LogEnd))
	}
	if r.Option.DisableServerResponseInspection != "" {
		parts = append(parts, "Disable Server Response Inspection: "+yesNoLabel(r.Option.DisableServerResponseInspection))
	}
	return strings.Join(parts, "; ")
}

// panoramaRuleStatus maps the spiffy/v1 unusedPolicies @status field to the
// label the SCM UI shows in the Status column. Empty string is the default
// "awaiting review" state, which the UI renders as "Pending Review".
func panoramaRuleStatus(s string) string {
	if s == "" {
		return "Pending Review"
	}
	return s
}

// daysSincePanoramaTime computes whole days (ceiling) between two timestamps
// both in "2006-01-02 15:04:05" format, as used by spiffy/v1 unusedPolicies
// @currentTime and @createdTime fields.
func daysSincePanoramaTime(now, createdTime string) string {
	const layout = "2006-01-02 15:04:05"
	cur, err := time.Parse(layout, now)
	if err != nil {
		return ""
	}
	ts, err := time.Parse(layout, createdTime)
	if err != nil {
		return ""
	}
	diff := cur.Sub(ts)
	days := int(diff / (24 * time.Hour))
	if diff%(24*time.Hour) > 0 {
		days++
	}
	if days < 0 {
		days = 0
	}
	return strconv.Itoa(days)
}

// WriteUnusedPoliciesCSV exports the "Zero Hit Policy Rules" view for
// Panorama-sourced data from PanoramaClient.UnusedPolicies.
func WriteUnusedPoliciesCSV(path string, resp *UnusedPoliciesResponse) error {
	now := resp.Result.CurrentTime
	header := []string{
		"Name", "Status", "Days with Zero Hits", "Action",
		"Source Zone", "Source Address", "User", "Source Device",
		"Subscriber", "Equipment", "Network Container",
		"Destination Zone", "Destination Address", "Destination Device",
		"Allow Applications", "Block Applications",
		"Allow URL Categories", "Block URL Categories",
		"Application", "Service", "URL Category", "Security Profiles",
		"Options", "Tag", "Description",
		"UUID", "Location", "Rulebase", "Type", "Created", "Updated",
	}
	rows := make([][]string, 0, len(resp.Result.Result.Entry))
	for _, r := range resp.Result.Result.Entry {
		rows = append(rows, []string{
			r.Name,
			panoramaRuleStatus(r.ReviewStatus),
			daysSincePanoramaTime(now, r.CreatedTime),
			r.Action,
			strings.Join(r.From.Member, ";"),
			strings.Join(r.Source.Member, ";"),
			strings.Join(r.SourceUser.Member, ";"),
			strings.Join(r.SourceHIP.Member, ";"),
			strings.Join(r.Subscriber.Member, ";"),
			strings.Join(r.Equipment.Member, ";"),
			strings.Join(r.NwContainer.Member, ";"),
			strings.Join(r.To.Member, ";"),
			strings.Join(r.Destination.Member, ";"),
			strings.Join(r.DestinationHIP.Member, ";"),
			strings.Join(r.AllowWebApplication.names(), ";"),
			strings.Join(r.BlockWebApplication.names(), ";"),
			strings.Join(r.AllowURLCategory.names(), ";"),
			strings.Join(r.BlockURLCategory.names(), ";"),
			strings.Join(r.Application.Member, ";"),
			strings.Join(r.Service.Member, ";"),
			strings.Join(r.Category.Member, ";"),
			strings.Join(r.ProfileSetting.Group.Member, ";"),
			options(r),
			strings.Join(r.Tag.Member, ";"),
			r.Description,
			r.UUID,
			r.Loc,
			rulebase(r),
			r.Type,
			r.CreatedTime,
			r.UpdatedTime,
		})
	}
	return writeCSV(path, header, rows)
}

// WriteZeroHitPolicyRulesCSV exports the "Zero Hit Policy Rules" view for
// Cloud Manager. now is the server's current time (RFC1123, as returned in
// the unreferencedObjects response's currentTime field) used for "Days with
// Zero Hits". No Status column -- it is not shown in the Cloud Manager UI.
func WriteZeroHitPolicyRulesCSV(path string, resp *RulesResponse, now string) error {
	header := []string{
		"Name", "Days with Zero Hits", "Action",
		// Source columns (match UI's "SOURCE" group header)
		"Source Zone", "Source Address", "User", "Source Device",
		"Subscriber", "Equipment", "Network Container",
		// Destination columns (match UI's "DESTINATION" group header)
		"Destination Zone", "Destination Address", "Destination Device",
		// SWG / Access Control columns (shown as "Access C..." in UI)
		"Allow Applications", "Block Applications",
		"Allow URL Categories", "Block URL Categories",
		// Remaining UI columns
		"Application", "Service", "URL Category", "Security Profiles",
		"Options", "Tag", "Description",
		// Extra metadata not visible in UI but useful for auditing
		"UUID", "Location", "Rulebase", "Type", "Disabled", "Created", "Updated",
	}
	rows := make([][]string, 0, len(resp.Result.Result.Entry))
	for _, r := range resp.Result.Result.Entry {
		rows = append(rows, []string{
			r.Name,
			daysSinceCreated(now, r.CreatedTime),
			r.Action,
			strings.Join(r.From.Member, ";"),
			strings.Join(r.Source.Member, ";"),
			strings.Join(r.SourceUser.Member, ";"),
			strings.Join(r.SourceHIP.Member, ";"),
			strings.Join(r.Subscriber.Member, ";"),
			strings.Join(r.Equipment.Member, ";"),
			strings.Join(r.NwContainer.Member, ";"),
			strings.Join(r.To.Member, ";"),
			strings.Join(r.Destination.Member, ";"),
			strings.Join(r.DestinationHIP.Member, ";"),
			strings.Join(r.AllowWebApplication.names(), ";"),
			strings.Join(r.BlockWebApplication.names(), ";"),
			strings.Join(r.AllowURLCategory.names(), ";"),
			strings.Join(r.BlockURLCategory.names(), ";"),
			strings.Join(r.Application.Member, ";"),
			strings.Join(r.Service.Member, ";"),
			strings.Join(r.Category.Member, ";"),
			strings.Join(r.ProfileSetting.Group.Member, ";"),
			options(r),
			strings.Join(r.Tag.Member, ";"),
			r.Description,
			r.UUID,
			r.Loc,
			rulebase(r),
			r.Type,
			r.Disabled,
			r.CreatedTime,
			r.UpdatedTime,
		})
	}
	return writeCSV(path, header, rows)
}
