// Package procstat measures the resource usage of processes: how much CPU
// time they have consumed and how much memory they hold.
//
// It exists because "how much is the hive using?" cannot be answered from
// inside the daemon alone. The daemon knows its own usage, but the work that
// actually costs CPU and memory happens in the agent CLIs it spawns — an
// opencode or claude process per run, each with its own MCP servers and node
// children under it.
//
// The measurements are per-process and instantaneous:
//
//   - CPU is a *delta* of cumulative CPU time between two samples, not the
//     average ps reports. A short-lived agent that burns 2 seconds of CPU
//     over its 10-second life reads as 20% under ps's lifetime average, and
//     as 100% under a delta — the second is the number an operator means by
//     "is this thing pegging my CPU".
//   - Memory is the resident set size, which is what the kernel accounts and
//     what the operating system's own tools report.
//
// The package reads the system rather than tracking processes itself, so it
// works for anything running on the machine, including processes the daemon
// did not start.
package procstat

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Process is one process's resource usage at a point in time.
type Process struct {
	PID  int
	PPID int
	// Comm is the executable's path or name, as the system reports it.
	Comm string
	// RSS is the resident set size in bytes.
	RSS int64
	// CPUTime is the CPU time consumed since the process started.
	CPUTime time.Duration
	// CPUPercent is how much CPU the process used since the previous sample,
	// as a percentage of one core. It is zero on the first sample, when there
	// is nothing to difference against.
	CPUPercent float64
}

// Usage is a snapshot of the CPU and memory a set of processes is using.
type Usage struct {
	// Processes are the measured processes, in the order they were read.
	Processes []Process
	// TotalCPUTime is the sum of the CPU time of every process measured.
	TotalCPUTime time.Duration
	// TotalRSS is the sum of the resident set size of every process measured.
	TotalRSS int64
	// CPUPercent is the CPU used since the previous snapshot, as a percentage
	// of one core. It is zero when there is no previous snapshot to
	// difference against.
	CPUPercent float64
	// SampledAt is when the snapshot was taken.
	SampledAt time.Time
	// Baseline is true on the first snapshot: the totals are real but every
	// CPU percentage is zero, because there is no earlier reading to subtract.
	// A caller that shows a percentage should show "—" instead of 0%.
	Baseline bool
}

// TreeStats is the resource usage of one process and everything under it.
type TreeStats struct {
	PID int
	// Processes is how many processes the tree holds, the root included.
	Processes int
	// CPUPercent is the summed CPU of all of them, as a percentage of one
	// core: a tree using 200% is using two cores.
	CPUPercent float64
	// RSS is the summed resident set size in bytes.
	RSS int64
	// CPUTime is the summed CPU time consumed since each process started.
	CPUTime time.Duration
}

// Sampler measures process usage over time.
//
// It holds the previous snapshot so that successive reads can be differenced
// into an instantaneous CPU figure. The zero value is not usable: call
// NewSampler.
type Sampler struct {
	mu       sync.Mutex
	previous map[int]time.Duration
	prevAt   time.Time
}

// NewSampler returns a Sampler with no previous snapshot. The first Read
// establishes the baseline and reports a CPU percentage of zero; the second
// onwards report the real figure.
func NewSampler() *Sampler {
	return &Sampler{previous: map[int]time.Duration{}}
}

// Read takes a snapshot of the usage of every process on the machine, and
// computes the CPU used since the previous one.
//
// The whole machine is read because the daemon's own children are the point,
// and the tree is walked from the daemon's PID down.
func (s *Sampler) Read() (*Usage, error) {
	// The dashboard polls from a tea command, and a manual refresh can overlap
	// the timer: two reads racing on the previous snapshot would difference
	// against a baseline a few milliseconds old and report a bogus spike.
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := readAllProcesses()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	usage := &Usage{Processes: all, SampledAt: now, Baseline: s.prevAt.IsZero()}

	// The CPU used since the last snapshot is the sum of each process's own
	// growth. A process that started since the last snapshot has no previous
	// reading, so its whole lifetime counts — which is right: it started after
	// the last snapshot, so all of its CPU is new.
	var elapsed time.Duration
	if !s.prevAt.IsZero() {
		elapsed = now.Sub(s.prevAt)
	}
	var delta time.Duration
	for i := range usage.Processes {
		p := &usage.Processes[i]
		usage.TotalCPUTime += p.CPUTime
		usage.TotalRSS += p.RSS

		growth := p.CPUTime
		if prev, existed := s.previous[p.PID]; existed {
			growth = p.CPUTime - prev
		}
		if growth < 0 {
			// A PID reused, or a reading that went backwards: count nothing
			// rather than a negative that would cancel real work out.
			growth = 0
		}
		delta += growth
		if elapsed > 0 {
			p.CPUPercent = float64(growth) / float64(elapsed) * 100
		}
	}
	if elapsed > 0 {
		usage.CPUPercent = float64(delta) / float64(elapsed) * 100
	}

	// Remember this snapshot for the next read.
	s.previous = make(map[int]time.Duration, len(all))
	for _, p := range all {
		s.previous[p.PID] = p.CPUTime
	}
	s.prevAt = now

	return usage, nil
}

