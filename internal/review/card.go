package review

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/discobox-ai/review/internal/git"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Card is the meat of a change, as whoever read it whole picked it out: a few
// bullets of what changed, and the places in the diff that are its core —
// apart from the tests, comments, documentation and edits that only follow
// from another. The window reviews those first, and the rest after.
type Card struct {
	// By is who wrote the card, and At when.
	By string `json:"by,omitempty"`
	At string `json:"at,omitempty"`

	// Commit is the commit the card's line numbers were taken at. Lines are
	// numbered as the review numbers a comment's: in the file as the new side
	// has it, or as the base has it for a place with side=old.
	Commit string `json:"commit,omitempty"`

	// Base is the commit the writer recommends the review be measured from:
	// the one after which the change the card describes begins. It is a hash
	// and never a ref, because a ref moves and what the card says about the
	// change does not. It is a recommendation, and never moves the review's
	// own base by itself.
	Base string `json:"base,omitempty"`

	// Summary is what changed, a bullet to a line.
	Summary []string `json:"summary"`

	// Core is the places in the diff that are the core of the change, each
	// written path#L12-L20; see [ParseLink].
	Core []string `json:"core"`
}

// CoreLinks is the card's core places, read; one that does not read is left
// out.
func (c *Card) CoreLinks() []Link {
	var out []Link
	for _, at := range c.Core {
		if l, ok := ParseLink(at); ok {
			out = append(out, l)
		}
	}
	return out
}

// Holds reports whether a line of a diff is one the change made inside a
// place: a line it added within lines of the new side, or one it removed
// within lines of the old. A deletion's seam is neither — it is a line the
// change left alone — so a place with only a seam in it holds nothing. A
// place naming a whole file holds every line the change made in it.
//
// It is the one rule for which hunks a card's core is: card set refuses a
// place that holds no line, and the window opens the hunks that hold one.
func (l Link) Holds(d git.DiffLine) bool {
	if l.First == 0 {
		// The whole file has no side: it is every line the change made in
		// it, added or removed — a file the branch deleted is all removals.
		return d.Kind == '+' || d.Kind == '-'
	}
	n := d.New
	if l.Side == SideOld {
		if d.Kind != '-' {
			return false
		}
		n = d.Old
	} else if d.Kind != '+' {
		return false
	}
	return n >= l.First && n <= l.Last
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

// Link is a place in the change a card names: lines of a file, or a whole
// file.
type Link struct {
	Path string
	Side string // SideNew or SideOld

	// First and Last are the lines, both included; 0 for the whole file.
	First, Last int
}

// ParseLink reads a place in the change, and reports whether it is one: a web
// address is not.
//
// The form is the one GitHub writes a place in a file in, because it is what
// an agent already writes and it still works pasted somewhere that is not
// this program:
//
//	internal/git/git.go#L12-L20   lines 12 to 20, as the new side has them
//	internal/git/git.go#L12       one line
//	internal/git/git.go           the file
//	internal/git/git.go?side=old#L4-L9  lines the change removed
//
// The form the commands take, path:12-20, is read as well.
func ParseLink(target string) (Link, bool) {
	target = strings.TrimSpace(target)
	if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return Link{}, false
	}
	l := Link{Side: SideNew}
	rest, frag, hasFrag := strings.Cut(target, "#")
	rest, query, _ := strings.Cut(rest, "?")
	switch query {
	case "":
	case "side=old":
		l.Side = SideOld
	case "side=new":
	default:
		return Link{}, false
	}
	if !hasFrag {
		// path:12 and path:12-20 are the commands' own way of writing it.
		if i := strings.LastIndexByte(rest, ':'); i > 0 {
			if first, last, ok := lineRange(rest[i+1:], ""); ok {
				rest, l.First, l.Last = rest[:i], first, last
			}
		}
	} else {
		first, last, ok := lineRange(frag, "L")
		if !ok {
			return Link{}, false
		}
		l.First, l.Last = first, last
	}
	l.Path = strings.TrimPrefix(strings.TrimPrefix(rest, "./"), "/")
	if l.Path == "" || strings.ContainsAny(l.Path, " \t\n") {
		return Link{}, false
	}
	return l, true
}

