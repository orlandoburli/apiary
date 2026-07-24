package daemon

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// gateSource is a minimal source.Adapter that also implements source.AuthorGate,
// controlled by test-supplied callbacks.
type gateSource struct {
	trusted  bool
	trustErr error
	parked   []model.SourceItem
	parkErr  error
}

func (g *gateSource) ID() string                                    { return "gate" }
func (g *gateSource) Connect(context.Context, map[string]any) error { return nil }
func (g *gateSource) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (g *gateSource) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (g *gateSource) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (g *gateSource) WebhookHandler() http.Handler { return nil }

func (g *gateSource) IsAuthorTrusted(_ context.Context, _ model.SourceItem) (bool, error) {
	return g.trusted, g.trustErr
}
func (g *gateSource) ParkUntrusted(_ context.Context, item model.SourceItem) error {
	g.parked = append(g.parked, item)
	return g.parkErr
}

// Ensure gateSource satisfies the interface at compile time.
var _ source.AuthorGate = (*gateSource)(nil)

// TestCheckAndParkUntrusted_TrustedAuthor verifies that a trusted author passes
// through: checkAndParkUntrusted returns false and nothing is parked.
func TestCheckAndParkUntrusted_TrustedAuthor(t *testing.T) {
	g := &gateSource{trusted: true}
	d := &Dispatcher{}
	cell := model.SourceItem{ID: "1", Title: "safe issue"}

	if skip := d.checkAndParkUntrusted(context.Background(), cell, g); skip {
		t.Error("want skip=false for trusted author, got true")
	}
	if len(g.parked) != 0 {
		t.Errorf("want no parked items, got %d", len(g.parked))
	}
}

// TestCheckAndParkUntrusted_UntrustedAuthor verifies that an untrusted author
// causes the item to be parked and checkAndParkUntrusted returns true.
func TestCheckAndParkUntrusted_UntrustedAuthor(t *testing.T) {
	g := &gateSource{trusted: false}
	d := &Dispatcher{}
	cell := model.SourceItem{ID: "7", Title: "untrusted issue"}

	if skip := d.checkAndParkUntrusted(context.Background(), cell, g); !skip {
		t.Error("want skip=true for untrusted author, got false")
	}
	if len(g.parked) != 1 || g.parked[0].ID != "7" {
		t.Errorf("want item 7 parked, got %v", g.parked)
	}
}

// TestCheckAndParkUntrusted_TrustCheckError verifies fail-open: when the trust
// check returns an error, the item is NOT parked and skip=false so dispatch
// proceeds rather than blocking legitimate work.
func TestCheckAndParkUntrusted_TrustCheckError(t *testing.T) {
	g := &gateSource{trusted: false, trustErr: errors.New("API timeout")}
	d := &Dispatcher{}
	cell := model.SourceItem{ID: "3", Title: "flaky check"}

	if skip := d.checkAndParkUntrusted(context.Background(), cell, g); skip {
		t.Error("want skip=false on trust-check error (fail-open), got true")
	}
	if len(g.parked) != 0 {
		t.Errorf("want no parked items on trust-check error, got %d", len(g.parked))
	}
}

// TestCheckAndParkUntrusted_NoGateInterface verifies that an adapter without
// the AuthorGate interface is a no-op: checkAndParkUntrusted returns false.
func TestCheckAndParkUntrusted_NoGateInterface(t *testing.T) {
	bare := &fakeBareSource{}
	d := &Dispatcher{}
	cell := model.SourceItem{ID: "5"}

	if skip := d.checkAndParkUntrusted(context.Background(), cell, bare); skip {
		t.Error("want skip=false when adapter has no AuthorGate interface, got true")
	}
}

// TestCheckAndParkUntrusted_ParkErrorDoesNotPreventSkip verifies that even
// when ParkUntrusted returns an error, the item is still skipped (we don't
// want to dispatch an untrusted item just because the label update failed).
func TestCheckAndParkUntrusted_ParkErrorDoesNotPreventSkip(t *testing.T) {
	g := &gateSource{trusted: false, parkErr: errors.New("label API down")}
	d := &Dispatcher{}
	cell := model.SourceItem{ID: "9", Title: "park fails"}

	if skip := d.checkAndParkUntrusted(context.Background(), cell, g); !skip {
		t.Error("want skip=true even when ParkUntrusted errors, got false")
	}
}
