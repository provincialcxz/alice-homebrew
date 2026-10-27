//go:build spotify

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"debug/elf"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed all:runtime
var bundled embed.FS

// installRuntime writes the bundled glibc, shim and CA bundle to plugin data
// once per plugin version; a digest file tells whether they are current.
func installRuntime(l layout) error {
	sum := sha256.New()
	files := map[string][]byte{}
	e := fs.WalkDir(bundled, "runtime", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "README.md" {
			return err
		}
		b, err := bundled.ReadFile(p)
		files[p] = b
		fmt.Fprintf(sum, "%s %d\n", p, len(b))
		sum.Write(b)
		return err
	})
	if e != nil {
		return e
	}
	if _, ok := files["runtime/lib/ld-linux-aarch64.so.1"]; !ok {
		return errors.New("plugin was built without its runtime (run build-runtime.sh)")
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	marker := filepath.Join(l.runtime(), ".digest")
	if b, _ := os.ReadFile(marker); string(b) == digest {
		return nil
	}
	next := l.runtime() + ".next"
	os.RemoveAll(next)
	for p, b := range files {
		target := filepath.Join(next, filepath.FromSlash(p[len("runtime/"):]))
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
		if e = os.WriteFile(target, b, 0700); e != nil {
			return e
		}
	}
	if e = os.WriteFile(filepath.Join(next, ".digest"), []byte(digest), 0600); e != nil {
		return e
	}
	os.RemoveAll(l.runtime())
	return os.Rename(next, l.runtime())
}

// Spotify publishes the current build at a fixed URL and does not allow it to
// be redistributed, so each speaker downloads it itself.
const soloistURL = "https://soloist-builds.spotifycdn.com/soloist_release_arm64.tar.gz"

const (
	maxSoloist    = 96 << 20
	checkInterval = 12 * time.Hour
	lifetime      = 90 * 24 * time.Hour
)

