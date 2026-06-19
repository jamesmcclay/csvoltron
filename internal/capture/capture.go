// Package capture records browser network traffic (request/response pairs for
// XHR and fetch calls) so the real API endpoints behind a JS-rendered page can
// be discovered without manually reading browser devtools.
package capture

import (
	"context"
	"encoding/base64"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Entry is one captured request/response pair.
type Entry struct {
	RequestID       string            `json:"requestId"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	RequestBody     string            `json:"requestBody,omitempty"`
	Status          int64             `json:"status"`
	MimeType        string            `json:"mimeType"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	ResponseBody    string            `json:"responseBody,omitempty"`
	StartedAt       time.Time         `json:"startedAt"`
}

// Recorder accumulates Entry values as it observes CDP network events on a
// chromedp context. Only XHR and Fetch resource types are kept; everything
// else (documents, scripts, images, fonts, websockets...) is discarded.
type Recorder struct {
	// DomainFilter, if non-empty, restricts captured requests to URLs whose
	// host contains this substring (case-insensitive). Leave empty to
	// capture everything.
	DomainFilter string

	mu      sync.Mutex
	pending map[network.RequestID]*Entry
	done    []Entry
}

func NewRecorder(domainFilter string) *Recorder {
	return &Recorder{
		DomainFilter: domainFilter,
		pending:      make(map[network.RequestID]*Entry),
	}
}

// Attach registers CDP event listeners on ctx. Call this before navigating.
func (r *Recorder) Attach(ctx context.Context) {
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			r.onRequest(e)
		case *network.EventResponseReceived:
			r.onResponse(e)
		case *network.EventLoadingFinished:
			r.onLoadingFinished(ctx, e)
		}
	})
}

func (r *Recorder) matchesFilter(url string) bool {
	if r.DomainFilter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(url), strings.ToLower(r.DomainFilter))
}

func (r *Recorder) onRequest(e *network.EventRequestWillBeSent) {
	if e.Type != network.ResourceTypeXHR && e.Type != network.ResourceTypeFetch {
		return
	}
	if !r.matchesFilter(e.Request.URL) {
		return
	}

	headers := make(map[string]string, len(e.Request.Headers))
	for k, v := range e.Request.Headers {
		headers[k] = toString(v)
	}

	entry := &Entry{
		RequestID:      string(e.RequestID),
		Method:         e.Request.Method,
		URL:            e.Request.URL,
		RequestHeaders: headers,
		RequestBody:    decodePostData(e.Request.PostDataEntries),
		StartedAt:      e.Timestamp.Time(),
	}

	r.mu.Lock()
	r.pending[e.RequestID] = entry
	r.mu.Unlock()
}

func (r *Recorder) onResponse(e *network.EventResponseReceived) {
	r.mu.Lock()
	entry, ok := r.pending[e.RequestID]
	r.mu.Unlock()
	if !ok {
		return
	}

	headers := make(map[string]string, len(e.Response.Headers))
	for k, v := range e.Response.Headers {
		headers[k] = toString(v)
	}

	r.mu.Lock()
	entry.Status = e.Response.Status
	entry.MimeType = e.Response.MimeType
	entry.ResponseHeaders = headers
	r.mu.Unlock()
}

func (r *Recorder) onLoadingFinished(ctx context.Context, e *network.EventLoadingFinished) {
	r.mu.Lock()
	entry, ok := r.pending[e.RequestID]
	if ok {
		delete(r.pending, e.RequestID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}

	// Event callbacks run synchronously on the same goroutine that reads
	// the CDP websocket (see chromedp.ListenTarget's docs). GetResponseBody
	// sends a command and blocks waiting for its reply, which can only
	// arrive via that same read loop -- calling it inline here would
	// deadlock the whole connection on the very first finished request. It
	// must run on its own goroutine instead.
	go func() {
		body, err := network.GetResponseBody(e.RequestID).Do(ctx)
		if err == nil {
			entry.ResponseBody = string(body)
		}

		r.mu.Lock()
		r.done = append(r.done, *entry)
		r.mu.Unlock()
	}()
}

// Entries returns all captured request/response pairs so far, sorted by
// start time.
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.done))
	copy(out, r.done)
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// decodePostData concatenates the (base64-encoded) post data entries CDP
// gives us into the original request body.
func decodePostData(entries []*network.PostDataEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.Bytes == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(e.Bytes)
		if err != nil {
			continue
		}
		sb.Write(decoded)
	}
	return sb.String()
}
