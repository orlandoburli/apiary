package daemon

import "time"

// StatusResponse is the JSON payload returned by GET /status on the IPC socket.
type StatusResponse struct {
	Version     string             `json:"version"`
	ConfigFile  string             `json:"config_file"`
	Uptime      string             `json:"uptime"`
	Sources     []SourceStatus     `json:"sources"`
	ActiveRuns  []ActiveRunStatus  `json:"active_runs"`
	Concurrency ConcurrencyStatus  `json:"concurrency"`
}

type SourceStatus struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	LastPoll   string `json:"last_poll"`   // human-readable "Xs ago"
	LastCount  int    `json:"last_count"`  // cells found in last poll
	InFlight   int    `json:"in_flight"`   // cells currently being dispatched from this source
}

type ActiveRunStatus struct {
	ID       string `json:"id"`
	CellID   string `json:"cell_id"`
	Title    string `json:"title"`
	WorkerID string `json:"worker_id"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	Elapsed  string `json:"elapsed"`
}

type ConcurrencyStatus struct {
	Max    int `json:"max"`
	Active int `json:"active"`
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	if d < time.Hour {
		m := d / time.Minute
		s := (d % time.Minute) / time.Second
		return formatMinSec(int(m), int(s))
	}
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	return formatHourMin(int(h), int(m))
}

func formatMinSec(m, s int) string {
	return padTwo(m) + ":" + padTwo(s)
}

func formatHourMin(h, m int) string {
	return padTwo(h) + "h" + padTwo(m) + "m"
}

func padTwo(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
