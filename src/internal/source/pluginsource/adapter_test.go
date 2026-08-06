package pluginsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

// fakeInvoker records invocations and returns canned results per method.
type fakeInvoker struct {
	id      string
	results map[string]any   // method → result value (JSON round-tripped)
	errs    map[string]error // method → forced error
	calls   []recordedCall
}

type recordedCall struct {
	capability pluginsdk.Capability
	method     string
	payload    any
}

func (f *fakeInvoker) ID() string { return f.id }

func (f *fakeInvoker) Invoke(_ context.Context, capability pluginsdk.Capability, method string, payload any, result any) error {
	f.calls = append(f.calls, recordedCall{capability, method, payload})
	if err := f.errs[method]; err != nil {
		return err
	}
	value, ok := f.results[method]
	if !ok || result == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

func connected(t *testing.T, f *fakeInvoker) *Adapter {
	t.Helper()
	a := &Adapter{}
	a.SetID("bridged")
	a.BindPluginLookup(func(id string) (Invoker, bool) {
		if id == f.id {
			return f, true
		}
		return nil, false
	})
	if err := a.Connect(context.Background(), map[string]any{"plugin": f.id}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return a
}

func TestRegisteredReadOnly(t *testing.T) {
	a, ok := source.New("plugin")
	if !ok {
		t.Fatal("plugin source adapter not registered")
	}
	// Read-only: the capability lint must see none of the write interfaces.
	if _, ok := a.(source.StateSetter); ok {
		t.Error("plugin source must not implement StateSetter")
	}
	if _, ok := a.(source.LabelAdder); ok {
		t.Error("plugin source must not implement LabelAdder")
	}
	if _, ok := a.(source.TaskPoller); ok {
		t.Error("plugin source must not implement TaskPoller")
	}
	if _, ok := a.(source.CIStatusPoller); ok {
		t.Error("plugin source must not implement CIStatusPoller")
	}
	if _, ok := a.(source.SubIssueCreator); ok {
		t.Error("plugin source must not implement SubIssueCreator")
	}
}

func TestConnectValidation(t *testing.T) {
	lookup := func(id string) (Invoker, bool) {
		if id == "com.example.ok" {
			return &fakeInvoker{id: id}, true
		}
		return nil, false
	}

	cases := []struct {
		name    string
		bind    bool
		cfg     map[string]any
		wantErr string
	}{
		{"missing plugin key", true, map[string]any{}, "config.plugin is required"},
		{"unknown plugin", true, map[string]any{"plugin": "com.example.nope"}, "not an enabled plugin"},
		{"no lookup injected", false, map[string]any{"plugin": "com.example.ok"}, "no plugin registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{}
			if tc.bind {
				a.BindPluginLookup(lookup)
			}
			err := a.Connect(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Connect = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}

	a := &Adapter{}
	a.BindPluginLookup(lookup)
	if err := a.Connect(context.Background(), map[string]any{"plugin": "com.example.ok"}); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestPollMapsItems(t *testing.T) {
	f := &fakeInvoker{id: "com.example.mon", results: map[string]any{
		pluginsdk.SourceMethodPoll: pluginsdk.SourcePollResult{Items: []pluginsdk.SourceItem{
			{
				ID:          "prob-1",
				Number:      "P-1",
				Title:       "Disk almost full",
				Description: "90% on /var",
				Labels:      []string{"severity:high", "host:db1"},
				Type:        "alert",
				Priority:    "high",
				State:       "open",
				URL:         "https://mon.example.com/prob-1",
				Metadata:    map[string]any{"origin": "nagios"},
				CreatedAt:   "2026-08-05T10:00:00Z",
				UpdatedAt:   "2026-08-05T11:00:00Z",
			},
			{ID: "prob-2"},         // minimal item: defaults fill in
			{Title: "no id, drop"}, // no ID → dropped
		}},
	}}
	a := connected(t, f)
	a.SetFilters([]string{"open"}, []string{"team:sre"})

	since := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	items, err := a.Poll(context.Background(), since)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (id-less dropped)", len(items))
	}

	full := items[0]
	if full.ID != "prob-1" || full.SourceID != "bridged" || full.Number != "P-1" || full.Priority != "high" || full.Type != "alert" {
		t.Errorf("mapped item wrong: %+v", full)
	}
	if !full.CreatedAt.Equal(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)) || !full.UpdatedAt.Equal(time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("timestamps wrong: %v / %v", full.CreatedAt, full.UpdatedAt)
	}

	minimal := items[1]
	if minimal.Number != "prob-2" || minimal.Title != "plugin item prob-2" {
		t.Errorf("defaults wrong: %+v", minimal)
	}
	if minimal.CreatedAt.IsZero() || !minimal.UpdatedAt.Equal(minimal.CreatedAt) {
		t.Errorf("time defaults wrong: %v / %v", minimal.CreatedAt, minimal.UpdatedAt)
	}

	// The poll payload carries since + filters for backend-side filtering.
	call := f.calls[len(f.calls)-1]
	if call.capability != pluginsdk.CapabilitySource || call.method != pluginsdk.SourceMethodPoll {
		t.Fatalf("unexpected call: %+v", call)
	}
	req, ok := call.payload.(pluginsdk.SourcePollRequest)
	if !ok {
		t.Fatalf("payload type %T", call.payload)
	}
	if req.Since != "2026-08-05T09:00:00Z" || len(req.States) != 1 || len(req.Labels) != 1 {
		t.Errorf("poll request wrong: %+v", req)
	}
}

func TestPollError(t *testing.T) {
	f := &fakeInvoker{id: "com.example.mon", errs: map[string]error{
		pluginsdk.SourceMethodPoll: errors.New("boom"),
	}}
	a := connected(t, f)
	if _, err := a.Poll(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Poll = %v, want wrapped boom", err)
	}
}

func TestAcknowledgeAndWriteResultForward(t *testing.T) {
	f := &fakeInvoker{id: "com.example.mon", results: map[string]any{
		pluginsdk.SourceMethodAcknowledge: pluginsdk.SourceOKResult{OK: true},
		pluginsdk.SourceMethodWriteResult: pluginsdk.SourceOKResult{OK: true},
	}}
	a := connected(t, f)

	cell := model.SourceItem{ID: "prob-1", Title: "t", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := a.Acknowledge(context.Background(), cell, "dispatched"); err != nil {
		t.Errorf("Acknowledge: %v", err)
	}
	ack := f.calls[len(f.calls)-1]
	if ack.method != pluginsdk.SourceMethodAcknowledge {
		t.Fatalf("ack method %q", ack.method)
	}
	if req := ack.payload.(pluginsdk.SourceAckRequest); req.Item.ID != "prob-1" || req.Action != "dispatched" {
		t.Errorf("ack payload wrong: %+v", req)
	}

	if err := a.WriteResult(context.Background(), cell, model.RunResult{Success: false, Output: "log", Error: fmt.Errorf("step failed")}); err != nil {
		t.Errorf("WriteResult: %v", err)
	}
	wr := f.calls[len(f.calls)-1].payload.(pluginsdk.SourceWriteResultRequest)
	if wr.Success || wr.Output != "log" || wr.Error != "step failed" {
		t.Errorf("write_result payload wrong: %+v", wr)
	}
}
