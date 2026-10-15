package ui

import (
	"strings"
	"testing"
)

// A path too long for the pane is cut in the middle, so both ends survive:
// the front says where in the tree the file is and the end says which file it
// is, and a cut at either end throws one of those away.
func TestALongNameIsCutInTheMiddle(t *testing.T) {
	const path = "internal/ui/reviewview.go"
	for _, w := range []int{1, 2, 3, 9, 16, 20, 24, 25, 40} {
		got := elideMiddle(path, w)
		if width(got) > w {
			t.Errorf("elideMiddle(%q, %d) = %q, %d columns wide", path, w, got, width(got))
		}
		if w >= width(path) {
			if got != path {
				t.Errorf("elideMiddle(%q, %d) = %q, want it whole", path, w, got)
			}
			continue
		}
		head, tail, cut := strings.Cut(got, "…")
		if !cut {
			t.Errorf("elideMiddle(%q, %d) = %q, with nothing to say it was cut", path, w, got)
			continue
		}
		if !strings.HasPrefix(path, head) || !strings.HasSuffix(path, tail) {
			t.Errorf("elideMiddle(%q, %d) = %q, which is not the two ends of it", path, w, got)
		}
		// Room for a name at all means the name is the end that gets it.
		if w >= 4 && tail == "" {
			t.Errorf("elideMiddle(%q, %d) = %q, which threw away the file name", path, w, got)
		}
	}
}

// The cut is made at a slash whenever the name it stands in front of will fit,
// so what follows the ellipsis is a whole path segment. A name with a hole in
// the middle of it reads as a different file; a path with one does not.
func TestTheCutIsMadeAtASlash(t *testing.T) {
	for _, c := range []struct {
		path string
		w    int
		want string
	}{
		{"internal/ui/reviewview.go", 25, "internal/ui/reviewview.go"},
		{"internal/ui/reviewview.go", 24, "internal/…/reviewview.go"},
		{"internal/ui/reviewview.go", 18, "int…/reviewview.go"},
		{"cmd/discobox-review/main.go", 10, "c…/main.go"},
		// The name will not fit even on its own, and there is no slash to cut
		// at: the two ends share what there is instead.
		{"internal/ui/reviewview.go", 14, "intern…view.go"},
		{"averylongfilenamewithnodirectoryatall.go", 14, "averyl…tall.go"},
	} {
		if got := elideMiddle(c.path, c.w); got != c.want {
			t.Errorf("elideMiddle(%q, %d) = %q, want %q", c.path, c.w, got, c.want)
		}
	}
}
