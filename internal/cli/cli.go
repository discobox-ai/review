// Package cli is difftui without the window: the review read and written from
// a command line.
//
// The review has always been a file so that something other than difftui could
// act on it. These are that something's hands. An agent answering a remark can
// run one command rather than parse, edit and re-emit a JSON document, and get
// the ids, the timestamps and the branch swap right by construction — and a
// window open on the same review picks the change up within the second.
//
// Every command reads the file, changes it, and writes it back. Nothing is
// held between commands, which is what makes them safe to interleave with a
// running window.
package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/discobox-ai/difftui/internal/git"
	"github.com/discobox-ai/difftui/internal/review"
)

// Commands is every subcommand, in the order help lists them.
var Commands = []struct {
	Name, Args, What string
}{
	{"diff", "[--path P] [--stat] [--raw]", "the review itself"},
	{"base", "[<ref>] [--auto]", "show or set what it is measured from"},
	{"list", "[--path P] [--open] [--resolved]", "list the comments"},
	{"show", "<id>", "one conversation in full"},
	{"comment", "<path>:<line>[-<line>] <body>", "start a conversation"},
	{"reply", "<id> <body>", "answer one"},
	{"resolve", "<id>…", "close one"},
	{"reopen", "<id>…", "open one again"},
	{"delete", "<id> [n]", "delete a conversation, or one remark of it"},
	{"approve", "<path>…", "sign off a file"},
	{"unapprove", "<path>…", "take that back"},
	{"status", "", "what is left to do"},
}

// Is reports whether a word names a subcommand, which is what tells the
// program to do this rather than open a window.
func Is(word string) bool {
	for _, c := range Commands {
		if c.Name == word {
			return true
		}
	}
	return word == "help"
}

// Run carries out one subcommand. args is the command and its arguments.
func Run(repo *git.Repo, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" {
		Usage(out)
		return nil
	}
	cmd, rest := args[0], args[1:]

	// Flags first, so that the positional arguments left are just that.
	opts, rest, err := parseFlags(rest)
	if err != nil {
		return err
	}

	branch := repo.CurrentBranch()
	st, err := review.Load(repo, branch)
	if err != nil {
		return fmt.Errorf("%s: %w", review.FileName, err)
	}

	switch cmd {
	case "diff":
		return diff(out, repo, st, opts)
	case "list":
		return list(out, st, opts)
	case "status":
		return status(out, repo, st, branch, opts)
	case "show":
		return show(out, st, rest)
	}

	// The rest change something, so they save.
	var note string
	switch cmd {
	case "comment":
		note, err = comment(st, repo, rest, opts)
	case "reply":
		note, err = reply(st, repo, rest, opts)
	case "resolve":
		note, err = setResolved(st, rest, true)
	case "reopen":
		note, err = setResolved(st, rest, false)
	case "base":
		note, err = setBase(out, st, rest, opts)
	case "delete":
		note, err = remove(st, rest)
	case "approve":
		note, err = setApproved(st, repo, rest, opts, true)
	case "unapprove":
		note, err = setApproved(st, repo, rest, opts, false)
	default:
		return fmt.Errorf("no such command: %s", cmd)
	}
	if err != nil {
		return err
	}
	if err := review.Save(repo, branch, st); err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(out, note)
	}
	return nil
}

