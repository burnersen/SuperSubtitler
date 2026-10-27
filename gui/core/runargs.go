package core

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RunOptions are the switches of the Subtitle page. Every field maps to one
// CLI flag; nothing here is decided by the window itself.
type RunOptions struct {
	Language  string `json:"language"` // "" = detect
	Translate bool   `json:"translate"`
	SrtOnly   bool   `json:"srtOnly"`
	Lean      bool   `json:"lean"`
	NoVad     bool   `json:"noVad"`
	LowMem    bool   `json:"lowMem"`
	Auto      bool   `json:"auto"`
	Force     bool   `json:"force"`
	OutDir    string `json:"outDir"` // "" = next to the source
	// Channel is the physical channel to isolate ("0", "2", …), "" = the
	// automatic voice-focused downmix. A string on purpose: an int would make
	// a missing value mean "channel 0" instead of "automatic".
	Channel string `json:"channel"`
}

// maxChannel covers 7.1 with room to spare; ffmpeg rejects what is not there.
const maxChannel = 15

func channelArg(ch string) (string, error) {
	ch = strings.TrimSpace(ch)
	if ch == "" {
		return "", nil
	}
	n, err := strconv.Atoi(ch)
	if err != nil || n < 0 || n > maxChannel {
		return "", fmt.Errorf("audio channel must be a number from 0 to %d", maxChannel)
	}
	return "-channel:" + strconv.Itoa(n), nil
}

var langCode = regexp.MustCompile(`^[a-z]{2,3}$`)

// ValidLanguage reports whether code is usable for -lang (2–3 lowercase letters).
func ValidLanguage(code string) bool { return langCode.MatchString(code) }

// Every run of the CLI from the window carries these: events on stdout, no
// "Press Enter" at the end.
var baseArgs = []string{"-json", "-nowait"}

// SubtitleArgs builds the flags of a subtitle run (without the files).
// -nomerge: the Subtitle page subtitles what it lists; merging has its own page.
func SubtitleArgs(o RunOptions) ([]string, error) {
	args := append(append([]string{}, baseArgs...), "-nomerge")
	if o.Language != "" {
		lang := strings.ToLower(strings.TrimSpace(o.Language))
		if !ValidLanguage(lang) {
			return nil, errors.New("language must be a 2- or 3-letter code such as de, en or es")
		}
		args = append(args, "-lang:"+lang)
	}
	for _, f := range []struct {
		on   bool
		flag string
	}{
		{o.Translate, "-translate"}, {o.SrtOnly, "-srtonly"}, {o.Lean, "-lean"},
		{o.NoVad, "-novad"}, {o.LowMem, "-lowmem"}, {o.Auto, "-auto"}, {o.Force, "-force"},
	} {
		if f.on {
			args = append(args, f.flag)
		}
	}
	ch, err := channelArg(o.Channel)
	if err != nil {
		return nil, err
	}
	if ch != "" {
		args = append(args, ch)
	}
	if o.OutDir != "" {
		args = append(args, "-out:"+o.OutDir)
	}
	return args, nil
}

// SplitArgs builds the flags of a channel export (-split): every physical
// channel of the chosen audio track becomes "<name>.chNN.wav" next to the
// source, so the speech can be found by ear. track is 1-based; 0 = first.
func SplitArgs(track int) ([]string, error) {
	if track < 0 || track > 99 {
		return nil, errors.New("audio track must be a number from 1 to 99")
	}
	args := append(append([]string{}, baseArgs...), "-split")
	if track > 0 {
		args = append(args, "-track:"+strconv.Itoa(track))
	}
	return args, nil
}

// MergeArgs builds the full command of a merge: one video plus the extra
// audio/subtitle files. -auto answers the CLI's "merge these?" with yes — the
// user already said so by pressing Start on the Merge page.
func MergeArgs(outDir, video string, extras []string) ([]string, error) {
	if video == "" {
		return nil, errors.New("choose the video first")
	}
	if len(extras) == 0 {
		return nil, errors.New("add at least one audio or subtitle file")
	}
	args := append(append([]string{}, baseArgs...), "-auto")
	if outDir != "" {
		args = append(args, "-out:"+outDir)
	}
	args = append(args, video)
	return append(args, extras...), nil
}

// SetupArgs builds the command of the one-time setup. With translate the large
// translation model is fetched as well.
func SetupArgs(translate bool) []string {
	args := append(append([]string{}, baseArgs...), "-setup")
	if translate {
		args = append(args, "-translate")
	}
	return args
}

// MaxCommandLine keeps a safe distance from Windows' 32767-character limit
// for one command line (quotes and the program path need room too).
const MaxCommandLine = 30000

// Batches splits files into groups whose command line (flags + files) stays
// below limit. A batch is one CLI run: questions for all its files come at its
// start, so batches should be as large as possible — usually there is one.
func Batches(flags []string, files []string, limit int) [][]string {
	fixed := 0
	for _, f := range flags {
		fixed += len(f) + 3
	}
	var out [][]string
	var cur []string
	size := fixed
	for _, f := range files {
		n := len(f) + 3 // quotes and the separating space
		if len(cur) > 0 && size+n > limit {
			out = append(out, cur)
			cur, size = nil, fixed
		}
		cur = append(cur, f)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
