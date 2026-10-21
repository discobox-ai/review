package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// cardHelp is what card --help prints. It is the whole of the format, because
// it is what an agent asked to write a card is pointed at.
const cardHelp = `A card is the meat of a change: a few bullets of what changed, and the
places in the diff that are its core — apart from the tests, comments,
documentation and edits that only follow from another. The window
reviews the hunks of those places first, and the rest after.

With no argument it prints the card. set reads one from a file, or from
standard input given -, checks it, and keeps it in the review; clear
takes it out again. schema prints the JSON Schema a card is held to.

  {
    "base": "5e23e87",
    "summary": [
      "Harness manifests come from bundles, not runnable images",
      "A bundle is refused for a platform layer or a whiteout"
    ],
    "core": [
      "harness/imagelayer.go#L118-L148",
      "server/internal/resources/harnessconfigs/service.go#L664-L695"
    ]
  }

  base     the commit to review the change from, as a hash — not a ref,
           which moves. Usually the merge base: git merge-base HEAD
           origin/main. set expands it to the full hash. The window
           offers to switch to it.
  summary  what changed, a short bullet each, at most six.
  core     the places that need a reviewer's judgement, most important
           first. Not tests, comments, docs or mechanical edits.
  commit   the commit the line numbers were taken at; set fills in HEAD.

A core place is written:

  path#L12-L20           lines 12 to 20, as the working tree has them
  path#L12               one line
  path                   the whole file
  path?side=old#L4-L9    lines the change removed, as the base has them

Line numbers are the ones discobox-review diff prints.

set refuses a card that does not match the schema, and one whose links
do not hold: a path that is not in the change, a line past the end of
its file. Each is reported with where in
the card it was, so it can be put right.`

