package maker

import (
	"context"
	"fmt"
	"slices"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

// BeadTrailer names the card a commit works; Landed looks for it.
const BeadTrailer = "Bead"

// CheckTrailers fails unless rev carries `Bead: <bead>` (and, for an andon
// card, `Andon-Event: <event>`) as git trailers, i.e. in the message's final
// paragraph. A trailer stranded in an earlier paragraph — say by a
// Co-Authored-By block appended after it — is not seen by Landed or the
// guard, so the station's merge client refuses it before submitting.
func CheckTrailers(ctx context.Context, repo gitx.Repo, rev, bead, event string) error {
	want := []struct{ key, value string }{{BeadTrailer, bead}}
	if event != "" {
		want = append(want, struct{ key, value string }{guard.FixTrailer, event})
	}
	for _, w := range want {
		values, err := repo.Trailer(ctx, rev, w.key)
		if err != nil {
			return err
		}
		if !slices.Contains(values, w.value) {
			return fmt.Errorf("%s lacks the trailer `%s: %s`: put it in the commit message's last paragraph, with nothing after that paragraph, and amend", rev, w.key, w.value)
		}
	}
	return nil
}
