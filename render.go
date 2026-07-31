package main

import (
	"fmt"
	"math"
	"strings"
)

// ---------------------------------------------------------------------------
// 24-bit colour
// ---------------------------------------------------------------------------

type rgb struct{ r, g, b uint8 }

// fg colours the text.
func (c rgb) fg(txt string) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[39m", c.r, c.g, c.b, txt)
}

// bd colours the text in bold.
func (c rgb) bd(txt string) string {
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm%s\x1b[22;39m", c.r, c.g, c.b, txt)
}

// seq returns the opening escape only, to colour long runs at once.
func (c rgb) seq() string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b)
}

// dim darkens a colour (f = 0 black, 1 unchanged).
func dim(c rgb, f float64) rgb {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return rgb{uint8(float64(c.r) * f), uint8(float64(c.g) * f), uint8(float64(c.b) * f)}
}

var (
	cInk   = rgb{228, 234, 246}
	cMuted = rgb{124, 136, 160}
	cLine  = rgb{62, 72, 94}
	cTrack = rgb{44, 52, 70}
	cAcc   = rgb{104, 208, 255}
	cAcc2  = rgb{176, 148, 255}
	cGood  = rgb{78, 206, 148}
	cWarn  = rgb{246, 200, 92}
)

type stop struct {
	at float64
	c  rgb
}

