package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRemovedLegacyPathsRetainHistoryAndUpgrade(t *testing.T) {
	for _, withOther := range []bool{false, true} {
		original := `{"version":2,"branch":"main","files":[{"path":"gone","verdicts":[{"by":"a","hunks":["old-run"],"at":"then"}]}]}`
		fs := fakeFiles{FileName: original}
		err := Update(fs, "main", 0, func(st *State) error {
			ids := map[string]Identity{}
			if withOther {
				ids["other"] = Identity{Digest: "canonical", Fingerprint: "pair"}
			}
			st.ReconcileCanonical(ids, true)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		st, err := Parse([]byte(fs[FileName]))
		if err != nil {
			t.Fatal(err)
		}
		v := st.VerdictOf("gone", "a")
		if st.Version != 4 || len(fs) != 2 || v == nil || len(v.Legacy) != 1 || !v.Legacy[0].Retired || v.Legacy[0].Hunks[0] != "old-run" || v.Legacy[0].At != "then" {
			t.Fatalf("removed history or backup lost: %s, %v", fs[FileName], fs)
		}
	}
}

func TestDuplicateVerdictsKeepRetiredHistory(t *testing.T) {
	for _, duplicateFile := range []bool{false, true} {
		claim := LegacyApproval{Approved: true, Digest: "old", Fingerprint: "pair", Retired: true, Hunks: []string{"run"}}
		st := New("main")
		st.Scheme = IdentityScheme
		st.Approve("f", Identity{Digest: "canonical"}, "a")
		st.VerdictOf("f", "a").Legacy = []LegacyApproval{claim}
		later := &Verdict{By: "a", Approved: true, Digest: "replacement"}
		// A stale copy must not bring a retired claim back to pending.
		claim.Retired = false
		later.Legacy = []LegacyApproval{claim}
		if duplicateFile {
			st.Files = append(st.Files, &File{Path: "f", Verdicts: []*Verdict{later}})
		} else {
			st.Files[0].Verdicts = append(st.Files[0].Verdicts, later)
		}
		st.Version = 4
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		st, err = Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		v := st.VerdictOf("f", "a")
		if v.Digest != "replacement" || len(v.Legacy) != 1 || !v.Legacy[0].Retired || len(st.NeedsReview("f")) != 0 {
			t.Fatalf("history lost or revived: %+v", v)
		}
	}
}

func TestHandwrittenReplacementRetiresPendingClaims(t *testing.T) {
	st, err := Parse([]byte(`{"version":4,"scheme":"myers-lines-v1","files":[{"path":"f","verdicts":[
		{"by":"a","approved":false,"scheme":"myers-lines-v1","legacy":[{"approved":true,"digest":"old","fingerprint":"pair"}]},
		{"by":"a","approved":true,"digest":"replacement","scheme":"myers-lines-v1"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Approved("f", "a") || len(st.NeedsReview("f")) != 0 || len(st.VerdictOf("f", "a").Legacy) != 1 {
		t.Fatalf("replacement did not retire and preserve history: %s", st.Bytes())
	}
}

func TestUnknownVerdictSchemesAreRefused(t *testing.T) {
	for _, data := range []string{
		`{"version":4,"scheme":"myers-lines-v1","files":[{"path":"f","approved":true,"by":"a","scheme":"future"}]}`,
		`{"version":4,"scheme":"myers-lines-v1","files":[{"path":"f","verdicts":[{"by":"a","approved":true,"scheme":"future"}]}]}`,
		`{"version":2,"files":[{"path":"f","approved":true,"scheme":"myers-lines-v1"}]}`,
		`{"version":2,"files":[{"path":"f","legacy":[{"approved":true,"fingerprint":"pair"}]}]}`,
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted unsupported verdict: %s", data)
		}
	}
}

func TestHandwrittenLegacyReviewDoesNotManufactureVersionFour(t *testing.T) {
	st, err := Parse([]byte(`{"files":[{"path":"f","approved":true,"by":"a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := Parse(data)
	if err != nil || roundtrip.Version != versionWithout || roundtrip.Scheme != "" || !roundtrip.Approved("f", "a") {
		t.Fatalf("parse manufactured an invalid schema: %s, %v", data, err)
	}
}

func TestLegacyClaimsNeedEvidenceAndNeverRevive(t *testing.T) {
	for _, version := range []string{"1", "2", "3"} {
		t.Run(version, func(t *testing.T) {
			st, err := Parse([]byte(`{"version":` + version + `,"branch":"main","files":[{"path":"f","verdicts":[
				{"by":"a","approved":true,"digest":"old","hunks":["run"],"at":"then"},
				{"by":"b","approved":false,"nullified":true,"digest":"old"},
				{"by":"c","approved":true}]}]}`))
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]Identity{"f": {Digest: "canonical", Fingerprint: "pair-one", Runs: []string{"run"}}}
			if !st.ReconcileCanonical(ids, true) {
				t.Fatal("migration was not recorded")
			}
			if st.Approved("f", "a") || st.Approved("f", "b") || len(st.ApprovedHunks("f", "a")) != 0 {
				t.Fatal("legacy checksum granted a broader canonical approval")
			}
			v := st.VerdictOf("f", "a")
			if len(v.Legacy) != 1 || v.Legacy[0].At != "then" || v.Legacy[0].Digest != "old" || v.Legacy[0].Hunks[0] != "run" {
				t.Fatalf("original claim lost: %+v", v)
			}
			if got := st.NeedsReview("f"); len(got) != 1 || got[0] != "a" {
				t.Fatalf("needs review: %v", got)
			}
			if !st.Approved("f", "c") {
				t.Fatal("explicit current-change approval was not bound")
			}
			if st.ReconcileCanonical(ids, true) {
				t.Fatal("second migration changed the review")
			}
			ids["f"] = Identity{Digest: "edited", Fingerprint: "pair-two"}
			st.ReconcileCanonical(ids, true)
			ids["f"] = Identity{Digest: "canonical", Fingerprint: "pair-one"}
			st.ReconcileCanonical(ids, true)
			if len(st.NeedsReview("f")) != 0 || st.Approved("f", "c") {
				t.Fatal("undo revived a retired claim")
			}
			st.UnapproveAll("f")
			round, err := Parse(st.Bytes())
			if err != nil || round.Version != 4 || round.Scheme != IdentityScheme || len(round.Files) != 1 {
				t.Fatalf("upgrade or history lost after withdrawal: %s, %v", st.Bytes(), err)
			}
		})
	}
}

func TestUnavailablePairsLeaveLegacyClaimsUntouched(t *testing.T) {
	st := New("main")
	st.Files = []*File{{Path: "f", Verdicts: []*Verdict{{By: "a", Approved: true, Digest: "old"}}}}
	before := string(st.Bytes())
	if st.ReconcileCanonical(map[string]Identity{"f": {}}, true) || before != string(st.Bytes()) {
		t.Fatal("unavailable pair changed the claim")
	}
	if st.ReconcileCanonical(nil, false) || before != string(st.Bytes()) {
		t.Fatal("incomplete manifest changed the claim")
	}
}

func TestCanonicalPartialApprovalsNeverReviveAfterUndo(t *testing.T) {
	st := New("main")
	st.Scheme = IdentityScheme
	st.SetHunks("f", "a", []string{"first", "second"})
	ids := map[string]Identity{"f": {Digest: "one", Fingerprint: "pair-one", Runs: []string{"first", "second"}}}
	st.ReconcileCanonical(ids, true)
	ids["f"] = Identity{Digest: "two", Fingerprint: "pair-two", Runs: []string{"first", "changed"}}
	st.ReconcileCanonical(ids, true)
	ids["f"] = Identity{Digest: "one", Fingerprint: "pair-one", Runs: []string{"first", "second"}}
	st.ReconcileCanonical(ids, true)
	got := st.ApprovedHunks("f", "a")
	if len(got) != 1 || !got["first"] {
		t.Fatalf("unaffected run lost or disappeared run revived: %v", got)
	}
}

type backupFailure struct{ fakeFiles }

func (f backupFailure) WriteStateFile(name, content string) error {
	if strings.Contains(name, ".legacy-") {
		return errors.New("backup refused")
	}
	return f.fakeFiles.WriteStateFile(name, content)
}

func TestUpgradeRetainsOriginalBeforeReplacingReview(t *testing.T) {
	original := `{"version":2,"branch":"main","files":[{"path":"f","approved":true,"digest":"old","by":"a"}]}`
	change := func(st *State) error {
		st.ReconcileCanonical(map[string]Identity{"f": {Digest: "canonical", Fingerprint: "pair"}}, true)
		return nil
	}
	fs := fakeFiles{FileName: original}
	if err := Update(backupFailure{fs}, "main", 0, change); err == nil || fs[FileName] != original {
		t.Fatal("backup failure replaced original review")
	}
	if err := Update(fs, "main", 0, change); err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("expected one original backup: %v", fs)
	}
	for name, data := range fs {
		if name != FileName && data != original {
			t.Fatal("backup did not preserve exact bytes")
		}
	}
	if err := Update(fs, "main", 0, change); err != nil || len(fs) != 2 {
		t.Fatalf("upgrade is not idempotent: %v", err)
	}
}
