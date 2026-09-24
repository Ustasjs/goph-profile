package httpserver

import (
	"net/http"

	"github.com/ustasjs/goph-profile/web"
)

const (
	pageUpload  = web.Upload
	pageGallery = web.Gallery
)

// servePage answers with one embedded HTML page.
func servePage(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := web.FS.ReadFile(path)
		if err != nil {
			// Embedded files are read at build time; a miss here is a
			// programming error, not a runtime condition.
			writeError(w, http.StatusInternalServerError, "page is missing from the build")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}
}