func lerp8(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

func grad(stops []stop, t float64) rgb {
	if math.IsNaN(t) {
		t = 0
	}
	last := len(stops) - 1
	if t <= stops[0].at {
		return stops[0].c
	}
	if t >= stops[last].at {
		return stops[last].c
	}
	for i := 0; i < last; i++ {
		a, b := stops[i], stops[i+1]
		if t >= a.at && t <= b.at {
			u := (t - a.at) / (b.at - a.at)
			return rgb{lerp8(a.c.r, b.c.r, u), lerp8(a.c.g, b.c.g, u), lerp8(a.c.b, b.c.b, u)}
		}
	}
	return stops[last].c
}

var loadStops = []stop{
	{0.00, rgb{72, 198, 142}},
	{0.45, rgb{146, 208, 108}},
	{0.65, rgb{244, 202, 88}},
	{0.82, rgb{248, 150, 64}},
	{1.00, rgb{246, 84, 96}},
}

var tempStops = []stop{
	{0.00, rgb{88, 172, 255}},
	{0.35, rgb{72, 198, 142}},
	{0.60, rgb{244, 202, 88}},
	{0.78, rgb{248, 150, 64}},
	{1.00, rgb{246, 84, 96}},
}

var fanStops = []stop{
	{0.00, rgb{96, 176, 232}},
	{0.40, rgb{120, 214, 224}},
	{0.70, rgb{244, 202, 88}},
	{1.00, rgb{248, 128, 92}},
}

// loadColor maps a 0..1 fraction.
func loadColor(f float64) rgb { return grad(loadStops, f) }

// tempColor maps degrees Celsius, saturating at 100.
func tempColor(deg float64) rgb { return grad(tempStops, deg/100) }

// ---------------------------------------------------------------------------
// Measuring and cutting strings that contain ANSI escapes
// ---------------------------------------------------------------------------

func isFinalByte(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// visLen counts visible columns, ignoring ANSI escape sequences.
func visLen(s string) int {
	n, in := 0, false
	for _, r := range s {
		if in {
			if isFinalByte(r) {
				in = false
			}
			continue
		}
		if r == 0x1b {
			in = true
			continue
		}
		n++
	}
	return n
}

// clip truncates to w visible columns while keeping ANSI sequences intact.
func clip(s string, w int) string {
	if visLen(s) <= w {
		return s
	}
	var sb strings.Builder
	n, in := 0, false
	for _, r := range s {
		if in {
			sb.WriteRune(r)
			if isFinalByte(r) {
				in = false
			}
			continue
		}
		if r == 0x1b {
			in = true
			sb.WriteRune(r)
			continue
		}
		if n >= w {
			break
		}
		sb.WriteRune(r)
		n++
	}
	sb.WriteString("\x1b[39m")
	return sb.String()
}

// padTo right-pads to w visible columns.
func padTo(s string, w int) string {
	if d := w - visLen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// padLeft left-pads to w visible columns.
func padLeft(s string, w int) string {
	if d := w - visLen(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// ---------------------------------------------------------------------------
// Boxes and assembly
// ---------------------------------------------------------------------------

// box frames content in a panel of total width w.
func box(title string, w int, content []string, accent rgb) []string {
	inner := w - 4
	if inner < 1 {
		inner = 1
	}
	t := ""
	if title != "" {
		t = " " + title + " "
	}
	tl := len([]rune(t))
	dash := w - 3 - tl
	if dash < 0 {
		dash = 0
	}
	out := make([]string, 0, len(content)+2)
	out = append(out, cLine.fg("╭─")+accent.bd(t)+cLine.fg(strings.Repeat("─", dash)+"╮"))
	for _, l := range content {
		out = append(out, cLine.fg("│ ")+padTo(clip(l, inner), inner)+cLine.fg(" │"))
	}
	out = append(out, cLine.fg("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

// joinH places blocks side by side, separated by `gap` spaces.
func joinH(gap int, blocks ...[]string) []string {
	h := 0
	widths := make([]int, len(blocks))
	for i, b := range blocks {
		if len(b) > h {
			h = len(b)
		}
		for _, l := range b {
			if v := visLen(l); v > widths[i] {
				widths[i] = v
			}
		}
	}
	sep := strings.Repeat(" ", gap)
	out := make([]string, h)
	for y := 0; y < h; y++ {
		var sb strings.Builder
		for i, b := range blocks {
			if i > 0 {
				sb.WriteString(sep)
			}
			line := ""
			if y < len(b) {
				line = b[y]
			}
			sb.WriteString(padTo(line, widths[i]))
		}
		out[y] = sb.String()
	}
	return out
}

// ---------------------------------------------------------------------------
// Bars
// ---------------------------------------------------------------------------

var eighths = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// bar draws a w-column bar filled to `frac` (0..1).
func bar(w int, frac float64, c rgb) string {
	if math.IsNaN(frac) || frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	units := frac * float64(w)
	full := int(units)
	if full > w {
		full = w
	}
	rest := w - full
	head := strings.Repeat("█", full)
	if rest > 0 {
		if idx := int((units - float64(full)) * 8); idx > 0 {
			head += string(eighths[idx])
			rest--
		}
	}
	return c.fg(head) + cTrack.fg(strings.Repeat("░", rest))
}

// gauge is a bar wrapped in thin half-brackets.
func gauge(w int, frac float64, c rgb) string {
	return cLine.fg("▕") + bar(w, frac, c) + cLine.fg("▏")
}

// ---------------------------------------------------------------------------
// Braille history graph (filled area)
// ---------------------------------------------------------------------------

// bits[dot row][dot column]
var brailleBits = [4][2]byte{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// brailleArea draws a filled area: 2 samples per column, 4 levels per row.
// `cf` maps a 0..1 fraction to a colour.
func brailleArea(vals []float64, w, h int, max float64, cf func(float64) rgb) []string {
	if w < 1 || h < 1 {
		return nil
	}
	if max <= 0 {
		max = 1
	}
	cols := w * 2
	grid := make([][]byte, h)
	for i := range grid {
		grid[i] = make([]byte, w)
	}
	colVal := make([]float64, w)
	for i := range colVal {
		colVal[i] = math.NaN()
	}

	n := len(vals)
	for i := 0; i < cols; i++ {
		idx := n - cols + i
		if idx < 0 {
			continue // no history yet: leave the column blank
		}
		v := vals[idx]
		if math.IsNaN(v) {
			continue
		}
		cx, sub := i/2, i%2
		if math.IsNaN(colVal[cx]) || v > colVal[cx] {
			colVal[cx] = v
		}
		lvl := int(math.Round(v / max * float64(h*4)))
		if v > 0 && lvl < 1 {
			lvl = 1
		}
		if lvl > h*4 {
			lvl = h * 4
		}
		for k := 0; k < lvl; k++ {
			grid[h-1-k/4][cx] |= brailleBits[3-k%4][sub]
		}
	}

	out := make([]string, h)
	for y := 0; y < h; y++ {
		var sb strings.Builder
		var cur rgb
		started := false
		for x := 0; x < w; x++ {
			col := cTrack
			if !math.IsNaN(colVal[x]) {
				col = cf(colVal[x] / max)
			}
			if !started || col != cur {
				sb.WriteString(col.seq())
				cur, started = col, true
			}
			sb.WriteRune(rune(0x2800 + int(grid[y][x])))
		}
		sb.WriteString("\x1b[39m")
		out[y] = sb.String()
	}
	return out
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

func humanBytes(v uint64) string {
	f := float64(v)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	switch {
	case i == 0:
		return fmt.Sprintf("%.0f %s", f, units[i])
	case f >= 100:
		return fmt.Sprintf("%.0f %s", f, units[i])
	case f >= 10:
		return fmt.Sprintf("%.1f %s", f, units[i])
	default:
		return fmt.Sprintf("%.2f %s", f, units[i])
	}
}

func humanRate(bytesPerSec float64) string {
	f := bytesPerSec
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 || f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func humanFreq(mhz float64) string {
	if mhz >= 1000 {
		return fmt.Sprintf("%.2f GHz", mhz/1000)
	}
	return fmt.Sprintf("%.0f MHz", mhz)
}
