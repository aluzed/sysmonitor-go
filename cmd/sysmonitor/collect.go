package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Structures
// ---------------------------------------------------------------------------

type Sensor struct {
	Chip  string
	Label string
	Value float64
}

type MountInfo struct {
	Path  string
	FS    string
	Total uint64
	Used  uint64
	Frac  float64
}

type Snapshot struct {
	Host     string
	Kernel   string
	CPUModel string
	Uptime   time.Duration
	Load     [3]float64
	ProcRun  int
	ProcTot  int

	CPUAll   float64   // 0..100
	CPUCores []float64 // 0..100
	Freqs    []float64 // MHz per core
	FreqAvg  float64
	FreqMax  float64
	Boost    int // 1 on, 0 off, -1 unknown

	MemTotal, MemUsed, MemAvail uint64
	Cached, Buffers             uint64
	SwapTotal, SwapUsed         uint64

	Mounts       []MountInfo
	DiskR, DiskW float64 // bytes/s
	NetIface     string
	NetRx, NetTx float64 // bytes/s

	Temps      []Sensor
	Fans       []Sensor
	CPUTemp    float64
	CPUFan     float64 // -1 when no sensor
	CPUFanName string
	GPUBusy    float64
	GPUok      bool
}

type cpuTimes struct{ total, idle uint64 }

// Collector keeps the previous sample so rates can be derived.
type Collector struct {
	prevAll   cpuTimes
	prevCores []cpuTimes
	prevDiskR uint64
	prevDiskW uint64
	prevNetRx uint64
	prevNetTx uint64
	prevAt    time.Time
	primed    bool

	host, kernel, model string
}

func NewCollector() *Collector {
	c := &Collector{}
	c.host, _ = os.Hostname()
	c.kernel = strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	c.model = "unknown CPU"
	for _, l := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
		if strings.HasPrefix(l, "model name") {
			if i := strings.Index(l, ":"); i >= 0 {
				c.model = cleanModel(l[i+1:])
			}
			break
		}
	}
	return c
}

// cleanModel strips marketing filler from the CPU model string:
// "AMD Ryzen 9 3950X 16-Core Processor" → "AMD Ryzen 9 3950X".
func cleanModel(m string) string {
	m = strings.TrimSpace(m)
	m = strings.NewReplacer("(R)", "", "(TM)", "", "(r)", "", "(tm)", "").Replace(m)
	if i := strings.Index(m, " with "); i > 0 {
		m = m[:i]
	}
	if i := strings.Index(m, " @ "); i > 0 {
		m = m[:i]
	}
	if i := strings.Index(m, "-Core Processor"); i > 0 {
		j := i
		for j > 0 && m[j-1] >= '0' && m[j-1] <= '9' {
			j--
		}
		m = m[:j]
	}
	m = strings.TrimSuffix(strings.TrimSpace(m), " Processor")
	m = strings.TrimSuffix(m, " CPU")
	return strings.Join(strings.Fields(m), " ")
}

