package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/discobox-ai/review/pkg/source"
)

type pairSource struct {
	contents source.Contents
	err      error
}

func (s pairSource) Scan(string, int) (source.Scan, error)             { return source.Scan{}, nil }
func (s pairSource) ReadContents(source.Scan) (source.Contents, error) { return s.contents, s.err }
func (pairSource) CommitDiff(string, int) (string, error)              { return "", nil }
func (pairSource) FileContent(string, string) (string, error) {
	panic("comparison must reuse complete pairs")
}

func TestReadingIgnoresHostPatchAlignmentAndTruncation(t *testing.T) {
	pair := source.Change{Old: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("}\na\n}\n")}, New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("}\nb\n}\n")}}
	src := pairSource{contents: source.Contents{Complete: true, Files: []source.Change{pair}}}
	first := CompareScan(src, source.Scan{}, 3)
	for _, host := range []string{"", "diff --git a/f b/f\n@@ -99 +99 @@\n-host alignment\n+entirely different patch\n", "truncated patch"} {
		got := CompareScan(src, source.Scan{Diff: host, Incomplete: "host truncated the patch"}, 3)
		if !got.Complete || got.Scan.Incomplete != "" || got.Scan.Diff != first.Scan.Diff || !reflect.DeepEqual(got.Identities, first.Identities) {
			t.Fatalf("host patch affected canonical reading: %+v", got)
		}
	}
}

func TestUnavailableReadingRetainsClaimsAndHostDiagnostic(t *testing.T) {
	scan := source.Scan{Diff: "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n", Incomplete: "host diagnostic"}
	reading := CompareScan(pairSource{err: errors.New("offline")}, scan, 3)
	if reading.Complete || reading.Identities["f"].Digest != "" || !strings.Contains(reading.Scan.Incomplete, "host diagnostic") || !strings.Contains(reading.Scan.Diff, "+new") {
		t.Fatalf("unavailable reading: %+v", reading)
	}
	st := New("branch")
	st.Approve("f", Identity{Digest: "existing"}, "reader")
	before := string(st.Bytes())
	st.ReconcileCanonical(reading.Identities, reading.Complete)
	if string(st.Bytes()) != before {
		t.Fatal("unavailable reading changed saved claim")
	}
}

func TestMalformedManifestCannotVerifyOrRemovePaths(t *testing.T) {
	pair := source.Change{New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("new\n")}}
	r := CompareScan(pairSource{contents: source.Contents{Complete: true, Files: []source.Change{pair, pair}}}, source.Scan{}, 3)
	if r.Complete || r.Identities["f"].Digest != "" {
		t.Fatal("duplicate paths granted verification")
	}
	st := New("branch")
	st.Approve("elsewhere", Identity{Digest: "existing"}, "reader")
	before := string(st.Bytes())
	st.ReconcileCanonical(r.Identities, r.Complete)
	if string(st.Bytes()) != before {
		t.Fatal("invalid manifest removed omitted approval")
	}
}
