// A temporary, loopback-only archive endpoint for kubectl port-forward.
// ServeContent provides Content-Length and Range support; unlike exec stdout,
// an incomplete transfer is detectable and can be resumed.
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: transfer /path/to/completed-archive.tar.gz")
	}
	path := os.Args[1]
	http.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "archive unavailable", http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			http.Error(w, "archive unavailable", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, "workspace.tar.gz", st.ModTime(), f)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:18089", nil))
}
