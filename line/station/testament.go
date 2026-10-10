package station

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

const (
	// maxTestament bounds a testament body.
	maxTestament  = 64 << 10
	testamentRef  = "refs/line/testament/"
	testamentFile = "testament"
)

// routeTestament handles GET/PUT /v1/testament (viceroy only). Each PUT
// stores the body as a blob in a commit on refs/line/testament/<role>, whose
// parent is the previous commit, so every version stays in the line remote.
func (s *Server) routeTestament(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		return "", fail(http.StatusMethodNotAllowed, "use GET or PUT for %s", r.URL.Path)
	}
	if st.Role != viceroyRole {
		return "", fail(http.StatusForbidden, "role %q may not use the testament", st.Role)
	}
	repo := s.Guard.Repo
	if repo.Dir == "" {
		return "", fail(http.StatusServiceUnavailable, "no line remote")
	}
	ctx := r.Context()
	ref := testamentRef + st.Role
	old := ""
	if ok, err := repo.RefExists(ctx, ref); err != nil {
		return "", err
	} else if ok {
		if old, err = repo.Ref(ctx, ref); err != nil {
			return "", err
		}
	}
	if r.Method == http.MethodGet {
		if old == "" {
			w.WriteHeader(http.StatusNoContent)
			return "testament none", nil
		}
		body, err := repo.ReadBlob(ctx, old+":"+testamentFile)
		if err != nil {
			return "", err
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
		return fmt.Sprintf("testament get %s (%d bytes)", old, len(body)), nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTestament))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", fail(http.StatusRequestEntityTooLarge, "testament exceeds %d bytes", maxTestament)
		}
		return "", fail(http.StatusBadRequest, "read body: %v", err)
	}
	blob, err := repo.WriteBlob(ctx, body)
	if err != nil {
		return "", err
	}
	tree, err := repo.WriteTreeWithBlob(ctx, testamentFile, blob)
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitTree(ctx, tree, old, "testament of "+st.Role, st.Role)
	if err != nil {
		return "", err
	}
	if err := repo.UpdateRef(ctx, ref, commit, old); err != nil {
		if errors.Is(err, gitx.ErrRefMoved) {
			return "", fail(http.StatusConflict, "testament changed concurrently; retry")
		}
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]string{"commit": commit})
	return fmt.Sprintf("testament put %s (%d bytes)", commit, len(body)), nil
}
