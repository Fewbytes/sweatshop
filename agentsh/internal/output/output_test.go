package output

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestPreviewTruncationHasExactMetadataAndRecovery(t *testing.T) {
	var input strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&input, "line-%04d payload payload payload\n", i)
	}
	ref, err := Preview(strings.NewReader(input.String()), "inv_deadbeef", "stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Truncated || len(ref.Preview) > PreviewByteLimit+500 {
		t.Fatalf("unexpected preview bounds: %d %+v", len(ref.Preview), ref)
	}
	for _, want := range []string{"17000 bytes, 500 lines", "showing 1-100 and 401-500", `BashOutput(id="inv_deadbeef", stream="stdout")`} {
		if !strings.Contains(ref.Preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, ref.Preview)
		}
	}
}

func TestReadLinesAndGrepContextAreNumbered(t *testing.T) {
	input := "alpha\nbeta\nerror here\ndelta\nepsilon\n"
	result, err := Read(strings.NewReader(input), Options{Grep: "error", Context: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:error here\n4:delta" {
		t.Fatalf("grep result = %q", result.Text)
	}
	result, err = Read(strings.NewReader(input), Options{Lines: "2:4"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:error here\n4:delta" {
		t.Fatalf("lines result = %q", result.Text)
	}
}

func TestLargeStreamIsReadWithoutCap(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	const size = 500 * 1024 * 1024
	reader := io.LimitReader(strings.NewReader(strings.Repeat("0123456789abcde\n", size/16)), size)
	result, err := Read(reader, Options{Lines: "1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != size {
		t.Fatalf("read %d bytes, want %d", result.Bytes, size)
	}
}
