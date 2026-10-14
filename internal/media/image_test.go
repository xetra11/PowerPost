package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var feed = ImageConstraints{Formats: []string{"jpeg"}, MaxWidth: 1080, MaxHeight: 1350, AspectMin: 0.8, AspectMax: 1.91, MaxBytes: 8 << 20, MaxCount: 10, SameAspect: true}

var gallery = ImageConstraints{Formats: []string{"jpeg", "png", "webp"}, MaxWidth: 4096, MaxHeight: 4096, MaxBytes: 10 << 20, MaxCount: 10}

func png(w, h int, size int64) Info {
	return Info{Container: "png_pipe", VideoCodec: "png", PixFmt: "rgb24", Width: w, Height: h, HasVideo: true, Image: true, Size: size}
}

func TestImagePlanDownscalesAndConverts(t *testing.T) {
	p := NewImagePlan(png(1920, 1080, 2<<20), feed, StrategyBlur, 0)
	if p.VideoCopy || p.Fill || p.Width != 1080 || p.Height != 608 || p.Format != "jpeg" || p.Ext() != ".jpg" {
		t.Fatalf("unexpected plan %+v", p)
	}
	if got := strings.Join(p.Fixes, "; "); got != "Downscaled 1920x1080 → 1080x608; Converted PNG → JPEG" {
		t.Fatalf("fixes %q", got)
	}
}

func TestImagePlanFillsOutsideAspectRange(t *testing.T) {
	// 9:16 is taller than Instagram's 4:5 minimum.
	p := NewImagePlan(png(1080, 1920, 1<<20), feed, StrategyPad, 0)
	if !p.Fill || p.Strategy != StrategyPad || p.Width != 1080 || p.Height != 1350 {
		t.Fatalf("unexpected plan %+v", p)
	}
	args := strings.Join(p.imageArgs("in.png", "out.jpg"), " ")
	if !strings.Contains(args, "pad=1080:1350") || !strings.Contains(args, "-c:v mjpeg") {
		t.Fatalf("args %s", args)
	}
}

func TestImagePlanCarouselUsesFirstAspect(t *testing.T) {
	first := png(1920, 1080, 1<<20)
	aspect := feed.CanvasAspect(first)
	p := NewImagePlan(png(1000, 1000, 1<<20), feed, StrategyCrop, aspect)
	if !p.Fill || p.Width != 1080 || p.Height != 608 {
		t.Fatalf("unexpected plan %+v", p)
	}
}

func TestImagePlanKeepsAcceptedFiles(t *testing.T) {
	if p := NewImagePlan(png(1920, 1080, 2<<20), gallery, StrategyBlur, 0); !p.VideoCopy || p.Format != "png" || len(p.Fixes) != 0 {
		t.Fatalf("PNG for Discord should be kept: %+v", p)
	}
	// Too large for the upload limit: re-encoded as JPEG, even though PNG is accepted.
	if p := NewImagePlan(png(1920, 1080, 12<<20), gallery, StrategyBlur, 0); p.VideoCopy || p.Format != "jpeg" {
		t.Fatalf("oversized PNG should become JPEG: %+v", p)
	}
	// Resized PNG stays PNG where accepted.
	if p := NewImagePlan(png(6000, 3000, 5<<20), gallery, StrategyBlur, 0); p.VideoCopy || p.Format != "png" || p.Width != 4096 || p.Height != 2048 {
		t.Fatalf("resized PNG: %+v", p)
	}
}

func TestImageCheck(t *testing.T) {
	v := feed.Check(png(1080, 1920, 9<<20))
	codes := []string{}
	for _, x := range v {
		codes = append(codes, x.Code)
	}
	if strings.Join(codes, ",") != "IMAGE_FORMAT,RESOLUTION_TOO_HIGH,ASPECT_RATIO,FILE_TOO_LARGE" {
		t.Fatalf("violations %v", v)
	}
	ok := Info{Container: "image2", VideoCodec: "mjpeg", Width: 1080, Height: 1350, Image: true, Size: 1 << 20}
	if v := feed.Check(ok); len(v) != 0 {
		t.Fatalf("unexpected violations %v", v)
	}
}

func TestParseProbeImage(t *testing.T) {
	out := `{"streams":[{"codec_type":"video","codec_name":"mjpeg","width":1920,"height":1080,"pix_fmt":"yuvj420p","avg_frame_rate":"25/1"}],
	"format":{"format_name":"image2","duration":"0.040000","size":"75331"}}`
	i, err := parseProbe([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if !i.Image || i.FPS != 0 || i.Duration != 0 || ImageFormat(i) != "jpeg" || i.Size != 75331 {
		t.Fatalf("unexpected info %+v", i)
	}
	video := `{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`
	if i, _ := parseProbe([]byte(video)); i.Image {
		t.Fatal("video detected as image")
	}
}

// TestConvertImageRealFile runs ffmpeg end to end; it is skipped when ffmpeg is missing.
func TestConvertImageRealFile(t *testing.T) {
	r := Runner{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if r.Available() != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1080x1920", "-frames:v", "1", "-update", "1", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	ctx := context.Background()
	info, err := r.Probe(ctx, src)
	if err != nil || !info.Image {
		t.Fatalf("probe: %v %+v", err, info)
	}
	for _, s := range []Strategy{StrategyBlur, StrategyCrop, StrategyPad} {
		plan := NewImagePlan(info, feed, s, 0)
		out := filepath.Join(dir, "out-"+string(s)+plan.Ext())
		if err := r.ConvertImage(ctx, src, out, plan); err != nil {
			t.Fatal(err)
		}
		got, err := r.Probe(ctx, out)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Image || got.Width != 1080 || got.Height != 1350 || ImageFormat(got) != "jpeg" {
			t.Fatalf("%s: unexpected output %+v", s, got)
		}
		if v := feed.Check(got); len(v) != 0 {
			t.Fatalf("%s: violations %v", s, v)
		}
	}
	// Unchanged images are copied as they are.
	keep := NewImagePlan(info, gallery, StrategyBlur, 0)
	out := filepath.Join(dir, "copy"+keep.Ext())
	if err := r.ConvertImage(ctx, src, out, keep); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(out)
	if string(a) != string(b) {
		t.Fatal("copied image differs")
	}
}
