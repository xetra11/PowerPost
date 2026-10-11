package media

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTranscodeRealFile runs ffmpeg end to end; it is skipped when ffmpeg is missing.
func TestTranscodeRealFile(t *testing.T) {
	r := Runner{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if r.Available() != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=120", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=96000",
		"-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	ctx := context.Background()
	info, err := r.Probe(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	plan := NewPlan(info, reels, StrategyBlur, "libx264")
	out := filepath.Join(dir, "out.mp4")
	var last float64
	if err := r.Transcode(ctx, src, out, plan, info.Duration, func(f float64) { last = f }); err != nil {
		t.Fatal(err)
	}
	got, err := r.Probe(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 1080 || got.Height != 1920 || got.FPS != 60 || got.VideoCodec != "h264" || got.AudioSampleRate != 48000 || got.AudioChannels != 2 {
		t.Fatalf("unexpected output %+v", got)
	}
	if v := reels.Check(got); len(v) != 1 || v[0].Code != "DURATION_TOO_SHORT" { // the 2 s clip is below the 3 s Reels minimum
		t.Fatalf("unexpected violations %v", v)
	}
	if last <= 0 {
		t.Error("no progress reported")
	}
	if err := r.Thumbnail(ctx, out, filepath.Join(dir, "t.jpg"), 0.5); err != nil {
		t.Fatal(err)
	}
}
