package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

type ring struct {
	buf []float64
	cap int
}

func newRing(n int) *ring { return &ring{buf: make([]float64, 0, n), cap: n} }

func (r *ring) push(v float64) {
	r.buf = append(r.buf, v)
	if len(r.buf) > r.cap {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.cap:]...)
	}
}

func (r *ring) peak(floor float64) float64 {
	m := floor
	for _, v := range r.buf {
		if v > m {
			m = v
		}
	}
	return m
}

type history struct {
	cpu, mem, diskR, diskW, netRx, netTx *ring
}

func newHistory(n int) *history {
	return &history{
		cpu: newRing(n), mem: newRing(n),
		diskR: newRing(n), diskW: newRing(n),
		netRx: newRing(n), netTx: newRing(n),
	}
}

func (h *history) push(s Snapshot) {
	h.cpu.push(s.CPUAll)
	if s.MemTotal > 0 {
		h.mem.push(float64(s.MemUsed) / float64(s.MemTotal) * 100)
	}
	h.diskR.push(s.DiskR)
	h.diskW.push(s.DiskW)
	h.netRx.push(s.NetRx)
	h.netTx.push(s.NetTx)
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

type winsize struct{ rows, cols, x, y uint16 }

func termSize() (int, int) {
	ws := winsize{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.cols == 0 || ws.rows == 0 {
		return 0, 0
	}
	return int(ws.cols), int(ws.rows)
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func stty(args ...string) {
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	_ = c.Run()
}

// ---------------------------------------------------------------------------
// Layout helpers
// ---------------------------------------------------------------------------

// fit forces a block to exactly n lines, padding or truncating.
func fit(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines[:n]
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Header and footer
// ---------------------------------------------------------------------------

func headerLines(s Snapshot, w int, paused bool) []string {
	runes := []rune("SYSMONITOR")
	var t strings.Builder
	for i, r := range runes {
		c := grad([]stop{{0, cAcc}, {1, cAcc2}}, float64(i)/float64(len(runes)-1))
		t.WriteString(c.bd(string(r)))
	}

	nCore := maxi(1, len(s.CPUCores))
	info := []string{
		cInk.fg(s.Host),
		cMuted.fg("Linux " + s.Kernel),
		cMuted.fg("up ") + cInk.fg(fmtUptime(s.Uptime)),
		cMuted.fg("load ") + loadColor(s.Load[0]/float64(nCore)).fg(
			fmt.Sprintf("%.2f %.2f %.2f", s.Load[0], s.Load[1], s.Load[2])),
		cMuted.fg(fmt.Sprintf("%d tasks", s.ProcTot)),
	}
	left := cAcc2.fg("▞▚ ") + t.String() + "   " + strings.Join(info, cLine.fg(" · "))
	right := cInk.fg(time.Now().Format("15:04:05"))
	if paused {
		right = cWarn.bd("⏸ PAUSED") + "  " + right
	}

	gap := w - visLen(left) - visLen(right)
	if gap < 1 {
		left = clip(left, maxi(1, w-visLen(right)-1))
		gap = 1
	}
	return []string{left + strings.Repeat(" ", gap) + right, gradRule(w)}
}

// gradRule draws a horizontal gradient rule.
func gradRule(w int) string {
	var sb strings.Builder
	var cur rgb
	started := false
	for i := 0; i < w; i++ {
		c := dim(grad([]stop{{0, cAcc}, {0.5, cAcc2}, {1, cLine}}, float64(i)/float64(maxi(1, w-1))), 0.65)
		if !started || c != cur {
			sb.WriteString(c.seq())
			cur, started = c, true
		}
		sb.WriteRune('─')
	}
	sb.WriteString("\x1b[39m")
	return sb.String()
}

// ---------------------------------------------------------------------------
// Cooler — height: fanH + 4
// ---------------------------------------------------------------------------

func coolerContent(s Snapshot, inner, fanH int, phase float64) []string {
	heat := clamp01((s.CPUTemp - 30) / 55)
	var spin float64
	var rpmTxt, srcTxt string

	if s.CPUFan >= 0 {
		spin = clamp01(s.CPUFan / 2200)
		rpmTxt = fmt.Sprintf("%.0f rpm", s.CPUFan)
		srcTxt = "sensor " + s.CPUFanName
	} else {
		spin = clamp01(s.CPUAll / 100)
		rpmTxt = "— rpm"
		srcTxt = "no sensor → CPU load"
	}

	out := renderCooler(inner, fanH, phase, heat, spin)

	// "SPEED  " (7) + gauge (gw+2) + space + value (10) = gw + 20
	gw := inner - 20
	if gw < 5 {
		gw = 5
	}
	out = append(out, "")
	out = append(out, cMuted.fg("SPEED  ")+gauge(gw, spin, grad(fanStops, spin))+" "+
		padLeft(grad(fanStops, spin).fg(rpmTxt), 10))
	out = append(out, cMuted.fg("HEAT   ")+gauge(gw, heat, tempColor(s.CPUTemp))+" "+
		padLeft(tempColor(s.CPUTemp).fg(fmt.Sprintf("%.0f °C", s.CPUTemp)), 10))
	out = append(out, cLine.fg(clip(srcTxt, inner)))
	return out
}

// ---------------------------------------------------------------------------
// CPU — height: graphH + 5
// ---------------------------------------------------------------------------

func cpuContent(s Snapshot, h *history, inner, graphH int) []string {
	var out []string

	boost := ""
	switch s.Boost {
	case 0:
		boost = cLine.fg(" · ") + cWarn.fg("turbo off")
	case 1:
		boost = cLine.fg(" · ") + cGood.fg("turbo on")
	}
	out = append(out, cInk.bd(s.CPUModel)+cLine.fg(" · ")+
		cMuted.fg(fmt.Sprintf("%d threads", len(s.CPUCores)))+boost)

	pctTxt := fmt.Sprintf("%5.1f %%", s.CPUAll)
	gw := inner - 10
	if gw < 8 {
		gw = 8
	}
	out = append(out, gauge(gw, s.CPUAll/100, loadColor(s.CPUAll/100))+" "+
		loadColor(s.CPUAll/100).bd(pctTxt))

	stats := []string{
		cMuted.fg("avg ") + cInk.fg(humanFreq(s.FreqAvg)),
		cMuted.fg("peak ") + cInk.fg(humanFreq(s.FreqMax)),
	}
	if inner >= 62 {
		for _, t := range s.Temps {
			if t.Chip == "CPU" {
				stats = append(stats, cMuted.fg(t.Label+" ")+
					tempColor(t.Value).fg(fmt.Sprintf("%.0f°C", t.Value)))
			}
		}
	} else if s.CPUTemp > 0 {
		stats = append(stats, cMuted.fg("temp ")+
			tempColor(s.CPUTemp).fg(fmt.Sprintf("%.0f°C", s.CPUTemp)))
	}
	if s.GPUok {
		stats = append(stats, cMuted.fg("GPU ")+
			loadColor(s.GPUBusy/100).fg(fmt.Sprintf("%.0f%%", s.GPUBusy)))
	}
	out = append(out, strings.Join(stats, cLine.fg(" · ")))
	out = append(out, "")
	out = append(out, brailleArea(h.cpu.buf, inner, graphH, 100, loadColor)...)

	secs := len(h.cpu.buf)
	legend := fmt.Sprintf("CPU load history · %d s", secs)
	out = append(out, cLine.fg(padTo(clip(legend, inner-6), inner-6))+cLine.fg(padLeft("100 %", 6)))
	return out
}

// ---------------------------------------------------------------------------
// Cores
// ---------------------------------------------------------------------------

type coreLayout struct {
	cols, rows, barW int
	entryW           int
	withFreq         bool
	compact          bool
}

// compactCores falls back to a single line, one vertical bar per thread.
func compactCores() coreLayout {
	return coreLayout{cols: 1, rows: 1, compact: true}
}

func planCores(inner, n int) coreLayout {
	if n == 0 {
		return coreLayout{cols: 1, rows: 1, barW: 6, entryW: inner}
	}
	cols := 4
	for cols > 1 {
		if (inner-(cols-1)*2)/cols >= 17 {
			break
		}
		cols--
	}
	entryW := (inner - (cols-1)*2) / cols
	withFreq := entryW >= 26
	fixed := 4 + 2 + 5 // "c00 " + brackets + " 100%"
	if withFreq {
		fixed += 6 // " 4.12G"
	}
	barW := entryW - fixed
	if barW < 4 {
		barW = 4
	}
	return coreLayout{cols: cols, rows: (n + cols - 1) / cols, barW: barW, entryW: entryW, withFreq: withFreq}
}

var vBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func coresContent(s Snapshot, inner int, l coreLayout) []string {
	n := len(s.CPUCores)
	if n == 0 {
		return []string{cMuted.fg("no data")}
	}

	if l.compact {
		// one bar per thread, widened when there is room
		lbl := cMuted.fg("load ")
		cw := (inner - visLen(lbl) - 7) / n
		if cw > 3 {
			cw = 3
		}
		if cw < 1 {
			cw = 1
		}
		var sb strings.Builder
		var cur rgb
		started := false
		for _, pct := range s.CPUCores {
			c := loadColor(pct / 100)
			if !started || c != cur {
				sb.WriteString(c.seq())
				cur, started = c, true
			}
			r := vBlocks[int(clamp01(pct/100)*float64(len(vBlocks)-1)+0.5)]
			sb.WriteString(strings.Repeat(string(r), cw))
		}
		sb.WriteString("\x1b[39m")
		bars := padTo(sb.String(), inner-visLen(lbl)-7)
		return []string{lbl + bars +
			padLeft(loadColor(s.CPUAll/100).bd(fmt.Sprintf("%.0f%%", s.CPUAll)), 7)}
	}

	withFreq := l.withFreq && len(s.Freqs) >= n

	lines := make([]string, l.rows)
	for r := 0; r < l.rows; r++ {
		parts := make([]string, 0, l.cols)
		for c := 0; c < l.cols; c++ {
			i := c*l.rows + r // fill column by column
			if i >= n {
				parts = append(parts, strings.Repeat(" ", l.entryW))
				continue
			}
			pct := s.CPUCores[i]
			col := loadColor(pct / 100)
			e := cMuted.fg(fmt.Sprintf("c%02d ", i)) + gauge(l.barW, pct/100, col) +
				padLeft(col.fg(fmt.Sprintf("%3.0f%%", pct)), 5)
			if withFreq {
				e += cLine.fg(fmt.Sprintf(" %4.1fG", s.Freqs[i]/1000))
			}
			parts = append(parts, padTo(e, l.entryW))
		}
		lines[r] = strings.Join(parts, "  ")
	}
	return lines
}

// ---------------------------------------------------------------------------
// Memory — caller-imposed height
// ---------------------------------------------------------------------------

func memContent(s Snapshot, h *history, inner, total int) []string {
	var out []string

	memFrac := 0.0
	if s.MemTotal > 0 {
		memFrac = float64(s.MemUsed) / float64(s.MemTotal)
	}
	swFrac := 0.0
	if s.SwapTotal > 0 {
		swFrac = float64(s.SwapUsed) / float64(s.SwapTotal)
	}
	gw := inner - 14
	if gw < 8 {
		gw = 8
	}

	out = append(out, cMuted.fg("RAM  ")+gauge(gw, memFrac, loadColor(memFrac))+
		padLeft(loadColor(memFrac).bd(fmt.Sprintf("%.0f%%", memFrac*100)), 6))
	if inner >= 36 {
		out = append(out, "     "+cLine.fg("used ")+cInk.fg(humanBytes(s.MemUsed))+
			cLine.fg(" · free ")+cGood.fg(humanBytes(s.MemAvail)))
		out = append(out, "     "+cLine.fg("cache ")+cMuted.fg(humanBytes(s.Cached))+
			cLine.fg(" · buff. ")+cMuted.fg(humanBytes(s.Buffers)))
	} else {
		out = append(out, "     "+cInk.fg(humanBytes(s.MemUsed))+cLine.fg(" / ")+
			cMuted.fg(humanBytes(s.MemTotal)))
		out = append(out, "     "+cLine.fg("free ")+cGood.fg(humanBytes(s.MemAvail)))
	}
	out = append(out, cMuted.fg("SWAP ")+gauge(gw, swFrac, loadColor(swFrac))+
		padLeft(loadColor(swFrac).fg(fmt.Sprintf("%.0f%%", swFrac*100)), 6))
	out = append(out, "     "+cInk.fg(humanBytes(s.SwapUsed))+
		cLine.fg(" of ")+cMuted.fg(humanBytes(s.SwapTotal)))

	if g := total - len(out) - 1; g > 0 {
		out = append(out, "")
		out = append(out, brailleArea(h.mem.buf, inner, g, 100, loadColor)...)
	}
	return fit(out, total)
}

// ---------------------------------------------------------------------------
// Disks — caller-imposed height
// ---------------------------------------------------------------------------

func diskContent(s Snapshot, h *history, inner, total int) []string {
	var out []string

	lblW := 10
	for _, m := range s.Mounts {
		if l := len([]rune(m.Path)); l > lblW && l <= 16 {
			lblW = l
		}
	}
	// label + space + gauge(gw+2) + percentage(5) + size(9)
	if lblW > inner-23 {
		lblW = inner - 23 // keep at least 6 gauge columns
	}
	if lblW < 4 {
		lblW = 4
	}
	gw := inner - lblW - 17
	if gw < 6 {
		gw = 6
	}

	maxMounts := total - 3
	if maxMounts < 1 {
		maxMounts = 1
	}
	shown := s.Mounts
	extra := 0
	if len(shown) > maxMounts {
		shown, extra = shown[:maxMounts-1], len(shown)-(maxMounts-1)
	}
	for _, m := range shown {
		c := loadColor(m.Frac)
		out = append(out, cInk.fg(padTo(clip(m.Path, lblW), lblW))+" "+
			gauge(gw, m.Frac, c)+padLeft(c.fg(fmt.Sprintf("%3.0f%%", m.Frac*100)), 5)+
			padLeft(cLine.fg(humanBytes(m.Total)), 9))
	}
	if extra > 0 {
		out = append(out, cLine.fg(fmt.Sprintf("… %d more mount(s)", extra)))
	}

	out = append(out, "")
	out = append(out, cMuted.fg("I/O  ")+
		cAcc.fg("↓ ")+cInk.fg(padTo(humanRate(s.DiskR), 12))+
		cAcc2.fg("↑ ")+cInk.fg(humanRate(s.DiskW)))

	if g := total - len(out); g > 0 {
		mx := math.Max(h.diskR.peak(0), h.diskW.peak(0))
		if mx < 1<<20 {
			mx = 1 << 20 // 1 MiB/s floor: keeps the graph from going wild on idle noise
		}
		out = append(out, brailleArea(h.diskR.buf, inner, g, mx,
			func(f float64) rgb { return grad([]stop{{0, dim(cAcc, 0.55)}, {1, cAcc}}, f) })...)
	}
	return fit(out, total)
}

// ---------------------------------------------------------------------------
// Sensors — caller-imposed height
// ---------------------------------------------------------------------------

var chipRank = map[string]int{"CPU": 0, "GPU": 1, "NVMe": 2, "Board": 3}

func rankOf(c string) int {
	if v, ok := chipRank[c]; ok {
		return v
	}
	return 9
}

func sensorsContent(s Snapshot, inner, total int) []string {
	temps := append([]Sensor(nil), s.Temps...)
	sort.SliceStable(temps, func(i, j int) bool { return rankOf(temps[i].Chip) < rankOf(temps[j].Chip) })

	// label + space + gauge(gw+2) + value(valW)
	lblW, valW := 14, 9
	if lblW > inner-3-valW-6 {
		lblW = inner - 3 - valW - 6
	}
	if lblW < 6 {
		lblW = 6
	}
	gw := inner - lblW - 3 - valW
	if gw < 6 {
		gw = 6
	}

	line := func(lbl string, frac float64, c rgb, val string) string {
		return cMuted.fg(padTo(clip(lbl, lblW), lblW)) + " " +
			gauge(gw, frac, c) + padLeft(c.fg(val), valW)
	}

	// reserved: separator + fans + separator + network
	nFans := maxi(1, len(s.Fans))
	reserved := 3 + nFans
	budget := total - reserved
	if budget < 1 {
		budget = 1
	}

	// when it does not all fit, the last budgeted line becomes the counter
	show := len(temps)
	if show > budget {
		show = budget - 1
		if show < 0 {
			show = 0
		}
	}

	var out []string
	for _, t := range temps[:show] {
		out = append(out, line(t.Chip+" "+t.Label, clamp01((t.Value-20)/75),
			tempColor(t.Value), fmt.Sprintf("%.0f°C", t.Value)))
	}
	if r := len(temps) - show; r > 0 {
		out = append(out, cLine.fg(fmt.Sprintf("… %d more sensor(s)", r)))
	}

	out = append(out, cLine.fg(strings.Repeat("╌", inner)))
	if len(s.Fans) == 0 {
		out = append(out, cWarn.fg("no fan detected"))
	}
	for _, f := range s.Fans {
		spin := clamp01(f.Value / 2500)
		out = append(out, line(f.Chip+" "+f.Label, spin, grad(fanStops, spin),
			fmt.Sprintf("%.0f rpm", f.Value)))
	}

	out = append(out, cLine.fg(strings.Repeat("╌", inner)))
	out = append(out, cMuted.fg(padTo(clip(s.NetIface, lblW), lblW))+" "+
		cAcc.fg("↓ ")+cInk.fg(padTo(humanRate(s.NetRx), 12))+
		cAcc2.fg("↑ ")+cInk.fg(humanRate(s.NetTx)))

	return fit(out, total)
}

// ---------------------------------------------------------------------------
// Composition
// ---------------------------------------------------------------------------

// Three layouts, chosen by width:
//
//	A (>=114): cooler|cpu · cores · memory|disks|sensors
//	B (>= 68): cooler|cpu · cores · memory|disks · sensors
//	C (<  68): everything stacked in one column
//
// In each case the height is budgeted explicitly so the total lands exact.
func compose(s Snapshot, h *history, w, ht int, phase float64, paused bool) []string {
	const margin = 1
	usable := w - 2*margin
	if usable < 40 {
		usable = 40
	}

	layout := 'C'
	switch {
	case usable >= 114:
		layout = 'A'
	case usable >= 68:
		layout = 'B'
	}

	cores := planCores(usable-4, len(s.CPUCores))

	// Height budget. The total is exact for A and B:
	//   A : 13 + fanH + rows + bottomH
	//   B : 15 + fanH + rows + bottomH + sensH
	fanH, sensH, bottomH := 11, 7, 0
	fixed := 13
	if layout == 'B' {
		fanH, fixed = 9, 15
	}

	if layout == 'C' {
		fanH, sensH, bottomH = 5, 6, 5
	} else {
		calc := func() {
			bottomH = ht - fixed - fanH - cores.rows
			if layout == 'B' {
				bottomH -= sensH
			}
		}
		shrink := func() {
			calc()
			for bottomH < 6 && (fanH > 3 || (layout == 'B' && sensH > 4)) {
				if fanH > 3 {
					fanH--
				} else {
					sensH--
				}
				calc()
			}
		}
		shrink()
		// The core grid does not fit: fall back to the compact single line.
		if bottomH < 6 && !cores.compact {
			cores = compactCores()
			fanH, sensH = 11, 7
			if layout == 'B' {
				fanH = 9
			}
			shrink()
		}
		// very tall terminal: hand the surplus back to the fan
		for bottomH > 30 && fanH < 19 {
			fanH++
			calc()
		}
		if bottomH < 3 {
			bottomH = 3
		}
		if bottomH > 30 {
			bottomH = 30
		}
	}

	// Panels are assembled as whole blocks: one that no longer fits is
	// dropped rather than cut in half.
	var blocks [][]string
	add := func(b []string) { blocks = append(blocks, b) }

	// --- cooler + CPU --------------------------------------------
	vw := 33
	if usable < vw {
		vw = usable
	}
	if layout == 'C' {
		add(box("COOLER", vw, coolerContent(s, vw-4, fanH, phase), cAcc))
		add(box("CPU", usable, cpuContent(s, h, usable-4, 3), cAcc))
	} else {
		cw := usable - vw - 1
		add(joinH(1,
			box("COOLER", vw, coolerContent(s, vw-4, fanH, phase), cAcc),
			box("CPU", cw, cpuContent(s, h, cw-4, fanH-1), cAcc),
		))
	}

	// --- cores -------------------------------------------------------------
	add(box(fmt.Sprintf("CORES · %d threads", len(s.CPUCores)), usable,
		coresContent(s, usable-4, cores), cAcc2))

	// --- memory / disks / sensors ---------------------------------------
	memTitle := "MEMORY · " + humanBytes(s.MemTotal)
	switch layout {
	case 'A':
		mw, dw := 42, 42
		sw := usable - mw - dw - 2
		if sw < 34 {
			sw = 34
			dw = usable - mw - sw - 2
		}
		add(joinH(1,
			box(memTitle, mw, memContent(s, h, mw-4, bottomH), cAcc),
			box("DISKS", dw, diskContent(s, h, dw-4, bottomH), cAcc),
			box("SENSORS", sw, sensorsContent(s, sw-4, bottomH), cAcc2),
		))
	case 'B':
		mw := usable / 2
		dw := usable - mw - 1
		add(joinH(1,
			box(memTitle, mw, memContent(s, h, mw-4, bottomH), cAcc),
			box("DISKS", dw, diskContent(s, h, dw-4, bottomH), cAcc),
		))
		add(box("SENSORS", usable, sensorsContent(s, usable-4, sensH), cAcc2))
	default:
		add(box(memTitle, usable, memContent(s, h, usable-4, bottomH), cAcc))
		add(box("DISKS", usable, diskContent(s, h, usable-4, 4), cAcc))
		add(box("SENSORS", usable, sensorsContent(s, usable-4, sensH), cAcc2))
	}

	out := headerLines(s, usable, paused)
	for _, b := range blocks {
		if len(out)+len(b)+1 > ht {
			continue
		}
		out = append(out, b...)
	}
	out = append(out, cLine.fg("  ")+
		cInk.fg("q")+cMuted.fg(" quit")+cLine.fg("   ·   ")+
		cInk.fg("p")+cMuted.fg(" pause"))

	pad := strings.Repeat(" ", margin)
	for i := range out {
		out[i] = pad + out[i]
	}
	return out
}

// ---------------------------------------------------------------------------
// Main loop
// ---------------------------------------------------------------------------

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

// buildVersion falls back to the module version the toolchain embeds, so a
// binary produced by `go install module/cmd/x@v1.2.3` still reports v1.2.3
// even though no ldflags were passed.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}

func main() {
	once := flag.Bool("once", false, "render a single frame and exit")
	fw := flag.Int("w", 0, "force width, in columns")
	fh := flag.Int("h", 0, "force height, in rows")
	fps := flag.Int("fps", 15, "frames per second (1-60)")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("sysmonitor-go %s\n", buildVersion())
		return
	}

	if *fps < 1 {
		*fps = 1
	}
	if *fps > 60 {
		*fps = 60
	}

	col := NewCollector()
	hist := newHistory(1200)

	size := func() (int, int) {
		w, h := termSize()
		if *fw > 0 {
			w = *fw
		}
		if *fh > 0 {
			h = *fh
		}
		if w <= 0 {
			w = 120
		}
		if h <= 0 {
			h = 40
		}
		return w, h
	}

	if *once {
		col.Collect()
		time.Sleep(150 * time.Millisecond)
		s := col.Collect()
		hist.push(s)
		w, h := size()
		fmt.Println(strings.Join(compose(s, hist, w, h, 0.7, false), "\n"))
		return
	}

	tty := isTTY()
	if tty {
		stty("raw", "-echo")
		fmt.Print("\x1b[?1049h\x1b[?25l") // alternate screen + hidden cursor
	}
	restore := func() {
		if tty {
			fmt.Print("\x1b[?25h\x1b[?1049l")
			stty("sane")
		}
	}
	defer restore()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	keys := make(chan byte, 8)
	if tty {
		go func() {
			b := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(b)
				if err != nil {
					return
				}
				if n > 0 {
					keys <- b[0]
				}
			}
		}()
	}

	frame := time.Duration(float64(time.Second) / float64(*fps))
	tick := time.NewTicker(frame)
	defer tick.Stop()

	col.Collect() // prime the deltas
	snap := col.Collect()
	hist.push(snap)

	var phase float64
	paused := false
	lastSample, lastHist := time.Now(), time.Now()
	var prev []string

	for {
		select {
		case <-sig:
			restore()
			return

		case k := <-keys:
			switch k {
			case 'q', 'Q', 3, 27: // q, Ctrl-C, Esc
				restore()
				return
			case 'p', 'P', ' ':
				paused = !paused
			}

		case <-tick.C:
			now := time.Now()
			if !paused {
				if now.Sub(lastSample) >= 500*time.Millisecond {
					snap = col.Collect()
					lastSample = now
				}
				if now.Sub(lastHist) >= time.Second {
					hist.push(snap)
					lastHist = now
				}
				spin := clamp01(snap.CPUAll / 100)
				if snap.CPUFan >= 0 {
					spin = clamp01(snap.CPUFan / 2200)
				}
				phase += (0.6 + 6.3*spin) * frame.Seconds()
			}

			w, h := size()
			lines := compose(snap, hist, w, h, phase, paused)

			n := len(lines)
			if n > h {
				n = h
			}
			// Differential rendering: only changed lines are rewritten.
			// Most panels are static between frames; only the fan moves
			// every time.
			var sb strings.Builder
			if len(prev) != n {
				sb.WriteString("\x1b[H\x1b[J")
				prev = make([]string, n)
				for i := range prev {
					prev[i] = "\x00" // force a full repaint
				}
			}
			for i := 0; i < n; i++ {
				if lines[i] == prev[i] {
					continue
				}
				fmt.Fprintf(&sb, "\x1b[%d;1H", i+1)
				sb.WriteString(lines[i])
				sb.WriteString("\x1b[K")
				prev[i] = lines[i]
			}
			if sb.Len() > 0 {
				os.Stdout.WriteString(sb.String())
			}
		}
	}
}