// lineRange reads 12, 12-20 or, with the prefix L, L12 and L12-L20.
func lineRange(s, prefix string) (first, last int, ok bool) {
	a, b, span := strings.Cut(s, "-")
	num := func(s string) (int, bool) {
		if prefix != "" {
			s = strings.TrimPrefix(s, prefix)
		}
		n, err := strconv.Atoi(s)
		return n, err == nil && n > 0
	}
	if prefix != "" && !strings.HasPrefix(a, prefix) {
		return 0, 0, false
	}
	first, ok = num(a)
	if !ok {
		return 0, 0, false
	}
	last = first
	if span {
		if last, ok = num(b); !ok || last < first {
			return 0, 0, false
		}
	}
	return first, last, true
}

// String is the link the way the commands write a place: path:12-20.
func (l Link) String() string {
	s := l.Path
	switch {
	case l.First == 0:
	case l.Last > l.First:
		s += ":" + strconv.Itoa(l.First) + "-" + strconv.Itoa(l.Last)
	default:
		s += ":" + strconv.Itoa(l.First)
	}
	if l.Side == SideOld {
		s += " (old)"
	}
	return s
}

// ---------------------------------------------------------------------------
// Checking a card
// ---------------------------------------------------------------------------

// CardSchema is the JSON Schema a card is written to. It is what an agent
// writing one is handed, and what [ParseCard] holds a card to.
//
//go:embed card.schema.json
var CardSchema string

// cardSchemaURL is the schema's own $id.
const cardSchemaURL = "https://github.com/discobox-ai/review/card.schema.json"

var compiledCardSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(CardSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(cardSchemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(cardSchemaURL)
})

// ParseCard reads a card written somewhere else, holding it to [CardSchema].
// Everything wrong with it is said at once, each where in the card it is, so
// that whoever wrote it can put the lot right in one go. A card with problems
// is handed back too, where it reads at all, for its places to be checked
// with the rest; it is nil only when it cannot be read. Whether its places
// are in the change is for whoever can read the change to say.
//
// The card in a review file is not read this way: a hand that edited it
// there gets the card it wrote, shown as well as it can be.
func ParseCard(data []byte) (*Card, []string, error) {
	sch, err := compiledCardSchema()
	if err != nil {
		return nil, nil, fmt.Errorf("the card schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("the card is not JSON: %w", err)
	}
	var problems []string
	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return nil, nil, err
		}
		problems = schemaProblems(ve)
	}
	// A card the schema refuses is still read where it can be, so that what
	// is wrong with its places is said along with what is wrong with it.
	var c Card
	if err := json.Unmarshal(data, &c); err != nil {
		if problems != nil {
			return nil, problems, nil
		}
		return nil, nil, fmt.Errorf("the card: %w", err)
	}
	return &c, problems, nil
}

// schemaProblems is a schema's verdict as one line for each thing wrong, at
// the place in the card it is wrong. Only the innermost of what the validator
// says is kept: above it are only the references and items it failed inside,
// which the place already names.
func schemaProblems(ve *jsonschema.ValidationError) []string {
	en := message.NewPrinter(language.English)
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		out = append(out, fieldPath(e.InstanceLocation)+": "+e.ErrorKind.LocalizedString(en))
	}
	walk(ve)
	sort.Strings(out)
	return out
}

// fieldPath writes a place in the card the way the rest of its problems name
// one: core[1].
func fieldPath(at []string) string {
	if len(at) == 0 {
		return "card"
	}
	var b strings.Builder
	for _, part := range at {
		if _, err := strconv.Atoi(part); err == nil {
			b.WriteString("[" + part + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(part)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The card as text
// ---------------------------------------------------------------------------

// Markdown is the card as one markdown document, which the card command
// prints.
func (c *Card) Markdown() string {
	var b strings.Builder
	var about []string
	if c.Base != "" {
		about = append(about, "review from "+short(c.Base))
	}
	if c.By != "" {
		about = append(about, "by "+c.By)
	}
	if c.Commit != "" {
		about = append(about, "at "+short(c.Commit))
	}
	if len(about) > 0 {
		b.WriteString(strings.Join(about, " · ") + "\n\n")
	}
	for _, s := range c.Summary {
		b.WriteString("- " + oneLine(s) + "\n")
	}
	if len(c.Core) > 0 {
		b.WriteString("\nCore:\n")
		for _, at := range c.Core {
			if l, ok := ParseLink(at); ok {
				at = l.String()
			}
			b.WriteString("  " + at + "\n")
		}
	}
	return b.String()
}

// oneLine is text that has to stay on one line, with its line breaks made
// spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// short is a commit hash as the bar abbreviates it.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
