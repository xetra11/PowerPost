package media

import (
	"fmt"
	"slices"
	"strings"
)

// VideoConstraints are a platform's limits for video uploads, plus the canvas we convert to.
type VideoConstraints struct {
	Containers         []string `json:"containers"`  // matched against ffprobe format_name parts
	VideoCodecs        []string `json:"videoCodecs"` // ffprobe codec names: h264, hevc, ...
	AudioCodecs        []string `json:"audioCodecs"`
	MaxWidth           int      `json:"maxWidth"`
	MaxHeight          int      `json:"maxHeight"`
	TargetWidth        int      `json:"targetWidth"` // canvas the converter produces
	TargetHeight       int      `json:"targetHeight"`
	AspectMin          float64  `json:"aspectMin"` // width / height
	AspectMax          float64  `json:"aspectMax"`
	FPSMin             float64  `json:"fpsMin"`
	FPSMax             float64  `json:"fpsMax"`
	DurationMin        float64  `json:"durationMin"` // seconds
	DurationMax        float64  `json:"durationMax"`
	MaxBytes           int64    `json:"maxBytes"`
	MaxBitrateKbps     int      `json:"maxBitrateKbps,omitempty"`
	AudioSampleRateMax int      `json:"audioSampleRateMax,omitempty"`
}

// Violation is a hard-limit problem found in a (converted) file.
type Violation struct {
	Code    string
	Message string
}

// fpsTolerance lets 59.94 pass a 60 fps maximum and 23.976 pass a 23 fps minimum.
const fpsTolerance = 0.5

func (c VideoConstraints) containerOK(i Info) bool {
	parts := strings.Split(i.Container, ",")
	for _, want := range c.Containers {
		if slices.Contains(parts, want) {
			return true
		}
	}
	return false
}

func (c VideoConstraints) videoCodecOK(i Info) bool {
	return slices.Contains(c.VideoCodecs, i.VideoCodec) && (i.PixFmt == "yuv420p" || i.PixFmt == "yuvj420p")
}

func (c VideoConstraints) audioOK(i Info) bool {
	if !i.HasAudio {
		return true
	}
	if !slices.Contains(c.AudioCodecs, i.AudioCodec) {
		return false
	}
	if c.AudioSampleRateMax > 0 && i.AudioSampleRate > c.AudioSampleRateMax {
		return false
	}
	return i.AudioChannels <= 2
}

func (c VideoConstraints) fpsOK(i Info) bool {
	if i.FPS == 0 {
		return true // unknown (variable frame rate); let the platform decide
	}
	return i.FPS >= c.FPSMin-fpsTolerance && i.FPS <= c.FPSMax+fpsTolerance
}

func (c VideoConstraints) bitrateOK(i Info) bool {
	return c.MaxBitrateKbps == 0 || i.Bitrate/1000 <= int64(c.MaxBitrateKbps)
}

// DurationViolations checks length limits. These cannot be fixed by conversion.
func (c VideoConstraints) DurationViolations(i Info) []Violation {
	var v []Violation
	if c.DurationMax > 0 && i.Duration > c.DurationMax {
		v = append(v, Violation{"DURATION_TOO_LONG", fmt.Sprintf("Video is %s long; maximum is %s. Trim it before posting.", FormatDuration(i.Duration), FormatDuration(c.DurationMax))})
	}
	if c.DurationMin > 0 && i.Duration < c.DurationMin {
		v = append(v, Violation{"DURATION_TOO_SHORT", fmt.Sprintf("Video is %s long; minimum is %s.", FormatDuration(i.Duration), FormatDuration(c.DurationMin))})
	}
	return v
}

// Check validates a file against every hard limit.
func (c VideoConstraints) Check(i Info) []Violation {
	v := c.DurationViolations(i)
	if !c.containerOK(i) {
		v = append(v, Violation{"CONTAINER", fmt.Sprintf("Container %q is not accepted (allowed: %s).", i.Container, strings.Join(c.Containers, ", "))})
	}
	if !c.videoCodecOK(i) {
		v = append(v, Violation{"VIDEO_CODEC", fmt.Sprintf("Video codec %s/%s is not accepted.", i.VideoCodec, i.PixFmt)})
	}
	if !c.audioOK(i) {
		v = append(v, Violation{"AUDIO", fmt.Sprintf("Audio %s %d Hz %dch is not accepted.", i.AudioCodec, i.AudioSampleRate, i.AudioChannels)})
	}
	if (c.MaxWidth > 0 && i.Width > c.MaxWidth) || (c.MaxHeight > 0 && i.Height > c.MaxHeight) {
		v = append(v, Violation{"RESOLUTION_TOO_HIGH", fmt.Sprintf("Resolution %dx%d exceeds %dx%d.", i.Width, i.Height, c.MaxWidth, c.MaxHeight)})
	}
	if a := i.Aspect(); (c.AspectMin > 0 && a < c.AspectMin-0.005) || (c.AspectMax > 0 && a > c.AspectMax+0.005) {
		v = append(v, Violation{"ASPECT_RATIO", fmt.Sprintf("Aspect ratio %.3f is outside %.3f–%.3f.", a, c.AspectMin, c.AspectMax)})
	}
	if !c.fpsOK(i) {
		v = append(v, Violation{"FRAME_RATE", fmt.Sprintf("Frame rate %.2f fps is outside %g–%g fps.", i.FPS, c.FPSMin, c.FPSMax)})
	}
	if c.MaxBytes > 0 && i.Size > c.MaxBytes {
		v = append(v, Violation{"FILE_TOO_LARGE", fmt.Sprintf("File is %s; maximum is %s.", FormatBytes(i.Size), FormatBytes(c.MaxBytes))})
	}
	if !c.bitrateOK(i) {
		v = append(v, Violation{"BITRATE_TOO_HIGH", fmt.Sprintf("Bitrate %d kbps exceeds %d kbps.", i.Bitrate/1000, c.MaxBitrateKbps)})
	}
	return v
}

func FormatDuration(sec float64) string {
	s := int(sec + 0.5)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

func FormatBytes(n int64) string {
	const mb = 1 << 20
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/mb)
}
