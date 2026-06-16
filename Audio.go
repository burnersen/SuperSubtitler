//go:build windows && amd64

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pterm/pterm"
)

// ----------------------------------------------------------------------------
// ffprobe data
// ----------------------------------------------------------------------------

type probeData struct {
	Streams []probeStream `json:"streams"`
	Format  probeFormat   `json:"format"`
}

type probeStream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	Tags          map[string]string `json:"tags"`
}

type probeFormat struct {
	Duration string `json:"duration"`
}

func probeFile(ctx context.Context, path string) (*probeData, error) {
	// A corrupt file can make ffprobe hang indefinitely; bound it.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := runProcess(ctx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	if err != nil {
		return nil, fmt.Errorf("Audio.go: probeFile: %w", err)
	}
	var pd probeData
	if err := json.Unmarshal(out, &pd); err != nil {
		return nil, fmt.Errorf("Audio.go: probeFile (unmarshal): %w", err)
	}
	return &pd, nil
}

func (pd *probeData) durationSec() float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(pd.Format.Duration), 64)
	if err != nil {
		return 0
	}
	return f
}

func (pd *probeData) audioStreams() []probeStream {
	var a []probeStream
	for _, s := range pd.Streams {
		if s.CodecType == "audio" {
			a = append(a, s)
		}
	}
	return a
}

// pickAudioStream returns the chosen audio stream and its 0-based ordinal among
// the audio streams (used for -map 0:a:N). forcedTrack is 1-based.
func pickAudioStream(cfg *AppConfig, pd *probeData) (probeStream, int, error) {
	audio := pd.audioStreams()
	if len(audio) == 0 {
		return probeStream{}, 0, fmt.Errorf("Audio.go: pickAudioStream: no audio stream")
	}
	ordinal := 0
	if cfg.forcedTrack > 0 {
		ordinal = cfg.forcedTrack - 1
		if ordinal >= len(audio) {
			return probeStream{}, 0, fmt.Errorf("Audio.go: pickAudioStream: -track:%d out of range (file has %d audio streams)", cfg.forcedTrack, len(audio))
		}
	}
	return audio[ordinal], ordinal, nil
}

// trackSel is a chosen audio stream and its 0-based ordinal among the audio
// streams (used for -map 0:a:N).
type trackSel struct {
	stream  probeStream
	ordinal int
}

// selectTracks decides which audio streams to subtitle. -track:N forces exactly
// one; a single audio stream is taken without asking; several streams with no
// -track trigger an interactive menu (Enter = all, or numbers like "1,3").
func selectTracks(cfg *AppConfig, pd *probeData) ([]trackSel, error) {
	audio := pd.audioStreams()
	if len(audio) == 0 {
		return nil, fmt.Errorf("Audio.go: selectTracks: no audio stream")
	}
	if cfg.forcedTrack > 0 {
		ord := cfg.forcedTrack - 1
		if ord >= len(audio) {
			return nil, fmt.Errorf("Audio.go: selectTracks: -track:%d out of range (file has %d audio streams)", cfg.forcedTrack, len(audio))
		}
		return []trackSel{{stream: audio[ord], ordinal: ord}}, nil
	}
	if len(audio) == 1 {
		return []trackSel{{stream: audio[0], ordinal: 0}}, nil
	}
	chosen := promptTrackSelection(audio)
	sels := make([]trackSel, 0, len(chosen))
	for _, ord := range chosen {
		sels = append(sels, trackSel{stream: audio[ord], ordinal: ord})
	}
	return sels, nil
}

// promptTrackSelection lists the audio streams and asks which to subtitle.
// Returns the 0-based ordinals; empty input selects all.
func promptTrackSelection(audio []probeStream) []int {
	fmt.Println()
	pInfo.Printf("This file has %d audio tracks — which should be subtitled?\n", len(audio))
	for i, s := range audio {
		lang := containerLang(s)
		if lang == "" {
			lang = "und"
		}
		layout := s.ChannelLayout
		if layout == "" {
			layout = fmt.Sprintf("%dch", s.Channels)
		}
		line := fmt.Sprintf("  %d) %-3s  %-6s %-8s", i+1, lang, s.CodecName, layout)
		if s.Tags != nil && s.Tags["title"] != "" {
			line += "  " + s.Tags["title"]
		}
		pterm.Println(pterm.LightCyan(line))
	}
	pterm.Print(pterm.Gray("  Enter numbers (e.g. 1,3) or press Enter for all: "))
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return parseTrackChoice(line, len(audio))
}

// parseTrackChoice turns "1,3" / "1 3" into 0-based ordinals, ignoring junk and
// out-of-range values and de-duplicating. Empty (or all-invalid) selects all.
func parseTrackChoice(line string, n int) []int {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	})
	seen := map[int]bool{}
	var out []int
	for _, f := range fields {
		k, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || k < 1 || k > n || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k-1)
	}
	if len(out) == 0 {
		out = make([]int, n)
		for i := range out {
			out[i] = i
		}
	}
	return out
}

