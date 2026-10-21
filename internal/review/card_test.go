package review

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/discobox-ai/review/internal/git"
)

func TestParseLink(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Link
		ok   bool
	}{
		{"internal/git/git.go#L12-L20", Link{Path: "internal/git/git.go", Side: SideNew, First: 12, Last: 20}, true},
		{"internal/git/git.go#L12", Link{Path: "internal/git/git.go", Side: SideNew, First: 12, Last: 12}, true},
		{"internal/git/git.go", Link{Path: "internal/git/git.go", Side: SideNew}, true},
		{"./a.go#L3", Link{Path: "a.go", Side: SideNew, First: 3, Last: 3}, true},
		{"a.go?side=old#L4-L9", Link{Path: "a.go", Side: SideOld, First: 4, Last: 9}, true},
		{"a.go:12-20", Link{Path: "a.go", Side: SideNew, First: 12, Last: 20}, true},
		{"https://example.com/a.go#L1", Link{}, false},
		{"#section", Link{}, false},
		{"a.go#L20-L12", Link{}, false},
		{"a.go#12", Link{}, false},
		{"a.go?side=sideways#L1", Link{}, false},
	} {
		got, ok := ParseLink(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseLink(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Everything the schema can say about a card is said at once, each where in
// the card it is.
func TestParseCardHoldsACardToTheSchema(t *testing.T) {
	_, problems, err := ParseCard([]byte(`{
	  "base": "origin/main",
	  "summary": [],
	  "core": ["a b.go#L1", "a.go#12"],
	  "extra": 1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base:", "summary:", "core[0]:", "core[1]:", "'extra'"} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("nothing said about %s: %q", want, problems)
		}
	}
}

func TestParseCard(t *testing.T) {
	c, problems, err := ParseCard([]byte(`{
	  "base": "cbe0001",
	  "summary": ["what changed"],
	  "core": ["a.go#L3-L5", "b.go?side=old#L2"]
	}`))
	if err != nil || len(problems) > 0 {
		t.Fatalf("err %v, problems %q", err, problems)
	}
	got := c.CoreLinks()
	want := []Link{{Path: "a.go", Side: SideNew, First: 3, Last: 5}, {Path: "b.go", Side: SideOld, First: 2, Last: 2}}
	if !slices.Equal(got, want) {
		t.Errorf("core = %+v, want %+v", got, want)
	}
}

// The schema and the type say the same thing about which fields a card has: a
// field in one and not the other is one that is refused, or one that is
// dropped.
func TestTheSchemaIsTheCard(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(CardSchema), &schema); err != nil {
		t.Fatal(err)
	}
	var fields []string
	for f := range reflect.TypeFor[Card]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	for p := range schema.Properties {
		if !slices.Contains(fields, p) {
			t.Errorf("the schema has %q, which the card has not", p)
		}
	}
	for _, f := range fields {
		if _, ok := schema.Properties[f]; !ok {
			t.Errorf("the card has %q, which the schema has not", f)
		}
	}
}

// A review with no card is written as version 2, which a discobox-review from
// before cards reads; one with a card as 3, which it refuses rather than
// writing back without the card.
func TestTheVersionWrittenSaysWhetherThereIsACard(t *testing.T) {
	s := New("main")
	if v := versionOf(t, s.Bytes()); v != 2 {
		t.Errorf("a review with no card is version %d", v)
	}
	s.Card = &Card{Summary: []string{"x"}, Core: []string{"a.go#L1"}}
	data := s.Bytes()
	if v := versionOf(t, data); v != 3 {
		t.Errorf("a review with a card is version %d", v)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Card == nil || !slices.Equal(back.Card.Core, []string{"a.go#L1"}) {
		t.Errorf("the card did not come back: %+v", back.Card)
	}
}

func versionOf(t *testing.T, data []byte) int {
	t.Helper()
	var doc struct{ Version int }
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Version
}

// The card as text: who and from where, the bullets, and the core as places
// the other commands take.
func TestMarkdown(t *testing.T) {
	c := &Card{
		By: "claude", Base: "cbe00016979684dd", Summary: []string{"one", "two"},
		Core: []string{"a.go#L3-L5", "b.go"},
	}
	want := "review from cbe0001 · by claude\n\n- one\n- two\n\nCore:\n  a.go:3-5\n  b.go\n"
	if got := c.Markdown(); got != want {
		t.Errorf("Markdown =\n%s\nwant\n%s", got, want)
	}
}

func TestHolds(t *testing.T) {
	added := git.DiffLine{Kind: '+', New: 4}
	removed := git.DiffLine{Kind: '-', Old: 4}
	context := git.DiffLine{Kind: ' ', Old: 3, New: 3}
	for _, c := range []struct {
		at   string
		line git.DiffLine
		want bool
	}{
		{"a.go#L3-L5", added, true},
		{"a.go#L5", added, false},
		{"a.go#L3", context, false},
		{"a.go#L4", removed, false},
		{"a.go?side=old#L4", removed, true},
		{"a.go?side=old#L4", added, false},
		{"a.go", added, true},
		{"a.go", removed, true},
		{"a.go", context, false},
	} {
		l, _ := ParseLink(c.at)
		if got := l.Holds(c.line); got != c.want {
			t.Errorf("%s holds %+v = %v, want %v", c.at, c.line, got, c.want)
		}
	}
}
