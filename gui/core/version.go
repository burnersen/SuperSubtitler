package core

import (
	"regexp"
	"strconv"
	"strings"
)

// MinCLIVersion is the first SuperSubtitler that speaks the -json protocol.
const MinCLIVersion = "0.2.0"

var (
	bareVersion   = regexp.MustCompile(`^\s*v?(\d+\.\d+\.\d+)\s*$`)
	bannerVersion = regexp.MustCompile(`SuperSubtitler v(\d+\.\d+\.\d+)`)
)

// ParseVersion reads the output of "SuperSubtitler.exe -version". A current
// CLI prints just "0.2.0"; an older one does not know the flag and prints its
// banner ("SuperSubtitler v0.1.0") instead — that version is found too, so the
// window can say "too old" rather than "not found".
func ParseVersion(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if m := bareVersion.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	if m := bannerVersion.FindStringSubmatch(output); m != nil {
		return m[1], true
	}
	return "", false
}

// AtLeast compares two X.Y.Z versions numerically.
func AtLeast(v, min string) bool {
	a, b := splitVersion(v), splitVersion(min)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func splitVersion(v string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out
}

// ReleaseVersion reads the version from a release tag ("v0.2.0" -> "0.2.0").
func ReleaseVersion(tag string) (string, bool) {
	v := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if !bareVersion.MatchString(v) {
		return "", false
	}
	return v, true
}

// UpdateDecision says whether the newest release should replace the
// installed CLI (installed "" = none or unknown). An update never goes
// backwards and never installs a CLI too old for this window: the
// "latest release" on GitHub can be older than a build the user already has.
func UpdateDecision(installed, releaseTag string) (bool, string) {
	rel, ok := ReleaseVersion(releaseTag)
	if !ok {
		return false, "the newest release (" + releaseTag + ") has no readable version number"
	}
	if !AtLeast(rel, MinCLIVersion) {
		return false, "the newest release on GitHub is " + rel + ", older than the " + MinCLIVersion +
			" this window needs — your current SuperSubtitler.exe was left as it is"
	}
	if installed != "" && AtLeast(installed, rel) {
		if installed == rel {
			return false, "SuperSubtitler " + installed + " is already the newest release"
		}
		return false, "your SuperSubtitler " + installed + " is newer than the newest release (" + rel + ") — nothing to update"
	}
	return true, ""
}

// Release is the part of GitHub's "latest release" answer the window uses.
type Release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// AssetURL returns the download URL of the named asset (case-insensitive).
func (r Release) AssetURL(name string) (string, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a.URL, true
		}
	}
	return "", false
}
