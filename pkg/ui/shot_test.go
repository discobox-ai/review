package ui

// The README's screenshots are made here.
//
// They are pictures of the real window: the same model, driven by the same
// keys, rendered by the same code that draws it on a terminal. A picture of a
// program is a claim about it, and the only way to keep that claim true as the
// program moves is to take it from the program rather than from a text editor.
//
// This is a test so that it compiles with everything else and cannot quietly
// rot; it does nothing unless it is asked. `make screenshots` asks.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// ---------------------------------------------------------------------------
// The repository the pictures are taken in
// ---------------------------------------------------------------------------

// A small HTTP service with a rate limiter being added to it, because the
// screenshots have to show the thing this program is for: real code, in a real
// language, with a real objection written against a real line of it. A fixture
// of foo.txt and bar.txt would photograph as a diff viewer.
const (
	shotServerBase = `package server

import (
	"context"
	"net/http"
	"time"
)

// Store is the part of the database the handlers touch.
type Store interface {
	Get(ctx context.Context, id string) (Item, error)
}

// Server is the API: a mux, a store, and a deadline on every request.
type Server struct {
	mux     *http.ServeMux
	store   Store
	timeout time.Duration
}

// New wires the routes up and hands back something that serves them.
func New(store Store, timeout time.Duration) *Server {
	s := &Server{mux: http.NewServeMux(), store: store, timeout: timeout}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /v1/items/{id}", s.handleItem)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}
`

	// The working tree: the limiter is wired in, and not committed yet.
	shotServerNow = `package server

import (
	"context"
	"net/http"
	"time"
)

// Store is the part of the database the handlers touch.
type Store interface {
	Get(ctx context.Context, id string) (Item, error)
}

// Server is the API: a mux, a store, and a deadline on every request.
type Server struct {
	mux     *http.ServeMux
	store   Store
	timeout time.Duration
	limiter *Limiter
}

// New wires the routes up and hands back something that serves them.
func New(store Store, timeout time.Duration, rate int) *Server {
	s := &Server{
		mux:     http.NewServeMux(),
		store:   store,
		timeout: timeout,
		limiter: NewLimiter(rate),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /v1/items/{id}", s.handleItem)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}
`

	shotHandlerBase = `package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such item", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}
`

	// Committed on the branch: reads go through the limiter.
	shotHandlerNow = `package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow(r.RemoteAddr) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	item, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such item", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}
`

	// New on the branch, and the thing the review has something to say about.
	shotLimit = `package server

import (
	"sync"
	"time"
)

// Limiter is a token bucket per caller, refilled once a second.
type Limiter struct {
	mu      sync.Mutex
	rate    int
	buckets map[string]bucket
}

type bucket struct {
	tokens int
	seen   time.Time
}

func NewLimiter(rate int) *Limiter {
	return &Limiter{rate: rate, buckets: map[string]bucket{}}
}

// Allow takes a token from the caller's bucket, refilling it first.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if time.Since(b.seen) > time.Second {
		b.tokens, b.seen = l.rate, time.Now()
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	l.buckets[key] = b
	return true
}
`

	shotLimitTest = `package server

import "testing"

func TestAllowSpendsTheBucket(t *testing.T) {
	l := NewLimiter(2)
	for i := range 2 {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("token %d was refused", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("a third request got through an empty bucket")
	}
}
`

	shotStoreBase = `package store

import (
	"context"
	"database/sql"
)

// Item is one row of the items table.
type Item struct {
	ID   string ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

// Store reads items out of the database.
type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	row := s.db.QueryRowContext(ctx,
		` + "`SELECT id, name FROM items WHERE id = ?`" + `, id)
	var it Item
	err := row.Scan(&it.ID, &it.Name)
	return it, err
}
`

	// Staged, not committed: soft-deleted rows stop coming back.
	shotStoreNow = `package store

import (
	"context"
	"database/sql"
)

// Item is one row of the items table.
type Item struct {
	ID   string ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

// Store reads items out of the database.
type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	row := s.db.QueryRowContext(ctx,
		` + "`SELECT id, name FROM items WHERE id = ? AND deleted_at IS NULL`" + `, id)
	var it Item
	err := row.Scan(&it.ID, &it.Name)
	return it, err
}
`
)

// shotDate fixes every commit in the fixture so the hashes in the pictures are
// the same hashes the next time they are taken. A screenshot that changes for
// no reason is a diff nobody can review.
const shotDate = "2026-08-28T11:20:00+01:00"

