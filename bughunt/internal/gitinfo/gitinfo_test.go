package gitinfo

import (
	"reflect"
	"testing"
)

const diff = `diff --git a/internal/scan/scan.go b/internal/scan/scan.go
index 1111111..2222222 100644
--- a/internal/scan/scan.go
+++ b/internal/scan/scan.go
@@ -10,0 +11,2 @@ func Run() {
+	added := 1
+	_ = added
@@ -40 +42 @@ func Other() {
-	old()
+	replaced()
diff --git a/deleted.go b/deleted.go
deleted file mode 100644
--- a/deleted.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package x
-
-func Gone() {}
`

func TestParseUnifiedDiff(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	want := map[string][]int{"internal/scan/scan.go": {11, 12, 42}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseUnifiedDiff = %v, want %v", got, want)
	}
}

func TestParseUnifiedDiffIgnoresDeletedFiles(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	if _, ok := got["deleted.go"]; ok {
		t.Fatal("a deleted file has no changed lines to gate on")
	}
}

func TestParseUnifiedDiffEmptyInput(t *testing.T) {
	if got := ParseUnifiedDiff(""); len(got) != 0 {
		t.Fatalf("ParseUnifiedDiff(\"\") = %v, want empty", got)
	}
}