// ---------------------------------------------------------------------------
// Reading helpers
// ---------------------------------------------------------------------------

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func readUint(p string) (uint64, bool) {
	s := strings.TrimSpace(readFile(p))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// ---------------------------------------------------------------------------
// Collection
// ---------------------------------------------------------------------------

func (c *Collector) Collect() Snapshot {
	now := time.Now()
	s := Snapshot{Host: c.host, Kernel: c.kernel, CPUModel: c.model, CPUFan: -1, Boost: -1}

	// --- uptime / load ---------------------------------------------------
	if f := strings.Fields(readFile("/proc/uptime")); len(f) > 0 {
		s.Uptime = time.Duration(atof(f[0])) * time.Second
	}
	if f := strings.Fields(readFile("/proc/loadavg")); len(f) >= 4 {
		s.Load = [3]float64{atof(f[0]), atof(f[1]), atof(f[2])}
		if p := strings.SplitN(f[3], "/", 2); len(p) == 2 {
			s.ProcRun, _ = strconv.Atoi(p[0])
			s.ProcTot, _ = strconv.Atoi(p[1])
		}
	}

	// --- CPU: utilisation --------------------------------------------------
	all, cores := readCPUTimes()
	dt := now.Sub(c.prevAt).Seconds()
	if c.primed {
		s.CPUAll = pctBusy(c.prevAll, all)
		s.CPUCores = make([]float64, len(cores))
		for i := range cores {
			if i < len(c.prevCores) {
				s.CPUCores[i] = pctBusy(c.prevCores[i], cores[i])
			}
		}
	} else {
		s.CPUCores = make([]float64, len(cores))
	}

	// --- CPU: frequencies --------------------------------------------------
	// amd-pstate does not report scaling_cur_freq reliably; /proc/cpuinfo
	// exposes the actually measured frequency (aperf/mperf).
	for _, l := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
		if strings.HasPrefix(l, "cpu MHz") {
			if i := strings.Index(l, ":"); i >= 0 {
				s.Freqs = append(s.Freqs, atof(l[i+1:]))
			}
		}
	}
	// Fallback for platforms where /proc/cpuinfo has no "cpu MHz" field
	// (ARM, RISC-V, some kernels): the current frequency from cpufreq.
	if len(s.Freqs) == 0 {
		for i := 0; i < 4096; i++ {
			v, ok := readUint("/sys/devices/system/cpu/cpu" + strconv.Itoa(i) +
				"/cpufreq/scaling_cur_freq")
			if !ok {
				break
			}
			s.Freqs = append(s.Freqs, float64(v)/1000)
		}
	}
	for _, f := range s.Freqs {
		s.FreqAvg += f
		if f > s.FreqMax {
			s.FreqMax = f
		}
	}
	if len(s.Freqs) > 0 {
		s.FreqAvg /= float64(len(s.Freqs))
	}
	if v, ok := readUint("/sys/devices/system/cpu/cpufreq/boost"); ok {
		s.Boost = int(v)
	} else if v, ok := readUint("/sys/devices/system/cpu/intel_pstate/no_turbo"); ok {
		s.Boost = 1 - int(v)
	}

	// --- memory -----------------------------------------------------------
	mi := map[string]uint64{}
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		mi[strings.TrimSuffix(f[0], ":")] = v * 1024
	}
	s.MemTotal = mi["MemTotal"]
	s.MemAvail = mi["MemAvailable"]
	s.Cached = mi["Cached"]
	s.Buffers = mi["Buffers"]
	if s.MemTotal > s.MemAvail {
		s.MemUsed = s.MemTotal - s.MemAvail
	}
	s.SwapTotal = mi["SwapTotal"]
	if s.SwapTotal > mi["SwapFree"] {
		s.SwapUsed = s.SwapTotal - mi["SwapFree"]
	}

	// --- filesystems ---------------------------------------------
	s.Mounts = readMounts()

	// --- disk I/O --------------------------------------------------------
	dr, dw := readDiskBytes()
	if c.primed && dt > 0 {
		if dr >= c.prevDiskR {
			s.DiskR = float64(dr-c.prevDiskR) / dt
		}
		if dw >= c.prevDiskW {
			s.DiskW = float64(dw-c.prevDiskW) / dt
		}
	}

	// --- network ------------------------------------------------------------
	iface, rx, tx := readNetBytes()
	s.NetIface = iface
	if c.primed && dt > 0 {
		if rx >= c.prevNetRx {
			s.NetRx = float64(rx-c.prevNetRx) / dt
		}
		if tx >= c.prevNetTx {
			s.NetTx = float64(tx-c.prevNetTx) / dt
		}
	}

	// --- sensors ------------------------------------------------------------
	s.Temps, s.Fans = readHwmon()
	s.CPUTemp = pickCPUTemp(s.Temps)
	s.CPUFan, s.CPUFanName = pickCPUFan(s.Fans)

	// --- GPU ---------------------------------------------------------------
	if m, _ := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent"); len(m) > 0 {
		if v, ok := readUint(m[0]); ok {
			s.GPUBusy, s.GPUok = float64(v), true
		}
	}

	c.prevAll, c.prevCores = all, cores
	c.prevDiskR, c.prevDiskW = dr, dw
	c.prevNetRx, c.prevNetTx = rx, tx
	c.prevAt, c.primed = now, true
	return s
}