func shotRepo(t *testing.T) *git.Repo {
	t.Helper()
	// Named, because the window puts the repository's name in its top right
	// corner and a temporary directory's is not a name.
	dir := filepath.Join(t.TempDir(), "items-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+shotDate, "GIT_COMMITTER_DATE="+shotDate)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.name", "Darren")
	run("config", "user.email", "darren@example.com")
	run("config", "commit.gpgsign", "false")

	write("internal/server/server.go", shotServerBase)
	write("internal/server/handler.go", shotHandlerBase)
	write("internal/store/store.go", shotStoreBase)
	run("add", ".")
	run("commit", "-qm", "an items API")

	run("checkout", "-q", "-b", "rate-limit")
	write("internal/server/limit.go", shotLimit)
	run("add", ".")
	run("commit", "-qm", "add a token bucket")
	write("internal/server/handler.go", shotHandlerNow)
	run("commit", "-qam", "turn the limiter on for reads")

	// Left over: one edit in the working tree, one in the index, one file git
	// has never heard of.
	write("internal/server/server.go", shotServerNow)
	write("internal/store/store.go", shotStoreNow)
	run("add", "internal/store/store.go")
	write("internal/server/limit_test.go", shotLimitTest)

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// shotModel opens the fixture at a given size, with the palette a dark
// terminal gets — which is the one the images are rendered against.
func shotModel(t *testing.T, repo *git.Repo, w, h int) Model {
	t.Helper()
	m := New(repo, Options{Base: "main", Author: "darren"})
	m.dark, m.st = true, newStyles(DefaultTheme(), true)
	return drive(t, m, tea.WindowSizeMsg{Width: w, Height: h}, m.rescanCmd()())
}

// wheel scrolls the review pane without moving the cursor, which is how a
// picture gets its subject off the bottom edge of the window.
func wheel(t *testing.T, m Model, lines int) Model {
	t.Helper()
	for range lines {
		m = drive(t, m, tea.MouseWheelMsg{X: m.width - 10, Y: 6, Button: tea.MouseWheelDown})
	}
	return m
}

// ---------------------------------------------------------------------------
// The review the pictures are taken of
// ---------------------------------------------------------------------------

// shotReview approves a file and starts a conversation, then rewrites the
// names and dates the app stamped on them. Those come from the clock and the
// git config, and a picture wants neither: it wants the same two people saying
// the same two things every time it is taken.
func shotReview(t *testing.T, m Model) Model {
	t.Helper()
	m = onLine(t, m, "internal/server/handler.go", '+', "s.limiter.Allow")
	m = drive(t, m, letterKey("a"))

	m = onLine(t, m, "internal/server/limit.go", '+', "buckets map[string]bucket")
	m = comment(t, m, "This map only grows: one entry per address, kept for "+
		"the life of the process. A busy afternoon is a memory leak.")

	err := review.Update(m.src.(*git.Repo), "rate-limit", 0, func(st *review.State) error {
		// An agent has been through two of the files already, which is the
		// second column of verdicts: its approval stands beside the reader's
		// own and is not taken for it.
		st.Approve("internal/server/handler.go", "", "claude")
		st.Approve("internal/store/store.go", "", "claude")
		for _, f := range st.Files {
			for _, v := range f.Verdicts {
				v.At = "2026-08-28T19:41:02+01:00"
			}
		}
		for _, th := range st.Threads {
			th.Comments[0].By = "darren"
			th.Comments[0].At = "2026-08-28T19:59:14+01:00"
			th.Comments = append(th.Comments, review.Comment{
				By: "claude", At: "2026-08-28T20:04:37+01:00",
				Body: "Fair. Sweeping anything untouched for a minute on write, " +
					"so the map tracks callers rather than history.",
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return drive(t, m, m.rescanCmd()())
}

// ---------------------------------------------------------------------------
// Taking them
// ---------------------------------------------------------------------------

func TestWriteScreenshots(t *testing.T) {
	dir := os.Getenv("SHOTS")
	if dir == "" {
		t.Skip("set SHOTS=<dir> to write the README's screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	shots := []struct {
		name string
		w, h int
		// take drives the model from a freshly loaded review into whatever
		// the picture is of.
		take func(t *testing.T, m Model) Model
	}{{
		// The window itself: the stack, the history under it, an approval, an
		// argument, and the margin down the side of what is not committed.
		name: "review", w: 128, h: 38,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			return onLine(t, m, "internal/server/limit.go", '+', "buckets map[string]bucket")
		},
	}, {
		// A comment being written, in the place it will be read.
		name: "comment", w: 128, h: 26,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			m = onLine(t, m, "internal/server/limit.go", '+', "if b.tokens <= 0")
			m = drive(t, m, letterKey("c"))
			m = typeText(t, m, "Refusing here without writing b back means the "+
				"bucket never refills once it empties.")
			return wheel(t, m, 2)
		},
	}, {
		// Every file at once: how the window opens, and what a review looks
		// like from above.
		name: "folded", w: 128, h: 18,
		take: func(_ *testing.T, m Model) Model { return m },
	}, {
		// One commit on its own, with a remark made against the whole review
		// carried back to the line that introduced it.
		name: "commit", w: 128, h: 28,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			// F opens what the commit touched: a window opens folded.
			return drive(t, selectCommit(t, m, "add a token bucket"), letterKey("F"))
		},
	}, {
		// And the other half of that: a commit this remark is not about keeps
		// it under the file's heading rather than putting it on a wrong line.
		name: "loose", w: 128, h: 20,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			return drive(t, selectCommit(t, m, "turn the limiter on for reads"), letterKey("F"))
		},
	}, {
		// Side by side, with the panes out of the way. No s: a review opens
		// split, so this is the view without doing anything to it.
		name: "split", w: 128, h: 28,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			m = onLine(t, m, "internal/server/server.go", '+', "limiter: NewLimiter")
			return drive(t, m, altKey("z"))
		},
	}, {
		// The whole file, with the lines the review touches marked in the
		// margin — a file the review only partly changed, so there is
		// something for the margin to pick out.
		name: "file", w: 128, h: 30,
		take: func(t *testing.T, m Model) Model {
			t.Helper()
			m = onLine(t, m, "internal/server/server.go", '+', "limiter *Limiter")
			return drive(t, m, letterKey("v"))
		},
	}}

	// The review is made once, into the repository, and every picture is then
	// taken of a window that loaded it — which is what a second morning of
	// reviewing actually looks like.
	repo := shotRepo(t)
	shotReview(t, shotModel(t, repo, 128, 38))

	for _, s := range shots {
		t.Run(s.name, func(t *testing.T) {
			m := s.take(t, shotModel(t, repo, s.w, s.h))
			out := filepath.Join(dir, s.name+".ansi")
			if err := os.WriteFile(out, []byte(m.frame()+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", out)
		})
	}

	// The review file goes out beside them, because the README shows it and it
	// should be the review these pictures are of rather than a hand-written
	// approximation of one.
	st := reviewJSON(t, repo)
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "review.json"), append(body, '\n'), 0o644)
}
