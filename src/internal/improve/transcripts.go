package improve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Hotspot is a step worth reading transcripts for, ranked by how much it costs
// the pipeline overall.
type Hotspot struct {
	WorkflowID string
	StepID     string
	Score      float64
}

// RankHotspots orders steps by cost × (1 + fail rate) × log-ish run weight, so
// both a cheap step that fails constantly and an expensive step that always
// passes surface. Ranking on cost alone would hide the flaky-but-cheap steps
// that waste the most wall-clock.
func RankHotspots(steps []StepMetrics, limit int) []Hotspot {
	out := make([]Hotspot, 0, len(steps))
	for _, s := range steps {
		if s.Runs == 0 {
			continue
		}
		// Reworked/failed runs weigh double: a failure costs its own tokens and
		// then whatever the retry costs.
		score := s.TotalCostUSD * (1 + 2*s.FailRate)
		// A step with no recorded cost (a runner that reports none) still matters
		// if it fails; fall back to run count so it is not invisible.
		if score == 0 {
			score = float64(s.Runs) * s.FailRate
		}
		out = append(out, Hotspot{WorkflowID: s.WorkflowID, StepID: s.StepID, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].WorkflowID != out[j].WorkflowID {
			return out[i].WorkflowID < out[j].WorkflowID
		}
		return out[i].StepID < out[j].StepID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// transcriptRef points at one transcript file on disk together with the outcome
// of the run that produced it.
type transcriptRef struct {
	taskID  string
	file    string
	outcome string
}

// SampleTranscripts reads up to perHotspot transcripts for each hotspot: the
// most recent failures first, plus one successful run as a control. The control
// matters — an advisor shown only failures will confidently explain a difference
// that is not there.
//
// Missing transcript files are skipped silently: transcripts are pruned by log
// retention while the rows that reference them survive, so their absence is
// normal rather than an error.
func SampleTranscripts(ctx context.Context, db source, logDir string, w Window, hotspots []Hotspot, perHotspot, byteBudget int) ([]TranscriptExcerpt, error) {
	if perHotspot <= 0 || len(hotspots) == 0 {
		return nil, nil
	}

	var out []TranscriptExcerpt
	for _, h := range hotspots {
		refs, err := transcriptRefs(ctx, db, logDir, w, h, perHotspot)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			path := filepath.Join(logDir, "transcripts", ref.taskID, ref.file)
			data, err := os.ReadFile(path)
			if err != nil {
				continue // pruned by retention, or never written
			}
			content := elide(string(data), byteBudget)
			out = append(out, TranscriptExcerpt{
				WorkflowID: h.WorkflowID,
				StepID:     h.StepID,
				TaskID:     ref.taskID,
				File:       ref.file,
				Outcome:    ref.outcome,
				Bytes:      len(data),
				Content:    content,
			})
		}
	}
	return out, nil
}

// transcriptRefs finds the task ids whose runs of this step are worth reading:
// the most recent failures, then one success.
func transcriptRefs(ctx context.Context, db source, logDir string, w Window, h Hotspot, n int) ([]transcriptRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(te.task_id, ''), sr.state
		FROM step_runs sr
		JOIN workflow_instances wi ON wi.id = sr.workflow_instance_id
		LEFT JOIN task_executions te
		       ON te.workflow_instance_id = sr.workflow_instance_id
		      AND te.step_id = sr.step_id
		WHERE wi.workflow_id = ? AND sr.step_id = ?
		  AND sr.started_at >= ? AND sr.started_at < ?
		  AND sr.state IN ('failed', 'passed')
		GROUP BY sr.id
		ORDER BY CASE sr.state WHEN 'failed' THEN 0 ELSE 1 END, sr.started_at DESC`,
		h.WorkflowID, h.StepID, w.Start, w.End)
	if err != nil {
		return nil, fmt.Errorf("query transcript refs: %w", err)
	}
	defer rows.Close()

	var refs []transcriptRef
	failures, successes := 0, 0
	// n-1 failures plus exactly one success, so every hotspot carries a control.
	maxFailures := n - 1
	if maxFailures < 1 {
		maxFailures = 1
	}

	for rows.Next() {
		var taskID, state string
		if err := rows.Scan(&taskID, &state); err != nil {
			return nil, fmt.Errorf("scan transcript ref: %w", err)
		}
		if taskID == "" {
			continue
		}
		if state == "failed" && failures >= maxFailures {
			continue
		}
		if state == "passed" && successes >= 1 {
			continue
		}
		file := transcriptForStep(filepath.Join(logDir, "transcripts", taskID), h.StepID)
		if file == "" {
			continue
		}
		if state == "failed" {
			failures++
		} else {
			successes++
		}
		refs = append(refs, transcriptRef{taskID: taskID, file: file, outcome: state})
		if len(refs) >= n {
			break
		}
	}
	return refs, rows.Err()
}

// transcriptForStep returns the transcript belonging to a specific step of a
// task, or "" when there is none.
//
// A task's transcript directory holds one file per step run, named
// "<instance>_<n>-<stepID>.md". Selecting by modification time instead returns
// whichever step ran LAST — almost always the final step of the workflow — so
// every hotspot receives the same terminal transcript regardless of which step
// was being sampled. That silently replaces the evidence the analysis depends
// on: an "implement" hotspot gets handed the "merge" session, and the whole
// point of reading transcripts is lost.
//
// When several runs of the same step exist (a rework loop — precisely the case
// worth reading), the most recent is returned, since that is the attempt whose
// outcome the metrics describe.
func transcriptForStep(dir, stepID string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	suffix := "-" + stepID + ".md"

	var best string
	var bestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); mod > bestMod {
			bestMod, best = mod, e.Name()
		}
	}
	return best
}

// elide truncates a transcript to a byte budget, keeping the head and the tail.
// The middle of an agent session is usually repetitive tool traffic; the setup
// and the outcome are where the signal is.
func elide(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	const marker = "\n\n… [middle elided] …\n\n"
	half := (budget - len(marker)) / 2
	if half <= 0 {
		return s[:budget]
	}
	return s[:half] + marker + s[len(s)-half:]
}
