package review

import (
	"os"
	"reflect"
	"testing"

	"github.com/discobox-ai/review/pkg/source"
)

func TestCanonicalComparisonSurvivesRealignedFences(t *testing.T) {
	old, err := os.ReadFile("testdata/context-alignment/base.md")
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile("testdata/context-alignment/head.md")
	if err != nil {
		t.Fatal(err)
	}
	pair := source.Change{Old: source.Side{Path: "AGENTS.md", Mode: "100644", Present: true, Data: old}, New: source.Side{Path: "AGENTS.md", Mode: "100644", Present: true, Data: newBytes}}
	initial, err := Compare(pair, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := New("branch")
	st.ReconcileCanonical(map[string]Identity{"AGENTS.md": initial.Identity}, true)
	st.Approve("AGENTS.md", initial.Identity, "reviewer")
	st.SetHunks("AGENTS.md", "partial", initial.Identity.Runs)
	for _, context := range []int{0, 1, 3, 5, 100, 1000} {
		got, err := Compare(pair, context)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Identity, initial.Identity) {
			t.Fatalf("context %d changed canonical identity", context)
		}
		st.ReconcileCanonical(map[string]Identity{"AGENTS.md": got.Identity}, true)
		if !st.Approved("AGENTS.md", "reviewer") || len(st.ApprovedHunks("AGENTS.md", "partial")) != len(initial.Identity.Runs) {
			t.Fatalf("context %d changed approvals", context)
		}
		var runs []string
		for _, h := range got.Hunks {
			runs = append(runs, h...)
		}
		if !reflect.DeepEqual(runs, initial.Identity.Runs) {
			t.Fatalf("context %d hunk scope diverged", context)
		}
	}
	pair.New.Data = append(append([]byte(nil), newBytes...), []byte("\nactual edit\n")...)
	changed, err := Compare(pair, 5)
	if err != nil {
		t.Fatal(err)
	}
	st.ReconcileCanonical(map[string]Identity{"AGENTS.md": changed.Identity}, true)
	if st.Approved("AGENTS.md", "reviewer") || !st.Nullified("AGENTS.md", "reviewer") {
		t.Fatal("actual edit did not invalidate file approval")
	}
}
