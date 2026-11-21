package blob

import (
	"net/http"
	"path"
)

func newEmbeddedHandler(st *Store) http.Handler {
	read := &URLHandler{Store: st}
	up := &UploadHandler{Store: st}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := parseUploadPath(r.URL.Path); ok {
			up.ServeHTTP(w, r)
			return
		}
		read.ServeHTTP(w, r)
	})
	return http.StripPrefix(path.Clean(HandlerRoute()), mux)
}
