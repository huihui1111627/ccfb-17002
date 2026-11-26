package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"

	"shepherd/internal/acceptance"
	"shepherd/internal/reference"
	shepherd "shepherd/internal/shepherd"
)

type SimResponse struct {
	*shepherd.Report
	ReferenceMatch bool   `json:"referenceMatch"`
	ReferenceDiff  string `json:"referenceDiff,omitempty"`
}

//go:embed static
var staticFS embed.FS

func NewHandler() http.Handler {
	mux := http.NewServeMux()

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(staticFile("index.html"))
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/sim", func(w http.ResponseWriter, r *http.Request) {
		variant := parseIntDefault(r.URL.Query().Get("variant"), 0)
		seed := int64(parseIntDefault(r.URL.Query().Get("seed"), int(shepherd.FixedSeedBase)))
		if variant < 0 {
			variant = 0
		}
		if variant > 63 {
			variant = 63
		}
		sc := shepherd.GenerateScenario(variant, seed)
		rep := shepherd.Run(sc)
		ref := reference.RunReference(sc)
		match, diff := acceptance.CompareItems(rep.Items, ref.Items)
		writeJSON(w, SimResponse{Report: rep, ReferenceMatch: match, ReferenceDiff: diff})
	})

	mux.HandleFunc("/api/acceptance", func(w http.ResponseWriter, r *http.Request) {
		count := parseIntDefault(r.URL.Query().Get("runs"), 64)
		if count <= 0 || count > 200 {
			count = 64
		}
		writeJSON(w, acceptance.RunSuite(count))
	})

	return mux
}

func staticFile(name string) []byte {
	b, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		return []byte("missing static file: " + name)
	}
	return b
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
