// Command sandbox serves the mock vendor APIs (sandbox.acyclic.sh) and the
// agent playground (agentsdk.acyclic.sh) from one binary.
//
//	DATABASE_URL        Postgres URL; memory when empty
//	PUBLIC_MOCK_BASE    public origin of the mock APIs (default http://localhost:PORT)
//	OPENROUTER_API_KEY  model proxy key; the playground's chat is off without it
//	MODEL               default deepseek/deepseek-v4.1-flash-1017
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/mock"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/proxy"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/store"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/synth"
	"github.com/GrahamGoudeau/acyclic-sandbox/web"
)


func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	port := flag.String("port", env("PORT", "8190"), "listen port")
	flag.String("log", "", "ignored (systemd template compatibility)")
	flag.Parse()
	ctx := context.Background()

	var db store.Store = store.NewMemory()
	if u := os.Getenv("DATABASE_URL"); u != "" {
		pg, err := store.OpenPostgres(ctx, u)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		db = pg
		log.Print("store: postgres")
	} else {
		log.Print("store: memory (set DATABASE_URL for Postgres)")
	}
	defer db.Close()

	public := env("PUBLIC_MOCK_BASE", "http://localhost:"+*port)
	data := synth.Build(time.Now().UTC())
	ms, err := mock.New(ctx, public, data, db)
	if err != nil {
		log.Fatalf("mock: %v", err)
	}

	model := env("MODEL", "deepseek/deepseek-v4.1-flash-1017")
	tools := map[string]bool{}
	for _, n := range []string{"find_patient", "select_patient", "get_patient", "list_conditions", "list_medications", "list_allergies", "get_labs_vitals", "list_encounters", "list_appointments", "list_documents", "read_document", "get_coverage", "create_note"} {
		tools[n] = true
	}
	px := proxy.New(proxy.Config{
		Upstream: env("MODEL_UPSTREAM", "https://openrouter.ai/api/v1"), APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: model,
		ExtraBody:   map[string]any{"provider": map[string]any{"data_collection": "deny", "zdr": true}},
		MaxTokens:   4096, PerIPWindow: 40, PerIPDay: 300, GlobalDay: 4000, AllowedTools: tools,
		TrustProxy: os.Getenv("TRUST_PROXY") == "1",
	})

	mux := http.NewServeMux()
	ms.Routes(mux)
	mux.Handle("/api/model/chat/completions", px)

	vendors := []map[string]any{}
	for _, v := range mock.Vendors {
		vendors = append(vendors, map[string]any{
			"id": v.ID, "label": v.Label, "auth": v.Auth[0], "fidelity": v.Fidelity, "corsNote": v.CORSNote,
			"baseUrl": public + v.FHIRPath, "tokenUrl": public + v.TokenPath, "registerUrl": public + "/register",
			"practiceId": v.PracticeID,
		})
	}
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"mockBase": public, "vendors": vendors, "model": model, "credsVersion": "v1", "chat": os.Getenv("OPENROUTER_API_KEY") != ""})
	})

	app := &runs{db: db}
	mux.HandleFunc("GET /api/runs", app.list)
	mux.HandleFunc("PUT /api/runs/{id}", app.save)
	mux.HandleFunc("POST /api/runs/{id}/events", app.append)
	mux.HandleFunc("GET /api/runs/{id}/events", app.events)
	mux.HandleFunc("POST /api/runs/{id}/share", app.share)

	files := http.FileServer(http.FS(web.FS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "sandbox.") && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "Acyclic EHR sandbox: vendor-compatible mock FHIR R4 APIs with synthetic patients.\nPlayground: https://agentsdk.acyclic.sh\nSource: https://github.com/GrahamGoudeau/acyclic-sandbox\n")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/r/") {
			r.URL.Path = "/"
		}
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})

	go func() {
		for {
			if err := db.Purge(ctx, 7*24*time.Hour, 30*24*time.Hour); err != nil {
				log.Printf("purge: %v", err)
			}
			ms.Purge(7 * 24 * time.Hour)
			time.Sleep(time.Hour)
		}
	}()

	srv := &http.Server{Addr: "127.0.0.1:" + *port, Handler: logged(mux), ReadHeaderTimeout: 10 * time.Second}
	if os.Getenv("LISTEN_ALL") == "1" {
		srv.Addr = ":" + *port
	}
	log.Printf("listening on %s (mock base %s)", srv.Addr, public)
	log.Fatal(srv.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// logged records method, path (never the query string) and status.
func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(rw, r)
		if !strings.HasSuffix(r.URL.Path, ".css") && !strings.HasPrefix(r.URL.Path, "/sdk/") {
			log.Printf("%s %s %d %dms", r.Method, r.URL.Path, rw.status, time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

// ---------- recorded runs ----------

type runs struct{ db store.Store }

var runID = regexp.MustCompile(`^run_[a-f0-9]{8,40}$`)

func session(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("sbx_session"); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "sbx_session", Value: id, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	return id
}

func (a *runs) list(w http.ResponseWriter, r *http.Request) {
	rs, err := a.db.Runs(r.Context(), session(w, r), 20)
	if err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	if rs == nil {
		rs = []store.Run{}
	}
	writeJSON(w, rs)
}

func (a *runs) save(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !runID.MatchString(id) {
		http.Error(w, "bad run id", 400)
		return
	}
	var body struct{ Vendor, PatientID, Title string }
	var raw map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&raw); err != nil {
		http.Error(w, "bad JSON", 400)
		return
	}
	body.Vendor, body.PatientID, body.Title = raw["vendor"], raw["patient_id"], raw["title"]
	if mock.VendorByID(body.Vendor) == nil {
		http.Error(w, "bad vendor", 400)
		return
	}
	sess := session(w, r)
	old, err := a.db.Run(r.Context(), id)
	if err == nil && old.Session != sess {
		http.Error(w, "not your run", 403)
		return
	}
	now := time.Now()
	run := store.Run{ID: id, Session: sess, Vendor: body.Vendor, PatientID: body.PatientID, Title: clip(body.Title, 120), Shared: old.Shared, Created: now, Updated: now}
	if err := a.db.SaveRun(r.Context(), run); err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	w.WriteHeader(204)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *runs) own(w http.ResponseWriter, r *http.Request) (store.Run, bool) {
	run, err := a.db.Run(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && run.Session != session(w, r)) {
		http.Error(w, "not found", 404)
		return run, false
	}
	if err != nil {
		http.Error(w, "storage unavailable", 500)
		return run, false
	}
	return run, true
}

func (a *runs) append(w http.ResponseWriter, r *http.Request) {
	run, ok := a.own(w, r)
	if !ok {
		return
	}
	var body struct {
		From   int               `json:"from"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil || len(body.Events) > 500 || body.From < 0 || body.From > 5000 {
		http.Error(w, "bad events", 400)
		return
	}
	if err := a.db.AppendEvents(r.Context(), run.ID, body.From, body.Events); err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	run.Updated = time.Now()
	a.db.SaveRun(r.Context(), run)
	w.WriteHeader(204)
}

func (a *runs) events(w http.ResponseWriter, r *http.Request) {
	run, err := a.db.Run(r.Context(), r.PathValue("id"))
	if err != nil || (!run.Shared && run.Session != session(w, r)) {
		http.Error(w, "not found", 404)
		return
	}
	evs, err := a.db.Events(r.Context(), run.ID)
	if err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	if evs == nil {
		evs = []store.Event{}
	}
	writeJSON(w, map[string]any{"run": run, "events": evs})
}

func (a *runs) share(w http.ResponseWriter, r *http.Request) {
	run, ok := a.own(w, r)
	if !ok {
		return
	}
	run.Shared = true
	run.Updated = time.Now()
	if err := a.db.SaveRun(r.Context(), run); err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	w.WriteHeader(204)
}
