package git

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestContentsPreserveRepositoryBytesAndMetadata(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "binary", "old\x00bytes")
	write(t, r.Dir, "empty", "")
	run(t, r, "add", ".")
	run(t, r, "commit", "-qm", "seed")
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "mv", "foo.txt", "renamed.txt")
	write(t, r.Dir, "binary", "new\x00bytes")
	write(t, r.Dir, "added", "line without terminator")
	if err := os.Remove(filepath.Join(r.Dir, "empty")); err != nil {
		t.Fatal(err)
	}
	scan, err := r.Scan(base, 5)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadContents(scan)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || len(got.Files) != 4 {
		t.Fatalf("manifest: %+v", got)
	}
	found := map[string]bool{}
	for _, c := range got.Files {
		if c.Problem != "" {
			t.Fatal(c.Problem)
		}
		name := c.New.Path
		if name == "" {
			name = c.Old.Path
		}
		found[name] = true
		switch name {
		case "renamed.txt":
			if c.Old.Path != "foo.txt" || c.New.Mode != "100644" || !bytes.Equal(c.Old.Data, c.New.Data) {
				t.Fatalf("rename: %+v", c)
			}
		case "binary":
			if string(c.Old.Data) != "old\x00bytes" || string(c.New.Data) != "new\x00bytes" {
				t.Fatalf("binary: %+v", c)
			}
		case "added":
			if c.Old.Present || !c.New.Present || string(c.New.Data) != "line without terminator" {
				t.Fatalf("addition: %+v", c)
			}
		case "empty":
			if !c.Old.Present || len(c.Old.Data) != 0 || c.New.Present {
				t.Fatalf("empty deletion: %+v", c)
			}
		default:
			t.Fatalf("unexpected path: %s", name)
		}
	}
	for _, name := range []string{"renamed.txt", "binary", "added", "empty"} {
		if !found[name] {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestContentsUseCleanFiltersAndSnapshotRevision(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, ".gitattributes", "*.txt text eol=crlf\n")
	run(t, r, "add", ".")
	run(t, r, "commit", "-qm", "attributes")
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "one\r\nTWO\r\nthree\r\nfour\r\nfive\r\n")
	scan, err := r.Scan(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := r.ReadContents(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents.Files) != 1 || string(contents.Files[0].New.Data) != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("filter representation: %+v", contents)
	}
	run(t, r, "commit", "-qam", "change")
	if _, err := r.ReadContents(scan); err == nil {
		t.Fatal("accepted a moved HEAD")
	}
	snapshot, err := r.Snapshot("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := snapshot.Scan(base, 5)
	if err != nil {
		t.Fatal(err)
	}
	write(t, r.Dir, "foo.txt", "unrelated worktree\n")
	contents, err = snapshot.ReadContents(saved)
	if err != nil || len(contents.Files) != 1 || string(contents.Files[0].New.Data) != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("snapshot read the working tree: %+v, %v", contents, err)
	}
}

func TestContentManifestIgnoresSubmoduleDisplayConfiguration(t *testing.T) {
	r := testRepo(t)
	first := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "another commit\n")
	run(t, r, "commit", "-qam", "second")
	second := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "update-index", "--add", "--cacheinfo", "160000,"+first+",sub")
	run(t, r, "commit", "-qm", "first gitlink")
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "update-index", "--cacheinfo", "160000,"+second+",sub")
	run(t, r, "commit", "-qm", "second gitlink")
	run(t, r, "config", "diff.ignoreSubmodules", "all")
	run(t, r, "config", "submodule.sub.ignore", "all")
	snapshot, err := r.Snapshot("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := snapshot.Scan(base, 5)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := snapshot.ReadContents(scan)
	if err != nil {
		t.Fatal(err)
	}
	if !contents.Complete || len(contents.Files) != 1 || contents.Files[0].Old.Mode != "160000" || string(contents.Files[0].Old.Data) != first || string(contents.Files[0].New.Data) != second {
		t.Fatalf("ignored gitlink change: %+v", contents)
	}
}