// Usage is the help these commands print, and the one the window's own help
// points at.
func Usage(out io.Writer) {
	fmt.Fprintln(out, "difftui reviews a branch. With no command it opens a window;")
	fmt.Fprintln(out, "these read and write the same review from a script.")
	fmt.Fprintln(out)
	for _, c := range Commands {
		fmt.Fprintf(out, "  difftui %-9s %-32s %s\n", c.Name, c.Args, c.What)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  --base REF   measure this one diff from here, without saving it")
	fmt.Fprintln(out, "  --auto       let difftui work the base out again")
	fmt.Fprintln(out, "  --context N  lines of context either side (default", strconv.Itoa(git.DefaultContext)+")")
	fmt.Fprintln(out, "  --stat       just the files and what stands against them")
	fmt.Fprintln(out, "  --raw        the plain unified diff, for piping somewhere else")
	fmt.Fprintln(out, "  --by NAME    who a remark is from (default: git's user.name)")
	fmt.Fprintln(out, "  --side old   anchor to the line the change removed, not the one it added")
	fmt.Fprintln(out, "  --path P     list only this file's comments")
	fmt.Fprintln(out, "  --open       list only what is still open, --resolved only what is not")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The review is .git/"+review.FileName+", and a window open on it")
	fmt.Fprintln(out, "picks these changes up within the second.")
}

// options are the flags any command may carry.
type options struct {
	by       string
	side     string
	path     string
	base     string
	auto     bool
	context  int
	stat     bool
	raw      bool
	open     bool
	resolved bool
}

func parseFlags(args []string) (options, []string, error) {
	o := options{context: git.DefaultContext}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		need := func() (string, error) {
			if inline {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s wants a value", name)
			}
			i++
			return args[i], nil
		}
		var err error
		switch name {
		case "by":
			o.by, err = need()
		case "side":
			o.side, err = need()
		case "path":
			o.path, err = need()
		case "base":
			o.base, err = need()
		case "context":
			var v string
			if v, err = need(); err == nil {
				if o.context, err = strconv.Atoi(v); err != nil || o.context < 0 {
					err = fmt.Errorf("--context wants a number of lines, not %q", v)
				}
			}
		case "auto":
			o.auto = true
		case "stat":
			o.stat = true
		case "raw":
			o.raw = true
		case "open":
			o.open = true
		case "resolved":
			o.resolved = true
		default:
			err = fmt.Errorf("no such flag: --%s", name)
		}
		if err != nil {
			return o, nil, err
		}
	}
	return o, rest, nil
}

