package scm

import (
	"encoding/csv"
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

// WriteUnusedObjectsCSV exports the "Unused Objects" view.
func WriteUnusedObjectsCSV(path string, resp *UnreferencedObjectsResponse) error {
	header := []string{"name", "object_type", "location", "days_unused", "unreferenced_since", "created", "updated"}
	rows := make([][]string, 0, len(resp.UnreferencedObjects))
	for _, o := range resp.UnreferencedObjects {
		name, location := nameAndLocation(o.Xpath)
		rows = append(rows, []string{
			name,
			humanizeType(o.Type, o.Xpath),
			humanizeLocation(location),
			daysSince(resp.CurrentTime, o.UnreferencedTimestamp),
			o.UnreferencedTimestamp,
			o.CreatedTimestamp,
			o.UpdatedTimestamp,
		})
	}
	return writeCSV(path, header, rows)
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
		"rule_name", "rule_uuid", "location", "action",
		"source", "destination", "application", "service", "url_category", "tags",
		"zero_hit_object_count", "zero_hit_objects",
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		r, ok := ruleByUUID[e.RuleUUID]
		if !ok {
			r = RuleEntry{Name: e.RuleUUID}
		}

		objects := make([]string, len(e.Objects))
		for i, o := range e.Objects {
			objects[i] = fmt.Sprintf("%s (%s)", o.ObjectName, o.ObjectType)
		}

		rows = append(rows, []string{
			r.Name,
			e.RuleUUID,
			r.Loc,
			r.Action,
			strings.Join(r.Source.Member, ";"),
			strings.Join(r.Destination.Member, ";"),
			strings.Join(r.Application.Member, ";"),
			strings.Join(r.Service.Member, ";"),
			strings.Join(r.Category.Member, ";"),
			strings.Join(r.Tag.Member, ";"),
			strconv.Itoa(len(e.Objects)),
			strings.Join(objects, "; "),
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

// WriteZeroHitPolicyRulesCSV exports the "Zero Hit Policy Rules" view. now
// is the server's current time (RFC1123, as returned in the
// unreferencedObjects response's currentTime field -- this endpoint
// doesn't return its own "now", so the caller passes one from a call made
// around the same time) and is used for the "days_with_zero_hits" column.
func WriteZeroHitPolicyRulesCSV(path string, resp *RulesResponse, now string) error {
	header := []string{
		"name", "uuid", "location", "rulebase", "days_with_zero_hits", "type", "disabled", "action",
		"source_zone", "source_address", "source_user", "source_device",
		"destination_zone", "destination_address", "destination_device",
		"access_control_allow_apps", "access_control_block_apps",
		"access_control_allow_url_categories", "access_control_block_url_categories",
		"application", "service", "url_category", "security_profiles", "tags",
		"description", "created", "updated",
	}
	rows := make([][]string, 0, len(resp.Result.Result.Entry))
	for _, r := range resp.Result.Result.Entry {
		rows = append(rows, []string{
			r.Name,
			r.UUID,
			r.Loc,
			rulebase(r),
			daysSinceCreated(now, r.CreatedTime),
			r.Type,
			r.Disabled,
			r.Action,
			strings.Join(r.From.Member, ";"),
			strings.Join(r.Source.Member, ";"),
			strings.Join(r.SourceUser.Member, ";"),
			strings.Join(r.SourceHIP.Member, ";"),
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
			strings.Join(r.Tag.Member, ";"),
			r.Description,
			r.CreatedTime,
			r.UpdatedTime,
		})
	}
	return writeCSV(path, header, rows)
}
