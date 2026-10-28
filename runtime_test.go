//go:build windows && amd64

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// The embedded manifest is what every user installs; a typo here would only
// show on someone's machine, so it is checked field by field.
func TestRuntimeManifestIsComplete(t *testing.T) {
	m := pins
	if m.Mirror.Repo != "burnersen/SuperSubtitler" || m.Mirror.Tag == "" {
		t.Errorf("mirror = %+v", m.Mirror)
	}
	if m.Mirror.SplitOver >= 2<<30 || m.Mirror.PartSize > m.Mirror.SplitOver {
		t.Errorf("parts must stay below GitHub's 2 GiB asset limit: %+v", m.Mirror)
	}
	for _, f := range []pinnedFile{m.Python, m.Pip, m.FFmpeg} {
		if f.File == "" || f.Size <= 0 || !hex64.MatchString(f.SHA256) || f.Version == "" {
			t.Errorf("incomplete pin: %+v", f)
		}
		if !strings.HasPrefix(f.URL, "https://") && !(f.URL == "" && f.Mirror) {
			t.Errorf("%s needs an original URL, or must live in the mirror: %+v", f.File, f)
		}
	}
	if !strings.HasPrefix(m.Python.File, "python-"+m.Python.Version+"-embed-amd64") {
		t.Errorf("python file %q does not match version %s", m.Python.File, m.Python.Version)
	}
	if appSettingsDefault := defaultSettings(); appSettingsDefault.pythonVersion != m.Python.Version {
		t.Errorf("default python_version %s must equal the pinned %s, or the pins never apply", appSettingsDefault.pythonVersion, m.Python.Version)
	}
	names := map[string]bool{}
	for _, p := range m.Packages {
		if p.Name == "" || p.Version == "" || p.Size <= 0 || !hex64.MatchString(p.SHA256) || !strings.HasSuffix(p.File, ".whl") {
			t.Errorf("incomplete package pin: %+v", p)
		}
		if names[strings.ToLower(p.Name)] {
			t.Errorf("package %s pinned twice", p.Name)
		}
		names[strings.ToLower(p.Name)] = true
	}
	for _, need := range []string{"faster-whisper", "ctranslate2", "av", "onnxruntime", "tokenizers", "colorama", "nvidia-cublas-cu12", "nvidia-cudnn-cu12"} {
		if !names[need] {
			t.Errorf("package %s is not pinned", need)
		}
	}
	assets := map[string]bool{}
	add := func(a string) {
		if assets[a] {
			t.Errorf("mirror asset %s would be used twice", a)
		}
		assets[a] = true
	}
	add(m.Python.File)
	add(m.Pip.File)
	if m.FFmpeg.Mirror {
		add(m.FFmpeg.File)
	}
	if !m.PackagesMirror {
		t.Error("the wheels are meant to be mirrored too")
	}
	for _, p := range m.Packages {
		for _, a := range m.partNames(p.File, p.Size) {
			add(a)
		}
	}
	for _, model := range []string{"large-v3-turbo", "large-v3"} {
		pm, ok := m.Models[model]
		if !ok {
			t.Fatalf("model %s is not pinned", model)
		}
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pm.Revision) || !pm.Mirror {
			t.Errorf("%s: revision %q mirror %v", model, pm.Revision, pm.Mirror)
		}
		have := map[string]bool{}
		for _, f := range pm.Files {
			have[f.Name] = true
			if f.Size <= 0 || !hex64.MatchString(f.SHA256) {
				t.Errorf("%s/%s incomplete: %+v", model, f.Name, f)
			}
			for _, p := range m.partNames(modelAsset(model, f.Name), f.Size) {
				add(p)
			}
		}
		for _, need := range []string{"model.bin", "config.json", "tokenizer.json", "vocabulary.json", "preprocessor_config.json"} {
			if !have[need] {
				t.Errorf("%s misses %s", model, need)
			}
		}
	}
}

