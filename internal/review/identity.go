package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/discobox-ai/review/pkg/source"
)

// changeRun uses zero-based offsets into the complete line-token sequences.
// Tokens retain their exact terminators, including an absent final newline.
type changeRun struct {
	oldStart, newStart int
	removed, added     []string
}

// comparison owns the canonical runs and the content pair that produced
// them. Display hunks may group these runs but cannot redefine their IDs.
type comparison struct {
	change source.Change
	runs   []changeRun
}

// identityBytes is the scheme's length-delimited encoding. All integers use
// unsigned 64-bit big endian, including lengths; domains are encoded fields.
type identityBytes []byte

func (b *identityBytes) number(n int) {
	*b = binary.BigEndian.AppendUint64(*b, uint64(n))
}

func (b *identityBytes) field(data []byte) {
	b.number(len(data))
	*b = append(*b, data...)
}

func (b *identityBytes) side(side source.Side) {
	b.field([]byte(side.Path))
	b.field([]byte(side.Mode))
	if side.Present {
		b.number(1)
	} else {
		b.number(0)
	}
}

func (b *identityBytes) tokens(tokens []string) {
	b.number(len(tokens))
	for _, token := range tokens {
		b.field([]byte(token))
	}
}

func approvalHash(domain string, payload identityBytes) string {
	var encoded identityBytes
	encoded.field([]byte(IdentityScheme))
	encoded.field([]byte(domain))
	encoded.field(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (c comparison) identity() Identity {
	var metadata identityBytes
	metadata.side(c.change.Old)
	metadata.side(c.change.New)
	fingerprint := append(identityBytes(nil), metadata...)
	fingerprint.field(c.change.Old.Data)
	fingerprint.field(c.change.New.Data)
	id := Identity{Fingerprint: approvalHash("pair", fingerprint)}
	file := append(identityBytes(nil), metadata...)
	binaryChange := c.change.Old.Mode == "160000" || c.change.New.Mode == "160000" ||
		bytes.IndexByte(c.change.Old.Data, 0) >= 0 || bytes.IndexByte(c.change.New.Data, 0) >= 0
	if binaryChange || len(c.runs) == 0 {
		file.number(0)
		file.field(c.change.Old.Data)
		file.field(c.change.New.Data)
	} else {
		file.number(len(c.runs))
		for _, run := range c.runs {
			encoded := append(identityBytes(nil), metadata...)
			encoded.number(run.oldStart)
			encoded.number(run.newStart)
			encoded.tokens(run.removed)
			encoded.tokens(run.added)
			digest := approvalHash("run", encoded)
			id.Runs = append(id.Runs, digest)
			file.field([]byte(digest))
		}
	}
	id.Digest = approvalHash("file", file)
	return id
}
