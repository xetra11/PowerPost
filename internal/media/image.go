package media

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// ImageConstraints are a platform's limits for image posts (one image, or
// several shown as a carousel).
type ImageConstraints struct {
	Formats   []string `json:"formats"`  // accepted file formats: jpeg, png, webp
	MaxWidth  int      `json:"maxWidth"` // larger images are scaled down to fit MaxWidth x MaxHeight
	MaxHeight int      `json:"maxHeight"`
	AspectMin float64  `json:"aspectMin,omitempty"` // width / height; 0 = any
	AspectMax float64  `json:"aspectMax,omitempty"`
	MaxBytes  int64    `json:"maxBytes"` // per image
	MaxCount  int      `json:"maxCount"` // images per post
	// SameAspect: every image of a carousel is fitted to the first image's
	// aspect ratio (the platform would crop them to it otherwise).
	SameAspect bool `json:"sameAspect,omitempty"`
	// NeedsPublicURL: the platform downloads the images itself, so publishing
	// needs the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL).
	NeedsPublicURL bool `json:"needsPublicUrl,omitempty"`
}

// ImageFormat names the file format of a still image: jpeg, png, webp, …
func ImageFormat(i Info) string {
	if i.VideoCodec == "mjpeg" {
		return "jpeg"
	}
	return i.VideoCodec
}

// CanvasAspect is the aspect ratio an image is fitted to: its own, clamped to
// the platform's range. For SameAspect platforms pass the post's first image.
func (c ImageConstraints) CanvasAspect(first Info) float64 {
	a := first.Aspect()
	if c.AspectMin > 0 && a < c.AspectMin {
		a = c.AspectMin
	}
	if c.AspectMax > 0 && a > c.AspectMax {
		a = c.AspectMax
	}
	return a
}

// NewImagePlan fits a still image into constraints. aspect is the canvas
// aspect ratio (see CanvasAspect); 0 uses the image's own, clamped.
func NewImagePlan(src Info, c ImageConstraints, s Strategy, aspect float64) Plan {
	if !s.Valid() {
		s = StrategyBlur
	}
	if aspect <= 0 {
		aspect = c.CanvasAspect(src)
	}
	in := ImageFormat(src)
	p := Plan{Image: true, VideoCopy: true, Format: in, Width: src.Width, Height: src.Height, Fixes: []string{}}

	if math.Abs(src.Aspect()-aspect)/aspect >= 0.01 {
		p.VideoCopy = false
		p.Fill, p.Strategy = true, s
		p.Width, p.Height = fitBox(aspect, c.MaxWidth, c.MaxHeight)
		p.Fixes = append(p.Fixes, fmt.Sprintf("Reframed %dx%d → %dx%d (%s)", src.Width, src.Height, p.Width, p.Height, s.label()))
	} else if (c.MaxWidth > 0 && src.Width > c.MaxWidth) || (c.MaxHeight > 0 && src.Height > c.MaxHeight) {
		p.VideoCopy = false
		p.Width, p.Height = fitBox(src.Aspect(), c.MaxWidth, c.MaxHeight)
		p.Fixes = append(p.Fixes, fmt.Sprintf("Downscaled %dx%d → %dx%d", src.Width, src.Height, p.Width, p.Height))
	}

	accepted := slices.Contains(c.Formats, in)
	tooBig := c.MaxBytes > 0 && src.Size > c.MaxBytes
	if !accepted || tooBig {
		p.VideoCopy = false
	}
	if !p.VideoCopy {
		// Keep PNG lossless where the platform takes it (pixel art, UI
		// screenshots); everything else becomes JPEG.
		if in != "png" || !accepted || tooBig {
			p.Format = "jpeg"
		}
		if p.Format != in {
			p.Fixes = append(p.Fixes, fmt.Sprintf("Converted %s → %s", strings.ToUpper(in), strings.ToUpper(p.Format)))
		}
		if tooBig {
			p.Fixes = append(p.Fixes, fmt.Sprintf("Compressed %s to fit %s", FormatBytes(src.Size), FormatBytes(c.MaxBytes)))
		}
	}
	return p
}

// fitBox returns the largest even size with the given aspect ratio inside w x h.
func fitBox(aspect float64, w, h int) (int, int) {
	ow, oh := w, int(math.Round(float64(w)/aspect))
	if h > 0 && oh > h {
		ow, oh = int(math.Round(float64(h)*aspect)), h
	}
	return ow &^ 1, oh &^ 1
}

// Ext is the file extension of the plan's output.
func (p Plan) Ext() string {
	switch {
	case !p.Image:
		return ".mp4"
	case p.Format == "jpeg":
		return ".jpg"
	default:
		return "." + p.Format
	}
}

// Check validates an image against every hard limit.
func (c ImageConstraints) Check(i Info) []Violation {
	var v []Violation
	if f := ImageFormat(i); !slices.Contains(c.Formats, f) {
		v = append(v, Violation{"IMAGE_FORMAT", fmt.Sprintf("Format %s is not accepted (allowed: %s).", strings.ToUpper(f), strings.ToUpper(strings.Join(c.Formats, ", ")))})
	}
	if (c.MaxWidth > 0 && i.Width > c.MaxWidth) || (c.MaxHeight > 0 && i.Height > c.MaxHeight) {
		v = append(v, Violation{"RESOLUTION_TOO_HIGH", fmt.Sprintf("Resolution %dx%d exceeds %dx%d.", i.Width, i.Height, c.MaxWidth, c.MaxHeight)})
	}
	if a := i.Aspect(); (c.AspectMin > 0 && a < c.AspectMin-0.005) || (c.AspectMax > 0 && a > c.AspectMax+0.005) {
		v = append(v, Violation{"ASPECT_RATIO", fmt.Sprintf("Aspect ratio %.3f is outside %.3f–%.3f.", a, c.AspectMin, c.AspectMax)})
	}
	if c.MaxBytes > 0 && i.Size > c.MaxBytes {
		v = append(v, Violation{"FILE_TOO_LARGE", fmt.Sprintf("Image is %s; maximum is %s.", FormatBytes(i.Size), FormatBytes(c.MaxBytes))})
	}
	return v
}

// imageArgs builds the ffmpeg command line for an image plan (without the binary name).
func (p Plan) imageArgs(in, out string) []string {
	graph := p.fitGraph() + ",setsar=1"
	codec := []string{"-c:v", "png"}
	if p.Format == "jpeg" {
		graph += ",format=yuvj420p"
		codec = []string{"-c:v", "mjpeg", "-q:v", "2"}
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", in, "-filter_complex", graph + "[v]", "-map", "[v]", "-frames:v", "1"}
	args = append(args, codec...)
	return append(args, "-f", "image2", "-update", "1", out)
}

// ConvertImage applies an image plan to in and writes out atomically.
// Plans without changes copy the file.
func (r Runner) ConvertImage(ctx context.Context, in, out string, plan Plan) error {
	tmp := out + ".part"
	if plan.VideoCopy {
		if err := copyFile(in, tmp); err != nil {
			return err
		}
		return os.Rename(tmp, out)
	}
	if b, err := exec.CommandContext(ctx, r.FFmpeg, plan.imageArgs(in, tmp)...).CombinedOutput(); err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("image conversion failed: %s", lastLines(string(b), 6))
	}
	return os.Rename(tmp, out)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
