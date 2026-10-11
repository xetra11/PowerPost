package media

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Runner executes ffmpeg and ffprobe.
type Runner struct {
	FFmpeg  string
	FFprobe string
}

// Available reports whether both binaries can be executed.
func (r Runner) Available() error {
	for _, bin := range []string{r.FFmpeg, r.FFprobe} {
		if err := exec.Command(bin, "-version").Run(); err != nil {
			return fmt.Errorf("%s not usable: %w", bin, err)
		}
	}
	return nil
}

// Transcode applies plan to in and writes out atomically. onProgress receives 0..1.
func (r Runner) Transcode(ctx context.Context, in, out string, plan Plan, duration float64, onProgress func(float64)) error {
	tmp := out + ".part"
	args := append([]string{"-progress", "pipe:1", "-nostats"}, plan.Args(in, tmp)...)
	cmd := exec.CommandContext(ctx, r.FFmpeg, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start ffmpeg: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok || onProgress == nil || duration <= 0 {
			continue
		}
		if key == "out_time_us" || key == "out_time_ms" { // both are microseconds
			if us, err := strconv.ParseInt(val, 10, 64); err == nil {
				onProgress(min(float64(us)/1e6/duration, 1))
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg failed: %s", lastLines(stderr.String(), 6))
	}
	return os.Rename(tmp, out)
}

// Thumbnail writes a small JPEG poster frame.
func (r Runner) Thumbnail(ctx context.Context, in, out string, at float64) error {
	cmd := exec.CommandContext(ctx, r.FFmpeg, "-hide_banner", "-nostdin", "-y", "-ss", fmt.Sprintf("%.2f", at), "-i", in,
		"-frames:v", "1", "-vf", "scale=360:-2", "-q:v", "4", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("thumbnail failed: %s", lastLines(string(b), 3))
	}
	return nil
}

// Checksum returns the hex SHA-256 of a file.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// tailBuffer keeps only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