func pctBusy(a, b cpuTimes) float64 {
	dt := float64(b.total) - float64(a.total)
	di := float64(b.idle) - float64(a.idle)
	if dt <= 0 {
		return 0
	}
	p := (dt - di) / dt * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p
}

func readCPUTimes() (cpuTimes, []cpuTimes) {
	var all cpuTimes
	var cores []cpuTimes
	for _, l := range strings.Split(readFile("/proc/stat"), "\n") {
		if !strings.HasPrefix(l, "cpu") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 8 {
			continue
		}
		var t cpuTimes
		for i := 1; i < len(f); i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				break
			}
			t.total += v
			// field 4 = idle, field 5 = iowait
			if i == 4 || i == 5 {
				t.idle += v
			}
		}
		if f[0] == "cpu" {
			all = t
		} else {
			cores = append(cores, t)
		}
	}
	return all, cores
}

var realFS = map[string]bool{
	"ext4": true, "ext3": true, "ext2": true, "xfs": true, "btrfs": true,
	"f2fs": true, "vfat": true, "exfat": true, "ntfs3": true, "zfs": true,
	"jfs": true, "reiserfs": true,
}

// unescapeMount decodes the octal escapes of /proc/mounts (\040 = space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func readMounts() []MountInfo {
	var out []MountInfo
	seen := map[string]bool{}
	for _, l := range strings.Split(readFile("/proc/mounts"), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !realFS[f[2]] {
			continue
		}
		dev := f[0]
		if seen[dev] {
			continue
		}
		path := unescapeMount(f[1])
		var st syscall.Statfs_t
		if syscall.Statfs(path, &st) != nil || st.Blocks == 0 {
			continue
		}
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		used := (st.Blocks - st.Bfree) * bs
		avail := st.Bavail * bs
		frac := 0.0
		if used+avail > 0 {
			frac = float64(used) / float64(used+avail)
		}
		seen[dev] = true
		out = append(out, MountInfo{Path: path, FS: f[2], Total: total, Used: used, Frac: frac})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// readDiskBytes sums the counters of whole disks, not partitions.
func readDiskBytes() (uint64, uint64) {
	var r, w uint64
	for _, l := range strings.Split(readFile("/proc/diskstats"), "\n") {
		f := strings.Fields(l)
		if len(f) < 10 {
			continue
		}
		name := f[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		// /sys/block/<name> only exists for whole disks, which keeps
		// partitions from being counted twice.
		if _, err := os.Stat("/sys/block/" + name); err != nil {
			continue
		}
		sr, _ := strconv.ParseUint(f[5], 10, 64)
		sw, _ := strconv.ParseUint(f[9], 10, 64)
		r += sr * 512
		w += sw * 512
	}
	return r, w
}

// readNetBytes sums every real interface and returns the busiest name.
func readNetBytes() (string, uint64, uint64) {
	var rx, tx uint64
	best, bestB := "", uint64(0)
	for _, l := range strings.Split(readFile("/proc/net/dev"), "\n") {
		i := strings.Index(l, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(l[:i])
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
			continue
		}
		f := strings.Fields(l[i+1:])
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
		if r+t > bestB {
			best, bestB = name, r+t
		}
	}
	if best == "" {
		best = "—"
	}
	return best, rx, tx
}

// prettyChip gives hwmon chips a readable name.
var chipNames = map[string]string{
	"k10temp":      "CPU",
	"coretemp":     "CPU",
	"zenpower":     "CPU",
	"amdgpu":       "GPU",
	"nouveau":      "GPU",
	"nvme":         "NVMe",
	"acpitz":       "ACPI",
	"gigabyte_wmi": "Board",
	"nct6798":      "Board",
	"nct6775":      "Board",
	"nct6797":      "Board",
	"it8686":       "Board",
	"iwlwifi_1":    "WiFi",
}

func prettyChip(name string) string {
	if v, ok := chipNames[name]; ok {
		return v
	}
	// Super-I/O chip families: Nuvoton (nct6xxx), ITE (it8xxx),
	// Fintek (f71xxx), SMSC. The suffix varies from model to model.
	if strings.HasPrefix(name, "nct") || strings.HasPrefix(name, "it8") ||
		strings.HasPrefix(name, "f71") || strings.HasPrefix(name, "smsc") {
		return "Board"
	}
	return name
}

func readHwmon() ([]Sensor, []Sensor) {
	var temps, fans []Sensor
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	sort.Strings(dirs)
	for _, d := range dirs {
		chip := strings.TrimSpace(readFile(filepath.Join(d, "name")))
		if chip == "" {
			chip = filepath.Base(d)
		}
		pretty := prettyChip(chip)

		files, _ := os.ReadDir(d)
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		sort.Strings(names)

		for _, n := range names {
			switch {
			case strings.HasPrefix(n, "temp") && strings.HasSuffix(n, "_input"):
				v, ok := readUint(filepath.Join(d, n))
				if !ok {
					continue
				}
				lbl := strings.TrimSpace(readFile(filepath.Join(d, strings.TrimSuffix(n, "_input")+"_label")))
				if lbl == "" {
					lbl = strings.TrimSuffix(n, "_input")
				}
				deg := float64(v) / 1000
				// Drop obviously bogus readings (acpitz at 16 °C, dead sensors).
				if deg < 1 || deg > 150 {
					continue
				}
				temps = append(temps, Sensor{Chip: pretty, Label: lbl, Value: deg})
			case strings.HasPrefix(n, "fan") && strings.HasSuffix(n, "_input"):
				v, ok := readUint(filepath.Join(d, n))
				if !ok {
					continue
				}
				// An unused header reads 0 forever. It cannot be told apart from
				// a stopped fan, but boards ship with far more headers than
				// fans actually plugged in, so zeros are dropped: otherwise the
				// panel fills up with nothing.
				if v == 0 {
					continue
				}
				lbl := strings.TrimSpace(readFile(filepath.Join(d, strings.TrimSuffix(n, "_input")+"_label")))
				if lbl == "" {
					lbl = strings.TrimSuffix(n, "_input")
				}
				fans = append(fans, Sensor{Chip: pretty, Label: lbl, Value: float64(v)})
			}
		}
	}
	return temps, fans
}

// pickCPUTemp prefers Tctl / Package / Tdie.
func pickCPUTemp(temps []Sensor) float64 {
	best := 0.0
	for _, t := range temps {
		if t.Chip != "CPU" {
			continue
		}
		l := strings.ToLower(t.Label)
		if strings.Contains(l, "tctl") || strings.Contains(l, "tdie") || strings.Contains(l, "package") {
			return t.Value
		}
		if t.Value > best {
			best = t.Value
		}
	}
	return best
}

// pickCPUFan looks for the fan attached to the CPU.
// Order of preference:
//
//  1. the SYSMONITOR_CPU_FAN environment variable (substring of "chip label")
//  2. a sensor the driver explicitly labels "CPU"
//  3. the first fan on the board Super-I/O: by near-universal convention
//     fan1 is the CPU_FAN header
//
// Returns -1 and an empty string when nothing matches.
func pickCPUFan(fans []Sensor) (float64, string) {
	id := func(f Sensor) string { return f.Chip + " " + f.Label }

	if want := strings.TrimSpace(os.Getenv("SYSMONITOR_CPU_FAN")); want != "" {
		for _, f := range fans {
			if strings.Contains(strings.ToLower(id(f)), strings.ToLower(want)) {
				return f.Value, id(f)
			}
		}
	}
	for _, f := range fans {
		if strings.Contains(strings.ToLower(id(f)), "cpu") {
			return f.Value, id(f)
		}
	}
	for _, f := range fans {
		if f.Chip == "Board" {
			return f.Value, id(f) + " (assumed)"
		}
	}
	return -1, ""
}

// fmtUptime formats as "3d 04h 12m"
func fmtUptime(d time.Duration) string {
	total := int(d.Seconds())
	days := total / 86400
	h := (total % 86400) / 3600
	m := (total % 3600) / 60
	if days > 0 {
		return itoa(days) + "j " + pad2(h) + "h " + pad2(m) + "m"
	}
	if h > 0 {
		return itoa(h) + "h " + pad2(m) + "m"
	}
	return itoa(m) + "m"
}

func itoa(v int) string { return strconv.Itoa(v) }

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}
