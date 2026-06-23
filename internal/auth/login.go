// Package auth drives a real, visible Chrome window so a human can complete
// Strata Cloud Manager's SSO + MFA login by hand, then extracts the
// short-lived bearer JWT (and tenant-specific API host) that the SCM web
// app itself uses to call its internal Optimize API. See internal/scm for
// what that API looks like.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	// IAMToken is the OIDC access token issued during the SSO login this
	// Login call drove (the body of the .../am/oauth2/access_token
	// exchange's "access_token" field) -- it's what scm.PanoramaClient
	// authenticates with for Panorama-sourced data. Captured on a
	// best-effort basis: it's normally available well before Host/JWT are
	// (it's issued early in the SSO flow), but is left empty rather than
	// failing Login if it somehow isn't seen in time.
	IAMToken string
}

// apiHostPattern matches the SCM backend API calls we want to piggyback
// on -- any call to a tenant's Panorama-as-a-Service host's config API.
var apiHostPattern = regexp.MustCompile(`^https://([^/]+)/api/config/v9\.2/`)

// accessTokenURLSuffix matches the OIDC token exchange call whose response
// body carries the access token PanoramaClient needs.
const accessTokenURLSuffix = "/am/oauth2/access_token"

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
		// Stops Chrome from keeping a process alive in the background (e.g.
		// as a macOS menu-bar/tray process) after its window closes.
		chromedp.Flag("disable-background-mode", true),
		chromedp.UserDataDir(profileDir),
	)
	if chromeExecPath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(chromeExecPath))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	found := make(chan Credentials, 1)
	var once sync.Once
	var mu sync.Mutex
	var iamToken string
	report := func(host, jwt string) {
		once.Do(func() {
			mu.Lock()
			tok := iamToken
			mu.Unlock()
			found <- Credentials{Host: host, JWT: jwt, IAMToken: tok}
		})
	}

	watch := func(watchCtx context.Context) {
		// Tracks requestIDs of in-flight access-token exchange calls, so
		// their response body (fetched once EventLoadingFinished fires) can
		// be matched back to the right request.
		pendingTokenReqs := make(map[network.RequestID]bool)

		chromedp.ListenTarget(watchCtx, func(ev interface{}) {
			switch e := ev.(type) {
			case *network.EventRequestWillBeSent:
				if e.Request.Method == "POST" && strings.Contains(e.Request.URL, accessTokenURLSuffix) {
					mu.Lock()
					pendingTokenReqs[e.RequestID] = true
					mu.Unlock()
				}

				m := apiHostPattern.FindStringSubmatch(e.Request.URL)
				if m == nil {
					return
				}
				jwt, ok := e.Request.Headers["x-auth-jwt"].(string)
				if !ok || jwt == "" {
					return
				}
				report(m[1], jwt)

			case *network.EventLoadingFinished:
				mu.Lock()
				isTokenReq := pendingTokenReqs[e.RequestID]
				delete(pendingTokenReqs, e.RequestID)
				mu.Unlock()
				if !isTokenReq {
					return
				}
				// Must run off this goroutine: GetResponseBody blocks on a
				// reply from the same CDP websocket read loop that's
				// delivering this very event (see internal/capture for the
				// same pattern).
				go func(reqID network.RequestID) {
					body, err := network.GetResponseBody(reqID).Do(watchCtx)
					if err != nil {
						return
					}
					var parsed struct {
						AccessToken string `json:"access_token"`
					}
					if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
						return
					}
					mu.Lock()
					iamToken = parsed.AccessToken
					mu.Unlock()
				}(e.RequestID)
			}
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
// the process outright (SIGKILL) rather than letting it shut down and clean
// up after itself normally.
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
