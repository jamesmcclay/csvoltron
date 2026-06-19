// Command update fetches the latest csvoltron source from GitHub and
// overwrites the current checkout with it. It's the Go equivalent of
// `git pull`, for the no-git Windows quick start flow: run it from inside
// your csvoltron-main checkout with `go run ./cmd/update`.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	zipURL     = "https://github.com/jamesmcclay/csvoltron/archive/refs/heads/main.zip"
	modulePath = "github.com/jamesmcclay/csvoltron"
)

// preserve lists top-level paths that hold user data rather than code, so
// they're left alone even if they exist (e.g. someone ran without the
// -profile-dir/-out-dir flags and ended up with default, in-tree dirs).
var preserve = map[string]bool{
	".git":                      true,
	".csvoltron-chrome-profile": true,
	"csv_output":                true,
	"capture-output":            true,
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := checkInCheckout(); err != nil {
		return err
	}

	fmt.Println("Downloading latest csvoltron...")
	body, err := download(zipURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	n, err := extract(zr, ".")
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	fmt.Printf("Updated %d file(s).\n", n)
	fmt.Println("Run csvoltron as usual, e.g.:")
	fmt.Println(`  go run . -profile-dir "../chrome-profile" -out-dir "../csv_output"`)
	return nil
}

// checkInCheckout guards against running this somewhere other than a
// csvoltron checkout, since it overwrites files in the current directory.
func checkInCheckout() error {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return fmt.Errorf("read go.mod (run this from inside your csvoltron checkout): %w", err)
	}
	first := strings.SplitN(string(data), "\n", 2)[0]
	if strings.TrimSpace(first) != "module "+modulePath {
		return fmt.Errorf("go.mod in current directory isn't %s; refusing to overwrite an unrelated directory", modulePath)
	}
	return nil
}

func download(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// extract writes every file in zr into dir, stripping the zip's single
// top-level directory (GitHub archives wrap everything in "<repo>-<branch>/")
// and skipping anything under a preserved path.
func extract(zr *zip.Reader, dir string) (int, error) {
	n := 0
	for _, f := range zr.File {
		rel := stripTopLevel(f.Name)
		if rel == "" || preserved(rel) {
			continue
		}

		target := filepath.Join(dir, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return n, err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return n, err
		}
		if err := extractFile(f, target); err != nil {
			return n, fmt.Errorf("%s: %w", rel, err)
		}
		n++
	}
	return n, nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// stripTopLevel removes the leading "<repo>-<branch>/" path component that
// GitHub's archive zips always wrap contents in.
func stripTopLevel(name string) string {
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return ""
	}
	return name[i+1:]
}

func preserved(rel string) bool {
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	return preserve[first]
}
