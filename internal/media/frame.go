package media

import (
	"fmt"
	"math"
)

// Frame is a hand-made framing of one media file for one platform: how large
// and where it sits on the platform's canvas. Uncovered canvas is filled by
// the post's strategy (blurred copy for "blur", black otherwise).
type Frame struct {
	// Aspect is the canvas width / height for image posts (clamped to the
	// platform's range); 0 keeps the automatic one. Videos always use the
	// platform's canvas.
	Aspect float64 `json:"aspect,omitempty"`
	// Scale is relative to the size at which the whole media just fits the
	// canvas: 1 = fit, larger zooms in (crops), smaller leaves more border.
	Scale float64 `json:"scale"`
	// X and Y are the center of the media on the canvas, in canvas widths and
	// heights (0.5, 0.5 = centered).
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

const (
	MinFrameScale = 0.25
	MaxFrameScale = 8
)

// Normalize clamps the values into their valid ranges.
func (f Frame) Normalize() Frame {
	if f.Scale <= 0 || math.IsNaN(f.Scale) {
		f.Scale = 1
	}
	f.Scale = math.Min(math.Max(f.Scale, MinFrameScale), MaxFrameScale)
	if math.IsNaN(f.X) {
		f.X = 0.5
	}
	if math.IsNaN(f.Y) {
		f.Y = 0.5
	}
	if f.Aspect < 0 || math.IsNaN(f.Aspect) {
		f.Aspect = 0
	}
	return f
}

// Placement is a Frame resolved to pixels: the part of the source that stays
// visible (Crop*) and where it lands on the canvas, scaled to W x H at X, Y.
type Placement struct {
	CropX int `json:"cropX"`
	CropY int `json:"cropY"`
	CropW int `json:"cropW"`
	CropH int `json:"cropH"`
	X     int `json:"x"`
	Y     int `json:"y"`
	W     int `json:"w"`
	H     int `json:"h"`
}

// place resolves a frame for a srcW x srcH source on a canvasW x canvasH canvas.
// The media is kept overlapping the canvas by at least a tenth of its size.
func (f Frame) place(srcW, srcH, canvasW, canvasH int) Placement {
	f = f.Normalize()
	cw, ch := float64(canvasW), float64(canvasH)
	s := math.Min(cw/float64(srcW), ch/float64(srcH)) * f.Scale
	dw, dh := float64(srcW)*s, float64(srcH)*s

	mx, my := math.Min(dw, cw)*0.1, math.Min(dh, ch)*0.1
	cx := math.Min(math.Max(f.X*cw, mx-dw/2), cw-mx+dw/2)
	cy := math.Min(math.Max(f.Y*ch, my-dh/2), ch-my+dh/2)
	left, top := cx-dw/2, cy-dh/2

	vx0, vx1 := math.Max(0, left), math.Min(cw, left+dw)
	vy0, vy1 := math.Max(0, top), math.Min(ch, top+dh)

	p := Placement{
		X: int(math.Round(vx0)), Y: int(math.Round(vy0)),
		W: max(2, int(math.Round(vx1-vx0))&^1), H: max(2, int(math.Round(vy1-vy0))&^1),
		CropX: int(math.Floor((vx0 - left) / s)), CropY: int(math.Floor((vy0 - top) / s)),
		CropW: int(math.Round((vx1 - vx0) / s)), CropH: int(math.Round((vy1 - vy0) / s)),
	}
	p.CropW = max(1, min(p.CropW, srcW-p.CropX))
	p.CropH = max(1, min(p.CropH, srcH-p.CropY))
	return p
}

func (f Frame) label() string {
	return fmt.Sprintf("custom framing, %d%% zoom", int(math.Round(f.Normalize().Scale*100)))
}
