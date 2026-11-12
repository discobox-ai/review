package review

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/discobox-ai/review/pkg/source"
	"github.com/discobox-ai/x/pkg/diff"
)

const (
	comparisonWork  = 8 << 20
	comparisonBytes = 16 << 20
	comparisonLines = 500000
)

// Compare derives approval identities and display hunks from one canonical
// script. Unavailable, invalid or oversized data returns an error rather than
// a partial comparison; the caller must leave its saved approvals untouched.
func Compare(change source.Change, context int) (Compared, error) {
	if change.Problem != "" {
		return Compared{}, fmt.Errorf("content verification unavailable: %s", change.Problem)
	}
	for _, side := range []source.Side{change.Old, change.New} {
		if !side.Present {
			if side.Path != "" || side.Mode != "" || len(side.Data) > 0 {
				return Compared{}, fmt.Errorf("absent content side carries metadata or bytes")
			}
			continue
		}
		if side.Path == "" || strings.IndexByte(side.Path, 0) >= 0 {
			return Compared{}, fmt.Errorf("content side has no valid path")
		}
		if len(side.Data) > comparisonBytes {
			return Compared{}, fmt.Errorf("content too large to verify approval")
		}
		if side.Mode == "160000" {
			value := string(side.Data)
			if len(value) != 40 && len(value) != 64 {
				return Compared{}, fmt.Errorf("gitlink has no complete object ID")
			}
			if _, err := hex.DecodeString(value); err != nil || value != strings.ToLower(value) {
				return Compared{}, fmt.Errorf("gitlink object ID is not lowercase hexadecimal")
			}
		}
		switch side.Mode {
		case "100644", "100755", "120000", "160000":
		default:
			return Compared{}, fmt.Errorf("unsupported file mode: %s", side.Mode)
		}
	}
	c := comparison{change: change}
	if change.Old.Mode == "160000" || change.New.Mode == "160000" || bytes.IndexByte(change.Old.Data, 0) >= 0 || bytes.IndexByte(change.New.Data, 0) >= 0 {
		return c.render(context)
	}
	tokens := func(data []byte) []string {
		lines := strings.SplitAfter(string(data), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	old, newLines := tokens(change.Old.Data), tokens(change.New.Data)
	if len(old) > comparisonLines || len(newLines) > comparisonLines-len(old) {
		return Compared{}, fmt.Errorf("too many lines to verify approval")
	}
	script, err := diff.Lines(old, newLines, comparisonWork)
	if err != nil {
		return Compared{}, fmt.Errorf("compare content pair: %w", err)
	}
	oi, ni := 0, 0
	var run *changeRun
	for _, edit := range script {
		if edit.Kind == diff.Equal {
			run = nil
			oi++
			ni++
			continue
		}
		if run == nil {
			c.runs = append(c.runs, changeRun{oldStart: oi, newStart: ni})
			run = &c.runs[len(c.runs)-1]
		}
		switch edit.Kind {
		case diff.Delete:
			run.removed = append(run.removed, edit.Text)
			oi++
		case diff.Insert:
			run.added = append(run.added, edit.Text)
			ni++
		}
	}
	return c.render(context)
}