func TestPartNamesAndSources(t *testing.T) {
	big := pins.Models["large-v3"].Files
	var bin modelFile
	for _, f := range big {
		if f.Name == "model.bin" {
			bin = f
		}
	}
	parts := pins.partNames(modelAsset("large-v3", "model.bin"), bin.Size)
	if len(parts) != 2 || parts[0] != "large-v3--model.bin.part00" || parts[1] != "large-v3--model.bin.part01" {
		t.Errorf("parts = %q", parts)
	}
	if p := pins.partNames("x", 1000); len(p) != 1 || p[0] != "x" {
		t.Errorf("small file must not be split: %q", p)
	}
	src := pins.sourcesFor("large-v3--model.bin", "https://huggingface.co/a/b", bin.Size, true)
	if len(src) != 2 || src[0].name != "mirror" || len(src[0].urls) != 2 || src[1].name != "huggingface.co" {
		t.Fatalf("sources = %+v", src)
	}
	if !strings.HasPrefix(src[0].urls[0], "https://github.com/burnersen/SuperSubtitler/releases/download/") {
		t.Errorf("mirror url = %s", src[0].urls[0])
	}
	if src := pins.sourcesFor("f.zip", "https://x.org/f.zip", 10, false); len(src) != 1 || src[0].name != "x.org" {
		t.Errorf("unmirrored file must come from its source only: %+v", src)
	}
	if src := pins.sourcesFor("own.zip", "", 10, true); len(src) != 1 || src[0].name != "mirror" {
		t.Errorf("a file without original URL comes from the mirror only: %+v", src)
	}
}

