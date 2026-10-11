// Package media probes, plans and converts media files with ffprobe/ffmpeg.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Info describes a media file as the viewer sees it (rotation already applied).
type Info struct {
	Container       string  `json:"container"` // ffprobe format_name, e.g. "mov,mp4,m4a,3gp,3g2,mj2"
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	FPS             float64 `json:"fps"`
	Duration        float64 `json:"duration"` // seconds
	VideoCodec      string  `json:"videoCodec"`
	PixFmt          string  `json:"pixFmt"`
	Bitrate         int64   `json:"bitrate"` // bits per second, whole file
	HasVideo        bool    `json:"hasVideo"`
	HasAudio        bool    `json:"hasAudio"`
	AudioCodec      string  `json:"audioCodec,omitempty"`
	AudioSampleRate int     `json:"audioSampleRate,omitempty"`
	AudioChannels   int     `json:"audioChannels,omitempty"`
	Size            int64   `json:"size"` // bytes
}

func (i Info) Aspect() float64 {
	if i.Height == 0 {
		return 0
	}
	return float64(i.Width) / float64(i.Height)
}

type probeOutput struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		PixFmt       string `json:"pix_fmt"`
		AvgFrameRate string `json:"avg_frame_rate"`
		RFrameRate   string `json:"r_frame_rate"`
		SampleRate   string `json:"sample_rate"`
		Channels     int    `json:"channels"`
		Tags         struct {
			Rotate string `json:"rotate"`
		} `json:"tags"`
		SideDataList []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// Probe runs ffprobe on path.
func (r Runner) Probe(ctx context.Context, path string) (Info, error) {
	cmd := exec.CommandContext(ctx, r.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return Info{}, fmt.Errorf("ffprobe failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return Info{}, fmt.Errorf("ffprobe failed: %w", err)
	}
	return parseProbe(out)
}

func parseProbe(data []byte) (Info, error) {
	var p probeOutput
	if err := json.Unmarshal(data, &p); err != nil {
		return Info{}, fmt.Errorf("cannot parse ffprobe output: %w", err)
	}
	info := Info{Container: p.Format.FormatName}
	info.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	info.Size, _ = strconv.ParseInt(p.Format.Size, 10, 64)
	info.Bitrate, _ = strconv.ParseInt(p.Format.BitRate, 10, 64)

	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if info.HasVideo || s.Disposition.AttachedPic == 1 {
				continue // first real video stream only; skip cover art
			}
			info.HasVideo = true
			info.VideoCodec = s.CodecName
			info.PixFmt = s.PixFmt
			info.Width, info.Height = s.Width, s.Height
			if rotation(s.Tags.Rotate, s.SideDataList) {
				info.Width, info.Height = info.Height, info.Width
			}
			info.FPS = parseRate(s.AvgFrameRate)
			if info.FPS == 0 {
				info.FPS = parseRate(s.RFrameRate)
			}
		case "audio":
			if info.HasAudio {
				continue
			}
			info.HasAudio = true
			info.AudioCodec = s.CodecName
			info.AudioSampleRate, _ = strconv.Atoi(s.SampleRate)
			info.AudioChannels = s.Channels
		}
	}
	if !info.HasVideo {
		return info, fmt.Errorf("file has no video stream")
	}
	return info, nil
}

// rotation reports whether the stream is rotated by ±90°, i.e. width and height swap on display.
func rotation(tag string, side []struct {
	Rotation float64 `json:"rotation"`
}) bool {
	deg := 0.0
	if tag != "" {
		deg, _ = strconv.ParseFloat(tag, 64)
	}
	for _, sd := range side {
		if sd.Rotation != 0 {
			deg = sd.Rotation
		}
	}
	return math.Mod(math.Abs(deg), 180) == 90
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return math.Round(n/d*1000) / 1000
}
