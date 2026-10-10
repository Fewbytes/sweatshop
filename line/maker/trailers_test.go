package maker

import (
	"context"
	"strings"
	"testing"
)

func TestCheckTrailers(t *testing.T) {
	cases := map[string]struct {
		msg, event, wantErr string
	}{
		"bead trailer":                  {msg: "fix\n\nBead: b-1", wantErr: ""},
		"bead with co-author":           {msg: "fix\n\nBead: b-1\nCo-Authored-By: x <x@y>", wantErr: ""},
		"bead pushed out by co-author":  {msg: "fix\n\nBead: b-1\n\nCo-Authored-By: x <x@y>", wantErr: "Bead: b-1"},
		"no bead":                       {msg: "fix", wantErr: "Bead: b-1"},
		"other bead":                    {msg: "fix\n\nBead: b-2", wantErr: "Bead: b-1"},
		"andon fix":                     {msg: "fix\n\nBead: b-1\nAndon-Event: e1", event: "e1", wantErr: ""},
		"andon fix without event":       {msg: "fix\n\nBead: b-1", event: "e1", wantErr: "Andon-Event: e1"},
		"andon event in body not block": {msg: "fix\n\nAndon-Event: e1\n\nBead: b-1", event: "e1", wantErr: "Andon-Event: e1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, work := remote(t)
			commit(t, work, tc.msg)
			err := CheckTrailers(context.Background(), work, "HEAD", "b-1", tc.event)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
			}
		})
	}
}
