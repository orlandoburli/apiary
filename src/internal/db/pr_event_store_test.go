package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPREventWatermarkRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "wm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	wm, err := c.GetPREventWatermark(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if !wm.IsZero() {
		t.Errorf("unset watermark must be zero, got %v", wm)
	}

	want := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	if err := c.SetPREventWatermark(ctx, "github", want); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetPREventWatermark(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("watermark = %v, want %v", got, want)
	}

	// Upsert advances in place.
	later := want.Add(time.Hour)
	if err := c.SetPREventWatermark(ctx, "github", later); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.GetPREventWatermark(ctx, "github"); !got.Equal(later) {
		t.Errorf("watermark after upsert = %v, want %v", got, later)
	}
}

func TestClaimPREventDispatch_ExactlyOnce(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	claimed, err := c.ClaimPREventDispatch(ctx, "github", "comment-1", "wf-a", 7)
	if err != nil || !claimed {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", claimed, err)
	}
	claimed, err = c.ClaimPREventDispatch(ctx, "github", "comment-1", "wf-a", 7)
	if err != nil || claimed {
		t.Fatalf("second claim = (%v, %v), want (false, nil)", claimed, err)
	}
	// Same event may claim a different workflow.
	if claimed, _ := c.ClaimPREventDispatch(ctx, "github", "comment-1", "wf-b", 7); !claimed {
		t.Error("a different workflow must claim the same event independently")
	}
	// Different event on the same PR claims fresh.
	if claimed, _ := c.ClaimPREventDispatch(ctx, "github", "comment-2", "wf-a", 7); !claimed {
		t.Error("a different event must claim independently")
	}

	// max_dispatches denominator counts per (source, workflow, PR).
	if n, _ := c.CountPREventDispatches(ctx, "github", "wf-a", 7); n != 2 {
		t.Errorf("wf-a dispatches on PR 7 = %d, want 2", n)
	}
	if n, _ := c.CountPREventDispatches(ctx, "github", "wf-b", 7); n != 1 {
		t.Errorf("wf-b dispatches on PR 7 = %d, want 1", n)
	}
	if n, _ := c.CountPREventDispatches(ctx, "github", "wf-a", 99); n != 0 {
		t.Errorf("dispatches on another PR = %d, want 0", n)
	}
}