func TestContentsReadLinksCheckedOutAsOrdinaryFiles(t *testing.T) {
	r := testRepo(t)
	run(t, r, "config", "core.symlinks", "false")
	object, err := (Local{}).Git(r.Dir, []byte("old-target"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	run(t, r, "update-index", "--add", "--cacheinfo", "120000,"+strings.TrimSpace(object)+",link")
	run(t, r, "commit", "-qm", "stored link")
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "link", "new-target")
	scan, err := r.Scan(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := r.ReadContents(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents.Files) != 1 || contents.Files[0].Problem != "" || contents.Files[0].New.Mode != "120000" || string(contents.Files[0].Old.Data) != "old-target" || string(contents.Files[0].New.Data) != "new-target" {
		t.Fatalf("checked-out link was not read in repository form: %+v", contents)
	}
}

type movingContentsSystem struct {
	Local
	move   func()
	called bool
}

func (s *movingContentsSystem) Git(dir string, stdin []byte, args ...string) (string, error) {
	out, err := s.Local.Git(dir, stdin, args...)
	if len(args) > 0 && args[0] == "hash-object" && !s.called {
		s.called = true
		s.move()
	}
	return out, err
}

func TestContentsRetryAMovingWorktree(t *testing.T) {
	r := testRepo(t)
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "first edit\n")
	scan, err := r.Scan(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.sys = &movingContentsSystem{move: func() { write(t, r.Dir, "foo.txt", "second edit\n") }}
	contents, err := r.ReadContents(scan)
	if err != nil || len(contents.Files) != 1 || string(contents.Files[0].New.Data) != "second edit\n" {
		t.Fatalf("did not retry changed content pair: %+v, %v", contents, err)
	}
}

func TestSnapshotContentsRejectAMovedReviewBranch(t *testing.T) {
	r := testRepo(t)
	snapshot, err := r.Snapshot("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := snapshot.Scan("main", 0)
	if err != nil {
		t.Fatal(err)
	}
	run(t, r, "checkout", "-qb", "another-review")
	if _, err := snapshot.ReadContents(scan); err == nil {
		t.Fatal("accepted contents under a moved review branch")
	}
}

type countedContentsSystem struct {
	Local
	calls int
}

func (s *countedContentsSystem) Git(dir string, stdin []byte, args ...string) (string, error) {
	s.calls++
	return s.Local.Git(dir, stdin, args...)
}

func TestContentsBatchGitCommandsAcrossFiles(t *testing.T) {
	counts := map[int]int{}
	for _, files := range []int{2, 24} {
		r := testRepo(t)
		for i := range files {
			write(t, r.Dir, "batch-"+strconv.Itoa(i), "old\n")
		}
		run(t, r, "add", ".")
		run(t, r, "commit", "-qm", "seed batch")
		base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
		for i := range files {
			write(t, r.Dir, "batch-"+strconv.Itoa(i), "new-"+strconv.Itoa(i)+"\n")
		}
		scan, err := r.Scan(base, 0)
		if err != nil {
			t.Fatal(err)
		}
		counter := &countedContentsSystem{}
		r.sys = counter
		contents, err := r.ReadContents(scan)
		if err != nil || len(contents.Files) != files {
			t.Fatalf("batch contents: %+v, %v", contents, err)
		}
		for _, change := range contents.Files {
			if change.Problem != "" || string(change.Old.Data) != "old\n" || !strings.HasPrefix(string(change.New.Data), "new-") {
				t.Fatalf("batch lost bytes: %+v", change)
			}
		}
		counts[files] = counter.calls
	}
	if counts[2] != counts[24] || counts[24] > 16 {
		t.Fatalf("Git calls grew with file count: %v", counts)
	}
}

func TestFilteredBatchPreservesFilenameBytes(t *testing.T) {
	r := testRepo(t)
	names := []string{"a space.txt", "é.txt"}
	if runtime.GOOS != "windows" {
		names = append(names, "a\ttab.txt", "a\nnewline.txt", "\x01control.txt", "a\\slash.txt")
	}
	for _, name := range names {
		write(t, r.Dir, name, "new content\n")
	}
	scan, err := r.Scan("HEAD", 0)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := r.ReadContents(scan)
	if err != nil || len(contents.Files) != len(names) {
		t.Fatalf("quoted batch: %+v, %v", contents, err)
	}
	seen := map[string]bool{}
	for _, change := range contents.Files {
		if change.Problem != "" || string(change.New.Data) != "new content\n" {
			t.Fatalf("filename or bytes lost: %+v", change)
		}
		seen[change.New.Path] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Fatalf("filename bytes changed: %q", name)
		}
	}
}

func TestBlobBatchRejectsIncompleteData(t *testing.T) {
	object := strings.Repeat("a", 40)
	for _, body := range []string{object + " blob 3\nab\n", object + " blob 2\nabc\n", object + " blob 3\nabc\nextra"} {
		sys := &fakeSystem{git: map[string]string{"cat-file --batch-check": object + " blob 3\n", "cat-file --batch": body}}
		r := &Repo{sys: sys}
		blobs, problems := r.contentBlobs([]string{object}, 100)
		if len(blobs) != 0 || problems[object] == "" {
			t.Fatalf("accepted incomplete blob stream: %q, %v", body, problems)
		}
	}
	sys := &fakeSystem{git: map[string]string{"cat-file --batch-check": object + " blob " + strconv.Itoa(maxContentBytes+1) + "\n"}}
	r := &Repo{sys: sys}
	blobs, problems := r.contentBlobs([]string{object}, maxReadingBytes)
	if len(blobs) != 0 || problems[object] == "" || len(sys.gitCalls) != 1 {
		t.Fatalf("oversized blob body was read: %v, %v", sys.gitCalls, problems)
	}
}
