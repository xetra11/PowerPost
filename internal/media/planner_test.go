package media

import (
	"slices"
	"strings"
	"testing"
)

var reels = VideoConstraints{
	Containers: []string{"mp4", "mov"}, VideoCodecs: []string{"h264", "hevc"}, AudioCodecs: []string{"aac"},
	MaxWidth: 1920, MaxHeight: 1920, TargetWidth: 1080, TargetHeight: 1920,
	FPSMin: 23, FPSMax: 60, DurationMin: 3, DurationMax: 900, MaxBytes: 300 << 20, MaxBitrateKbps: 25000, AudioSampleRateMax: 48000,
}

func compliant() Info {
	return Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", Width: 1080, Height: 1920, FPS: 30, Duration: 20, VideoCodec: "h264", PixFmt: "yuv420p",
		Bitrate: 8_000_000, HasVideo: true, HasAudio: true, AudioCodec: "aac", AudioSampleRate: 48000, AudioChannels: 2, Size: 20 << 20}
}

func TestPlanCompliantFileIsOnlyRemuxed(t *testing.T) {
	p := NewPlan(compliant(), reels, StrategyBlur, "libx264", nil)
	if !p.VideoCopy || p.Audio != "copy" || len(p.Fixes) != 0 {
		t.Fatalf("expected copy without fixes, got %+v", p)
	}
	args := strings.Join(p.Args("in.mp4", "out.mp4"), " ")
	if !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "+faststart") {
		t.Fatalf("unexpected args: %s", args)
	}
}

func TestPlan4KLandscapeGameplay(t *testing.T) {
	src := compliant()
	src.Width, src.Height, src.FPS, src.Bitrate = 3840, 2160, 120, 80_000_000
	p := NewPlan(src, reels, StrategyBlur, "libx264", nil)
	if p.VideoCopy || !p.Fill || p.Width != 1080 || p.Height != 1920 || p.FPS != 60 {
		t.Fatalf("unexpected plan %+v", p)
	}
	if p.Audio != "aac" {
		t.Fatalf("audio should be normalized when re-encoding, got %q", p.Audio)
	}
	args := strings.Join(p.Args("in.mp4", "out.mp4"), " ")
	for _, want := range []string{"split=2", "boxblur", "overlay", "fps=60", "format=yuv420p", "libx264", "-maxrate 10000k"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
}

func TestPlanStrategies(t *testing.T) {
	src := compliant()
	src.Width, src.Height = 1920, 1080
	for s, want := range map[Strategy]string{StrategyCrop: "crop=1080:1920", StrategyPad: "pad=1080:1920", StrategyBlur: "boxblur"} {
		args := strings.Join(NewPlan(src, reels, s, "libx264", nil).Args("i", "o"), " ")
		if !strings.Contains(args, want) {
			t.Errorf("%s: args missing %q", s, want)
		}
	}
}

func TestPlanVerticalTooLargeIsDownscaledWithoutFill(t *testing.T) {
	src := compliant()
	src.Width, src.Height = 2160, 3840
	p := NewPlan(src, reels, StrategyBlur, "h264_nvenc", nil)
	if p.Fill || p.Width != 1080 || p.Height != 1920 {
		t.Fatalf("unexpected plan %+v", p)
	}
	if !slices.ContainsFunc(p.Fixes, func(f string) bool { return strings.HasPrefix(f, "Downscaled") }) {
		t.Fatalf("missing downscale fix: %v", p.Fixes)
	}
	if !strings.Contains(strings.Join(p.Args("i", "o"), " "), "h264_nvenc") {
		t.Fatal("encoder not used")
	}
}

func TestPlanCodecAndAudioFixes(t *testing.T) {
	src := compliant()
	src.Container, src.VideoCodec, src.PixFmt = "matroska,webm", "vp9", "yuv420p10le"
	src.AudioCodec, src.AudioSampleRate = "opus", 48000
	p := NewPlan(src, reels, StrategyBlur, "libx264", nil)
	if p.VideoCopy || p.Audio != "aac" || len(p.Fixes) != 3 {
		t.Fatalf("unexpected plan %+v", p)
	}
}

func TestPlanNoAudio(t *testing.T) {
	src := compliant()
	src.HasAudio, src.AudioCodec = false, ""
	p := NewPlan(src, reels, StrategyBlur, "libx264", nil)
	if p.Audio != "none" || !slices.Contains(p.Args("i", "o"), "-an") {
		t.Fatalf("expected no audio, got %+v", p)
	}
}

func TestHashIgnoresDescriptions(t *testing.T) {
	a := NewPlan(compliant(), reels, StrategyBlur, "libx264", nil)
	b := a
	b.Fixes = []string{"something else"}
	if a.Hash() != b.Hash() {
		t.Fatal("hash must only depend on operations")
	}
	c := a
	c.FPS = 30
	if a.Hash() == c.Hash() {
		t.Fatal("hash must change with operations")
	}
}

func TestCheckFindsViolations(t *testing.T) {
	bad := compliant()
	bad.Width, bad.Height, bad.FPS, bad.Duration, bad.Size = 3840, 2160, 120, 1200, 400<<20
	codes := map[string]bool{}
	for _, v := range reels.Check(bad) {
		codes[v.Code] = true
	}
	for _, want := range []string{"RESOLUTION_TOO_HIGH", "FRAME_RATE", "DURATION_TOO_LONG", "FILE_TOO_LARGE"} {
		if !codes[want] {
			t.Errorf("missing %s in %v", want, codes)
		}
	}
	if v := reels.Check(compliant()); len(v) != 0 {
		t.Fatalf("compliant file reported %v", v)
	}
	// 59.94 fps must pass a 60 fps limit.
	ntsc := compliant()
	ntsc.FPS = 59.94
	if v := reels.Check(ntsc); len(v) != 0 {
		t.Fatalf("59.94 fps rejected: %v", v)
	}
}

func TestParseProbeRotation(t *testing.T) {
	out := `{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"30000/1001",
	"side_data_list":[{"rotation":-90}]},{"codec_type":"audio","codec_name":"aac","sample_rate":"44100","channels":2}],
	"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.5","size":"1000","bit_rate":"640"}}`
	i, err := parseProbe([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if i.Width != 1080 || i.Height != 1920 || i.FPS != 29.97 || i.AudioSampleRate != 44100 || i.Duration != 12.5 {
		t.Fatalf("unexpected info %+v", i)
	}
}

func TestPlanSmallSizeLimitCapsBitrate(t *testing.T) {
	discord := reels
	discord.MaxBytes = 10 << 20
	src := compliant() // 20 s, 20 MB
	p := NewPlan(src, discord, StrategyBlur, "libx264", nil)
	// 10 MiB * 8 * 0.9 / 20 s = 3774 kbps, minus 160 kbps audio.
	if p.VideoCopy || p.MaxrateKbps != 3614 {
		t.Fatalf("unexpected plan %+v", p)
	}
	if !strings.Contains(strings.Join(p.Args("i", "o"), " "), "-maxrate 3614k") {
		t.Error("maxrate not passed to ffmpeg")
	}

	// A large limit leaves the default cap alone (Instagram/TikTok plans are unchanged).
	src.Width, src.Height = 1920, 1080
	if p := NewPlan(src, reels, StrategyBlur, "libx264", nil); p.MaxrateKbps != defaultMaxrateKbps {
		t.Errorf("maxrate %d, want %d", p.MaxrateKbps, defaultMaxrateKbps)
	}
}
