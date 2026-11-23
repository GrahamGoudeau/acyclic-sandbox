// Package mock serves vendor-shaped FHIR R4 APIs over the synthetic data
// set. Each self-registered app is a tenant: it sees the shared synthetic
// charts plus only its own writes.
package mock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/store"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/synth"
)

type Server struct {
	Public string // public origin of the mock, e.g. https://sandbox.acyclic.sh
	Data   *synth.Store
	Auth   *Auth
	DB     store.Store

	mu       sync.Mutex
	toCanon  map[string]map[string]string // vendor -> "Type/vendorID" -> canonical id
	writes   map[string][]R               // client id -> written resources (canonical ids)
	loaded   map[string]bool
	limiter  map[string][]time.Time
	blocked  map[string]time.Time  // eCW-style: client blocked until this time
	sessions map[string]pageSession // Epic sessionID paging
}

type pageSession struct {
	query   url.Values
	typ     string
	offset  int
	expires time.Time
}

func New(ctx context.Context, public string, data *synth.Store, db store.Store) (*Server, error) {
	auth, err := NewAuth(ctx, db)
	if err != nil {
		return nil, err
	}
	s := &Server{Public: strings.TrimRight(public, "/"), Data: data, Auth: auth, DB: db,
		toCanon: map[string]map[string]string{}, writes: map[string][]R{}, loaded: map[string]bool{}, limiter: map[string][]time.Time{}, blocked: map[string]time.Time{}, sessions: map[string]pageSession{}}
	for _, v := range Vendors {
		m := map[string]string{}
		for key := range data.ByID {
			t, id, _ := strings.Cut(key, "/")
			m[t+"/"+v.IDs(t, id)] = id
		}
		for id := range data.Binaries {
			m["Binary/"+v.IDs("Binary", id)] = id
			m["Binary/"+v.IDs("Binary", id+".rtf")] = id + ".rtf"
		}
		s.toCanon[v.ID] = m
	}
	return s, nil
}

func (s *Server) Routes(mux *http.ServeMux) {
	for _, v := range Vendors {
		v := v
		mux.HandleFunc(v.TokenPath, s.cors(func(w http.ResponseWriter, r *http.Request) { s.token(w, r, v) }))
		mux.HandleFunc(v.FHIRPath+"/", s.cors(func(w http.ResponseWriter, r *http.Request) { s.fhir(w, r, v) }))
	}
	mux.HandleFunc("/athena/v1/", s.cors(s.athenaProprietary))
	mux.HandleFunc("/register", s.cors(s.register))
	mux.HandleFunc("/api/patients", s.cors(s.patients))
	mux.HandleFunc("/api/chart", s.cors(s.chart))
}

func (s *Server) cors(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Prefer")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Location, Retry-After, ETag, X-RateLimit-Limit, X-RateLimit-Remaining")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		h(w, r)
	}
}



