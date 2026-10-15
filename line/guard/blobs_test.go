package guard

import (
	"strings"
	"testing"
)

func peImage() string {
	head := make([]byte, 0x80)
	copy(head, "MZ")
	head[0x3c] = 0x40
	copy(head[0x40:], "PE\x00\x00")
	return string(head)
}

func TestMergeRejectsBlobsOverLimitAndExecutables(t *testing.T) {
	cases := []struct {
		name, path, content, wantDetail string
	}{
		{"large", "data/dump.txt", strings.Repeat("x", 1<<20+1), "1048577 bytes"},
		{"elf", "bin/tool", "\x7fELF" + strings.Repeat("\x00", 60), "ELF executable"},
		{"macho", "bin/tool", "\xcf\xfa\xed\xfe" + strings.Repeat("\x00", 60), "Mach-O executable"},
		{"pe", "bin/tool.exe", peImage(), "PE executable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			commit := f.commit(tc.path, tc.content)
			f.candidate(commit)
			f.expectRejected(commit, f.envelope(commit, f.key, nil), ReasonBlobForbidden)

			f.candidate(commit)
			result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, nil))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Detail, tc.path) || !strings.Contains(result.Detail, tc.wantDetail) {
				t.Fatalf("detail %q lacks path %q or %q", result.Detail, tc.path, tc.wantDetail)
			}
		})
	}
}

func TestMergeAcceptsSmallTextAndMZProse(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("notes.txt", "MZ is not a program")
	f.candidate(commit)
	result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, nil))
	if err != nil || result.Status != Merged {
		t.Fatalf("merge: %+v %v", result, err)
	}
}

func TestMergeHonoursConfiguredBlobLimitAndAllowList(t *testing.T) {
	f := newFixture(t)
	f.guard.Constitution.Guards.Blobs.MaxBytes = 10
	f.guard.Constitution.Guards.Blobs.Allow = []string{"vendor/**"}
	big := f.commit("big.txt", strings.Repeat("x", 11))
	f.candidate(big)
	f.expectRejected(big, f.envelope(big, f.key, nil), ReasonBlobForbidden)

	f = newFixture(t)
	f.guard.Constitution.Guards.Blobs.Allow = []string{"vendor/**"}
	allowed := f.commit("vendor/tool", "\x7fELF"+strings.Repeat("\x00", 60))
	f.candidate(allowed)
	result, err := f.guard.Merge(f.ctx, allowed, f.envelope(allowed, f.key, nil))
	if err != nil || result.Status != Merged {
		t.Fatalf("allow-listed merge: %+v %v", result, err)
	}
}
