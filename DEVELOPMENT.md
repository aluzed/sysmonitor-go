# Development notes

Enough to pick the project up cold, with nothing in your head.

---

## 1. Where things stand

Working and complete. Nothing is half-finished.

| Item | State |
|---|---|
| Collection, rendering, layout | done |
| Adapts from 50 to 240 columns | verified across 11 sizes |
| Motherboard fans | working since the out-of-tree `it87` driver was installed |

### Toolchain

Go may not be on the `PATH`. On the development machine it lives in
`~/.local/go`, installed without privileges:

```sh
PATH=$HOME/.local/go/bin:$PATH go build -o sysmonitor .
```

`install.sh` handles this on its own, and downloads Go when it is missing.

### Reference hardware

Gigabyte B550 GAMING X V2, Ryzen 9 3950X (32 threads), Radeon GPU, NVMe.

The Super-I/O is an **IT8689E**, unsupported by the in-tree `it87` driver (which
stops at `it8628`). The out-of-tree
[frankcrawford/it87](https://github.com/frankcrawford/it87) driver handles it,
installed through DKMS and loaded at boot by `/etc/modules-load.d/it87.conf`.
It loaded **without** needing `ignore_resource_conflict=1`, so there is no ACPI
override in place and nothing fragile on that side.

To remove it: `sudo dkms remove it87/<version> --all`, then delete
`/etc/modules-load.d/it87.conf`.

---

## 2. Architecture

One-way flow, with no shared state beyond the `Collector` and the history rings:

```
/proc, /sys ──> Collector.Collect() ──> Snapshot ──> compose() ──> []string ──> tty
                (deltas, rates)         (immutable)   (layout)      (diffed)
```

| File | Role |
|---|---|
| `collect.go` | Kernel reads, delta computation, `Snapshot`. No display code. |
| `render.go`  | Primitives: colour, bars, braille graphs, ANSI measurement. No domain knowledge. |
| `cooler.go`  | Procedural rendering of the CPU cooler. |
| `main.go`    | `compose()` (layout), main loop, terminal handling. |

A `Snapshot` is produced in one shot per sample and then treated as read-only.
To add a metric: one field in `Snapshot`, fill it in `Collect()`, display it in
a panel. Nothing else to touch.

### Clocks

Three deliberately decoupled rates:

| What | Period | Why |
|---|---|---|
| Frame | 1/`fps` (66 ms) | smooth blade rotation |
| `/proc` sample | 500 ms | a shorter delta is noisy |
| History point | 1 s | one point = one second, readable |

---

## 3. The layout engine

**This is the delicate part.** Read it before touching a panel.

### Height contract

Every panel produces an **exact, predictable** number of lines. `compose()`
relies on these identities for its budget:

| Function | Content lines |
|---|---|
| `coolerContent` | `fanH + 4` |
| `cpuContent`    | `graphH + 5` |
| `coresContent`  | `cores.rows` (or 1 when compact) |
| `memContent`, `diskContent`, `sensorsContent` | exactly the `total` argument |

`box()` adds 2 lines (top and bottom border).

The three bottom panels end with `fit(out, total)`, which pads or truncates.
**Adding a line to a bottom panel just eats into its graph — nothing else needs
recomputing.** Adding a line to the cooler or the CPU panel, on the other hand,
breaks the budget: the matching constant in `compose()` must be adjusted.

### The three layouts

| Mode | Usable width | Structure | Exact total |
|---|---|---|---|
| A | ≥ 114 | cooler ∥ cpu · cores · mem ∥ disk ∥ sensors | `13 + fanH + rows + bottomH` |
| B | 68–113 | cooler ∥ cpu · cores · mem ∥ disk · sensors | `15 + fanH + rows + bottomH + sensH` |
| C | < 68 | everything stacked | best-effort |

`compose()` derives `bottomH` from those formulas, then converges by successive
reductions:

1. `fanH` shrinks down to 3, `sensH` down to 4;
2. if `bottomH` is still < 6, the core grid switches to **compact mode** (one
   line, one bar per thread) and everything is recomputed;
3. conversely, on a very tall terminal the surplus is handed back to the fan
   (`fanH` up to 19).

### The safety net

Last line of defence in `compose()`: panels are assembled **block by block**,
and a block that no longer fits is **dropped**, never cut.

```go
for _, b := range blocks {
    if len(out)+len(b)+1 > ht { continue }
    out = append(out, b...)
}
```

This is what guarantees no half-drawn frame at any terminal size. Do not remove
it while "optimising".

---

## 4. ANSI rendering pitfalls

**Every width measurement goes through `visLen()`**, never `len()` nor
`utf8.RuneCountInString()`. The strings carry escape sequences that occupy no
columns. Same for truncation: `clip()`, never a byte slice. `padTo` and
`padLeft` both build on `visLen`.

**Colours are only emitted on change**, not per character — otherwise an
86-column braille graph costs twenty times its size. Direct consequence:
`replaceCell()` (the cooler's mounting screws) must **re-emit the active colour**
after inserting its character, or the rest of the line loses its tint. The bug
is visual and subtle; the code is commented at that spot.

**Rendering is differential**: only changed lines are rewritten, positioned with
`\x1b[{line};1H`. Measured gain: 2.9 MB → 1.0 MB over 6 s. When the height
changes, `prev` is reset and the screen cleared (`\x1b[H\x1b[J`).

**Careful when testing**: `awk length($0)` and `wc -c` count **bytes**. Box
drawing characters are 3 bytes each — enough to make you believe in a massive
overflow that does not exist. Use `wc -L`, which counts display columns.

---

## 5. The cooler

No hard-coded artwork: every cell is classified in polar coordinates.

```
rad > 1.0        → heatsink fins (background)
rad ≥ 0.93       → shroud
rad < 0.17       → hub
otherwise        → blade, or the gap between blades
```

For a blade: the angle is offset by `phase + fanSweep*(1-rad)` — that term is
what **curves** the blades — then folded modulo `2π/7`. The angular distance to
the blade axis, multiplied by the radius, gives an arc width compared against
four thresholds that pick `█ ▓ ▒ ░`. Past the last one, the fins show
**through**, which is what creates the sense of depth.

Brightness is boosted on the leading edge (`lead`), hence the perceived
direction of rotation. Speed follows the real fan when it is known, CPU load
otherwise.

Constants at the top of `cooler.go`: `fanBlades`, `fanSweep`, `hubRadius`,
`rimInner`. They are meant to be tuned by eye.

---

## 6. Braille graphs

One braille cell = **2 columns × 4 dots**. `brailleArea` fills from the bottom
(a filled area, not a line).

```go
var brailleBits = [4][2]byte{{0x01,0x08},{0x02,0x10},{0x04,0x20},{0x40,0x80}}
//                            dot row 0    row 1       row 2       row 3
```

The character is `0x2800 + bits`. A graph of width `w` consumes the last `2w`
values; when there are not enough, the left columns stay blank (`⠀`, U+2800,
which does occupy one column).

Scale: 0–100 for percentages. For throughput, a rolling maximum with a **1 MiB/s
floor** — without that floor the graph goes wild on idle noise.

---

## 7. Kernel sources and their subtleties

| Data | Source | Worth knowing |
|---|---|---|
| CPU load | `/proc/stat` | `idle + iowait` are fields 4 and 5. A delta is mandatory. |
| Frequency | `/proc/cpuinfo` `cpu MHz` | **`scaling_cur_freq` lies under `amd-pstate`**: it returns 1750 MHz permanently. `/proc/cpuinfo` gives the measured value (aperf/mperf). Falls back to `cpufreq` when absent (ARM). |
| Turbo | `cpufreq/boost` or `intel_pstate/no_turbo` | The two have **inverted** semantics. |
| Memory | `/proc/meminfo` | `MemUsed = MemTotal - MemAvailable`, not `MemFree`. |
| Disks | `statfs` | `df`-style percentage: `used / (used + avail)`, not `used / total`. |
| I/O | `/proc/diskstats` | Fields 5 and 9, 512-byte sectors. Keep whole disks only, **filtered by the existence of `/sys/block/<name>`** — otherwise partitions are counted twice. |
| Tasks | `/proc/loadavg` field 4 | `running/total`, and "total" counts **threads**. |
| Sensors | `/sys/class/hwmon/*` | Enumerated dynamically. Temperatures outside 1–150 °C are dropped (dead sensors). Fans at 0 rpm are dropped: empty headers. |
| GPU | `/sys/class/drm/card*/device/gpu_busy_percent` | `amdgpu` only. Absent elsewhere → the field disappears. |

### Picking the CPU fan

`pickCPUFan()`, in order of preference:

1. the `SYSMONITOR_CPU_FAN` environment variable (substring of "chip label");
2. a sensor the driver labels "CPU";
3. **the first fan on the Super-I/O** — by convention `fan1` is the CPU_FAN
   header. Shown with an `(assumed)` marker, never presented as certain.

On the reference board no header is labelled, so case 3 applies. If `fan1` is
not the right header:

```sh
SYSMONITOR_CPU_FAN="fan2" sysmonitor
```

---

## 8. Testing

There are no unit tests: the program is almost entirely display code, and the
eye beats an assertion here. These recipes cover the essentials.

**Readable frame** (one frame, colours stripped):

```sh
sysmonitor -once -w 128 -h 44 | sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g' | cat -n
```

**Size matrix** — no frame may overflow:

```sh
for d in "128 44" "120 40" "150 50" "100 38" "90 34" "80 30" \
         "72 26" "60 24" "50 20" "200 60" "240 70"; do
  set -- $d
  r=$(sysmonitor -once -w $1 -h $2 | sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g')
  printf "%8s : %2d/%2d lines  col %3d/%3d  %s\n" "${1}x${2}" \
    "$(printf '%s\n' "$r" | wc -l)" "$2" \
    "$(printf '%s\n' "$r" | wc -L)" "$1" \
    "$([ $(printf '%s\n' "$r"|wc -l) -le $2 ] && \
       [ $(printf '%s\n' "$r"|wc -L) -le $1 ] && echo OK || echo OVERFLOW)"
done
```

**Real loop** in a pseudo-terminal, without hijacking your session:

```sh
timeout 6 script -qc "stty rows 44 cols 128; sysmonitor" /dev/null > /tmp/f.log
wc -c < /tmp/f.log        # ~1 MB for 6 s: differential rendering is working
stty sane                 # just in case
```

**Before committing**: `gofmt -l .` must be silent, and so must `go vet ./...`.

---

## 9. Ideas

Nothing essential, in decreasing order of interest:

- **Process list** (optional panel, toggled by a key) — the biggest gap against
  `htop`.
- **Fan speed history**, to watch the curve climb during a build.
- **Config file** (`~/.config/sysmonitor.toml`): colour thresholds, panels to
  hide, CPU fan selection. Today everything is hard-coded or an env var.
- **NVIDIA GPU metrics** through `nvidia-smi`, should the need arise.
- Unit tests for `visLen`, `clip` and `brailleArea` — the only functions that
  are genuinely testable without a terminal.
