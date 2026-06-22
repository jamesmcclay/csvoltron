// Command csvoltron logs into Strata Cloud Manager (via a real, visible
// browser window, since SSO + MFA can't be scripted headlessly) and exports
// the Optimize page's 3 views -- Unused Objects, Zero Hit Objects, Zero Hit
// Policy Rules -- to CSV files.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jamesmcclay/csvoltron/internal/auth"
	"github.com/jamesmcclay/csvoltron/internal/scm"
)

func main() {
	startURL := flag.String("start-url", "https://stratacloudmanager.paloaltonetworks.com/manage/operation/optimize", "URL to open first (will redirect through SSO login if not already authenticated)")
	outDir := flag.String("out-dir", "./csv_output", "directory to write the 3 CSV files to")
	profileDir := flag.String("profile-dir", "./.csvoltron-chrome-profile", "persistent Chrome user-data-dir, so a logged-in session can be reused across runs")
	loginTimeout := flag.Duration("login-timeout", 5*time.Minute, "how long to wait for login + navigation to the Optimize page before giving up")
	portable := flag.Bool("portable", false, "use the portable Chromium downloaded by the installer instead of system Chrome (for managed Chrome installs that block DevTools/remote debugging)")
	flag.Parse()

	chromeExecPath := ""
	if *portable {
		chromeExecPath = filepath.Join("..", "chromium", "chrome-win", "chrome.exe")
	}

	if err := run(*startURL, *outDir, *profileDir, chromeExecPath, *loginTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(startURL, outDir, profileDir, chromeExecPath string, loginTimeout time.Duration) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	creds, err := auth.Login(context.Background(), startURL, profileDir, chromeExecPath, loginTimeout)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	client := &scm.Client{Host: creds.Host, JWT: creds.JWT}

	fmt.Println("Fetching Unused Objects...")
	unreferenced, err := client.UnreferencedObjects()
	if err != nil {
		return fmt.Errorf("fetch unused objects: %w", err)
	}

	fmt.Println("Fetching Zero Hit Objects...")
	zeroHitObjects, err := client.ZeroHitObjects()
	if err != nil {
		return fmt.Errorf("fetch zero hit objects: %w", err)
	}

	fmt.Println("Fetching Zero Hit Policy Rules...")
	unusedRules, err := client.UnusedRules()
	if err != nil {
		return fmt.Errorf("fetch zero hit policy rules: %w", err)
	}

	// zeroHitObjects only gives us rule UUIDs; AllSecurityRules is needed to
	// resolve those to human-readable rule names for the CSV.
	fmt.Println("Fetching rule names for Zero Hit Objects...")
	allRules, err := client.AllSecurityRules()
	if err != nil {
		return fmt.Errorf("fetch all security rules: %w", err)
	}

	date := time.Now().Format("2006-01-02_15-04-05")

	unusedObjectsPath := filepath.Join(outDir, fmt.Sprintf("unused_objects_%s.csv", date))
	if err := scm.WriteUnusedObjectsCSV(unusedObjectsPath, unreferenced); err != nil {
		return fmt.Errorf("write %s: %w", unusedObjectsPath, err)
	}
	fmt.Printf("Wrote %d row(s) -> %s\n", len(unreferenced.UnreferencedObjects), unusedObjectsPath)

	zeroHitObjectsPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_objects_%s.csv", date))
	if err := scm.WriteZeroHitObjectsCSV(zeroHitObjectsPath, zeroHitObjects, allRules); err != nil {
		return fmt.Errorf("write %s: %w", zeroHitObjectsPath, err)
	}
	fmt.Printf("Wrote %d rule(s) -> %s\n", len(zeroHitObjects), zeroHitObjectsPath)

	zeroHitRulesPath := filepath.Join(outDir, fmt.Sprintf("zero_hit_policy_rules_%s.csv", date))
	if err := scm.WriteZeroHitPolicyRulesCSV(zeroHitRulesPath, unusedRules, unreferenced.CurrentTime); err != nil {
		return fmt.Errorf("write %s: %w", zeroHitRulesPath, err)
	}
	fmt.Printf("Wrote %d rule(s) -> %s\n", len(unusedRules.Result.Result.Entry), zeroHitRulesPath)

	return nil
}