func writeJSON(w http.ResponseWriter, status int, ctype string, v any) {
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// ---------- registration ----------

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a JSON body: {\"vendor\":\"epic\",\"jwks\":{\"keys\":[...]}}", 405)
		return
	}
	var body struct {
		Vendor string          `json:"vendor"`
		Name   string          `json:"name"`
		JWKS   json.RawMessage `json:"jwks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, "application/json", R{"error": "bad JSON"})
		return
	}
	v := VendorByID(body.Vendor)
	if v == nil {
		writeJSON(w, 400, "application/json", R{"error": "vendor must be one of epic, oracle, athena, meditech, ecw"})
		return
	}
	var keys []json.RawMessage
	if len(body.JWKS) > 0 && string(body.JWKS) != "null" {
		var set struct{ Keys []json.RawMessage }
		if json.Unmarshal(body.JWKS, &set) != nil || len(set.Keys) == 0 {
			if json.Unmarshal(body.JWKS, &keys) != nil {
				writeJSON(w, 400, "application/json", R{"error": "jwks must be a JWK Set: {\"keys\":[...]}"})
				return
			}
		} else {
			keys = set.Keys
		}
	}
	method := "client_secret_basic"
	if len(keys) > 0 {
		method = "private_key_jwt"
	}
	if !v.accepts(method) {
		if method == "client_secret_basic" {
			writeJSON(w, 400, "application/json", R{"error": v.Label + " backend apps use a signed JWT: register a public key in jwks"})
		} else {
			writeJSON(w, 400, "application/json", R{"error": v.Label + " does not accept private_key_jwt here"})
		}
		return
	}
	name := body.Name
	if len(name) > 80 {
		name = name[:80]
	}
	app, secret, err := s.Auth.Register(r.Context(), v.ID, name, keys)
	if err != nil {
		writeJSON(w, 400, "application/json", R{"error": err.Error()})
		return
	}
	out := R{
		"client_id": app.ClientID, "vendor": v.ID, "auth": method,
		"fhir_base_url": s.Public + v.FHIRPath, "token_url": s.Public + v.TokenPath,
		"expires": app.Created.Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}
	if secret != "" {
		out["client_secret"] = secret
	}
	if v.PracticeID != "" {
		out["practice_id"] = v.PracticeID
	}
	writeJSON(w, 201, "application/json", out)
}










// ---------- tenant data ----------

func (s *Server) loadWrites(ctx context.Context, client string) error {
	s.mu.Lock()
	done := s.loaded[client]
	s.mu.Unlock()
	if done {
		return nil
	}
	ws, err := s.DB.Writes(ctx, client)
	if err != nil {
		return err
	}
	var rs []R
	for _, wr := range ws {
		var res R
		if json.Unmarshal(wr.Resource, &res) == nil {
			rs = append(rs, res)
		}
	}
	s.mu.Lock()
	s.writes[client] = rs
	s.loaded[client] = true
	app := s.Auth.App(client)
	if app != nil {
		v := VendorByID(app.Vendor)
		for _, res := range rs {
			t, id := str(res["resourceType"]), str(res["id"])
			s.toCanon[v.ID][t+"/"+v.IDs(t, id)] = id
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) all(client, typ string) []R {
	base := s.Data.ByType[typ]
	s.mu.Lock()
	defer s.mu.Unlock()
	var extra []R
	for _, r := range s.writes[client] {
		if str(r["resourceType"]) == typ {
			extra = append(extra, r)
		}
	}
	if len(extra) == 0 {
		return base
	}
	return append(append([]R(nil), base...), extra...)
}

func (s *Server) get(client, typ, id string) R {
	if r := s.Data.ByID[typ+"/"+id]; r != nil {
		return r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.writes[client] {
		if str(r["resourceType"]) == typ && str(r["id"]) == id {
			return r
		}
	}
	return nil
}

func (s *Server) canon(v *Vendor, typ, vid string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.toCanon[v.ID][typ+"/"+vid]
	return id, ok
}

// render deep-copies r with every id and reference in v's format.
func (s *Server) render(v *Vendor, r R) R {
	out := rewrite(v, r).(R)
	switch str(out["resourceType"]) {
	case "MedicationRequest":
		// MEDITECH documents only active, cancelled and completed.
		if v.ID == "meditech" && str(out["status"]) == "stopped" {
			out["status"] = "completed"
		}
	case "DocumentReference":
		if v.HTMLNotes {
			// Epic links one Binary per format: text/html and text/rtf.
			for _, c := range list(out["content"]) {
				att := c.(R)["attachment"].(R)
				if strings.HasPrefix(str(att["url"]), "Binary/") && str(att["contentType"]) == "text/plain" {
					canonBin := str(r["content"].([]any)[0].(R)["attachment"].(R)["url"])[len("Binary/"):]
					att["contentType"] = "text/html"
					out["content"] = []any{c, R{"attachment": R{"contentType": "text/rtf", "url": "Binary/" + v.IDs("Binary", canonBin+".rtf"), "title": att["title"]}, "format": c.(R)["format"]}}
					break
				}
			}
		}
	}
	return out
}

func rewrite(v *Vendor, x any) any {
	switch t := x.(type) {
	case R:
		o := R{}
		for k, val := range t {
			switch {
			case k == "id" && t["resourceType"] != nil:
				o[k] = v.IDs(str(t["resourceType"]), str(val))
			case (k == "reference" || k == "url") && isRef(str(val)):
				typ, id, _ := strings.Cut(str(val), "/")
				o[k] = typ + "/" + v.IDs(typ, id)
			default:
				o[k] = rewrite(v, val)
			}
		}
		return o
	case []any:
		o := make([]any, len(t))
		for i, e := range t {
			o[i] = rewrite(v, e)
		}
		return o
	}
	return x
}

func isRef(s string) bool {
	t, id, ok := strings.Cut(s, "/")
	return ok && id != "" && !strings.Contains(id, "/") && len(t) > 0 && t[0] >= 'A' && t[0] <= 'Z' && !strings.Contains(s, ":")
}








// ---------- chart viewer API (no auth: synthetic data, tenant by client id) ----------

func (s *Server) patients(w http.ResponseWriter, r *http.Request) {
	v := VendorByID(r.URL.Query().Get("vendor"))
	if v == nil {
		http.Error(w, "unknown vendor", 400)
		return
	}
	var out []R
	for i, p := range s.Data.Patients {
		prof := synth.PROFILES[i]
		var probs []string
		for _, c := range prof.Conds()[:min(2, len(prof.Conds()))] {
			probs = append(probs, c)
		}
		out = append(out, R{"id": v.IDs("Patient", str(p["id"])), "name": prof.Name(), "birth_date": str(p["birthDate"]), "summary": strings.Join(probs, ", ")})
	}
	writeJSON(w, 200, "application/json", out)
}

func (s *Server) chart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := VendorByID(q.Get("vendor"))
	if v == nil {
		http.Error(w, "unknown vendor", 400)
		return
	}
	client := q.Get("client_id")
	if app := s.Auth.App(client); app == nil || app.Vendor != v.ID {
		client = ""
	} else if err := s.loadWrites(r.Context(), client); err != nil {
		http.Error(w, "storage unavailable", 500)
		return
	}
	pid, ok := s.canon(v, "Patient", q.Get("patient"))
	if !ok {
		http.Error(w, "unknown patient", 404)
		return
	}
	pick := func(typ string, keep func(R) bool, max int) []any {
		var hits []R
		for _, res := range s.all(client, typ) {
			if patientOf(res) == pid && (keep == nil || keep(res)) {
				hits = append(hits, res)
			}
		}
		sortDesc(hits)
		var out []any
		for i, res := range hits {
			if max > 0 && i >= max {
				break
			}
			out = append(out, s.render(v, res))
		}
		if out == nil {
			out = []any{}
		}
		return out
	}
	cat := func(c string) func(R) bool { return func(r R) bool { return matchOne(r, "category", c) } }
	var written []string
	s.mu.Lock()
	for _, res := range s.writes[client] {
		if patientOf(res) == pid {
			written = append(written, v.IDs(str(res["resourceType"]), str(res["id"])))
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, "application/json", R{
		"patient":      s.render(v, s.Data.ByID["Patient/"+pid]),
		"conditions":   pick("Condition", func(r R) bool { return firstCode(r["clinicalStatus"]) == "active" }, 0),
		"medications":  pick("MedicationRequest", func(r R) bool { return str(r["status"]) == "active" }, 0),
		"allergies":    pick("AllergyIntolerance", nil, 0),
		"labs":         pick("Observation", cat("laboratory"), 30),
		"vitals":       pick("Observation", cat("vital-signs"), 12),
		"documents":    pick("DocumentReference", nil, 10),
		"appointments": pick("Appointment", func(r R) bool { return str(r["status"]) == "booked" }, 0),
		"coverage":     pick("Coverage", nil, 0),
		"written":      written,
	})
}

// Purge drops expired registrations and their writes from memory.
func (s *Server) Purge(ttl time.Duration) {
	gone := s.Auth.Expire(ttl)
	s.mu.Lock()
	for _, id := range gone {
		delete(s.writes, id)
		delete(s.loaded, id)
		delete(s.limiter, id)
	}
	s.mu.Unlock()
}