// Descendants returns the processes under rootPID, rootPID itself included.
//
// A process belongs to the tree when its parent is in it, so an agent CLI and
// the MCP servers and node children it spawned are all counted. The walk is
// breadth-first over the parent/child relation.
func (u *Usage) Descendants(rootPID int) []Process {
	byParent := map[int][]Process{}
	for _, p := range u.Processes {
		byParent[p.PPID] = append(byParent[p.PPID], p)
	}
	var out []Process
	seen := map[int]bool{}
	queue := []int{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		for _, child := range byParent[pid] {
			out = append(out, child)
			queue = append(queue, child.PID)
		}
	}
	return out
}

// TotalFor returns the CPU time and RSS of a set of processes.
func TotalFor(procs []Process) (cpu time.Duration, rss int64) {
	for _, p := range procs {
		cpu += p.CPUTime
		rss += p.RSS
	}
	return cpu, rss
}

// Process returns the measured process with the given pid.
func (u *Usage) Process(pid int) (Process, bool) {
	if u == nil {
		return Process{}, false
	}
	for _, p := range u.Processes {
		if p.PID == pid {
			return p, true
		}
	}
	return Process{}, false
}

// Tree returns the usage of rootPID and every process under it.
//
// The root is included on purpose: the question is "how much is the daemon
// costing", and the daemon's own tens of megabytes are part of that answer.
// A tree that used the parent/child relation but dropped the root would
// understate it every time.
func (u *Usage) Tree(rootPID int) TreeStats {
	if u == nil || rootPID <= 0 {
		return TreeStats{}
	}
	stats := TreeStats{PID: rootPID}
	procs := make([]Process, 0, 8)
	for _, p := range u.Processes {
		if p.PID == rootPID {
			procs = append(procs, p)
			break
		}
	}
	procs = append(procs, u.Descendants(rootPID)...)
	for _, p := range procs {
		stats.Processes++
		stats.CPUPercent += p.CPUPercent
		stats.RSS += p.RSS
		stats.CPUTime += p.CPUTime
	}
	return stats
}

// FormatBytes renders a byte count the way an operator reads it.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// parsePSDuration parses the CPU time ps reports.
//
// The format is [[dd-]hh:]mm:ss[.frac] — the fraction is hundredths of a
// second, which is what makes a delta between two samples accurate enough to
// be worth reading.
func parsePSDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var days int
	if i := strings.Index(s, "-"); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("parse days in %q: %w", s, err)
		}
		days = d
		s = s[i+1:]
	}
	var frac time.Duration
	if i := strings.Index(s, "."); i >= 0 {
		f, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return 0, fmt.Errorf("parse fraction in %q: %w", s, err)
		}
		frac = time.Duration(f) * 10 * time.Millisecond
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	var seconds int
	switch len(parts) {
	case 3:
		// hh:mm:ss
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("parse hours in %q: %w", s, err)
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, fmt.Errorf("parse minutes in %q: %w", s, err)
		}
		sec, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, fmt.Errorf("parse seconds in %q: %w", s, err)
		}
		seconds = h*3600 + m*60 + sec
	case 2:
		// mm:ss. The first field is minutes, not hours: darwin's ps prints
		// launchd as "117:59.69" for 117 minutes, so the leading field is
		// unbounded minutes. Dropping the seconds field here read "0:01.28"
		// as 280ms instead of 1.28s.
		m, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("parse minutes in %q: %w", s, err)
		}
		sec, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, fmt.Errorf("parse seconds in %q: %w", s, err)
		}
		seconds = m*60 + sec
	case 1:
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("parse seconds in %q: %w", s, err)
		}
		seconds = v
	default:
		return 0, fmt.Errorf("unrecognised duration %q", s)
	}
	return time.Duration(days*86400+seconds)*time.Second + frac, nil
}
