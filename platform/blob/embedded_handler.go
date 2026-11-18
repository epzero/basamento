package blob

import (
	"net/http"
	"path"
	"strings"
)

func newEmbeddedHandler(st *Store) http.Handler {
	read := &URLHandler{Store: st}
	up := &UploadHandler{Store: st}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/upload/") {
			up.ServeHTTP(w, r)
			return
		}
		read.ServeHTTP(w, r)
	})
	return http.StripPrefix(path.Clean(HandlerRoute()), mux)
}