func TestRequirementsAndLockID(t *testing.T) {
	core := pins.requirementsText(false)
	cuda := pins.requirementsText(true)
	if !strings.Contains(core, "faster-whisper==") || strings.Contains(core, "nvidia-") {
		t.Errorf("core requirements:\n%s", core)
	}
	if !strings.Contains(cuda, "nvidia-cudnn-cu12==") || strings.Contains(cuda, "faster-whisper") {
		t.Errorf("cuda requirements:\n%s", cuda)
	}
	for _, line := range strings.Split(strings.TrimSpace(core+cuda), "\n") {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+==[^ ]+ --hash=sha256:[0-9a-f]{64}$`).MatchString(line) {
			t.Errorf("bad requirement line %q", line)
		}
	}
	if pins.lockID(true) == pins.lockID(false) {
		t.Error("CUDA on/off must give different lock ids")
	}
}

func TestUsesPinnedEngine(t *testing.T) {
	s := defaultSettings()
	if !usesPinnedEngine(s) {
		t.Fatal("the defaults must use the pinned set")
	}
	s.engineSpec = "faster-whisper==1.0.3"
	if usesPinnedEngine(s) {
		t.Error("a custom engine_spec must not claim the pinned set")
	}
	s = defaultSettings()
	s.pythonVersion = "3.13.1"
	if usesPinnedEngine(s) {
		t.Error("another Python cannot use cp312 wheels")
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownloadPinnedMirrorFirstAndFallbacks(t *testing.T) {
	payload := []byte(strings.Repeat("subtitles ", 5000))
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/mirror/ok":
			w.Write(payload)
		case "/mirror/part00":
			w.Write(payload[:20000])
		case "/mirror/part01":
			w.Write(payload[20000:])
		case "/mirror/bad":
			w.Write(append([]byte("X"), payload[1:]...)) // same size, wrong content
		case "/origin":
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	size, want := int64(len(payload)), sum(payload)
	src := func(name string, paths ...string) downloadSource {
		s := downloadSource{name: name}
		for _, p := range paths {
			s.urls = append(s.urls, srv.URL+p)
		}
		return s
	}
	ctx := context.Background()
	check := func(dest string) {
		t.Helper()
		got, err := os.ReadFile(dest)
		if err != nil || sum(got) != want {
			t.Fatalf("%s not written correctly: %v", dest, err)
		}
		if _, err := os.Stat(dest + ".download"); err == nil {
			t.Errorf("temp file left behind for %s", dest)
		}
	}

	d1 := filepath.Join(dir, "mirror.bin")
	if err := downloadPinned(ctx, "t", "mirror", d1, size, want, []downloadSource{src("mirror", "/mirror/ok"), src("origin", "/origin")}); err != nil {
		t.Fatal(err)
	}
	check(d1)
	if hits["/origin"] != 0 {
		t.Error("the original source must not be asked when the mirror works")
	}

	d2 := filepath.Join(dir, "parts.bin")
	if err := downloadPinned(ctx, "t", "parts", d2, size, want, []downloadSource{src("mirror", "/mirror/part00", "/mirror/part01")}); err != nil {
		t.Fatal(err)
	}
	check(d2)

	d3 := filepath.Join(dir, "missing.bin")
	if err := downloadPinned(ctx, "t", "missing", d3, size, want, []downloadSource{src("mirror", "/mirror/none"), src("origin", "/origin")}); err != nil {
		t.Fatal(err)
	}
	check(d3)

	d4 := filepath.Join(dir, "corrupt.bin")
	if err := downloadPinned(ctx, "t", "corrupt", d4, size, want, []downloadSource{src("mirror", "/mirror/bad"), src("origin", "/origin")}); err != nil {
		t.Fatal(err)
	}
	check(d4)

	d5 := filepath.Join(dir, "never.bin")
	err := downloadPinned(ctx, "t", "never", d5, size, want, []downloadSource{src("mirror", "/mirror/bad")})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a wrong checksum must fail loudly, got %v", err)
	}
	if _, err := os.Stat(d5); err == nil {
		t.Error("a file that failed its checksum must never be put in place")
	}
}

func TestModelArg(t *testing.T) {
	old := modelPaths
	defer func() { modelPaths = old }()
	modelPaths = map[string]string{"large-v3": `C:\m\large`}
	if modelArg("large-v3") != `C:\m\large` || modelArg("tiny") != "tiny" {
		t.Error("modelArg must use the installed folder when known, else the name")
	}
}

func TestReuseCachedBlob(t *testing.T) {
	old := modelDir
	defer func() { modelDir = old }()
	modelDir = t.TempDir()
	data := []byte("model weights")
	f := modelFile{Name: "model.bin", Size: int64(len(data)), SHA256: sum(data)}
	blobs := filepath.Join(modelDir, "models--mobiuslabsgmbh--faster-whisper-large-v3-turbo", "blobs")
	os.MkdirAll(blobs, 0755)
	dest := filepath.Join(t.TempDir(), "model.bin")

	if reuseCachedBlob(f, dest) {
		t.Fatal("nothing cached yet")
	}
	os.WriteFile(filepath.Join(blobs, f.SHA256), []byte("model weightz"), 0644) // right name, wrong content
	if reuseCachedBlob(f, dest) {
		t.Fatal("a cached file with the wrong content must not be used")
	}
	os.WriteFile(filepath.Join(blobs, f.SHA256), data, 0644)
	if !reuseCachedBlob(f, dest) {
		t.Fatal("the matching cached blob was not reused")
	}
	if got, _ := os.ReadFile(dest); string(got) != string(data) {
		t.Errorf("reused file = %q", got)
	}
}

func TestSpeakerGroupStaysOutOfTheEngine(t *testing.T) {
	sp := pins.groupPackages("speakers")
	names := map[string]bool{}
	for _, p := range sp {
		names[p.Name] = true
		if p.CUDA {
			t.Errorf("%s: the speaker group needs no CUDA", p.Name)
		}
	}
	if !names["sherpa-onnx"] || !names["sherpa-onnx-core"] || len(sp) != 2 {
		t.Errorf("speaker group = %+v", sp)
	}
	// Optional groups must not change the engine's set (or its lock id, which
	// would reinstall everything, CUDA included, for every user).
	if core := pins.requirementsText(false) + pins.requirementsText(true); strings.Contains(core, "sherpa") {
		t.Errorf("sherpa leaked into the engine set:\n%s", core)
	}
	if id := pins.groupLockID("speakers"); !strings.HasSuffix(id, "|speakers") || id == pins.lockID(false) {
		t.Errorf("groupLockID = %q", id)
	}
	for _, m := range []string{speakerSegmentationModel, speakerEmbeddingModel} {
		pm := pins.Models[m]
		if !pm.Mirror || len(pm.Revision) != 40 {
			t.Errorf("%s: %+v", m, pm)
		}
		onnx := 0
		for _, f := range pm.Files {
			if strings.HasSuffix(f.Name, ".onnx") {
				onnx++
			}
		}
		if onnx != 1 {
			t.Errorf("%s must have exactly one .onnx file (speakerModelFile picks it), has %d", m, onnx)
		}
	}
}

func TestModelMarkerFollowsTheFiles(t *testing.T) {
	pm := pins.Models["speaker-embedding"]
	other := pm
	other.Files = append([]modelFile{}, pm.Files...)
	other.Files[0].SHA256 = strings.Repeat("0", 64)
	if pm.marker() == other.marker() || !strings.HasPrefix(pm.marker(), pm.Revision+"|") {
		t.Errorf("markers %q / %q", pm.marker(), other.marker())
	}
}
