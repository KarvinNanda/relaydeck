package server

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// Module scripts need a JavaScript MIME type, and with nosniff the browser
// refuses a script served as text/plain. On Windows the registry can map .js
// to something else, so the type is set here, not left to the OS. The test
// plays that registry override with wrong types.
func TestStaticContentTypesAreExplicit(t *testing.T) {
	for _, ext := range []string{".js", ".mjs", ".css"} {
		if err := mime.AddExtensionType(ext, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
		mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
		mime.AddExtensionType(".css", "text/css; charset=utf-8")
	})
	h := staticFiles(fstest.MapFS{
		"assets/app.js":   {Data: []byte("export {}")},
		"assets/mod.mjs":  {Data: []byte("export {}")},
		"assets/main.css": {Data: []byte("a{}")},
	})
	for path, want := range map[string]string{
		"/assets/app.js":   "text/javascript; charset=utf-8",
		"/assets/mod.mjs":  "text/javascript; charset=utf-8",
		"/assets/main.css": "text/css; charset=utf-8",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Header().Get("Content-Type"), want)
		}
	}
}
