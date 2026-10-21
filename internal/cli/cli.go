// Package cli is discobox-review without the window: the review read and written from
// a command line.
//
// The review has always been a file so that something other than discobox-review could
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// Command is one subcommand: what it is called, what it takes, and what it is
// for. The table is the only place any of that is written down — the listing,
// one command's own help, and the check that a flag belongs to the command it
// was given to all read it, so none of them can come to disagree.
type Command struct {
	Name, Args, What string

	// Long is the paragraph `discobox-review <name> --help` prints, and Example the
	// line under it. Both are empty for a command with nothing to add.
	Long, Example string

	// Flags are the flags this command takes, by name. Anything else given to
	// it is refused rather than quietly ignored.
	Flags []string

	// ByOptional marks a command that takes --by to narrow what it does to
	// one reviewer, rather than to sign something. Such a command writes no
	// name, so there is a way to run it without one.
	ByOptional bool

	// FlagHelp is what a flag means to this command, where that is not what
	// it means to the others.
	FlagHelp map[string]string
}

// Commands is every subcommand, in the order help lists them.
var Commands = []Command{
	{
		Name: "diff", Args: "[--path P] [--stat] [--raw]", What: "the review itself",
		Flags: []string{"path", "base", "context", "stat", "raw"},
		Long: "Every change between the base and the working tree — committed,\n" +
			"staged and unstaged alike — with the line numbers a comment anchors\n" +
			"to. Each line carries its own side's number, a ! where the change is\n" +
			"not in a commit yet, and <t1> for whatever has already been said\n" +
			"about it.\n\n" +
			"--stat prints the headings alone. --raw prints the plain unified\n" +
			"diff with none of that, which git apply will take.",
		Example: "discobox-review diff --path internal/git/git.go",
	},
	{
		Name: "base", Args: "[<ref>] [--auto]", What: "show or set what it is measured from",
		Flags: []string{"auto"},
		Long: "With no argument it says where the review starts. With one it moves\n" +
			"it there, and saves: the base is part of the review, so the window\n" +
			"and every later command agree about what is under review. --auto\n" +
			"hands it back to whatever Discobox Review works out.\n\n" +
			"The --base flag on diff and status is the other thing — one command\n" +
			"seen from somewhere else, changing nothing.",
		Example: "discobox-review base origin/main",
	},
	{
		Name: "list", Args: "[--path P] [--open] [--resolved]", What: "list the comments",
		Flags:   []string{"path", "open", "resolved"},
		Long:    "One conversation to a line: its id, where it is, whether anybody is\nstill waiting on it, who started it, and what they said.",
		Example: "discobox-review list --open",
	},
	{
		Name: "show", Args: "<id>", What: "one conversation in full",
		Long:    "Every remark on it, numbered, so that one of them can be deleted by\nnumber.",
		Example: "discobox-review show t3",
	},
	{
		Name: "comment", Args: "--by <who> <path>:<line>[-<line>] <body>", What: "start a conversation",
		Flags: []string{"by", "side"},
		Long: "A place is written path:line, or path:line-line for a block — the way\n" +
			"a place in a review is written in an error message or a stack trace.\n" +
			"The numbers are the ones discobox-review diff prints.\n\n" +
			"--side old anchors to a line the change removed, which has no line in\n" +
			"the file as it now stands.\n\n" +
			"--by says who is speaking and has no default: an agent gives its own\n" +
			"name, and a person gives git-user, which is git's user.name.",
		Example: `discobox-review comment --by git-user server.go:42 "why not check the error here?"`,
	},
	{
		Name: "reply", Args: "--by <who> <id> <body>", What: "answer one",
		Flags:   []string{"by"},
		Example: `discobox-review reply t3 --by claude "good catch, fixing"`,
	},
	{
		Name: "resolve", Args: "<id>…", What: "close one",
		Long:    "Closing a conversation keeps the record of what was asked and\nanswered, which is nearly always what you want instead of deleting it.",
		Example: "discobox-review resolve t1 t2",
	},
	{Name: "reopen", Args: "<id>…", What: "open one again"},
	{
		Name: "delete", Args: "<id> [n]", What: "delete a conversation, or one remark of it",
		Long: "With a number it deletes that one remark, counting from zero as show\n" +
			"prints them. Without, it deletes the whole conversation. Taking the\n" +
			"last remark out takes the conversation with it: an anchor on a line\n" +
			"with nothing to say is not a thing to leave behind.",
		Example: "discobox-review delete t3 1",
	},
	{
		Name: "approve", Args: "--by <who> <path>…", What: "sign off a file",
		Flags: []string{"by"},
		Long: "The approval is --by's own, and stands beside anybody else's: a file\n" +
			"can be approved by a reviewer and still be unread by the person.\n\n" +
			"No digest is written, exactly as a hand-written approval leaves none:\n" +
			"Discobox Review stamps in the change as it stands the next time anything\n" +
			"reads the review, which is what keeps such an approval nullifiable.\n" +
			"Edit the file afterwards and every approval of it falls.",
		Example: "discobox-review approve --by git-user internal/git/git.go",
	},
	{
		Name: "unapprove", Args: "--by <who> | --all <path>…", What: "take that back",
		Flags: []string{"by", "all"}, ByOptional: true,
		FlagHelp: map[string]string{"by": "whose approval — git-user for git's own user.name"},
		Long: "With --by it takes back that reviewer's approval and leaves the\n" +
			"others standing. --all takes back every approval the file has,\n" +
			"whoever gave it.\n\n" +
			"It wants one or the other, and has no default for the reason --by\n" +
			"has none anywhere else: an approval is somebody's, and a command\n" +
			"that guessed whose would take back one nobody meant it to.",
		Example: "discobox-review unapprove --by reviewer internal/git/git.go",
	},
	{
		Name: "card", Args: "[set <file>|- | clear | schema] [--json]", What: "what the change is, and where to look",
		Flags: []string{"by", "json"}, ByOptional: true,
		FlagHelp: map[string]string{
			"by":   "who wrote the card, for set — the card's own by when left out",
			"json": "the card as it is stored, rather than as it reads",
		},
		Long:    cardHelp,
		Example: "discobox-review card set card.json",
	},
	{
		Name: "status", Args: "[--by <who>]", What: "what is left to do, one fact to a line",
		Flags: []string{"base", "by"}, ByOptional: true,
		FlagHelp: map[string]string{"by": "whose approvals to count — git-user for git's own user.name; anybody's when left out"},
		Long: "Reads the repository as well as the review, because two of the things\n" +
			"worth knowing are not in the file: merge-base is the commit the base\n" +
			"and this branch last had in common, and unapproved counts the files\n" +
			"there are to approve.\n\n" +
			"A file counts as approved when anybody's approval of it stands. With\n" +
			"--by it is that reviewer's approval that counts, and nobody else's.\n\n" +
			"One fact to a line, so a script can read any of it without reading\n" +
			"all of it.",
		Example: "discobox-review status | grep ^unapproved",
	},
}