// card is the card command: print it, keep a new one, or take it out.
func card(out io.Writer, in io.Reader, repo *git.Repo, branch string, args []string, o options) error {
	verb := ""
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	switch verb {
	case "schema":
		if len(args) > 0 {
			return errors.New("card schema wants nothing more")
		}
		fmt.Fprint(out, review.CardSchema)
		return nil
	case "", "show":
		if len(args) > 0 {
			return errors.New("card wants nothing more to show it")
		}
		st, err := review.Load(repo, branch, lockWait)
		if err != nil {
			return fmt.Errorf("%s: %w", review.FileName, err)
		}
		if st.Card == nil {
			fmt.Fprintln(out, "no card: discobox-review card set <file> keeps one")
			return nil
		}
		if o.json {
			data, err := json.MarshalIndent(st.Card, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(data))
			return nil
		}
		fmt.Fprint(out, cardText(st.Card))
		return nil

	case "set":
		if len(args) != 1 {
			return errors.New("card set wants one file, or - for standard input")
		}
		c, problems, err := readCard(repo, in, args[0])
		if err != nil {
			return err
		}
		if c == nil {
			return cardProblems(problems)
		}
		if c.By == "" && o.by == "" {
			return errors.New("card set wants to know who wrote it: by in the card, or --by")
		}
		if o.by != "" {
			c.By = o.by
		}
		st, err := review.Load(repo, branch, lockWait)
		if err != nil {
			return fmt.Errorf("%s: %w", review.FileName, err)
		}
		if c.Commit == "" {
			c.Commit = repo.Head()
		}
		// Everything wrong is said at once: what the schema refused, a base
		// that is no commit, and the places, checked against the review's
		// own base when the card's cannot be used.
		base := st.Base
		if c.Base != "" {
			full, ok := repo.CommitOf(c.Base)
			switch {
			case ok && len(problems) > 0:
				base = full // a ref the schema refused still says where to look
			case ok:
				c.Base, base = full, full
			default:
				problems = append(problems, "base: "+c.Base+" is not a commit in this repository")
			}
		}
		places, err := checkPlaces(repo, base, c)
		if err != nil {
			return err
		}
		problems = append(problems, places...)
		if len(problems) > 0 {
			return cardProblems(problems)
		}
		c.At = review.Now()
		err = review.Update(repo, branch, lockWait, func(st *review.State) error {
			st.Card = c
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", review.FileName, err)
		}
		fmt.Fprintf(out, "card: %d core places\n", len(c.Core))
		// What the review is measured from is a commit too, the merge base,
		// whatever ref it was asked for by: that is what the two are compared
		// as.
		if now := repo.ResolveBase(st.Base, repo.HasHEAD()); c.Base != "" && c.Base != now.SHA {
			fmt.Fprintf(out, "it recommends reviewing from %s; the review is measured from %s (%s)\n",
				c.Base[:min(len(c.Base), 7)], nameOr(now.Ref, "nothing yet"), now.Commit)
			fmt.Fprintf(out, "  discobox-review base %s   to follow it\n", c.Base)
		}
		return nil

	case "clear":
		if len(args) > 0 {
			return errors.New("card clear wants nothing more")
		}
		err := review.Update(repo, branch, lockWait, func(st *review.State) error {
			st.Card = nil
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", review.FileName, err)
		}
		fmt.Fprintln(out, "card cleared")
		return nil
	}
	return fmt.Errorf("card %s: card takes set, clear, schema, or nothing", verb)
}

// readCard reads a card from a file, or from in for "-", and holds it to the
// schema: see [review.ParseCard]. A field the schema does not have is refused
// rather than dropped: it is nearly always one spelled wrong.
func readCard(repo *git.Repo, in io.Reader, name string) (*review.Card, []string, error) {
	var (
		data []byte
		err  error
	)
	if name == "-" {
		data, err = io.ReadAll(in)
	} else {
		data, err = repo.ReadInput(name)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the card: %w", err)
	}
	return review.ParseCard(data)
}

// cardProblems is what card set says about a card it will not keep.
func cardProblems(problems []string) error {
	return fmt.Errorf("the card does not hold together:\n  %s", strings.Join(problems, "\n  "))
}

// checkPlaces is every core place of a card that is not in the change measured
// from base: one that does not read, a path the review is not of, a line past
// the end of its file, or a run of lines with nothing in it the change made —
// which the window could not find a hunk for, and would leave out of the core
// without saying so.
func checkPlaces(repo *git.Repo, base string, c *review.Card) ([]string, error) {
	scan, err := repo.Scan(base, 0)
	if err != nil {
		return nil, err
	}
	if scan.Base.Err != "" {
		return nil, fmt.Errorf("the card's base: %s", scan.Base.Err)
	}
	// The lines of the change, by file.
	lines := map[string][]git.DiffLine{}
	for _, p := range git.ParseMulti(scan.Diff) {
		lines[p.Path] = p.Render()
	}

	// The new side is the card's commit, or the working tree when that is
	// what is checked out; the old side is the merge base.
	newSide := ""
	if head := repo.Head(); c.Commit != "" && !strings.HasPrefix(head, c.Commit) {
		newSide = c.Commit
	}
	lengths := map[string]int{}
	length := func(commit, path string) int {
		key := commit + "\x00" + path
		if n, ok := lengths[key]; ok {
			return n
		}
		n := -1
		if body, err := repo.FileContent(commit, path); err == nil {
			n = strings.Count(strings.TrimSuffix(body, "\n"), "\n") + 1
		}
		lengths[key] = n
		return n
	}

	var problems []string
	for i, at := range c.Core {
		where := fmt.Sprintf("core[%d]", i)
		l, ok := review.ParseLink(at)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %q is not a place: write path#L12-L20, the first line first", where, at))
			continue
		}
		if _, in := lines[l.Path]; !in {
			problems = append(problems, fmt.Sprintf("%s: %s is not in the change, measured from %s", where, l.Path, nameOr(base, "the base worked out")))
			continue
		}
		if l.First == 0 {
			if !slices.ContainsFunc(lines[l.Path], l.Holds) {
				problems = append(problems, fmt.Sprintf("%s: %s holds no line the change made", where, l.Path))
			}
			continue
		}
		commit, side := newSide, "as it now stands"
		if l.Side == review.SideOld {
			commit, side = scan.Base.SHA, "as the base has it"
		}
		switch n := length(commit, l.Path); {
		case n < 0:
			problems = append(problems, fmt.Sprintf("%s: %s has no lines %s", where, l.Path, side))
			continue
		case l.Last > n:
			problems = append(problems, fmt.Sprintf("%s: %s has %d lines %s, and %s runs past them", where, l.Path, n, side, l))
			continue
		}
		if !slices.ContainsFunc(lines[l.Path], l.Holds) {
			// A run of lines the change took something out of holds only the
			// seam; the lines it took are numbered as the base has them, and
			// the hint names them.
			hint, first, last := "", 0, 0
			for _, d := range lines[l.Path] {
				if d.Kind == '-' && d.Seam >= l.First && d.Seam <= l.Last {
					if first == 0 {
						first = d.Old
					}
					last = d.Old
				}
			}
			if first > 0 && l.Side != review.SideOld {
				hint = "; what it removed there is " + l.Path + "?side=old#L" + strconv.Itoa(first)
				if last > first {
					hint += "-L" + strconv.Itoa(last)
				}
			}
			problems = append(problems, fmt.Sprintf("%s: %s holds no line the change made%s", where, l, hint))
		}
	}
	return problems, nil
}

// cardText is the card as the command prints it, with every place written the
// way the other commands take one.
func cardText(c *review.Card) string { return c.Markdown() }
