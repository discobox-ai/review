package git

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/discobox-ai/review/pkg/source"
)

const (
	maxContentBytes = 16 << 20
	maxReadingBytes = 64 << 20
)

type contentEntry struct {
	change               source.Change
	oldObject, newObject string
}

func (r *Repo) contents(base, head string) (source.Contents, error) {
	entries, err := r.contentManifest(base, head)
	if err != nil {
		return source.Contents{}, err
	}
	var paths []string
	for i := range entries {
		entry := &entries[i]
		if head == "" && entry.change.New.Present && (entry.change.New.Mode == "100644" || entry.change.New.Mode == "100755") {
			info, statErr := r.sys.Lstat(filepath.Join(r.Dir, entry.change.New.Path))
			switch {
			case statErr != nil:
				entry.change.Problem = statErr.Error()
			case !info.Mode.IsRegular():
				entry.change.Problem = "file kind moved while reading contents"
			case info.Size > maxContentBytes:
				entry.change.Problem = "content too large to verify"
			default:
				paths = append(paths, entry.change.New.Path)
			}
		}
	}
	hashed, hashErr := r.hashContents(paths)
	linksConfigured, plainLinks := false, false
	directBytes := 0
	for i := range entries {
		entry := &entries[i]
		if !entry.change.New.Present || head != "" {
			continue
		}
		side := &entry.change.New
		switch side.Mode {
		case "100644", "100755":
			if entry.change.Problem != "" {
				continue
			}
			if hashErr != nil {
				entry.change.Problem = hashErr.Error()
			} else {
				entry.newObject = hashed[side.Path]
			}
		case "120000":
			target, linkErr := r.sys.Readlink(filepath.Join(r.Dir, side.Path))
			if linkErr != nil {
				if !linksConfigured {
					links, _ := r.run("config", "--bool", "core.symlinks")
					plainLinks, linksConfigured = strings.TrimSpace(links) == "false", true
				}
				if plainLinks {
					info, statErr := r.sys.Lstat(filepath.Join(r.Dir, side.Path))
					if statErr != nil {
						linkErr = statErr
					} else if !info.Mode.IsRegular() || info.Size > maxContentBytes {
						linkErr = fmt.Errorf("link content unavailable")
					} else {
						data, readErr := r.sys.ReadFile(filepath.Join(r.Dir, side.Path))
						target, linkErr = string(data), readErr
					}
				}
			}
			if linkErr != nil {
				entry.change.Problem = linkErr.Error()
			} else if len(target) > maxContentBytes || len(target) > maxReadingBytes-directBytes {
				entry.change.Problem = "content too large to verify"
			} else {
				side.Data = []byte(target)
				directBytes += len(target)
			}
		case "160000":
			if strings.Trim(entry.newObject, "0") == "" {
				object, subErr := r.run("-C", side.Path, "rev-parse", "HEAD")
				if subErr != nil {
					entry.change.Problem = subErr.Error()
				} else {
					entry.newObject = strings.TrimSpace(object)
				}
			}
		}
	}
	var objects []string
	seen := map[string]bool{}
	for _, entry := range entries {
		for _, side := range []struct {
			side     source.Side
			object   string
			worktree bool
		}{{entry.change.Old, entry.oldObject, false}, {entry.change.New, entry.newObject, head == ""}} {
			if !side.side.Present || side.side.Mode == "160000" || side.worktree && side.side.Mode == "120000" || entry.change.Problem != "" {
				continue
			}
			if !seen[side.object] {
				objects = append(objects, side.object)
				seen[side.object] = true
			}
		}
	}
	blobs, problems := r.contentBlobs(objects, maxReadingBytes-directBytes)
	out := source.Contents{Complete: true}
	for _, entry := range entries {
		for _, side := range []struct {
			side     *source.Side
			object   string
			worktree bool
		}{{&entry.change.Old, entry.oldObject, false}, {&entry.change.New, entry.newObject, head == ""}} {
			if !side.side.Present {
				continue
			}
			if side.side.Mode == "160000" {
				side.side.Data = []byte(side.object)
				continue
			}
			if side.worktree && side.side.Mode == "120000" || entry.change.Problem != "" {
				continue
			}
			if problem := problems[side.object]; problem != "" {
				entry.change.Problem = problem
				continue
			}
			side.side.Data = blobs[side.object]
		}
		out.Files = append(out.Files, entry.change)
	}
	return out, nil
}