// flagHelp describes every flag once, so that a command listing one of them
// does not have to say again what it means.
var flagHelp = map[string]string{
	"path":     "only this file",
	"base":     "measure this one command from here, without saving it",
	"context":  "lines of context either side",
	"stat":     "the headings alone, without the diff under them",
	"raw":      "the plain unified diff, for piping somewhere else",
	"auto":     "let Discobox Review work the base out again",
	"all":      "every reviewer's, whoever they are",
	"by":       "who a remark is from — required; git-user for git's own user.name",
	"side":     "old, to anchor to a line the change removed",
	"open":     "only what is still open",
	"resolved": "only what is not",
	"json":     "as JSON",
}

// find is the command a word names.
func find(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// takes reports whether a command accepts a flag.
func (c Command) takes(flag string) bool {
	return slices.Contains(c.Flags, flag)
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

// lockWait is how long a command waits for the review's lock before giving
// up. Nobody is watching a command wait, and what is ahead of it is other
// commands that each hold the lock for one read and one write, so it can
// afford to queue behind a great many of them. It gives up at all so that a
// holder that has stopped — a suspended process, an editor left open under
// the lock — ends in an error that says so rather than in a script that never
// comes back.
const lockWait = time.Minute

// Run carries out one subcommand. args is the command and its arguments, and
// in is what a command reading "-" reads.
func Run(repo *git.Repo, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || (args[0] == "help" && len(args) == 1) {
		Usage(out)
		return nil
	}
	cmd, rest := args[0], args[1:]

	// `help <cmd>` and `<cmd> --help` are the same question asked two ways.
	if cmd == "help" && len(rest) == 1 {
		return helpFor(out, rest[0])
	}
	c, known := find(cmd)
	if !known {
		return fmt.Errorf("no such command: %s", cmd)
	}
	for _, a := range rest {
		if a == "--help" || a == "-h" {
			return helpFor(out, cmd)
		}
	}

	// Flags first, so that the positional arguments left are just that. Only
	// this command's own flags are allowed: one it does not take is a mistake
	// worth reporting rather than ignoring.
	opts, rest, err := parseFlags(c, rest)
	if err != nil {
		return err
	}

	// A command that writes a remark writes a name with it, and there is no
	// name to fall back on: see Author.
	if c.takes("by") && (!c.ByOptional || opts.by != "") {
		if opts.by, err = Author(repo, opts.by); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}

	branch := repo.CurrentBranch()

	// The commands that only read work from a copy of the review. What they
	// write — see reconcile — goes through review.Update like any other
	// write, rather than this copy going back over whatever landed since.
	// base with nothing after it is one of them: it says what the base is.
	reads := cmd == "base" && len(rest) == 0 && !opts.auto
	if cmd == "card" {
		return card(out, in, repo, branch, rest, opts)
	}
	switch cmd {
	case "diff", "list", "status", "show":
		reads = true
	}
	if reads {
		st, err := review.Load(repo, branch, lockWait)
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
		case "base":
			_, err := setBase(out, st, rest, opts)
			return err
		}
		return show(out, st, rest)
	}

	// The rest change something. Each is applied to the review as it stands
	// at that moment and saved before anything else can write, so that
	// commands run at the same time all land.
	var (
		note   string
		cmdErr error
	)
	err = review.Update(repo, branch, lockWait, func(st *review.State) error {
		switch cmd {
		case "comment":
			note, cmdErr = comment(st, repo, rest, opts)
		case "reply":
			note, cmdErr = reply(st, rest, opts)
		case "resolve":
			note, cmdErr = setResolved(st, rest, true)
		case "reopen":
			note, cmdErr = setResolved(st, rest, false)
		case "base":
			note, cmdErr = setBase(out, st, rest, opts)
		case "delete":
			note, cmdErr = remove(st, rest)
		case "approve":
			note, cmdErr = setApproved(st, rest, opts, true)
		case "unapprove":
			note, cmdErr = setApproved(st, rest, opts, false)
		}
		return cmdErr
	})
	if cmdErr != nil {
		return cmdErr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", review.FileName, err)
	}
	if note != "" {
		fmt.Fprintln(out, note)
	}
	return nil
}

// Usage lists the commands. It is what `discobox-review help` prints, and what a
// discobox-review with nowhere to draw prints instead of opening a window.
func Usage(out io.Writer) {
	fmt.Fprintln(out, "Discobox Review reviews a branch. With no command it opens a window;")
	fmt.Fprintln(out, "these read and write the same review from a script.")
	fmt.Fprintln(out)
	for _, c := range Commands {
		fmt.Fprintf(out, "  discobox-review %-9s %-40s %s\n", c.Name, c.Args, c.What)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  discobox-review <command> --help   what that one does, in full")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The review is .git/"+review.FileName+", and a window open on it")
	fmt.Fprintln(out, "picks these changes up within the second.")
}

// helpFor is one command's own page: what it takes, what it is for, the flags
// it accepts, and a line showing it used.
func helpFor(out io.Writer, name string) error {
	c, ok := find(name)
	if !ok {
		return fmt.Errorf("no such command: %s", name)
	}
	fmt.Fprintf(out, "discobox-review %s %s\n", c.Name, c.Args)
	fmt.Fprintf(out, "  %s\n", c.What)
	if c.Long != "" {
		fmt.Fprintln(out)
		for l := range strings.SplitSeq(c.Long, "\n") {
			// A blank line stays blank rather than becoming two spaces.
			if l == "" {
				fmt.Fprintln(out)
				continue
			}
			fmt.Fprintln(out, "  "+l)
		}
	}
	if len(c.Flags) > 0 {
		fmt.Fprintln(out)
		for _, f := range c.Flags {
			what := flagHelp[f]
			if here, ok := c.FlagHelp[f]; ok {
				what = here
			}
			if f == "context" {
				what += " (default " + strconv.Itoa(git.DefaultContext) + ")"
			}
			fmt.Fprintf(out, "  --%-9s %s\n", f, what)
		}
	}
	if c.Example != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  "+c.Example)
	}
	return nil
}

