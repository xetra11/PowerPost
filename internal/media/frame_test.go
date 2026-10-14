package media

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFramePlacement(t *testing.T) {
	// 1920x1080 on 1080x1920: fit = 1080x608, centered.
	p := Frame{Scale: 1, X: 0.5, Y: 0.5}.place(1920, 1080, 1080, 1920)
	if p != (Placement{CropX: 0, CropY: 0, CropW: 1920, CropH: 1080, X: 0, Y: 656, W: 1080, H: 608}) {
		t.Fatalf("fit: %+v", p)
	}
	// Zoomed 2x and moved left: the right half of the source is visible, full width.
	p = Frame{Scale: 2, X: 0, Y: 0.5}.place(1920, 1080, 1080, 1920)
	if p.X != 0 || p.W != 1080 || p.CropX != 960 || p.CropW != 960 || p.CropH != 1080 {
		t.Fatalf("zoomed: %+v", p)
	}
	// Dragged far off the canvas: a tenth stays visible.
	p = Frame{Scale: 1, X: 5, Y: 0.5}.place(1920, 1080, 1080, 1920)
	if p.X != 972 || p.W != 108 || p.CropX != 0 || p.CropW != 192 {
		t.Fatalf("clamped: %+v", p)
	}
	if f := (Frame{Scale: 100, X: 0.5, Y: 0.5}).Normalize(); f.Scale != MaxFrameScale {
		t.Fatalf("normalize: %+v", f)
	}
}

func TestFramedPlans(t *testing.T) {
	src := compliant() // already 1080x1920 H.264: copied unless framed
	if p := NewPlan(src, reels, StrategyBlur, "libx264", nil); !p.VideoCopy {
		t.Fatal("compliant video should be copied")
	}
	p := NewPlan(src, reels, StrategyPad, "libx264", &Frame{Scale: 0.8, X: 0.5, Y: 0.4})
	if p.VideoCopy || p.Place == nil || p.Encoder != "libx264" || p.Audio != "aac" || len(p.Fixes) != 1 {
		t.Fatalf("framed video: %+v", p)
	}
	args := strings.Join(p.Args("i", "o"), " ")
	if !strings.Contains(args, "drawbox=c=black:t=fill") || !strings.Contains(args, "overlay=") {
		t.Fatalf("framed video args: %s", args)
	}
	a := NewPlan(src, reels, StrategyBlur, "libx264", &Frame{Scale: 1.2, X: 0.5, Y: 0.5})
	b := NewPlan(src, reels, StrategyBlur, "libx264", &Frame{Scale: 1.3, X: 0.5, Y: 0.5})
	if a.Hash() == b.Hash() {
		t.Fatal("different frames must give different outputs")
	}

	// An image framed to 9:16 on a platform without aspect limits.
	gallery := ImageConstraints{Formats: []string{"jpeg", "webp"}, MaxWidth: 1080, MaxHeight: 1920}
	ip := NewImagePlan(Info{VideoCodec: "webp", Width: 1600, Height: 1200, Image: true}, gallery, StrategyBlur, 9.0/16, &Frame{Aspect: 9.0 / 16, Scale: 1.5, X: 0.5, Y: 0.5})
	if ip.VideoCopy || ip.Width != 1080 || ip.Height != 1920 || ip.Format != "jpeg" || ip.Place == nil {
		t.Fatalf("framed image: %+v", ip)
	}
}

// TestFramedTranscodeRealFile runs a framed video and image through ffmpeg.
func TestFramedTranscodeRealFile(t *testing.T) {
	r := Runner{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if r.Available() != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	ctx := context.Background()
	src := filepath.Join(dir, "src.mp4")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-t", "1", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	info, err := r.Probe(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Frame{{Scale: 2.5, X: 0.2, Y: 0.6}, {Scale: 0.5, X: 0.9, Y: 0.1}} {
		out := filepath.Join(dir, "out.mp4")
		if err := r.Transcode(ctx, src, out, NewPlan(info, reels, StrategyBlur, "libx264", &f), info.Duration, nil); err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		if got, err := r.Probe(ctx, out); err != nil || got.Width != 1080 || got.Height != 1920 {
			t.Fatalf("%+v: %+v %v", f, got, err)
		}
	}

	img := filepath.Join(dir, "src.png")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1600x1200", "-frames:v", "1", img).CombinedOutput(); err != nil {
		t.Fatalf("generate image: %v %s", err, out)
	}
	if info, err = r.Probe(ctx, img); err != nil {
		t.Fatal(err)
	}
	gallery := ImageConstraints{Formats: []string{"jpeg"}, MaxWidth: 1080, MaxHeight: 1920}
	out := filepath.Join(dir, "out.jpg")
	if err := r.ConvertImage(ctx, img, out, NewImagePlan(info, gallery, StrategyPad, 9.0/16, &Frame{Scale: 1.4, X: 0.5, Y: 0.5})); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Probe(ctx, out); err != nil || got.Width != 1080 || got.Height != 1920 {
		t.Fatalf("image: %+v %v", got, err)
	}
}
