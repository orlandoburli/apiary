//go:build linux

package procstat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// clockTicks is the number of scheduler ticks per second. Linux defines it as
// 100 on every architecture it supports in practice, and the kernel exposes
// it as USER_HZ; the conversion below uses this constant.
//
// The kernel's tick counter is what /proc reports for a process's CPU time, so
// this is the granularity of the measurement.
const clockTicks = 100

// readAllProcesses reads every process on the machine.
//
// /proc is the system of record on linux: it reports the parent PID, the
// resident set size and the CPU time in ticks, and reading it directly avoids
// spawning ps for a snapshot the dashboard takes every couple of seconds.
func readAllProcesses() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []Process
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, ok := readLinuxProcess(pid, e.Name())
		if !ok {
			continue
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// readLinuxProcess reads one process's stat line and status file.
func readLinuxProcess(pid int, name string) (Process, bool) {
	stat, err := os.ReadFile(filepath.Join("/proc", name, "stat"))
	if err != nil {
		return Process{}, false
	}
	// The comm field is in parentheses and may itself contain spaces and
	// parentheses, so the fields after it are located from the LAST closing
	// parenthesis rather than by splitting the whole line.
	s := string(stat)
	open := strings.Index(s, "(")
	close := strings.LastIndex(s, ")")
	if open < 0 || close < open {
		return Process{}, false
	}
	comm := s[open+1 : close]
	fields := strings.Fields(s[close+1:])
	// fields[0] is the state; utime is field 14 and stime field 15 of the
	// whole stat line, which is index 11 and 12 after the comm.
	if len(fields) < 13 {
		return Process{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return Process{}, false
	}
	rss, ok := readLinuxRSS(name)
	if !ok {
		return Process{}, false
	}
	return Process{
		PID:     pid,
		PPID:    ppid,
		Comm:    comm,
		RSS:     rss,
		CPUTime: time.Duration(utime+stime) * time.Second / clockTicks,
	}, true
}

// readLinuxRSS reads the resident set size from /proc/<pid>/statm, which
// reports it in pages.
//
// statm is used rather than status because it is one line and one field; the
// VmRSS in status is the same number spelled out in kilobytes.
func readLinuxRSS(name string) (int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", name, "statm"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, false
	}
	// The second field is the resident set size in pages.
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}
