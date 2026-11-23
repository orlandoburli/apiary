//go:build darwin

package procstat

import (
	"os/exec"
	"strconv"
	"strings"
)

// readAllProcesses reads every process on the machine.
//
// ps is the system of record on darwin: it reports the parent PID, the
// resident set size and the cumulative CPU time in one pass, which is exactly
// what a tree walk and a delta both need.
func readAllProcesses() ([]Process, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,time=,comm=").CombinedOutput()
	if err != nil {
		return nil, err
	}
	var procs []Process
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		rssKB, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		cpu, err := parsePSDuration(fields[3])
		if err != nil {
			continue
		}
		// The rest of the line is the executable path: a path with spaces is
		// not broken apart.
		comm := strings.Join(fields[4:], " ")
		procs = append(procs, Process{
			PID:     pid,
			PPID:    ppid,
			Comm:    comm,
			RSS:     rssKB * 1024,
			CPUTime: cpu,
		})
	}
	return procs, nil
}
