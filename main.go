// Command csvoltron logs into Strata Cloud Manager (via a real, visible
// browser window, since SSO + MFA can't be scripted headlessly) and exports
// Config Cleanup data -- Unused Objects, Zero Hit Objects, and (where
// available) Zero Hit Policy Rules -- to CSV files, for Cloud Manager and
// every Panorama appliance connected to the tenant.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jamesmcclay/csvoltron/internal/auth"
	"github.com/jamesmcclay/csvoltron/internal/scm"
)

func main() {
	startURL := flag.String("start-url", "https://stratacloudmanager.paloaltonetworks.com/manage/operation/optimize", "URL to open first (will redirect through SSO login if not already authenticated)")
	outDir := flag.String("out-dir", "./csv_output", "directory to write CSV files to")
	profileDir := flag.String("profile-dir", "./.csvoltron-chrome-profile", "persistent Chrome user-data-dir, so a logged-in session can be reused across runs")
	loginTimeout := flag.Duration("login-timeout", 5*time.Minute, "how long to wait for login + navigation to the Optimize page before giving up")
	source := flag.String("source", "", "only export from the data source whose hostname contains this text, case-insensitive (default: export from Cloud Manager and every connected Panorama)")
	flag.Parse()

	if err := run(*startURL, *outDir, *profileDir, portableChromePath(), *source, *loginTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// portableChromePath returns the path to the portable Chromium downloaded
// by the installer (see README), a sibling of the repo checkout that
// go run . is invoked from. Returns "" -- falling back to system Chrome --
// if there's no portable Chromium for this OS or none was downloaded.
func portableChromePath() string {
	var rel string
	switch runtime.GOOS {
	case "windows":
		rel = filepath.Join("..", "chromium", "chrome-win", "chrome.exe")
	case "darwin":
		rel = filepath.Join("..", "chromium", "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium")
	default:
		return ""
	}
	if _, err := os.Stat(rel); err != nil {
		return ""
	}
	return rel
}

func run(startURL, outDir, profileDir, chromeExecPath, source string, loginTimeout time.Duration) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	creds, err := auth.Login(context.Background(), startURL, profileDir, chromeExecPath, loginTimeout)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	panoramaClient := &scm.PanoramaClient{IAMToken: creds.IAMToken}
	managers, err := panoramaClient.Managers()
	if err != nil {
		return fmt.Errorf("list data sources (Cloud Manager / Panorama): %w", err)
	}

	if source != "" {
		managers, err = filterManagers(managers, source)
		if err != nil {
			return err
		}
	}

	cloudClient := &scm.Client{Host: creds.Host, JWT: creds.JWT}
	date := time.Now().Format("2006-01-02_15-04-05")

	var failedSources []string
	for _, m := range managers {
		fmt.Printf("\n=== %s ===\n", m.Hostname)
		var exportErr error
		if m.IsCloudManager() {
			exportErr = exportCloudManager(cloudClient, m, outDir, date)
		} else {
			exportErr = exportPanorama(panoramaClient, m, outDir, date)
		}
		if exportErr != nil {
			fmt.Fprintf(os.Stderr, "error exporting %s: %v\n", m.Hostname, exportErr)
			failedSources = append(failedSources, m.Hostname)
		}
	}

	if len(failedSources) > 0 {
		return fmt.Errorf("failed to export from: %s", strings.Join(failedSources, ", "))
	}
	return nil
}

// filterManagers narrows managers down to the ones whose hostname contains
// source, case-insensitive. Errors out (listing what's actually available)
// if that matches nothing.
func filterManagers(managers []scm.Manager, source string) ([]scm.Manager, error) {
	var matched []scm.Manager
	var available []string
	for _, m := range managers {
		available = append(available, m.Hostname)
		if strings.Contains(strings.ToLower(m.Hostname), strings.ToLower(source)) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("-source %q matches no data source; available: %s", source, strings.Join(available, ", "))
	}
	return matched, nil
}

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// sourceSlug turns a manager hostname (e.g. "Cloud Manager",
// "Panorama-HA-1") into a safe CSV filename fragment.
func sourceSlug(hostname string) string {
	return strings.Trim(unsafeFilenameChars.ReplaceAllString(strings.ToLower(hostname), "-"), "-")
}

// exportCloudManager exports all 3 views from Cloud Manager itself --
// the only data source before Panorama support was added.
func exportCloudManager(client *scm.Client, m scm.Manager, outDir, date string) error {
	slug := sourceSlug(m.Hostname)

	fmt.Println("Fetching Unused Objects...")
	unreferenced, err := client.UnreferencedObjects()
	if err != nil {
		return fmt.Errorf("fetch unused objects: %w", err)
	}
	unusedObjectsPath := filepath.Join(outDir, fmt.Sprintf("unused_objects_%s_%s.csv", slug, date))
	if err := scm.WriteUnusedObjectsCSV(unusedObjectsPath, unreferenced, false); err != nil {
		return fmt.Errorf("write %s: %w", unusedObjectsPath, err)
	}
	fmt.Printf("Wrote %d row(s) -> %s\n", len(unreferenced.UnreferencedObjects), unusedObjectsPath)

	fmt.Println("Fetching Zero Hit Objects...")
	zeroHitObjects, err := client.ZeroHitObjects()
	if err != nil {
		return fmt.Errorf("fetch zero hit objects: %w", err)
	}
	// zeroHitObjects only gives us rule UUIDs; AllSecurityRules is needed to
	// resolve those to human-readable rule names for the CSV.
	fmt.Println("Fetching rule names for Zero Hit Objects...")
	allRules, err := client.AllSecurityRules()
	if err != nil {
		return fmt.Errorf("fetch all security rules: %w", err)
	}
	zeroHitObjectsPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_objects_%s_%s.csv", slug, date))
	if err := scm.WriteZeroHitObjectsCSV(zeroHitObjectsPath, zeroHitObjects, allRules); err != nil {
		return fmt.Errorf("write %s: %w", zeroHitObjectsPath, err)
	}
	fmt.Printf("Wrote %d rule(s) -> %s\n", len(zeroHitObjects), zeroHitObjectsPath)

	fmt.Println("Fetching Zero Hit Policy Rules...")
	unusedRules, err := client.UnusedRules()
	if err != nil {
		return fmt.Errorf("fetch zero hit policy rules: %w", err)
	}
	zeroHitRulesPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_policy_rules_%s_%s.csv", slug, date))
	if err := scm.WriteZeroHitPolicyRulesCSV(zeroHitRulesPath, unusedRules); err != nil {
		return fmt.Errorf("write %s: %w", zeroHitRulesPath, err)
	}
	fmt.Printf("Wrote %d rule(s) -> %s\n", len(unusedRules.Result.Result.Entry), zeroHitRulesPath)

	return nil
}

// exportPanorama exports what's available for a Panorama-sourced manager:
// Unused Objects and Zero Hit Objects. Zero Hit Policy Rules is checked but
// not written -- csvoltron doesn't have a parser for that endpoint's
// response yet, since no real sample with actual data has ever been
// captured (every Panorama tested so far had none to give).
func exportPanorama(client *scm.PanoramaClient, m scm.Manager, outDir, date string) error {
	slug := sourceSlug(m.Hostname)

	fmt.Println("Fetching Unused Objects...")
	unreferenced, err := client.UnreferencedObjects(m)
	if err != nil {
		return fmt.Errorf("fetch unused objects: %w", err)
	}
	unusedObjectsPath := filepath.Join(outDir, fmt.Sprintf("unused_objects_%s_%s.csv", slug, date))
	if err := scm.WriteUnusedObjectsCSV(unusedObjectsPath, unreferenced, true); err != nil {
		return fmt.Errorf("write %s: %w", unusedObjectsPath, err)
	}
	fmt.Printf("Wrote %d row(s) -> %s\n", len(unreferenced.UnreferencedObjects), unusedObjectsPath)

	fmt.Println("Fetching Zero Hit Objects...")
	zeroHitObjects, err := client.ZeroHitObjects(m)
	if err != nil {
		return fmt.Errorf("fetch zero hit objects: %w", err)
	}
	rules := scm.RulesFromZeroHitObjects(zeroHitObjects)
	zeroHitObjectsPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_objects_%s_%s.csv", slug, date))
	if err := scm.WriteZeroHitObjectsCSV(zeroHitObjectsPath, zeroHitObjects, rules); err != nil {
		return fmt.Errorf("write %s: %w", zeroHitObjectsPath, err)
	}
	fmt.Printf("Wrote %d rule(s) -> %s\n", len(zeroHitObjects), zeroHitObjectsPath)

	fmt.Println("Fetching Zero Hit Policy Rules...")
	policies, ok, err := client.UnusedPolicies(m)
	if err != nil {
		return fmt.Errorf("fetch zero hit policy rules: %w", err)
	}
	if !ok {
		fmt.Println("No Zero Hit Policy Rules analysis available yet for this source -- skipped.")
	} else {
		zeroHitRulesPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_policy_rules_%s_%s.csv", slug, date))
		if err := scm.WriteUnusedPoliciesCSV(zeroHitRulesPath, policies); err != nil {
			return fmt.Errorf("write %s: %w", zeroHitRulesPath, err)
		}
		fmt.Printf("Wrote %d rule(s) -> %s\n", len(policies.Result.Result.Entry), zeroHitRulesPath)
	}

	return nil
}