// containerLang returns the normalised ISO language of the stream tag, or "".
func containerLang(s probeStream) string {
	if s.Tags == nil {
		return ""
	}
	for _, k := range []string{"language", "LANGUAGE", "lang"} {
		if v, ok := s.Tags[k]; ok {
			return normalizeLang(v)
		}
	}
	return ""
}

// ----------------------------------------------------------------------------
// Channel handling
// ----------------------------------------------------------------------------

// monoFilter builds the ffmpeg filter chain that reduces the chosen audio to a
// single voice-focused mono channel at 16 kHz.
//
//	-channel:N  -> isolate that exact physical channel
//	surround    -> center-emphasis or pure-center downmix (FL=0, FR=1, FC=2)
//	else        -> plain mono downmix
func monoFilter(cfg *AppConfig, s probeStream) string {
	if cfg.forcedChannel >= 0 {
		return fmt.Sprintf("pan=mono|c0=c%d,aresample=16000", cfg.forcedChannel)
	}
	if s.Channels >= 5 {
		switch appSettings.surroundMode {
		case "pure_center":
			return "pan=mono|c0=c2,aresample=16000"
		default: // center_emphasis
			return "pan=mono|c0=0.3*c0+0.3*c1+1.0*c2,aresample=16000"
		}
	}
	return "aresample=16000"
}

// ----------------------------------------------------------------------------
// Sample extraction for language detection
// ----------------------------------------------------------------------------

// extractDetectionChunks extracts the detection sample (clamped to the file) and
// splits it into ~30s mono WAV chunks. No lead-in here — detection does not need
// frame-accurate timing.
func extractDetectionChunks(ctx context.Context, cfg *AppConfig, path string, s probeStream, ordinal int, duration float64, outDir string) ([]string, error) {
	startSec := duration * float64(appSettings.detectStartPercent) / 100.0
	sampleSec := float64(appSettings.detectSeconds)
	if duration > 0 {
		if startSec+sampleSec > duration {
			sampleSec = duration - startSec
		}
		if sampleSec < 30 {
			startSec = 0
			sampleSec = duration
		}
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("Audio.go: extractDetectionChunks (mkdir): %w", err)
	}
	filter := monoFilter(cfg, s)
	pattern := filepath.Join(outDir, "det_%03d.wav")
	args := []string{
		"-y", "-v", "error",
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-t", fmt.Sprintf("%.3f", sampleSec),
		"-i", path,
		"-map", fmt.Sprintf("0:a:%d", ordinal),
		"-af", filter,
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "segment", "-segment_time", "30",
		pattern,
	}
	if _, err := runProcess(ctx, ffmpegPath, args...); err != nil {
		return nil, fmt.Errorf("Audio.go: extractDetectionChunks: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "det_*.wav"))
	if err != nil {
		return nil, fmt.Errorf("Audio.go: extractDetectionChunks (glob): %w", err)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, fmt.Errorf("Audio.go: extractDetectionChunks: no sample produced")
	}
	return matches, nil
}

// ----------------------------------------------------------------------------
// Main audio extraction (transcription input)
// ----------------------------------------------------------------------------

// extractMainWAV produces the full-length mono 16 kHz WAV fed to the model, with
// leadinMs of silence prepended ("Anlauf"). The added offset is subtracted again
// in Subtitle.go.
func extractMainWAV(ctx context.Context, cfg *AppConfig, path string, s probeStream, ordinal int, outWav string) error {
	filter := monoFilter(cfg, s)
	if appSettings.leadinMs > 0 {
		filter = fmt.Sprintf("%s,adelay=%d:all=1", filter, appSettings.leadinMs)
	}
	args := []string{
		"-y", "-v", "error",
		"-i", path,
		"-map", fmt.Sprintf("0:a:%d", ordinal),
		"-af", filter,
		"-ac", "1",
		"-c:a", "pcm_s16le",
		outWav,
	}
	if _, err := runProcess(ctx, ffmpegPath, args...); err != nil {
		return fmt.Errorf("Audio.go: extractMainWAV: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Diagnostic mode: split every physical channel into a playable WAV
// ----------------------------------------------------------------------------

func runDiagnostic(ctx context.Context, path string, s probeStream, ordinal int) ([]string, error) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	var written []string
	n := s.Channels
	if n < 1 {
		n = 1
	}
	for k := 0; k < n; k++ {
		out := fmt.Sprintf("%s.ch%02d.wav", base, k)
		args := []string{
			"-y", "-v", "error",
			"-i", path,
			"-map", fmt.Sprintf("0:a:%d", ordinal),
			"-af", fmt.Sprintf("pan=mono|c0=c%d", k),
			"-c:a", "pcm_s16le",
			out,
		}
		if _, err := runProcess(ctx, ffmpegPath, args...); err != nil {
			return written, fmt.Errorf("Audio.go: runDiagnostic (channel %d): %w", k, err)
		}
		written = append(written, out)
	}
	return written, nil
}