type build struct {
	Version      string    `json:"version"`
	Built        time.Time `json:"built"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
}

var versionRE = regexp.MustCompile(`soloist (\S+) build \d+ \((\d{8})\)`)

// parseVersion reads `soloist --version`, e.g.
// "soloist 1.3.10.20 build 1793102484 (20261027) (g19412df6ea) (linux/aarch64)".
func parseVersion(out string) (build, error) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return build{}, fmt.Errorf("unexpected version output %q", out)
	}
	built, e := time.Parse("20060102", m[2])
	if e != nil {
		return build{}, e
	}
	return build{Version: m[1], Built: built}, nil
}

type updater struct {
	l       layout
	url     string
	client  *http.Client
	current build
	// staged is a downloaded build waiting in soloist/next for an idle moment.
	staged *build
}

func newUpdater(l layout) *updater {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if pem, e := os.ReadFile(filepath.Join(l.runtime(), "ca-certificates.crt")); e == nil {
		roots.AppendCertsFromPEM(pem)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Proxy: http.ProxyFromEnvironment}
	return &updater{l: l, url: soloistURL, client: &http.Client{Transport: transport, Timeout: 10 * time.Minute}}
}

func (u *updater) dir(name string) string { return filepath.Join(u.l.root, "soloist", name) }

// ensure makes sure a working Soloist is installed, downloading it on first
// start and retrying until it succeeds or the plugin stops.
func (u *updater) ensure(ctx context.Context) error {
	if b, e := u.inspect(u.dir("current")); e == nil {
		u.current = b
		u.warnExpiry()
		return nil
	}
	delay := 30 * time.Second
	for {
		e := u.refresh(ctx)
		if e == nil {
			return nil
		}
		log.Printf("Soloist download failed: %v; retrying in %s", e, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
	}
}

// refresh downloads the current build and installs it right away.
func (u *updater) refresh(ctx context.Context) error {
	if _, e := u.download(ctx, true); e != nil {
		return e
	}
	return u.promote()
}

// loop checks for a new build every few hours and reports one that is staged.
func (u *updater) loop(ctx context.Context, ready chan<- struct{}) {
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fresh, e := u.download(ctx, false)
		if e != nil {
			log.Printf("Soloist update check failed: %v", e)
			u.warnExpiry()
			continue
		}
		if fresh {
			requeue(ready)
		}
	}
}

func (u *updater) warnExpiry() {
	if left := time.Until(u.current.Built.Add(lifetime)); left < 14*24*time.Hour {
		log.Printf("Soloist %s expires in %d days and no newer build has been downloaded yet", u.current.Version, int(left.Hours()/24))
	}
}

// download fetches the published archive into soloist/next unless it is the
// build already installed. It reports whether a new build was staged.
func (u *updater) download(ctx context.Context, force bool) (bool, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.url, nil)
	if e != nil {
		return false, e
	}
	if !force && u.current.ETag != "" {
		req.Header.Set("If-None-Match", u.current.ETag)
	}
	resp, e := u.client.Do(req)
	if e != nil {
		return false, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	if !force && resp.Header.Get("Last-Modified") != "" && resp.Header.Get("Last-Modified") == u.current.LastModified {
		return false, nil
	}
	next := u.dir("next")
	os.RemoveAll(next)
	// An update briefly needs room for two builds; cached audio is the
	// cheapest thing to give up for it.
	trimCache(u.l.cache(), 0)
	if e = extract(io.LimitReader(resp.Body, maxSoloist), next); e != nil {
		os.RemoveAll(next)
		return false, e
	}
	b, e := u.inspect(next)
	if e != nil {
		os.RemoveAll(next)
		return false, e
	}
	if !force && b.Version == u.current.Version && b.Built.Equal(u.current.Built) {
		os.RemoveAll(next)
		u.current.ETag, u.current.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
		return false, u.save(u.dir("current"), u.current)
	}
	b.ETag, b.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if e = u.save(next, b); e != nil {
		return false, e
	}
	u.staged = &b
	log.Printf("downloaded Soloist %s built %s", b.Version, b.Built.Format("2006-01-02"))
	return true, nil
}

// promote replaces the installed build with the staged one.
func (u *updater) promote() error {
	if u.staged == nil {
		return errors.New("nothing staged")
	}
	old := u.dir("old")
	os.RemoveAll(old)
	if e := os.Rename(u.dir("current"), old); e != nil && !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if e := os.Rename(u.dir("next"), u.dir("current")); e != nil {
		os.Rename(old, u.dir("current"))
		return e
	}
	os.RemoveAll(old)
	u.current, u.staged = *u.staged, nil
	log.Printf("using Soloist %s, expires %s", u.current.Version, u.current.Built.Add(lifetime).Format("2006-01-02"))
	return nil
}

func (u *updater) save(dir string, b build) error {
	raw, _ := json.Marshal(b)
	return os.WriteFile(filepath.Join(dir, "build.json"), raw, 0600)
}

// inspect checks that dir holds an arm64 Soloist that runs on the bundled
// runtime and returns its build.
func (u *updater) inspect(dir string) (build, error) {
	binary := filepath.Join(dir, "soloist")
	f, e := elf.Open(binary)
	if e != nil {
		return build{}, e
	}
	machine := f.Machine
	f.Close()
	if machine != elf.EM_AARCH64 {
		return build{}, fmt.Errorf("Soloist build is for %s, not arm64", machine)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, e := u.l.command(ctx, binary, "--version").CombinedOutput()
	if e != nil {
		return build{}, fmt.Errorf("soloist --version: %v: %s", e, bytes.TrimSpace(out))
	}
	b, e := parseVersion(string(out))
	if e != nil {
		return build{}, e
	}
	if raw, e := os.ReadFile(filepath.Join(dir, "build.json")); e == nil {
		var saved build
		if json.Unmarshal(raw, &saved) == nil && saved.Version == b.Version {
			b.ETag, b.LastModified = saved.ETag, saved.LastModified
		}
	}
	return b, nil
}

// extract unpacks the files Soloist ships with into dir. Anything else in
// the archive, and any path outside it, is ignored.
func extract(r io.Reader, dir string) error {
	z, e := gzip.NewReader(r)
	if e != nil {
		return e
	}
	t := tar.NewReader(z)
	wanted := map[string]os.FileMode{"soloist": 0700, "THIRD_PARTY_LICENSES.txt": 0600, "CHANGELOG.md": 0600}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	found := false
	for {
		h, e := t.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		name := path.Base(path.Clean("/" + h.Name))
		mode, ok := wanted[name]
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		out, e := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, t)
		if c := out.Close(); e == nil {
			e = c
		}
		if e != nil {
			return e
		}
		found = found || name == "soloist"
	}
	if !found {
		return errors.New("archive has no soloist binary")
	}
	return nil
}
