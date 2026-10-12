package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

// Strategy decides how a video with a different aspect ratio fills the target canvas.
type Strategy string

const (
	StrategyBlur Strategy = "blur" // blurred copy of the video behind it (default for gameplay)
	StrategyCrop Strategy = "crop" // fill the canvas, cut off the sides
	StrategyPad  Strategy = "pad"  // black bars
)

func (s Strategy) Valid() bool {
	return s == StrategyBlur || s == StrategyCrop || s == StrategyPad
}

func (s Strategy) label() string {
	switch s {
	case StrategyCrop:
		return "center crop"
	case StrategyPad:
		return "black bars"
	default:
		return "blurred background"
	}
}

// Plan is the minimal set of operations that makes a file fit a platform.
type Plan struct {
	VideoCopy   bool     `json:"videoCopy"` // true: no video re-encode, just remux
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Fill        bool     `json:"fill"` // aspect differs: apply Strategy
	Strategy    Strategy `json:"strategy,omitempty"`
	FPS         float64  `json:"fps,omitempty"` // 0 = keep source rate
	Audio       string   `json:"audio"`         // copy | aac | none
	Encoder     string   `json:"encoder,omitempty"`
	MaxrateKbps int      `json:"maxrateKbps,omitempty"`
	Fixes       []string `json:"fixes"` // human-readable list of what changes
}