// options are the flags any command may carry.
type options struct {
	by       string
	side     string
	path     string
	base     string
	auto     bool
	all      bool
	context  int
	stat     bool
	raw      bool
	open     bool
	resolved bool
	json     bool
}

func parseFlags(c Command, args []string) (options, []string, error) {
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
		case "all":
			o.all = true
		case "stat":
			o.stat = true
		case "raw":
			o.raw = true
		case "open":
			o.open = true
		case "resolved":
			o.resolved = true
		case "json":
			o.json = true
		default:
			err = fmt.Errorf("no such flag: --%s", name)
		}
		if err == nil && !c.takes(name) {
			err = fmt.Errorf("%s takes no --%s; see discobox-review %s --help", c.Name, name, c.Name)
		}
		if err != nil {
			return o, nil, err
		}
	}
	return o, rest, nil
}

// GitUser is what --by is given to file a remark under git's own user.name.
// It is spelled out rather than assumed, so that a name in the review is
// always one somebody chose to put there.
const GitUser = "git-user"

// Author is the name a remark is filed under. There is no default: the review
// is a record of who said what, and a name guessed on the writer's behalf is
// the one way that record can be wrong without anybody noticing — an agent
// running these commands would sign the person's name to its own remarks.
// So --by is said every time: an agent gives its own name, and a person gives
// git-user, which reads it from git.
func Author(repo *git.Repo, by string) (string, error) {
	switch by {
	case "":
		return "", fmt.Errorf("--by wants a name: who the remark is from, or %s to take git's own user.name", GitUser)
	case GitUser:
		return repo.UserName(), nil
	}
	return by, nil
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
	scan, err := repo.Scan(base, o.context)
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
	// Who approved it is said along with the fact, since one reviewer's
	// approval is no news about another's.
	if by := st.Approvers(p.Path); len(by) > 0 {
		verdict := "approved"
		if names := strings.Join(slices.DeleteFunc(by, func(n string) bool { return n == "" }), ", "); names != "" {
			verdict += " by " + names
		}
		parts = append(parts, verdict)
	} else if len(st.Fallen(p.Path)) > 0 {
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
		for l := range strings.SplitSeq(strings.TrimRight(c.Body, "\n"), "\n") {
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
	scan, err := repo.Scan(base, 0)
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

	// Anybody's approval, or the one reviewer's that was asked about.
	stands := func(path string) bool {
		if o.by != "" {
			return st.Approved(path, o.by)
		}
		return len(st.Approvers(path)) > 0
	}
	files, approved := 0, 0
	for _, p := range patches {
		if p.Path == "" {
			continue
		}
		files++
		if stands(p.Path) {
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
		if stands(f.Path) {
			continue
		}
		if by := st.Fallen(f.Path); (o.by == "" && len(by) > 0) || (o.by != "" && slices.Contains(by, o.by)) {
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
// digest of an approval given without one — which is how `discobox-review approve`
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
	if changed, _ := st.Reconcile(digests); !changed {
		return nil
	}
	// st was read before the diff was taken, and anything may have been
	// written since. So the verdict is given again on the review as it stands
	// now, and that is what the caller goes on to report.
	return review.Update(repo, branch, lockWait, func(now *review.State) error {
		now.Reconcile(digests)
		*st = *now
		return nil
	})
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
		Text: lineText(repo, st, side, path, first),
	}, o.by, strings.Join(args[1:], " "))
	return t.ID + "  " + where(t), nil
}

// lineText is the line a remark is being made about, as it read when the
// remark was made.
//
// Nothing depends on it, which is the point: it is there so that a review can
// be read on its own, without the repository beside it. The window records it
// for every comment, so a comment made from here has to record it too — one
// document written two ways is one document written badly.
//
// It is read from the file rather than from the diff, since the side a remark
// is anchored to *is* a file: "new" numbers the working tree and "old" numbers
// the base. That also gets the line right when it is nowhere in the diff,
// which the window can never ask for and this can.
//
// A line it cannot find is no line at all rather than a wrong one, and a file
// that is not text is left alone: the field is for a person reading the review
// in a text editor, and a blob of a binary file helps nobody.
func lineText(repo *git.Repo, st *review.State, side, path string, line int) string {
	if line < 1 {
		return ""
	}
	commit := ""
	if side == review.SideOld {
		// The old side is numbered in the base, so that is where to look —
		// the merge base itself, which is the commit the diff was taken from.
		base := repo.ResolveBase(st.Base, repo.HasHEAD())
		if base.Commit == "" {
			return ""
		}
		commit = base.Commit
	}
	body, err := repo.FileContent(commit, path)
	if err != nil || strings.IndexByte(body, 0) >= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimRight(lines[line-1], "\r")
}

func reply(st *review.State, args []string, o options) (string, error) {
	if len(args) < 2 {
		return "", errors.New("reply wants an id and something to say")
	}
	if !st.Reply(args[0], o.by, strings.Join(args[1:], " ")) {
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

func setApproved(st *review.State, args []string, o options, want bool) (string, error) {
	if len(args) == 0 {
		return "", errors.New("wants at least one path")
	}
	// Whose approval is taken back is said, one way or the other. Leaving it
	// out is how an agent withdrawing its own would take the person's too.
	if !want && (o.by == "") == !o.all {
		return "", fmt.Errorf("unapprove wants --by <who>, for one reviewer's approval, or --all, for everybody's: not both, and not neither")
	}
	for _, path := range args {
		if want {
			// No digest: discobox-review stamps in the change as it stands the next
			// time it reads the review, which is what makes an approval given
			// here nullifiable like any other.
			st.Approve(path, "", o.by)
			continue
		}
		if o.by != "" {
			st.Unapprove(path, o.by)
			continue
		}
		st.UnapproveAll(path)
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
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(before) + " …"
	}
	return strings.TrimSpace(s)
}
