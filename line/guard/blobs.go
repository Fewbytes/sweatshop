package guard

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

// executableKind names the executable format a blob's leading bytes show, or
// "" for anything else. Fat Mach-O (0xcafebabe) is not detected: Java class
// files share the magic, and a false rejection costs a Maker a round trip.
func executableKind(head []byte) string {
	for _, m := range []struct {
		kind  string
		magic []byte
	}{
		{"ELF", []byte{0x7f, 'E', 'L', 'F'}},
		{"Mach-O", []byte{0xfe, 0xed, 0xfa, 0xce}},
		{"Mach-O", []byte{0xfe, 0xed, 0xfa, 0xcf}},
		{"Mach-O", []byte{0xce, 0xfa, 0xed, 0xfe}},
		{"Mach-O", []byte{0xcf, 0xfa, 0xed, 0xfe}},
	} {
		if bytes.HasPrefix(head, m.magic) {
			return m.kind
		}
	}
	// A PE file is a DOS stub ("MZ") whose word at 0x3c points at "PE\0\0";
	// "MZ" alone also starts plain text.
	const peOffsetField = 0x3c
	if bytes.HasPrefix(head, []byte("MZ")) && len(head) >= peOffsetField+4 {
		at := int(binary.LittleEndian.Uint32(head[peOffsetField:]))
		if at >= 0 && at <= len(head)-4 && bytes.Equal(head[at:at+4], []byte("PE\x00\x00")) {
			return "PE"
		}
	}
	return ""
}

// blobViolation describes the first blob in blobs the rules reject, or "" if
// all pass. The message names the path and size and says what to do, because
// it is all a Maker sees of the rejection.
func blobViolation(rules constitution.BlobGuard, blobs []gitx.ChangedBlob) string {
	limit := rules.MaxBytesOrDefault()
	for _, b := range blobs {
		if rules.Allows(b.Path) {
			continue
		}
		var problem string
		if kind := executableKind(b.Head); kind != "" {
			problem = fmt.Sprintf("is a %s executable", kind)
		} else if b.Size > limit {
			problem = fmt.Sprintf("is %d bytes, over the %d byte limit", b.Size, limit)
		} else {
			continue
		}
		return fmt.Sprintf("%s (%d bytes) %s. Build output is never committed: unstage it (git rm --cached %s), add it to .gitignore, amend the commit and submit again. "+
			"If it truly belongs in the repository, a constitution change must add the path to guards.blobs.allow.", b.Path, b.Size, problem, b.Path)
	}
	return ""
}