// who is the name a remark is filed under: what was asked for, and otherwise
// whoever git thinks is committing.
func who(repo *git.Repo, o options) string {
	if o.by != "" {
		return o.by
	}
	return repo.UserName()
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// diff prints the review itself: every change from the base to the working
// tree, the way the window shows it.
//
// The line numbers are the point. A remark is anchored at path:line in the
// file as it now stands, so a diff that made you count lines to work that out
// would be handing back the one thing the review is careful about. Each line
// carries its own side's number — the new one for a line that is there, the
// old one for a line the change took away — which is exactly what `comment`
// takes back.
func diff(out io.Writer, repo *git.Repo, st *review.State, o options) error {
	base := st.Base
	if o.base != "" {
		base = o.base
	}
	scan, err := repo.Rescan(base, o.context)
	if err != nil {
		return err
	}
	if scan.Base.Err != "" {
		return errors.New(scan.Base.Err)
	}

	// The same tree measured from HEAD says which lines are not in a commit
	// yet, and the working tree scan says what stands against each file.
	uncommitted := map[string]map[int]bool{}
	for _, p := range git.ParseMulti(scan.Pending) {
		uncommitted[p.Path] = p.ChangedNewLines()
	}
	states := map[string]git.State{}
	for _, f := range scan.Files {
		states[f.Path] = f.State
	}

	patches := git.ParseMulti(scan.Diff)
	if err := reconcile(repo, repo.CurrentBranch(), st, patches); err != nil {
		return err
	}

	shown := 0
	for _, p := range patches {
		if p.Path == "" || (o.path != "" && p.Path != o.path) {
			continue
		}
		shown++
		if o.raw {
			// Verbatim, so it can be fed to anything that reads a patch.
			fmt.Fprint(out, rawPatch(p))
			continue
		}
		fmt.Fprintln(out, fileLine(p, st, states[p.Path]))
		if o.stat {
			continue
		}
		printPatch(out, p, st, uncommitted[p.Path])
	}
	if shown == 0 {
		fmt.Fprintln(out, "nothing to review")
	}
	return nil
}

// fileLine heads a file's section with everything standing against it, in the
// same words the window's own heading uses.
func fileLine(p *git.Patch, st *review.State, state git.State) string {
	adds, dels := p.Stat()
	parts := []string{p.Path}
	if p.From != "" {
		parts[0] = p.From + " → " + p.Path
	}
	if adds > 0 {
		parts = append(parts, "+"+strconv.Itoa(adds))
	}
	if dels > 0 {
		parts = append(parts, "−"+strconv.Itoa(dels))
	}
	switch {
	case st.Approved(p.Path):
		parts = append(parts, "approved")
	case st.Nullified(p.Path):
		parts = append(parts, "approval nullified")
	}
	if pending := state.Pending(); pending != "" {
		parts = append(parts, pending)
	}
	if total, open := st.CountFor(p.Path); total > 0 {
		if open > 0 {
			parts = append(parts, strconv.Itoa(open)+" open")
		} else {
			parts = append(parts, strconv.Itoa(total)+" resolved")
		}
	}
	return "=== " + strings.Join(parts, "  ")
}

// printPatch draws one file's change, a line at a time.
func printPatch(out io.Writer, p *git.Patch, st *review.State, uncommitted map[int]bool) {
	// Where each side's comments sit, so a line can say what has been said
	// about it without a search per line.
	byLine := map[string][]string{}
	for _, t := range st.ThreadsFor(p.Path) {
		first, _ := t.Span()
		byLine[t.Side+":"+strconv.Itoa(first)] = append(
			byLine[t.Side+":"+strconv.Itoa(first)], t.ID)
	}

	for _, l := range p.Render() {
		if !isCode(l.Kind) {
			// A hunk header, or what git said about the file itself.
			fmt.Fprintf(out, "%s%s\n", strings.Repeat(" ", numberWidth+3), l.Text)
			continue
		}

		side, number := review.SideNew, l.New
		if l.Kind == '-' {
			side, number = review.SideOld, l.Old
		}
		mark := " "
		if uncommitted[l.Seam] {
			// Not in a commit yet: the window draws this as a bar down the
			// margin, and here it is the one column there is room for.
			mark = "!"
		}
		line := fmt.Sprintf("%*d %c%s %s", numberWidth, number, l.Kind, mark, codeOf(l.Text))
		if ids := byLine[side+":"+strconv.Itoa(number)]; len(ids) > 0 {
			line += "    <" + strings.Join(ids, " ") + ">"
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
}

// numberWidth is the column the line numbers are printed in. It is fixed
// rather than measured so that two files' output line up under each other,
// which is what makes the whole thing greppable.
const numberWidth = 5

func isCode(kind byte) bool { return kind == ' ' || kind == '+' || kind == '-' }

// codeOf drops the leading +/-/space marker, which is already its own column.
func codeOf(s string) string {
	if s == "" {
		return ""
	}
	return s[1:]
}

// rawPatch puts a parsed patch back the way git printed it.
func rawPatch(p *git.Patch) string {
	var b strings.Builder
	for _, l := range p.Preamble {
		b.WriteString(l + "\n")
	}
	for _, h := range p.Hunks {
		b.WriteString(h.Header + "\n")
		for _, l := range h.Lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

func list(out io.Writer, st *review.State, o options) error {
	n := 0
	for _, t := range st.Threads {
		switch {
		case o.path != "" && t.Path != o.path:
			continue
		case o.open && t.Resolved:
			continue
		case o.resolved && !t.Resolved:
			continue
		}
		n++
		fmt.Fprintf(out, "%-4s %-24s %-8s %-16s %s\n",
			t.ID, where(t), state(t), t.Author(), firstLine(opening(t)))
	}
	if n == 0 {
		fmt.Fprintln(out, "no comments")
	}
	return nil
}

func show(out io.Writer, st *review.State, args []string) error {
	if len(args) != 1 {
		return errors.New("show wants one id")
	}
	t := st.Thread(args[0])
	if t == nil {
		return fmt.Errorf("no such comment: %s", args[0])
	}
	fmt.Fprintf(out, "%s  %s  %s\n", t.ID, where(t), state(t))
	if t.Text != "" {
		fmt.Fprintf(out, "    %s\n", strings.TrimSpace(t.Text))
	}
	if t.Origin != nil {
		fmt.Fprintf(out, "    written against %s:%d\n", t.Origin.Commit, t.Origin.Line)
	}
	for i, c := range t.Comments {
		fmt.Fprintf(out, "\n  [%d] %s  %s\n", i, c.By, c.At)
		for _, l := range strings.Split(strings.TrimRight(c.Body, "\n"), "\n") {
			fmt.Fprintf(out, "      %s\n", l)
		}
	}
	return nil
}

// status is what is left to do, one fact to a line so that a script can read
// any of it without reading all of it.
//
// It reads the repository as well as the review, because the two things worth
// knowing — where the review is measured from, and how much of it is still
// unread — are not in the file. The base a review names is a ref; what it is
// measured from is the commit that ref and this branch last had in common, and
// only git can say which that is. And the file records approvals, not the
// files there are to approve.
func status(out io.Writer, repo *git.Repo, st *review.State, branch string, o options) error {
	// One fact to a line, in a column wide enough for the longest key, so that
	// the values line up and either half can be cut out with a shell.
	say := func(key string, value any) { fmt.Fprintf(out, "%-12s%v\n", key, value) }
	say("branch", branch)

	base := st.Base
	if o.base != "" {
		base = o.base
	}
	// Context of none: the file names are all this wants, and every line of
	// context is a line to produce and parse for nothing.
	scan, err := repo.Rescan(base, 0)
	if err != nil {
		return err
	}
	say("base", nameOr(scan.Base.Ref, "(none)"))
	say("merge-base", nameOr(scan.Base.Commit, "(none)"))
	say("ahead", scan.Base.Ahead)
	say("behind", scan.Base.Behind)
	if scan.Base.Auto {
		// Nothing pinned it, so it may move when the branch does.
		say("base-chosen", "no")
	}

	patches := git.ParseMulti(scan.Diff)
	if err := reconcile(repo, branch, st, patches); err != nil {
		return err
	}

	files, approved := 0, 0
	for _, p := range patches {
		if p.Path == "" {
			continue
		}
		files++
		if st.Approved(p.Path) {
			approved++
		}
	}
	say("files", files)
	say("approved", approved)
	say("unapproved", files-approved)

	say("comments", len(st.Threads))
	say("open", st.Open())

	// A file whose approval fell is the one thing here somebody has to do
	// something about, so it is named rather than counted.
	var fallen []string
	for _, f := range st.Files {
		if f.Nullified && !f.Approved {
			fallen = append(fallen, f.Path)
		}
	}
	sort.Strings(fallen)
	for _, p := range fallen {
		say("nullified", p)
	}

	// A base that could not be used is reported rather than returned: this is
	// the command you run to find out what is wrong, so it has to answer.
	if scan.Base.Err != "" {
		say("problem", scan.Base.Err)
	}
	return nil
}

// reconcile checks every approval against the change now in front of the
// reader, and saves if any of them fell.
//
// The window does this on every scan; a command that did not would report an
// approval as standing when the file had moved under it, which is the one
// thing an approval is supposed to be unable to do. It also stamps in the
// digest of an approval given without one — which is how `difftui approve`
// leaves it — so such an approval becomes nullifiable as soon as anything
// reads the review.
//
// The digests come out the same whatever context the diff was taken with,
// since they count only the changed lines and their numbers.
func reconcile(repo *git.Repo, branch string, st *review.State, patches []*git.Patch) error {
	digests := make(map[string]string, len(patches))
	for _, p := range patches {
		if p.Path != "" {
			digests[p.Path] = review.Digest(p)
		}
	}
	if changed, _ := st.Reconcile(digests); changed {
		return review.Save(repo, branch, st)
	}
	return nil
}

func nameOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

func comment(st *review.State, repo *git.Repo, args []string, o options) (string, error) {
	if len(args) < 2 {
		return "", errors.New("comment wants a place and something to say")
	}
	path, first, span, err := anchor(args[0])
	if err != nil {
		return "", err
	}
	side := review.SideNew
	if o.side == review.SideOld {
		side = review.SideOld
	}
	t := st.Start(&review.Thread{
		Path: path, Side: side, Line: first, Lines: span,
	}, who(repo, o), strings.Join(args[1:], " "))
	return t.ID + "  " + where(t), nil
}

func reply(st *review.State, repo *git.Repo, args []string, o options) (string, error) {
	if len(args) < 2 {
		return "", errors.New("reply wants an id and something to say")
	}
	if !st.Reply(args[0], who(repo, o), strings.Join(args[1:], " ")) {
		return "", fmt.Errorf("no such comment: %s", args[0])
	}
	return "replied to " + args[0], nil
}

func setResolved(st *review.State, args []string, want bool) (string, error) {
	if len(args) == 0 {
		return "", errors.New("wants at least one id")
	}
	for _, id := range args {
		if !st.SetResolved(id, want) {
			return "", fmt.Errorf("no such comment: %s", id)
		}
	}
	verb := "resolved "
	if !want {
		verb = "reopened "
	}
	return verb + strings.Join(args, " "), nil
}

func remove(st *review.State, args []string) (string, error) {
	switch len(args) {
	case 1:
		if !st.Remove(args[0]) {
			return "", fmt.Errorf("no such comment: %s", args[0])
		}
		return "deleted " + args[0], nil
	case 2:
		at, err := strconv.Atoi(args[1])
		if err != nil {
			return "", fmt.Errorf("%q is not a comment number", args[1])
		}
		removed, gone := st.RemoveComment(args[0], at)
		if !removed {
			return "", fmt.Errorf("%s has no comment %d", args[0], at)
		}
		if gone {
			return fmt.Sprintf("deleted the last comment, and %s with it", args[0]), nil
		}
		return fmt.Sprintf("deleted comment %d of %s", at, args[0]), nil
	}
	return "", errors.New("delete wants an id, and optionally which remark of it")
}

// setBase shows or moves what the review is measured from.
//
// It is saved rather than passed, because it is part of the review: the window
// and every later command have to agree about what is under review, and a base
// that lived only in one command line would not be an agreement at all. The
// --base flag is the other thing — one diff seen from somewhere else, changing
// nothing.
func setBase(out io.Writer, st *review.State, args []string, o options) (string, error) {
	switch {
	case o.auto:
		st.Base = ""
		return "base: whatever this branch is built on", nil
	case len(args) == 0:
		if st.Base == "" {
			fmt.Fprintln(out, "(whatever this branch is built on)")
		} else {
			fmt.Fprintln(out, st.Base)
		}
		// Nothing changed, so nothing to report after the save.
		return "", nil
	case len(args) == 1:
		st.Base = args[0]
		return "base: " + args[0], nil
	}
	return "", errors.New("base wants one ref, or none to show it")
}

func setApproved(st *review.State, repo *git.Repo, args []string, o options, want bool) (string, error) {
	if len(args) == 0 {
		return "", errors.New("wants at least one path")
	}
	for _, path := range args {
		if want {
			// No digest: difftui stamps in the change as it stands the next
			// time it reads the review, which is what makes an approval given
			// here nullifiable like any other.
			st.Approve(path, "", who(repo, o))
			continue
		}
		st.Unapprove(path)
	}
	verb := "approved "
	if !want {
		verb = "unapproved "
	}
	return verb + strings.Join(args, " "), nil
}

// ---------------------------------------------------------------------------
// Odds and ends
// ---------------------------------------------------------------------------

// anchor reads "path:line" or "path:line-line", which is how a place in a
// review is written everywhere else — an error message, a stack trace, a
// remark in a chat.
func anchor(s string) (path string, first, span int, err error) {
	at := strings.LastIndex(s, ":")
	if at < 0 {
		return "", 0, 0, fmt.Errorf("%q is not path:line", s)
	}
	path, lines := s[:at], s[at+1:]
	if path == "" {
		return "", 0, 0, fmt.Errorf("%q names no file", s)
	}
	from, to, ranged := strings.Cut(lines, "-")
	first, err = strconv.Atoi(from)
	if err != nil || first <= 0 {
		return "", 0, 0, fmt.Errorf("%q is not a line number", from)
	}
	if !ranged {
		return path, first, 1, nil
	}
	last, err := strconv.Atoi(to)
	if err != nil || last < first {
		return "", 0, 0, fmt.Errorf("%q is not a line range", lines)
	}
	return path, first, last - first + 1, nil
}

// where is a thread's place, in the same shape the commands take it.
func where(t *review.Thread) string {
	at := t.Path + ":" + strconv.Itoa(t.Line)
	if first, last := t.Span(); last > first {
		at += "-" + strconv.Itoa(last)
	}
	if t.Line == 0 {
		at = t.Path + " (no line)"
	}
	if t.Side == review.SideOld {
		at += " (old)"
	}
	return at
}

func state(t *review.Thread) string {
	if t.Resolved {
		return "resolved"
	}
	return "open"
}

// opening is a thread's first remark, which is what a listing shows of it.
func opening(t *review.Thread) string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].Body
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return strings.TrimSpace(s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
