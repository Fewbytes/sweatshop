package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

func TestMergeResponse(t *testing.T) {
	result := witness.MergeResult{Guard: guard.Result{Status: guard.Merged, Commit: "abc123"}}
	mergeErr, recordErr := errors.New("push failed"), errors.New("disk full")
	tests := []struct {
		name         string
		mergeErr     error
		recordErr    error
		wantCode     string
		wantContains []string
	}{
		{"success", nil, nil, "", nil},
		{"record failure after merge", nil, recordErr, "attestation_not_recorded", []string{"merged", "abc123", "disk full"}},
		{"merge failure", mergeErr, nil, "merge", []string{"push failed"}},
		{"merge and record failure", mergeErr, recordErr, "merge", []string{"push failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := mergeResponse("id", result, tt.mergeErr, tt.recordErr)
			if tt.wantCode == "" {
				if response.Error != nil {
					t.Fatalf("error = %v", response.Error)
				}
				return
			}
			if response.Error == nil || response.Error.Code != tt.wantCode {
				t.Fatalf("error = %v, want code %s", response.Error, tt.wantCode)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(response.Error.Message, want) {
					t.Errorf("message %q lacks %q", response.Error.Message, want)
				}
			}
		})
	}
}
