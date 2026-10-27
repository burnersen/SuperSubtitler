//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

// Runtime.go — the pinned runtime: exactly which Python, pip, FFmpeg, Python
// packages and speech models SuperSubtitler installs (runtime.json).
//
// Why: "pip install faster-whisper" takes the newest of everything, and one
// day a new PyAV broke every fresh install. With the pins every user gets the
// same, tested set, and every file is checked against its SHA-256 before use.
//
// Mirror first: files marked "mirror" are fetched from this project's own
// GitHub release (tag runtime-v1, filled by .github/workflows/mirror-runtime.yml)
// and only then from their original source — so a renamed or deleted upstream
// repository can never break an install. Files above split_over are stored
// there in parts (GitHub's 2 GB asset limit) and joined while downloading.

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
)

//go:embed runtime.json
var runtimeJSON []byte

type pinnedFile struct {
	File    string `json:"file"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Mirror  bool   `json:"mirror"`
	Version string `json:"version"`
}

type pinnedPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	File    string `json:"file"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	CUDA    bool   `json:"cuda"`
}

type modelFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type pinnedModel struct {
	Repo     string      `json:"repo"`
	Revision string      `json:"revision"`
	Files    []modelFile `json:"files"`
	Mirror   bool        `json:"mirror"`
}

type runtimeManifest struct {
	Version int `json:"version"`
	Mirror  struct {
		Repo      string `json:"repo"`
		Tag       string `json:"tag"`
		SplitOver int64  `json:"split_over"`
		PartSize  int64  `json:"part_size"`
	} `json:"mirror"`
	Python   pinnedFile             `json:"python"`
	Pip      pinnedFile             `json:"pip"`
	FFmpeg   pinnedFile             `json:"ffmpeg"`
	Packages []pinnedPackage        `json:"packages"`
	Models   map[string]pinnedModel `json:"models"`
}

// pins is parsed once; a broken embedded file is a build error, caught by the
// tests, never something a user could run into.
var pins = mustParseRuntime(runtimeJSON)

func mustParseRuntime(data []byte) *runtimeManifest {
	var m runtimeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		panic("Runtime.go: runtime.json is invalid: " + err.Error())
	}
	return &m
}

// mirrorURL is the download URL of an asset in the mirror release.
func (m *runtimeManifest) mirrorURL(asset string) string {
	return "https://github.com/" + m.Mirror.Repo + "/releases/download/" + m.Mirror.Tag + "/" + asset
}

// partNames returns the mirror asset names of a file: just the name, or
// "<name>.part00", ".part01", … when it is too large for one release asset.
// The mirror workflow splits with exactly these names (split -d -a 2).
func (m *runtimeManifest) partNames(asset string, size int64) []string {
	if m.Mirror.SplitOver <= 0 || size <= m.Mirror.SplitOver {
		return []string{asset}
	}
	n := int((size + m.Mirror.PartSize - 1) / m.Mirror.PartSize)
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s.part%02d", asset, i)
	}
	return names
}

// modelAsset is a model file's name in the mirror ("large-v3--model.bin").
func modelAsset(model, file string) string { return model + "--" + file }

// hfURL is the original source of a model file at the pinned revision.
func hfURL(model pinnedModel, file string) string {
	return "https://huggingface.co/" + model.Repo + "/resolve/" + model.Revision + "/" + file
}

// downloadSource is one place a file can come from: a list of URLs whose
// contents, joined in order, are the file.
type downloadSource struct {
	name string // "mirror" or the original host, for the log
	urls []string
}

// sourcesFor lists where a file is fetched from, mirror first.
func (m *runtimeManifest) sourcesFor(asset, original string, size int64, mirrored bool) []downloadSource {
	var out []downloadSource
	if mirrored && m.Mirror.Repo != "" && m.Mirror.Tag != "" {
		var urls []string
		for _, p := range m.partNames(asset, size) {
			urls = append(urls, m.mirrorURL(p))
		}
		out = append(out, downloadSource{name: "mirror", urls: urls})
	}
	host := original
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return append(out, downloadSource{name: host, urls: []string{original}})
}

// ----------------------------------------------------------------------------
// Requirements (pip --require-hashes)
// ----------------------------------------------------------------------------

// requirementsText renders the pinned packages as a pip requirements file with
// hashes. In --require-hashes mode pip refuses anything not listed, including a
// dependency this list forgot — so an incomplete list fails loudly in CI
// instead of quietly pulling an unpinned version.
func (m *runtimeManifest) requirementsText(cuda bool) string {
	var b strings.Builder
	for _, p := range m.Packages {
		if p.CUDA != cuda {
			continue
		}
		fmt.Fprintf(&b, "%s==%s --hash=sha256:%s\n", p.Name, p.Version, p.SHA256)
	}
	return b.String()
}

