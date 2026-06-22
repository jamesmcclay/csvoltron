// Package auth drives a real, visible Chrome window so a human can complete
// Strata Cloud Manager's SSO + MFA login by hand, then extracts the
// short-lived bearer JWT (and tenant-specific API host) that the SCM web
// app itself uses to call its internal Optimize API. See internal/scm for
// what that API looks like.
package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Credentials is what's needed to call the Optimize API directly.
type Credentials struct {
	Host string // e.g. "paas-16.prod.panorama.paloaltonetworks.com"
	JWT  string // value of the x-auth-jwt header
}

// apiHostPattern matches the SCM backend API calls we want to piggyback
// on -- any call to a tenant's Panorama-as-a-Service host's config API.
var apiHostPattern = regexp.MustCompile(`^https://([^/]+)/api/config/v9\.2/`)

// Login opens a visible Chrome window at startURL, waits for the user to
// complete SSO + MFA and reach the Optimize page by hand, and returns as
// soon as it observes an authenticated API call -- at which point it has
// everything needed to call the API directly and closes the browser. It
// gives up after timeout if no such call is seen (e.g. the user didn't
// reach the Optimize page, or PAN changed the API).
// chromeExecPath, if non-empty, overrides which Chrome/Chromium binary is
// launched (e.g. a portable Chromium for machines whose managed Chrome
// install has DevTools/remote debugging disabled by policy). Empty string
// means "use chromedp's normal system Chrome detection".
func Login(ctx context.Context, startURL, profileDir, chromeExecPath string, timeout time.Duration) (Credentials, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return Credentials{}, fmt.Errorf("create profile dir: %w", err)
	}
	// Chrome on Windows refuses to enable remote debugging on a relative
	// --user-data-dir, silently disabling it instead (it logs "DevTools
	// remote debugging requires a non-default data directory" and chromedp
	// then times out waiting for a websocket URL that never gets printed).
	absProfileDir, err := filepath.Abs(profileDir)
	if err != nil {
		return Credentials{}, fmt.Errorf("resolve profile dir: %w", err)
	}
	profileDir = absProfileDir

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		// Without this, Chrome's BackgroundModeManager can keep a process
		// alive (e.g. as a macOS background/tray process) after the window
		// closes, even after a graceful CDP close. That leftover process
		// keeps holding profileDir's SingletonLock, so the *next* run's
		// Chrome silently hands off to it instead of starting fresh --
		// chromedp then times out waiting for a websocket URL that the
		// already-running process never prints ("websocket url timeout
		// reached").
		chromedp.Flag("disable-background-mode", true),
		chromedp.UserDataDir(profileDir),
	)
	if chromeExecPath != "" {
		if _, err := os.Stat(chromeExecPath); err != nil {
			return Credentials{}, fmt.Errorf("portable Chromium not found at %s (re-run the installer to download it): %w", chromeExecPath, err)
		}
		allocOpts = append(allocOpts, chromedp.ExecPath(chromeExecPath))
	}
	// TODO(temporary): forward Chrome's own stdout/stderr so we can see what
	// it prints (if anything) during the "websocket url timeout reached"
	// failure on reused profile dirs. Remove once that's diagnosed.
	allocOpts = append(allocOpts, chromedp.CombinedOutput(os.Stderr))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	found := make(chan Credentials, 1)
	var once sync.Once
	report := func(c Credentials) { once.Do(func() { found <- c }) }

	watch := func(watchCtx context.Context) {
		chromedp.ListenTarget(watchCtx, func(ev interface{}) {
			req, ok := ev.(*network.EventRequestWillBeSent)
			if !ok {
				return
			}
			m := apiHostPattern.FindStringSubmatch(req.Request.URL)
			if m == nil {
				return
			}
			jwt, ok := req.Request.Headers["x-auth-jwt"].(string)
			if !ok || jwt == "" {
				return
			}
			report(Credentials{Host: m[1], JWT: jwt})
		})
	}

	if err := chromedp.Run(browserCtx,
		network.Enable(),
		chromedp.ActionFunc(func(c context.Context) error {
			watch(c)
			return nil
		}),
		chromedp.Navigate(startURL),
	); err != nil {
		return Credentials{}, fmt.Errorf("launch/navigate: %w", err)
	}

	// SSO/MFA flows sometimes pop up a separate window for the credential
	// prompt rather than redirecting the original tab. Watch any such
	// new tab too, in case the user ends up browsing the Optimize page
	// there instead of the original tab.
	watchNewTabs(browserCtx, watch)

	fmt.Println("A Chrome window has opened.")
	fmt.Println("1. Complete SSO login + MFA by hand.")
	fmt.Println("2. Navigate to the Optimize page if you're not redirected there automatically:")
	fmt.Println("   " + startURL)
	fmt.Println("(This will be detected automatically -- no need to press Enter.)")

	select {
	case creds := <-found:
		fmt.Println("Authenticated session detected, closing browser.")
		closeGracefully(browserCtx)
		return creds, nil
	case <-time.After(timeout):
		closeGracefully(browserCtx)
		return Credentials{}, fmt.Errorf("timed out after %s waiting for an authenticated Optimize API call; did you reach the Optimize page?", timeout)
	case <-ctx.Done():
		closeGracefully(browserCtx)
		return Credentials{}, ctx.Err()
	}
}

// closeGracefully asks Chrome to quit via the CDP Browser.close command
// instead of leaving it to the deferred context cancellation, which kills
// the process outright (SIGKILL) and can leave stale SingletonLock /
// SingletonSocket files in profileDir on macOS/Linux. A stale lock makes
// the *next* run's Chrome hang trying to hand off to the (now-dead) old
// process instead of starting fresh, which chromedp sees as "websocket url
// timeout reached".
func closeGracefully(browserCtx context.Context) {
	if err := chromedp.Cancel(browserCtx); err != nil {
		fmt.Fprintln(os.Stderr, "warning: failed to gracefully close browser:", err)
	}
}

// watchNewTabs calls watch(subCtx) for every new top-level "page" target
// (tab/popup) created for the lifetime of browserCtx.
func watchNewTabs(browserCtx context.Context, watch func(context.Context)) {
	initialID := chromedp.FromContext(browserCtx).Target.TargetID

	var mu sync.Mutex
	seen := map[target.ID]bool{initialID: true}

	chromedp.ListenBrowser(browserCtx, func(ev interface{}) {
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
			// subCtx is intentionally long-lived (tied to browserCtx, not a
			// short timeout): ListenTarget stops delivering events as soon
			// as its context is cancelled.
			subCtx, _ := chromedp.NewContext(browserCtx, chromedp.WithTargetID(id))
			if err := chromedp.Run(subCtx, chromedp.ActionFunc(func(c context.Context) error {
				watch(c)
				return nil
			})); err != nil {
				fmt.Fprintln(os.Stderr, "warning: failed to watch new tab/popup", id, ":", err)
			}
		}(info.TargetID)
	})
}
