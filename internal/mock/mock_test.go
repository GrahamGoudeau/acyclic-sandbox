package mock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/store"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/synth"
)

type fixture struct {
	t   *testing.T
	srv *httptest.Server
	s   *Server
}

func newFixture(t *testing.T) *fixture {
	mux := http.NewServeMux()
	f := &fixture{t: t}
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	s, err := New(context.Background(), f.srv.URL, synth.Build(time.Now()), store.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	s.Routes(mux)
	f.s = s
	return f
}

// token registers a secret-auth app directly and mints a token with scopes.
func (f *fixture) token(vendor string, scopes ...string) string {
	app, _, err := f.s.Auth.Register(context.Background(), vendor, "test", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.s.Auth.Issue(app, scopes, time.Hour)
}

func (f *fixture) get(path, tok string, hdr ...string) (int, map[string]any, http.Header) {
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/fhir+json")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var j map[string]any
	json.Unmarshal(b, &j)
	return res.StatusCode, j, res.Header
}

func patientID(v *Vendor, n string) string { return v.IDs("Patient", n) }

func TestEpicRules(t *testing.T) {
	f := newFixture(t)
	v := VendorByID("epic")
	tok := f.token("epic", "system/*.read")
	base := v.FHIRPath
	pid := patientID(v, "pt-01")

	code, body, _ := f.get(base+"/Observation?patient="+pid+"&category=laboratory&code=http://loinc.org|4548-4", tok)
	if code != 400 || !strings.Contains(mustJSON(body), "4112") {
		t.Fatalf("category+code: want 400/4112, got %d %s", code, mustJSON(body))
	}
	code, _, _ = f.get(base+"/Patient/"+pid, tok, "Accept", "")
	if code != 406 {
		t.Fatalf("no Accept: want 406, got %d", code)
	}
	code, body, _ = f.get(base+"/AllergyIntolerance?patient="+patientID(v, "pt-02"), tok)
	if code != 200 || !strings.Contains(mustJSON(body), "716186003") {
		t.Fatalf("NKA: want a no-known-allergy resource, got %s", mustJSON(body))
	}
	code, body, _ = f.get(base+"/Observation?patient="+pid+"&category=laboratory&_count=5", tok)
	next := linkNext(body)
	if code != 200 || !strings.Contains(next, "sessionID=16-") {
		t.Fatalf("paging: want sessionID next link, got %q", next)
	}
	u, _ := url.Parse(next)
	code, body, _ = f.get(u.Path+"?"+u.RawQuery, tok)
	if code != 200 || len(body["entry"].([]any)) != 5 {
		t.Fatalf("session page: %d %d", code, len(body["entry"].([]any)))
	}
	code, body, _ = f.get(base+"/DocumentReference?patient="+pid, tok)
	if code != 400 {
		t.Fatalf("DocumentReference without category or type: want 400, got %d", code)
	}
	code, _, _ = f.get(base+"/Patient/"+pid, "nope")
	if code != 401 {
		t.Fatalf("bad token: want 401, got %d", code)
	}
}

func TestOracleRules(t *testing.T) {
	f := newFixture(t)
	v := VendorByID("oracle")
	if ok, _ := v.validScope("system/*.read"); ok {
		t.Fatal("oracle must reject wildcard scopes")
	}
	tok := f.token("oracle", "system/Appointment.read", "system/Observation.read")
	pid := patientID(v, "pt-01")
	code, _, _ := f.get(v.FHIRPath+"/Appointment?patient="+pid, tok)
	if code != 400 {
		t.Fatalf("Appointment without date: want 400, got %d", code)
	}
	code, _, _ = f.get(v.FHIRPath+"/Appointment?patient="+pid+"&date=ge2026-01-01", tok)
	if code != 400 {
		t.Fatalf("Appointment date without time: want 400, got %d", code)
	}
	code, body, _ := f.get(v.FHIRPath+"/Appointment?patient="+pid+"&date=ge2020-01-01T00:00:00Z", tok)
	if code != 200 || len(body["entry"].([]any)) == 0 {
		t.Fatalf("Appointment with date: %d %s", code, mustJSON(body))
	}
	code, body, _ = f.get(v.FHIRPath+"/Observation?patient="+pid+"&_count=3", tok)
	if code != 200 || !strings.Contains(linkNext(body), "-pageContext=") || body["total"] != nil {
		t.Fatalf("oracle paging: %s", linkNext(body))
	}
	code, _, _ = f.get(v.FHIRPath+"/Condition?patient="+pid, tok)
	if code != 403 {
		t.Fatalf("missing scope: want 403, got %d", code)
	}
}

func TestAthenaRules(t *testing.T) {
	f := newFixture(t)
	v := VendorByID("athena")
	tok := f.token("athena", "system/Patient.read", "system/MedicationRequest.read", "athena/service/Athenanet.MDP.*")
	pid := patientID(v, "pt-01")
	code, _, _ := f.get(v.FHIRPath+"/MedicationRequest?patient="+pid, tok)
	if code != 400 {
		t.Fatalf("MedicationRequest without intent: want 400, got %d", code)
	}
	code, _, _ = f.get(v.FHIRPath+"/Patient?family=Diaz&given=Josefina", tok)
	if code != 400 {
		t.Fatalf("Patient search without ah-practice: want 400, got %d", code)
	}
	code, body, _ := f.get(v.FHIRPath+"/Patient?family=Diaz&given=Josefina&ah-practice=Organization/a-1.Practice-195900", tok)
	if code != 200 || len(body["entry"].([]any)) != 1 {
		t.Fatalf("Patient search: %d %s", code, mustJSON(body))
	}
	enterprise := strings.TrimPrefix(pid, "a-195900.E-")
	code, body, _ = f.get("/athena/v1/195900/patients/"+enterprise+"/appointments", tok, "Accept", "application/json")
	if code != 200 || len(body["appointments"].([]any)) == 0 {
		t.Fatalf("proprietary appointments: %d %s", code, mustJSON(body))
	}
	seen := map[string]bool{}
	for i := 1; i <= len(synth.PROFILES); i++ {
		id := patientID(v, "pt-"+pad(i))
		if seen[id] {
			t.Fatalf("athena patient id collision %s", id)
		}
		seen[id] = true
	}
}

func TestECWRules(t *testing.T) {
	f := newFixture(t)
	v := VendorByID("ecw")
	tok := f.token("ecw", "system/Appointment.read", "system/Observation.read")
	code, _, _ := f.get(v.FHIRPath+"/Appointment?patient="+patientID(v, "pt-01"), tok)
	if code != 404 {
		t.Fatalf("eCW has no Appointment API: want 404, got %d", code)
	}
	code, _, _ = f.get(v.FHIRPath+"/Observation?patient="+patientID(v, "pt-01")+"&category=laboratory&_count=5", tok)
	if code != 400 {
		t.Fatalf("eCW has no _count: want 400, got %d", code)
	}
	code, body, _ := f.get(v.FHIRPath+"/Observation?patient="+patientID(v, "pt-02")+"&category=laboratory", tok)
	if code != 200 || !strings.Contains(linkNext(body), "page=2") {
		t.Fatalf("eCW paging: %s", linkNext(body))
	}
}

func TestMeditechScopes(t *testing.T) {
	v := VendorByID("meditech")
	if ok, _ := v.validScope("system/Patient.read"); ok {
		t.Fatal("meditech accepts SMART v2 system scopes only")
	}
	if ok, _ := v.validScope("system/Patient.rs"); !ok {
		t.Fatal("meditech should accept system/Patient.rs")
	}
}

func TestRateLimitECW(t *testing.T) {
	f := newFixture(t)
	v := VendorByID("ecw")
	w := httptest.NewRecorder()
	for i := 0; i < v.Limit; i++ {
		if f.s.rateLimited(w, v, "c1") {
			t.Fatalf("limited early at %d", i)
		}
	}
	if !f.s.rateLimited(w, v, "c1") || w.Header().Get("Retry-After") == "" {
		t.Fatal("want 429 with Retry-After after 500 calls in a minute")
	}
}

func linkNext(b map[string]any) string {
	for _, l := range list(b["link"]) {
		if m := l.(map[string]any); m["relation"] == "next" {
			return str(m["url"])
		}
	}
	return ""
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func pad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
