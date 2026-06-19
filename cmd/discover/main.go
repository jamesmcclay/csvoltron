// Command discover drives a real, visible Chrome window so a human can
// complete Strata Cloud Manager's SSO + MFA login by hand, then records the
// XHR/fetch network traffic the page makes. The captured traffic is what we
// use to reverse-engineer the Optimize page's internal API (it isn't part of
// the public PAN-OS / SCM API), without needing to read browser devtools
// directly.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/jamesmcclay/csvoltron/internal/capture"
)

func main() {
	startURL := flag.String("start-url", "https://stratacloudmanager.paloaltonetworks.com/manage/operation/optimize", "URL to open first (will redirect through SSO login if not already authenticated)")
	outDir := flag.String("out-dir", "./capture-output", "directory to write captured traffic and cookies to")
	domainFilter := flag.String("domain-filter", "paloaltonetworks.com", "only capture requests whose URL host contains this substring (empty = capture everything)")
	profileDir := flag.String("profile-dir", "./capture-output/chrome-profile", "persistent Chrome user-data-dir, so a logged-in session can be reused across runs")
	doneFile := flag.String("done-file", "", "path to poll for; once it exists, capture & exit (default: <out-dir>/DONE). Lets another process signal completion instead of pressing Enter.")
	flag.Parse()

	if *doneFile == "" {
		*doneFile = filepath.Join(*outDir, "DONE")
	}

	if err := run(*startURL, *outDir, *domainFilter, *profileDir, *doneFile); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(startURL, outDir, domainFilter, profileDir, doneFile string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	// Stale DONE file from a previous run would cause an immediate exit.
	os.Remove(doneFile)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.UserDataDir(profileDir),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	rec := capture.NewRecorder(domainFilter)

	if err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			rec.Attach(ctx)
			return nil
		}),
		chromedp.Navigate(startURL),
	); err != nil {
		return fmt.Errorf("launch/navigate: %w", err)
	}

	// SSO/MFA flows often pop up a separate window (e.g. an IdP credential
	// prompt or a verification step) rather than redirecting the original
	// tab. If we only watch the tab we navigated, all traffic in a popup --
	// and potentially the rest of the session, if the user keeps using that
	// new tab/window -- would be invisible to us. So watch for any new
	// "page" target for the lifetime of the run and attach the same
	// recorder to it too.
	attachToNewTabs(ctx, rec)

	fmt.Println("A Chrome window has opened.")
	fmt.Println("1. Complete SSO login + MFA by hand.")
	fmt.Println("2. Navigate to the Optimize page if you're not redirected there automatically:")
	fmt.Println("   " + startURL)
	fmt.Println("3. Click through all 3 views (Unused Objects, Zero Hit Objects, Zero Hit Policy Rules),")
	fmt.Println("   pausing a couple seconds on each so its data finishes loading.")
	fmt.Println("(If your login flow opens a separate popup/window, that's fine -- it'll be watched too.)")
	fmt.Println()
	fmt.Printf("Then either press Enter here, or create the file %q, to capture & exit.\n", doneFile)

	waitForDone(doneFile, func(n int) {
		fmt.Printf("...captured %d request(s) so far\n", n)
	}, rec)

	entries := rec.Entries()
	trafficPath := filepath.Join(outDir, "traffic.json")
	if err := writeJSON(trafficPath, entries); err != nil {
		return fmt.Errorf("write traffic: %w", err)
	}
	fmt.Printf("Captured %d XHR/fetch request(s) -> %s\n", len(entries), trafficPath)

	cookieCtx, cancelCookies := context.WithTimeout(ctx, 10*time.Second)
	defer cancelCookies()
	var cookies []*network.Cookie
	err := chromedp.Run(cookieCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		// storage.GetCookies (not network.GetCookies) returns every cookie
		// in the browser context, not just ones visible to whichever page
		// happens to currently be "current" for this target.
		cookies, err = storage.GetCookies().Do(ctx)
		return err
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not read cookies (continuing without them):", err)
		return nil
	}
	cookiesPath := filepath.Join(outDir, "cookies.json")
	if err := writeJSON(cookiesPath, cookies); err != nil {
		return fmt.Errorf("write cookies: %w", err)
	}
	fmt.Printf("Saved %d cookie(s) -> %s\n", len(cookies), cookiesPath)
	fmt.Println("NOTE: cookies.json contains live session credentials. Treat it as a secret; do not share or commit it.")

	return nil
}

// attachToNewTabs watches for new top-level "page" targets (tabs/popups)
// created for the rest of the program's lifetime, and attaches rec to each
// one as it appears. The target we're already attached to (ctx's own) is
// skipped since it's already wired up by the caller.
func attachToNewTabs(ctx context.Context, rec *capture.Recorder) {
	initialID := chromedp.FromContext(ctx).Target.TargetID

	var mu sync.Mutex
	seen := map[target.ID]bool{initialID: true}

	chromedp.ListenBrowser(ctx, func(ev interface{}) {
		var info *target.Info
		switch e := ev.(type) {
		case *target.EventTargetCreated:
			info = e.TargetInfo
		case *target.EventTargetInfoChanged:
			info = e.TargetInfo
		default:
			return
		}
		if info.Type != "page" {
			return
		}

		mu.Lock()
		already := seen[info.TargetID]
		seen[info.TargetID] = true
		mu.Unlock()
		if already {
			return
		}

		go func(id target.ID) {
			// subCtx is intentionally long-lived (tied to the parent ctx,
			// not a short timeout): ListenTarget below stops delivering
			// events as soon as its context is cancelled, so the context we
			// attach with must live for the rest of the run.
			subCtx, _ := chromedp.NewContext(ctx, chromedp.WithTargetID(id))
			if err := chromedp.Run(subCtx, chromedp.ActionFunc(func(c context.Context) error {
				rec.Attach(c)
				return nil
			})); err != nil {
				fmt.Fprintln(os.Stderr, "warning: failed to watch new tab/popup", id, ":", err)
				return
			}
			fmt.Println("...now also watching a new tab/popup (e.g. SSO/MFA window)")
		}(info.TargetID)
	})
}

// waitForDone blocks until either: the user presses Enter on stdin, or
// doneFile is created by another process (e.g. an orchestrating agent that
// can't type into this program's stdin). It also prints periodic progress
// via report so it's obvious the capture is actually working.
func waitForDone(doneFile string, report func(n int), rec *capture.Recorder) {
	stop := make(chan struct{})
	var once sync.Once
	closeStop := func() { once.Do(func() { close(stop) }) }

	go func() {
		// If stdin isn't a real interactive terminal (e.g. this process was
		// launched in the background with no TTY attached), ReadString
		// returns an immediate EOF. That must not be treated as "done" --
		// only an actual line (a real Enter press) should trigger exit.
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			closeStop()
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := os.Stat(doneFile); err == nil {
					closeStop()
					return
				}
			}
		}
	}()

	progress := time.NewTicker(5 * time.Second)
	defer progress.Stop()
	for {
		select {
		case <-stop:
			return
		case <-progress.C:
			report(len(rec.Entries()))
		}
	}
}

func writeJSON(path string, v interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
