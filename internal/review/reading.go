package review

import (
	"slices"
	"sort"
	"strings"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/pkg/source"
)

// Reading binds presentation, verification and the changed-path manifest.
// Unavailable paths explicitly have empty identities and a named problem.
type Reading struct {
	Scan       source.Scan
	Contents   source.Contents
	Files      []Compared
	Identities map[string]Identity
	Problems   map[string]string
	Complete   bool
}

// CompareScan uses complete pairs for both identity and display. A host patch
// is only a readable fallback when a pair cannot be verified, never evidence
// with which to grant or invalidate approval.
func CompareScan(src source.Source, scan source.Scan, context int) Reading {
	r := Reading{Scan: scan, Identities: map[string]Identity{}, Problems: map[string]string{}}
	fallback := map[string]*git.Patch{}
	for _, p := range git.ParseMulti(scan.Diff) {
		if p.Path != "" {
			fallback[p.Path] = p
		}
	}
	contents, err := src.ReadContents(scan)
	if err != nil {
		r.Scan.Incomplete = strings.TrimSpace(scan.Incomplete + "\nApproval verification unavailable: " + err.Error())
		for path, p := range fallback {
			r.Files = append(r.Files, Compared{Patch: p})
			r.Identities[path] = Identity{}
			r.Problems[path] = err.Error()
		}
	} else {
		contents.Files = slices.Clone(contents.Files)
		validPaths := map[string]bool{}
		invalid := false
		for _, change := range contents.Files {
			path := change.New.Path
			if !change.New.Present {
				path = change.Old.Path
			}
			if path == "" || validPaths[path] {
				invalid = true
			}
			validPaths[path] = true
		}
		if invalid {
			contents.Complete = false
			r.Scan.Incomplete = "Approval verification unavailable: invalid changed-path manifest"
			for i := range contents.Files {
				contents.Files[i].Problem = "invalid changed-path manifest"
			}
		}
		r.Contents, r.Complete = contents, contents.Complete
		seen := map[string]bool{}
		for _, change := range contents.Files {
			path := change.New.Path
			if !change.New.Present {
				path = change.Old.Path
			}
			if path == "" || seen[path] {
				// A malformed manifest cannot justify dropping an omitted path.
				r.Complete = false
				r.Scan.Incomplete = "Approval verification unavailable: invalid changed-path manifest"
				continue
			}
			seen[path] = true
			file, err := Compare(change, context)
			if err != nil {
				p := fallback[path]
				if p == nil {
					oldPath := path
					if change.Old.Present {
						oldPath = change.Old.Path
					}
					p = &git.Patch{Path: path, Preamble: []string{
						"diff --git " + git.QuotePath("a/"+oldPath) + " " + git.QuotePath("b/"+path),
						"Approval verification unavailable",
					}}
					if oldPath != path {
						p.From = oldPath
					}
				}
				r.Files = append(r.Files, Compared{Patch: p})
				r.Identities[path] = Identity{}
				r.Problems[path] = err.Error()
				continue
			}
			if file.Patch != nil {
				r.Files = append(r.Files, file)
				r.Identities[path] = file.Identity
			}
		}
		if !contents.Complete {
			for path, p := range fallback {
				if !seen[path] {
					r.Files = append(r.Files, Compared{Patch: p})
					r.Identities[path] = Identity{}
					r.Problems[path] = "incomplete changed-path manifest"
				}
			}
		} else if r.Scan.Incomplete == scan.Incomplete && r.Complete {
			r.Scan.Incomplete = ""
		}
	}
	sort.Slice(r.Files, func(i, j int) bool { return r.Files[i].Patch.Path < r.Files[j].Patch.Path })
	var display strings.Builder
	for _, file := range r.Files {
		display.WriteString(file.Patch.Unified())
	}
	r.Scan.Diff = display.String()
	return r
}