// Hash identifies the output (operations only, not the descriptions).
// Two targets with the same hash share one file and one encode.
func (p Plan) Hash() string {
	p.Fixes = nil
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

const defaultMaxrateKbps = 10000

// NewPlan compares a source file against constraints.
func NewPlan(src Info, c VideoConstraints, s Strategy, encoder string) Plan {
	if !s.Valid() {
		s = StrategyBlur
	}
	p := Plan{VideoCopy: true, Width: src.Width, Height: src.Height, Audio: "copy", Fixes: []string{}}

	targetAspect := float64(c.TargetWidth) / float64(c.TargetHeight)
	aspectMatch := math.Abs(src.Aspect()-targetAspect)/targetAspect < 0.01
	sizeOK := aspectMatch && src.Width <= c.TargetWidth && src.Height <= c.TargetHeight

	if !sizeOK {
		p.VideoCopy = false
		p.Width, p.Height = c.TargetWidth, c.TargetHeight
		if aspectMatch {
			p.Fixes = append(p.Fixes, fmt.Sprintf("Downscaled %dx%d → %dx%d", src.Width, src.Height, p.Width, p.Height))
		} else {
			p.Fill, p.Strategy = true, s
			p.Fixes = append(p.Fixes, fmt.Sprintf("Reframed %dx%d → %dx%d (%s)", src.Width, src.Height, p.Width, p.Height, s.label()))
		}
	}
	if src.FPS > 0 && src.FPS > c.FPSMax+fpsTolerance {
		p.VideoCopy = false
		p.FPS = c.FPSMax
		p.Fixes = append(p.Fixes, fmt.Sprintf("Frame rate %g → %g fps", src.FPS, c.FPSMax))
	} else if src.FPS > 0 && src.FPS < c.FPSMin-fpsTolerance {
		p.VideoCopy = false
		p.FPS = c.FPSMin
		p.Fixes = append(p.Fixes, fmt.Sprintf("Frame rate %g → %g fps", src.FPS, c.FPSMin))
	}
	if !c.videoCodecOK(src) {
		p.VideoCopy = false
		p.Fixes = append(p.Fixes, fmt.Sprintf("Re-encoded %s (%s) → H.264", src.VideoCodec, src.PixFmt))
	}
	if !c.bitrateOK(src) {
		p.VideoCopy = false
		p.Fixes = append(p.Fixes, fmt.Sprintf("Bitrate %d → ≤%d kbps", src.Bitrate/1000, c.MaxBitrateKbps))
	}
	if c.MaxBytes > 0 && src.Size > c.MaxBytes {
		p.VideoCopy = false
		p.Fixes = append(p.Fixes, fmt.Sprintf("Compressed %s to fit %s", FormatBytes(src.Size), FormatBytes(c.MaxBytes)))
	}
	if !c.containerOK(src) {
		p.Fixes = append(p.Fixes, fmt.Sprintf("Container %s → MP4", src.Container))
	}

	switch {
	case !src.HasAudio:
		p.Audio = "none"
	case !c.audioOK(src):
		p.Audio = "aac"
		p.Fixes = append(p.Fixes, fmt.Sprintf("Audio %s %d Hz → AAC 48 kHz stereo", src.AudioCodec, src.AudioSampleRate))
	case !p.VideoCopy:
		// Re-encoding anyway: normalize audio too, so platforms can share one file.
		p.Audio = "aac"
	}

	if !p.VideoCopy {
		p.Encoder = encoder
		p.MaxrateKbps = defaultMaxrateKbps
		if c.MaxBitrateKbps > 0 && c.MaxBitrateKbps < p.MaxrateKbps {
			p.MaxrateKbps = c.MaxBitrateKbps
		}
		// A small file limit (e.g. Discord's 10 MB) caps the bitrate so the encode fits.
		if b := sizeBudgetKbps(src, c, p.Audio); b > 0 && b < p.MaxrateKbps {
			p.MaxrateKbps = b
			p.Fixes = append(p.Fixes, fmt.Sprintf("Video bitrate ≤%d kbps to stay under %s", b, FormatBytes(c.MaxBytes)))
		}
	}
	return p
}

// minBudgetKbps is the floor for size-capped encodes; below it the video is
// unwatchable and the size check reports the file as too large instead.
const minBudgetKbps = 300

// sizeBudgetKbps is the video bitrate that keeps the output under MaxBytes
// (with 10% headroom for container overhead and rate-control drift), or 0.
func sizeBudgetKbps(src Info, c VideoConstraints, audio string) int {
	if c.MaxBytes <= 0 || src.Duration <= 0 {
		return 0
	}
	total := int(float64(c.MaxBytes) * 8 * 0.9 / src.Duration / 1000)
	if audio == "aac" {
		total -= 160
	}
	return max(total, minBudgetKbps)
}

// Args builds the ffmpeg command line (without the binary name).
func (p Plan) Args(in, out string) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", in}

	if p.VideoCopy {
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
	} else {
		w, h := p.Width, p.Height
		var graph string
		switch {
		case p.Fill && p.Strategy == StrategyCrop:
			graph = fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h)
		case p.Fill && p.Strategy == StrategyPad:
			graph = fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h, w, h)
		case p.Fill:
			graph = fmt.Sprintf("[0:v]split=2[bgsrc][fgsrc];"+
				"[bgsrc]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=20:2[bg];"+
				"[fgsrc]scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2[fg];"+
				"[bg][fg]overlay=(main_w-overlay_w)/2:(main_h-overlay_h)/2", w, h, w, h, w, h)
		default:
			graph = fmt.Sprintf("[0:v]scale=%d:%d", w&^1, h&^1)
		}
		graph += ",setsar=1"
		if p.FPS > 0 {
			graph += fmt.Sprintf(",fps=%g", p.FPS)
		}
		graph += ",format=yuv420p[v]"
		args = append(args, "-filter_complex", graph, "-map", "[v]")
		args = append(args, encoderArgs(p.Encoder, p.MaxrateKbps)...)
	}

	switch p.Audio {
	case "none":
		args = append(args, "-an")
	case "aac":
		args = append(args, "-map", "0:a:0", "-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2")
	default:
		args = append(args, "-map", "0:a:0", "-c:a", "copy")
	}
	return append(args, "-movflags", "+faststart", "-f", "mp4", out)
}

func encoderArgs(encoder string, maxrate int) []string {
	rate := []string{"-maxrate", fmt.Sprintf("%dk", maxrate), "-bufsize", fmt.Sprintf("%dk", maxrate*2)}
	if encoder == "h264_nvenc" {
		return append([]string{"-c:v", "h264_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "21", "-b:v", "0", "-profile:v", "high"}, rate...)
	}
	return append([]string{"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-profile:v", "high"}, rate...)
}
