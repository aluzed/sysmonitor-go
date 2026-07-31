package main

import (
	"math"
	"strings"
)

// The CPU cooler is drawn procedurally: the heatsink fin stack forms the
// background, and the fan is a disc of blades computed in polar coordinates.
// The phase advances every frame, so the blades genuinely rotate.

const (
	fanBlades = 7
	fanSweep  = 1.15 // blade curvature (0 = straight blades)
	hubRadius = 0.17
	rimInner  = 0.93
)

// renderCooler returns `h` lines of `w` columns.
//
//	phase : current angle in radians
//	heat  : 0..1, heatsink temperature (fin colour)
//	spin  : 0..1, fan speed (blade colour and brightness)
func renderCooler(w, h int, phase, heat, spin float64) []string {
	cx := float64(w-1) / 2
	cy := float64(h-1) / 2
	rx := float64(w) / 2
	ry := float64(h) / 2

	finCold := rgb{78, 92, 122}
	finHot := rgb{214, 118, 96}
	finCol := grad([]stop{{0, finCold}, {1, finHot}}, heat)
	bladeCol := grad(fanStops, spin)
	period := 2 * math.Pi / fanBlades

	out := make([]string, h)
	for y := 0; y < h; y++ {
		var sb strings.Builder
		var cur rgb
		started := false
		emit := func(c rgb, r rune) {
			if !started || c != cur {
				sb.WriteString(c.seq())
				cur, started = c, true
			}
			sb.WriteRune(r)
		}

		for x := 0; x < w; x++ {
			nx := (float64(x) - cx) / rx
			ny := (float64(y) - cy) / ry
			rad := math.Hypot(nx, ny)

			// --- outside the fan: the heatsink fins ------------------------
			if rad > 1.0 {
				emit(dim(finCol, 0.85), '│')
				continue
			}
			// --- shroud ---------------------------------------------------
			if rad >= rimInner {
				emit(cLine, '·')
				continue
			}
			// --- hub ------------------------------------------------------
			if rad < hubRadius {
				emit(bladeCol, '◉')
				continue
			}

			// --- blades ---------------------------------------------------
			th := math.Atan2(ny, nx)
			a := math.Mod(th+phase+fanSweep*(1-rad), period)
			if a < 0 {
				a += period
			}
			// angular distance to the nearest blade axis
			lead := a <= period/2
			d := a
			if !lead {
				d = period - a
			}
			arc := d * rad

			var ch rune
			switch {
			case arc < 0.050:
				ch = '█'
			case arc < 0.100:
				ch = '▓'
			case arc < 0.160:
				ch = '▒'
			case arc < 0.225:
				ch = '░'
			default:
				// between two blades: the fins show through
				emit(dim(finCol, 0.45), '│')
				continue
			}

			// brightness: highest at the blade centre, leading edge sharper
			shade := 0.55 + 0.45*(1-arc/0.225)
			if lead {
				shade += 0.12
			}
			shade *= 0.80 + 0.20*rad
			emit(dim(bladeCol, shade), ch)
		}
		sb.WriteString("\x1b[39m")
		out[y] = sb.String()
	}

	// mounting screws at the four corners of the shroud
	out = putScrews(out, w, h)
	return out
}

// putScrews replaces the 4 corners with screws, rebuilding each line while
// preserving the ANSI sequences already in it.
func putScrews(lines []string, w, h int) []string {
	if w < 4 || h < 4 {
		return lines
	}
	set := func(y, x int) {
		if y < 0 || y >= len(lines) {
			return
		}
		lines[y] = replaceCell(lines[y], x, cMuted, '◦')
	}
	set(0, 0)
	set(0, w-1)
	set(h-1, 0)
	set(h-1, w-1)
	return lines
}

// replaceCell replaces the nth visible cell of an ANSI line. The colour active
// before the cell is re-emitted afterwards, otherwise the rest of the line
// would lose its tint (escapes are only written on changes).
func replaceCell(line string, idx int, c rgb, r rune) string {
	var sb, seq strings.Builder
	last := ""
	n, in := 0, false
	for _, ru := range line {
		if in {
			sb.WriteRune(ru)
			seq.WriteRune(ru)
			if isFinalByte(ru) {
				in = false
				last = seq.String()
				seq.Reset()
			}
			continue
		}
		if ru == 0x1b {
			in = true
			sb.WriteRune(ru)
			seq.Reset()
			seq.WriteRune(ru)
			continue
		}
		if n == idx {
			sb.WriteString(c.seq())
			sb.WriteRune(r)
			sb.WriteString(last)
		} else {
			sb.WriteRune(ru)
		}
		n++
	}
	return sb.String()
}