// lockID identifies the exact installed set (packages, CUDA or not, Python).
// It is stored in Python\.ss_ready; when it changes, the engine is reinstalled.
func (m *runtimeManifest) lockID(cuda bool) string {
	h := sha256.New()
	io.WriteString(h, m.Python.Version+"\n"+m.requirementsText(false))
	if cuda {
		io.WriteString(h, m.requirementsText(true))
	}
	return fmt.Sprintf("lock:%x|cuda:%t", h.Sum(nil)[:8], cuda)
}

// usesPinnedEngine: the tested set applies with the default engine_spec and
// the pinned Python version. Anything else is an expert's own choice, which is
// installed the old way (newest versions, no hashes) with a warning.
func usesPinnedEngine(s AppSettings) bool {
	return s.engineSpec == defaultEngineSpec && s.pythonVersion == pins.Python.Version
}

// ----------------------------------------------------------------------------
// Verified downloads
// ----------------------------------------------------------------------------

// bigClient has no overall time limit: a 3 GB model on a slow line may take
// an hour. A stalled transfer is caught by the watchdog in fetchVerified
// instead; connecting and the first answer stay bounded.
var bigClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// downloadStallLimit ends a transfer that has received nothing for this long.
const downloadStallLimit = 2 * time.Minute

// errNotOnMirror marks a 404 from the mirror — normal before the mirror
// release exists, so it is reported quietly.
var errNotOnMirror = errors.New("not on the mirror")

// downloadPinned fetches a pinned file to dest, trying each source in turn.
// The file is written to dest only after its size and SHA-256 matched.
func downloadPinned(ctx context.Context, step, label, dest string, size int64, sha string, sources []downloadSource) error {
	var errs []string
	for _, src := range sources {
		err := fetchVerified(ctx, step, label, dest, size, sha, src)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errNotOnMirror) {
			pInfo.Printf("%s: not on the mirror yet — using %s.\n", label, nextName(sources, src))
		} else {
			pWarn.Printf("%s from %s failed: %s\n", label, src.name, shortErr(err))
		}
		errs = append(errs, src.name+": "+err.Error())
	}
	return fmt.Errorf("Runtime.go: %s could not be downloaded (%s)", label, strings.Join(errs, "; "))
}

func nextName(sources []downloadSource, cur downloadSource) string {
	for i, s := range sources {
		if s.name == cur.name && i+1 < len(sources) {
			return sources[i+1].name
		}
	}
	return "the next source"
}

// fetchVerified downloads one source (all its parts, joined) into a temp file
// next to dest, checks size and SHA-256, and only then renames it into place.
func fetchVerified(ctx context.Context, step, label, dest string, size int64, want string, src downloadSource) error {
	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	prog := newDLProgress(step, label+" ("+src.name+")", size, nil)
	if s := currentSpinner.Load(); s != nil {
		prog.spinner = s
	}
	w := io.MultiWriter(f, h, prog)

	for _, u := range src.urls {
		if err := streamURL(ctx, u, w, src.name == "mirror"); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if prog.done != size {
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", prog.done, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch (got %s…, expected %s…) — the file was not used", got[:12], want[:12])
	}
	prog.finish()
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(dest)
		if err := os.Rename(tmp, dest); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

// currentSpinner lets fetchVerified show its progress on the spinner of the
// step that called it (FFmpeg, Python, model) without threading it through.
var currentSpinner atomic.Pointer[pterm.SpinnerPrinter]

// streamURL copies one URL into w. A transfer that delivers nothing for
// downloadStallLimit is cancelled.
func streamURL(ctx context.Context, url string, w io.Writer, fromMirror bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SuperSubtitler/"+appVersion)
	resp, err := bigClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && fromMirror {
		return errNotOnMirror
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	stalled := atomic.Bool{}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > downloadStallLimit {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			last.Store(time.Now().UnixNano())
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			if stalled.Load() {
				return fmt.Errorf("no data for %s — connection stalled", downloadStallLimit)
			}
			return rerr
		}
	}
}

// sha256File hashes a file on disk (to check an existing install).
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runtimeSummary lists the pinned versions for -version style output and the
// third-party notice.
func runtimeSummary() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Python %s · pip %s · FFmpeg %s", pins.Python.Version, pins.Pip.Version, pins.FFmpeg.Version)
	for _, p := range pins.Packages {
		if p.Name == "faster-whisper" || p.Name == "ctranslate2" {
			fmt.Fprintf(&b, " · %s %s", p.Name, p.Version)
		}
	}
	names := make([]string, 0, len(pins.Models))
	for n := range pins.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(&b, " · models: %s", strings.Join(names, ", "))
	return b.String()
}
