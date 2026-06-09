package dashboard

import (
	"reflect"
	"testing"
)

// TestFilteredTasksSortStableOnTies guards against a regression where the sort
// comparator was not a valid strict weak ordering for descending order: it
// negated the "less" boolean, so two equal elements compared true in BOTH
// directions. sort.SliceStable then reshuffled ties on every pass, so a list
// re-sorted on each refresh tick never settled (status-desc was the worst case
// because statuses tie constantly). Re-sorting must be idempotent.
func TestFilteredTasksSortStableOnTies(t *testing.T) {
	mk := func() *TasksTab {
		return &TasksTab{
			SortField: "status",
			SortAsc:   false, // descending — the broken direction
			History: []TaskItem{
				{Number: "1", Status: "running"},
				{Number: "2", Status: "done"},
				{Number: "3", Status: "running"},
				{Number: "4", Status: "done"},
				{Number: "5", Status: "running"},
			},
		}
	}
	a := &App{model: NewModel()}

	first := a.filteredTasks(mk())

	// Statuses must be in descending order.
	for i := 1; i < len(first); i++ {
		if first[i-1].Status < first[i].Status {
			t.Fatalf("not sorted desc: %q before %q", first[i-1].Status, first[i].Status)
		}
	}

	// Re-sorting the same input must yield the identical order every time.
	for n := 0; n < 5; n++ {
		got := a.filteredTasks(mk())
		var gotNums, wantNums []string
		for _, it := range got {
			gotNums = append(gotNums, it.Number)
		}
		for _, it := range first {
			wantNums = append(wantNums, it.Number)
		}
		if !reflect.DeepEqual(gotNums, wantNums) {
			t.Fatalf("sort not idempotent on ties: pass %d got %v, want %v", n, gotNums, wantNums)
		}
	}
}