func (r *Repo) contentManifest(base, head string) ([]contentEntry, error) {
	if base == "" {
		base = EmptyTree
	}
	args := []string{"diff", "--raw", "--no-abbrev", "-z", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--find-renames=50%", "-l1000", base}
	if head != "" {
		args = append(args, head)
	}
	raw, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	if raw != "" && !strings.HasSuffix(raw, "\x00") {
		return nil, fmt.Errorf("incomplete changed-path manifest")
	}
	var entries []contentEntry
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for i := 0; i < len(fields) && raw != ""; {
		meta := strings.Fields(fields[i])
		if len(meta) != 5 || !strings.HasPrefix(meta[0], ":") || i+1 >= len(fields) {
			return nil, fmt.Errorf("incomplete changed-path manifest")
		}
		oldPath, newPath := fields[i+1], fields[i+1]
		i += 2
		if strings.HasPrefix(meta[4], "R") || strings.HasPrefix(meta[4], "C") {
			if i >= len(fields) {
				return nil, fmt.Errorf("incomplete rename manifest")
			}
			newPath = fields[i]
			i++
		}
		entry := contentEntry{oldObject: meta[2], newObject: meta[3]}
		oldMode := strings.TrimPrefix(meta[0], ":")
		if oldMode != "000000" {
			entry.change.Old = source.Side{Path: oldPath, Mode: oldMode, Present: true}
		}
		if meta[1] != "000000" {
			entry.change.New = source.Side{Path: newPath, Mode: meta[1], Present: true}
		}
		entries = append(entries, entry)
	}
	if head != "" {
		return entries, nil
	}
	untracked, err := r.run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if untracked != "" && !strings.HasSuffix(untracked, "\x00") {
		return nil, fmt.Errorf("incomplete untracked-path manifest")
	}
	fileMode, _ := r.run("config", "--bool", "core.filemode")
	for path := range strings.SplitSeq(untracked, "\x00") {
		if path == "" {
			continue
		}
		info, err := r.sys.Lstat(filepath.Join(r.Dir, path))
		if err != nil {
			return nil, err
		}
		mode := "100644"
		switch {
		case info.Mode&fs.ModeSymlink != 0:
			mode = "120000"
		case !info.Mode.IsRegular():
			return nil, fmt.Errorf("untracked file kind unavailable: %s", path)
		case strings.TrimSpace(fileMode) != "false" && info.Mode.Perm()&0o111 != 0:
			mode = "100755"
		}
		entries = append(entries, contentEntry{change: source.Change{New: source.Side{Path: path, Mode: mode, Present: true}}})
	}
	return entries, nil
}

func (r *Repo) hashContents(paths []string) (map[string]string, error) {
	objects := map[string]string{}
	if len(paths) == 0 {
		return objects, nil
	}
	var input strings.Builder
	for _, path := range paths {
		input.WriteString(QuotePath(path))
		input.WriteByte('\n')
	}
	out, err := r.sys.Git(r.Dir, []byte(input.String()), "hash-object", "-w", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	if len(ids) != len(paths) {
		return nil, fmt.Errorf("incomplete filtered-content hashes")
	}
	for i, path := range paths {
		objects[path] = ids[i]
	}
	return objects, nil
}

func (r *Repo) contentBlobs(objects []string, byteBudget int) (map[string][]byte, map[string]string) {
	blobs, problems := map[string][]byte{}, map[string]string{}
	if len(objects) == 0 {
		return blobs, problems
	}
	fail := func(message string) {
		for _, object := range objects {
			problems[object] = message
		}
	}
	info, err := r.sys.Git(r.Dir, []byte(strings.Join(objects, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		fail(err.Error())
		return blobs, problems
	}
	headers := strings.Split(strings.TrimSuffix(info, "\n"), "\n")
	if len(headers) != len(objects) {
		fail("incomplete content metadata")
		return blobs, problems
	}
	var wanted []string
	sizes := map[string]int{}
	total := 0
	for i, object := range objects {
		fields := strings.Fields(headers[i])
		if len(fields) != 3 || fields[0] != object || fields[1] != "blob" {
			problems[object] = "repository blob unavailable"
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > maxContentBytes || size > int64(byteBudget-total) {
			problems[object] = "content too large to verify"
			continue
		}
		sizes[object] = int(size)
		total += int(size)
		wanted = append(wanted, object)
	}
	if len(wanted) == 0 {
		return blobs, problems
	}
	body, err := r.sys.Git(r.Dir, []byte(strings.Join(wanted, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		fail(err.Error())
		return blobs, problems
	}
	for _, object := range wanted {
		header, rest, ok := strings.Cut(body, "\n")
		if !ok || header != object+" blob "+strconv.Itoa(sizes[object]) || len(rest) <= sizes[object] || rest[sizes[object]] != '\n' {
			fail("incomplete repository blob stream")
			return map[string][]byte{}, problems
		}
		blobs[object] = []byte(rest[:sizes[object]])
		body = rest[sizes[object]+1:]
	}
	if body != "" {
		fail("unexpected repository blob data")
		return map[string][]byte{}, problems
	}
	return blobs, problems
}
