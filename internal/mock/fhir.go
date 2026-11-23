package mock

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/store"
	"github.com/GrahamGoudeau/acyclic-sandbox/internal/synth"
)

// ---------- errors, in each vendor's shape ----------

// Epic's FHIR error codes (fhir.epic.com "FHIR Error Codes").
var epicCodes = map[string][2]string{
	"no-results":    {"4101", "Resource request returns no results."},
	"invalid-read":  {"4102", "The read resource request contained an invalid ID."},
	"missing-param": {"4111", "Required search parameter missing."},
	"combination":   {"4112", "Invalid parameter combination."},
	"session":       {"4113", "The search session has expired. Restart the search."},
	"bad-value":     {"4115", "Required search parameter has an invalid value."},
	"unknown-param": {"4122", "Unknown query parameter."},
	"element":       {"59108", "Required element missing."},
	"reference":     {"59144", "Reference not found."},
}

func issue(v *Vendor, severity, code, epicKey, diag string) R {
	is := R{"severity": severity, "code": code}
	switch v.OutcomeStyle {
	case "epic":
		if c, ok := epicCodes[epicKey]; ok {
			is["details"] = R{"coding": []any{R{"system": "urn:oid:1.2.840.114350.1.13.0.1.7.2.657369", "code": c[0], "display": c[1]}}, "text": c[1]}
		}
		if diag != "" {
			is["diagnostics"] = diag
		}
	case "athena":
		is["details"] = diag // athena sends details as a plain string
	default:
		is["diagnostics"] = diag
	}
	return is
}

func vOutcome(w http.ResponseWriter, v *Vendor, status int, code, epicKey, diag string) {
	writeJSON(w, status, "application/fhir+json; charset=utf-8", R{"resourceType": "OperationOutcome", "issue": []any{issue(v, "error", code, epicKey, diag)}})
}

func authFail(w http.ResponseWriter, v *Vendor, why string) {
	if v.EmptyAuthErr {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="The access token provided is not valid"`)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(401)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	if v.OutcomeStyle == "athena" {
		writeJSON(w, 401, "application/fhir+json", R{"resourceType": "OperationOutcome", "issue": []any{R{"severity": "error", "details": "Not Authorized", "code": "login"}}})
		return
	}
	vOutcome(w, v, 401, "login", "", why)
}

// ---------- token endpoint ----------

func (s *Server) token(w http.ResponseWriter, r *http.Request, v *Vendor) {
	fail := func(status int, code, desc string) {
		w.Header().Set("Cache-Control", "no-store")
		switch v.TokenShape {
		case "epic":
			writeJSON(w, 400, "application/json; charset=utf-8", R{"error": code, "error_description": nil})
		case "athena":
			msg := map[string]string{"invalid_client": "InvalidClient"}[code]
			if msg == "" {
				msg = code
			}
			writeJSON(w, status, "application/json; charset=utf-8", R{"error": msg, "detailedmessage": desc})
		default:
			writeJSON(w, status, "application/json", R{"error": code, "error_description": desc})
		}
	}
	if r.Method != http.MethodPost {
		fail(400, "invalid_request", "POST a form body")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		fail(400, "invalid_request", "send application/x-www-form-urlencoded in the body")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		fail(400, "unsupported_grant_type", "backend apps use grant_type=client_credentials")
		return
	}
	var app *App
	var err error
	if a := r.PostForm.Get("client_assertion"); a != "" {
		if !v.accepts("private_key_jwt") {
			fail(400, "invalid_client", "this token endpoint does not take client assertions")
			return
		}
		if r.PostForm.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
			fail(400, "invalid_request", "client_assertion_type must be urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
			return
		}
		app, err = s.Auth.VerifyAssertion(a, s.Public+v.TokenPath, v.ID)
	} else if id, secret, ok := r.BasicAuth(); ok {
		if !v.accepts("client_secret_basic") {
			fail(401, "invalid_client", "use a signed JWT client assertion")
			return
		}
		app, err = s.Auth.VerifySecret(id, secret, v.ID)
	} else {
		if r.PostForm.Get("client_secret") != "" {
			fail(401, "invalid_client", "send the client secret with HTTP Basic auth, not in the form body")
			return
		}
		fail(400, "invalid_request", "client authentication is required")
		return
	}
	if err != nil {
		oe, _ := err.(*OAuthError)
		if oe == nil {
			oe = &OAuthError{401, "invalid_client", "client authentication failed"}
		}
		status := oe.Status
		if oe.Code == "invalid_client" && v.TokenShape != "epic" {
			status = 401
		}
		fail(status, oe.Code, oe.Desc)
		return
	}
	requested := strings.Fields(r.PostForm.Get("scope"))
	if len(requested) == 0 {
		fail(400, "invalid_scope", "scope is required")
		return
	}
	var granted []string
	for _, sc := range requested {
		if ok, why := v.validScope(sc); ok {
			granted = append(granted, sc)
		} else {
			fail(400, "invalid_scope", why)
			return
		}
	}
	tok := s.Auth.Issue(app, granted, time.Duration(v.TokenTTL)*time.Second)
	w.Header().Set("Cache-Control", "no-store")
	switch v.TokenShape {
	case "epic":
		writeJSON(w, 200, "application/json; charset=utf-8", R{"access_token": tok, "token_type": "bearer", "expires_in": v.TokenTTL, "scope": strings.Join(granted, " ")})
	case "athena":
		// athena's documented 2-legged sample: expires_in as a string, no token_type.
		writeJSON(w, 200, "application/json; charset=utf-8", R{"access_token": tok, "expires_in": strconv.Itoa(v.TokenTTL)})
	default:
		writeJSON(w, 200, "application/json", R{"access_token": tok, "token_type": "Bearer", "expires_in": v.TokenTTL, "scope": strings.Join(granted, " ")})
	}
}

func (v *Vendor) validScope(sc string) (bool, string) {
	for _, x := range v.ExtraScopes {
		if sc == x {
			return true, ""
		}
	}
	if !strings.HasPrefix(sc, "system/") {
		return false, "backend apps request system/ scopes: " + sc
	}
	res, perm, ok := strings.Cut(strings.TrimPrefix(sc, "system/"), ".")
	if !ok {
		return false, "malformed scope " + sc
	}
	if res == "*" && !v.Wildcard {
		return false, v.Label + " does not support wildcard scopes; list each resource"
	}
	if res != "*" && searchParams[res] == nil && res != "Binary" {
		return false, "unsupported resource in scope " + sc
	}
	v1 := perm == "read" || perm == "write" || perm == "*"
	v2 := perm != "" && strings.Trim(perm, "cruds") == "" && !v1
	if v1 && v.Scopes == "v2" {
		return false, v.Label + " uses SMART v2 scopes (e.g. system/Patient.rs)"
	}
	if v2 && v.Scopes == "v1" {
		return false, v.Label + " uses SMART v1 scopes (e.g. system/Patient.read)"
	}
	if !v1 && !v2 {
		return false, "unknown permission in " + sc
	}
	return true, ""
}

// allowed reports whether scopes permit action ("read" or "create") on typ.
func allowed(scopes []string, typ, action string) bool {
	for _, sc := range scopes {
		if !strings.HasPrefix(sc, "system/") {
			continue
		}
		res, perm, _ := strings.Cut(strings.TrimPrefix(sc, "system/"), ".")
		if res != "*" && res != typ {
			continue
		}
		v1 := perm == "read" || perm == "write" || perm == "*"
		switch action {
		case "read":
			if perm == "read" || perm == "*" || (!v1 && strings.ContainsAny(perm, "rs")) {
				return true
			}
		case "create":
			if perm == "write" || perm == "*" || (!v1 && strings.Contains(perm, "c")) {
				return true
			}
		}
	}
	return false
}

// ---------- rate limits ----------

func (s *Server) rateLimited(w http.ResponseWriter, v *Vendor, client string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	key := v.ID + "/" + client
	if until, ok := s.blocked[key]; ok {
		if now.Before(until) {
			w.Header().Set("Retry-After", strconv.Itoa(int(until.Sub(now).Seconds())+1))
			return true
		}
		delete(s.blocked, key)
	}
	var start time.Time
	if v.FixedMinute {
		start = now.Truncate(time.Minute)
	} else {
		start = now.Add(-v.Window)
	}
	l := s.limiter[key][:0]
	for _, t := range s.limiter[key] {
		if !t.Before(start) {
			l = append(l, t)
		}
	}
	if len(l) >= v.Limit {
		s.limiter[key] = l
		if v.FixedMinute {
			s.blocked[key] = start.Add(time.Minute)
			w.Header().Set("Retry-After", strconv.Itoa(int(start.Add(time.Minute).Sub(now).Seconds())+1))
		} else {
			w.Header().Set("Retry-After", strconv.Itoa(int(v.Window.Seconds()+0.999)))
		}
		return true
	}
	s.limiter[key] = append(l, now)
	if v.ID == "athena" {
		w.Header().Set("X-RateLimit-Limit", "50000")
		w.Header().Set("X-RateLimit-Remaining", "49000")
	}
	return false
}

// ---------- dispatch ----------

func (s *Server) fhir(w http.ResponseWriter, r *http.Request, v *Vendor) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, v.FHIRPath), "/")
	switch rest {
	case ".well-known/smart-configuration":
		s.smartConfig(w, v)
		return
	case "metadata":
		s.metadata(w, v)
		return
	}
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		authFail(w, v, "missing bearer token")
		return
	}
	client, scopes, ok := s.Auth.Check(strings.TrimPrefix(authz, "Bearer "), v.ID)
	if !ok {
		authFail(w, v, "invalid or expired access token")
		return
	}
	if s.rateLimited(w, v, client) {
		vOutcome(w, v, 429, "throttled", "", "Too many requests. Honor Retry-After and back off.")
		return
	}
	if v.NeedsAccept != "" && r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/Binary/") {
		acc := r.Header.Get("Accept") + "," + r.URL.Query().Get("_format")
		if !strings.Contains(acc, "json") {
			w.WriteHeader(406)
			return
		}
	}
	if err := s.loadWrites(r.Context(), client); err != nil {
		vOutcome(w, v, 500, "exception", "", "storage unavailable")
		return
	}
	parts := strings.Split(rest, "/")
	typ := parts[0]
	if typ != "Binary" && (searchParams[typ] == nil || v.Required[typ] == nil) {
		vOutcome(w, v, 404, "not-supported", "", "resource type "+typ+" is not supported by "+v.Label)
		return
	}
	action := "read"
	if r.Method == http.MethodPost && len(parts) == 1 {
		action = "create"
	}
	if !allowed(scopes, typ, action) {
		if v.EmptyAuthErr {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", error_description="The access token provided is valid, but is not authorized for this service"`)
			w.WriteHeader(403)
			return
		}
		vOutcome(w, v, 403, "forbidden", "", "access token lacks a scope for "+action+" "+typ)
		return
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		s.search(w, r, v, client, typ)
	case r.Method == http.MethodGet && len(parts) == 2 && typ == "Binary":
		s.binary(w, r, v, parts[1])
	case r.Method == http.MethodGet && len(parts) == 2:
		s.read(w, v, client, typ, parts[1])
	case r.Method == http.MethodPost && len(parts) == 1:
		s.create(w, r, v, client, typ)
	default:
		vOutcome(w, v, 405, "not-supported", "", "method not supported")
	}
}

func (s *Server) smartConfig(w http.ResponseWriter, v *Vendor) {
	scopes := []string{}
	for t := range v.Required {
		if v.Scopes != "v2" {
			scopes = append(scopes, "system/"+t+".read")
		}
		if v.Scopes != "v1" {
			scopes = append(scopes, "system/"+t+".rs")
		}
	}
	scopes = append(scopes, v.ExtraScopes...)
	sort.Strings(scopes)
	writeJSON(w, 200, "application/json; charset=utf-8", R{
		"issuer":                                s.Public + strings.TrimSuffix(v.TokenPath, "/token"),
		"token_endpoint":                        s.Public + v.TokenPath,
		"grant_types_supported":                 []string{"client_credentials"},
		"token_endpoint_auth_methods_supported": v.Auth,
		"token_endpoint_auth_signing_alg_values_supported": []string{"RS384", "ES384"},
		"scopes_supported": scopes,
		"capabilities":     []string{"client-confidential-asymmetric", "client-confidential-symmetric", "permission-v1", "permission-v2"},
	})
}

func (s *Server) metadata(w http.ResponseWriter, v *Vendor) {
	var res []any
	types := make([]string, 0, len(v.Required))
	for t := range v.Required {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		var sp []any
		var names []string
		for p := range searchParams[t] {
			if !strings.HasPrefix(p, "_") || p == "_id" || p == "_count" {
				names = append(names, p)
			}
		}
		sort.Strings(names)
		for _, p := range names {
			sp = append(sp, R{"name": p, "type": "token"})
		}
		inter := []any{R{"code": "read"}, R{"code": "search-type"}}
		if v.Writes[t] {
			inter = append(inter, R{"code": "create"})
		}
		res = append(res, R{"type": t, "interaction": inter, "searchParam": sp})
	}
	res = append(res, R{"type": "Binary", "interaction": []any{R{"code": "read"}}})
	writeJSON(w, 200, "application/fhir+json; charset=utf-8", R{
		"resourceType": "CapabilityStatement", "status": "active", "kind": "instance", "fhirVersion": "4.0.1",
		"date":           synth.Anchor.Format("2006-01-02"),
		"software":       R{"name": v.Label + " compatible sandbox (Acyclic, not affiliated)", "version": "0.1.0"},
		"implementation": R{"description": v.Fidelity, "url": s.Public + v.FHIRPath},
		"format":         []string{"json"},
		"rest": []any{R{"mode": "server", "security": R{
			"cors":    true,
			"service": []any{R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/restful-security-service", "code": "SMART-on-FHIR"}}}},
			"extension": []any{R{"url": "http://fhir-registry.smarthealthit.org/StructureDefinition/oauth-uris", "extension": []any{
				R{"url": "token", "valueUri": s.Public + v.TokenPath},
			}}},
		}, "resource": res}},
	})
}

// ---------- read / binary ----------

func (s *Server) read(w http.ResponseWriter, v *Vendor, client, typ, vid string) {
	id, ok := s.canon(v, typ, vid)
	var res R
	if ok {
		res = s.get(client, typ, id)
	}
	if res == nil {
		vOutcome(w, v, 404, "not-found", "invalid-read", fmt.Sprintf("%s/%s was not found", typ, vid))
		return
	}
	w.Header().Set("ETag", `W/"1"`)
	writeJSON(w, 200, "application/fhir+json; charset=utf-8", s.render(v, res))
}

func (s *Server) binary(w http.ResponseWriter, r *http.Request, v *Vendor, vid string) {
	id, ok := s.canon(v, "Binary", vid)
	rtf := strings.HasSuffix(id, ".rtf")
	b, found := s.Data.Binaries[strings.TrimSuffix(id, ".rtf")]
	if !ok || !found {
		vOutcome(w, v, 404, "not-found", "reference", "Binary/"+vid+" was not found")
		return
	}
	ctype, data := b.ContentType, b.Data
	if v.HTMLNotes {
		ctype, data = "text/html", toHTML(b.Data)
		if rtf {
			ctype, data = "text/rtf", toRTF(b.Data)
		}
	}
	acc := r.Header.Get("Accept") + "," + r.URL.Query().Get("_format")
	if strings.Contains(acc, "fhir+json") || strings.Contains(acc, "json+fhir") || r.URL.Query().Get("_format") == "json" {
		writeJSON(w, 200, "application/fhir+json; charset=utf-8", R{"resourceType": "Binary", "id": vid, "contentType": ctype, "data": base64.StdEncoding.EncodeToString(data)})
		return
	}
	w.Header().Set("Content-Type", ctype+"; charset=utf-8")
	w.Write(data)
}

func toHTML(text []byte) []byte {
	var b strings.Builder
	b.WriteString("<html><head><title>Progress Note</title></head><body>")
	for _, para := range strings.Split(strings.TrimSpace(string(text)), "\n\n") {
		b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(para), "\n", "<br>") + "</p>")
	}
	b.WriteString("</body></html>")
	return []byte(b.String())
}

func toRTF(text []byte) []byte {
	esc := strings.NewReplacer(`\`, `\\`, "{", `\{`, "}", `\}`, "\n", `\par `).Replace(string(text))
	return []byte(`{\rtf1\ansi\deff0{\fonttbl{\f0 Arial;}}\f0\fs20 ` + esc + `}`)
}

// ---------- search ----------

func (s *Server) search(w http.ResponseWriter, r *http.Request, v *Vendor, client, typ string) {
	q := r.URL.Query()
	q.Del("_format")
	offset := 0
	var warnings []any
	base := s.Public + v.FHIRPath

	// Resume a page: each vendor's own next-link style.
	switch v.Paging {
	case "epic-session":
		if sid := q.Get("sessionID"); sid != "" {
			s.mu.Lock()
			sess, ok := s.sessions[sid]
			s.mu.Unlock()
			if !ok || time.Now().After(sess.expires) || sess.typ != typ {
				vOutcome(w, v, 400, "processing", "session", "paging session expired or unknown")
				return
			}
			q, offset = sess.query, sess.offset
		}
	case "oracle-context":
		offset = decodeOffset(q.Get("-pageContext"))
		q.Del("-pageContext")
		q.Del("-pageDirection")
	case "athena-cursor":
		offset = decodeOffset(q.Get("cursor"))
		q.Del("cursor")
	case "meditech-continue":
		offset = decodeOffset(q.Get("continue"))
		q.Del("continue")
	case "ecw-page":
		if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 {
			offset = (p - 1) * v.pageSize(typ)
		}
		q.Del("page")
	}

	for k, wants := range v.MustHave[typ] {
		if q.Get("_id") != "" {
			break
		}
		if q.Get(k) == "" {
			vOutcome(w, v, 400, "required", "missing-param", fmt.Sprintf("%s searches with a system scope need %s=%s", typ, k, wants))
			return
		}
		if q.Get(k) != wants {
			vOutcome(w, v, 403, "forbidden", "", "this app is not enabled for that practice")
			return
		}
	}
	q.Del("ah-practice")
	if v.NoCount && q.Get("_count") != "" {
		vOutcome(w, v, 400, "not-supported", "unknown-param", "_count is not supported; page with the next link")
		return
	}
	for k := range q {
		if k == "sessionID" {
			continue
		}
		if !searchParams[typ][k] || k == "_sort" {
			if v.IgnoreUnknownParams {
				warnings = append(warnings, R{"resource": R{"resourceType": "OperationOutcome", "issue": []any{issue(v, "warning", "processing", "unknown-param", "Unknown or unsupported parameter "+k+" was ignored.")}}, "search": R{"mode": "outcome"}})
				q.Del(k)
				continue
			}
			vOutcome(w, v, 400, "not-supported", "unknown-param", fmt.Sprintf("search parameter %q is not supported for %s", k, typ))
			return
		}
	}
	q.Del("sessionID")
	if sets := v.Required[typ]; len(sets) > 0 && !hasSet(q, sets) {
		vOutcome(w, v, 400, "required", "missing-param", fmt.Sprintf("%s search requires one of: %s", typ, describeSets(sets)))
		return
	}
	if pair, ok := v.Exclusive[typ]; ok && q.Get(pair[0]) != "" && q.Get(pair[1]) != "" {
		vOutcome(w, v, 400, "invalid", "combination", fmt.Sprintf("provide %s or %s, not both", pair[0], pair[1]))
		return
	}
	if v.ID == "oracle" && typ == "Observation" && q.Get("category") != "" {
		q.Del("code") // Oracle ignores code when category is given
	}
	if v.ID == "oracle" && typ == "Appointment" {
		for _, d := range q["date"] {
			if !strings.Contains(d, "T") {
				vOutcome(w, v, 400, "invalid", "", "Appointment date needs a prefix and a time, e.g. date=ge2026-01-01T00:00:00Z")
				return
			}
		}
	}

	// Translate vendor ids in reference parameters to canonical ids.
	cq := url.Values{}
	for k, vals := range q {
		cq[k] = append([]string(nil), vals...)
	}
	for _, k := range []string{"patient", "subject", "beneficiary", "actor", "_id", "encounter"} {
		vals := cq[k]
		for i, val := range vals {
			t := "Patient"
			if k == "_id" {
				t = typ
			} else if k == "encounter" {
				t = "Encounter"
			}
			vid := val
			if j := strings.LastIndex(val, "/"); j >= 0 {
				vid = val[j+1:]
			}
			if id, ok := s.canon(v, t, vid); ok {
				vals[i] = id
			} else if k == "patient" && v.OutcomeStyle == "epic" {
				vOutcome(w, v, 400, "invalid", "bad-value", "The patient parameter does not identify a patient.")
				return
			} else {
				vals[i] = "\x00unknown"
			}
		}
	}
	if v.ID == "meditech" && typ == "MedicationRequest" {
		// MEDITECH's status values are active, cancelled, completed.
		if st := cq.Get("status"); st != "" {
			cq.Set("status", strings.ReplaceAll(st, "completed", "completed,stopped"))
		}
	}
	count := v.pageSize(typ)
	if c, err := strconv.Atoi(q.Get("_count")); err == nil && c > 0 {
		count = c
		if v.MaxCount > 0 && count > v.MaxCount {
			count = v.MaxCount
		}
	}
	var hits []R
	for _, res := range s.all(client, typ) {
		if match(res, cq) {
			hits = append(hits, res)
		}
	}
	if typ == "Appointment" {
		sort.SliceStable(hits, func(i, j int) bool { return dateOf(hits[i]) < dateOf(hits[j]) })
	} else if typ != "Patient" {
		sortDesc(hits)
	}
	if v.NKA && typ == "AllergyIntolerance" && len(hits) == 0 && cq.Get("patient") != "\x00unknown" {
		hits = []R{nka(cq.Get("patient"))}
	}
	start := min(offset, len(hits))
	end := min(start+count, len(hits))
	entries := []any{}
	for _, res := range hits[start:end] {
		out := s.render(v, res)
		e := R{"fullUrl": base + "/" + typ + "/" + str(out["id"]), "resource": out, "search": R{"mode": "match"}}
		if v.ID == "epic" {
			e["link"] = []any{R{"relation": "self", "url": e["fullUrl"]}}
		}
		entries = append(entries, e)
	}
	entries = append(entries, warnings...)
	if v.ID == "epic" && len(hits) == 0 {
		entries = append(entries, R{"resource": R{"resourceType": "OperationOutcome", "issue": []any{issue(v, "warning", "processing", "no-results", "")}}, "search": R{"mode": "outcome"}})
	}
	links := []any{R{"relation": "self", "url": base + "/" + typ + "?" + q.Encode()}}
	if end < len(hits) {
		links = append(links, R{"relation": "next", "url": s.nextURL(v, typ, base, q, end)})
	}
	bundle := R{"resourceType": "Bundle", "type": "searchset", "link": links, "entry": entries}
	if v.ID == "oracle" {
		bundle["id"] = uuidish()
	} else {
		bundle["total"] = len(hits)
	}
	writeJSON(w, 200, "application/fhir+json; charset=utf-8", bundle)
}

func (s *Server) nextURL(v *Vendor, typ, base string, q url.Values, offset int) string {
	n := url.Values{}
	for k, vals := range q {
		n[k] = vals
	}
	tok := base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(offset)))
	switch v.Paging {
	case "epic-session":
		sid := fmt.Sprintf("16-%s", strings.ToUpper(randHex(16)))
		s.mu.Lock()
		s.sessions[sid] = pageSession{query: q, typ: typ, offset: offset, expires: time.Now().Add(10 * time.Minute)}
		for k, ss := range s.sessions {
			if time.Now().After(ss.expires) {
				delete(s.sessions, k)
			}
		}
		s.mu.Unlock()
		return base + "/" + typ + "?sessionID=" + sid
	case "oracle-context":
		n.Set("-pageContext", tok)
		n.Set("-pageDirection", "NEXT")
	case "athena-cursor":
		n.Set("cursor", tok)
	case "meditech-continue":
		n.Set("continue", tok)
	case "ecw-page":
		n.Set("page", strconv.Itoa(offset/v.pageSize(typ)+1))
	}
	return base + "/" + typ + "?" + n.Encode()
}

func decodeOffset(tok string) int {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) < 2 || b[0] != 'o' {
		return 0
	}
	n, _ := strconv.Atoi(string(b[1:]))
	return max(n, 0)
}

func uuidish() string {
	h := randHex(16)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Epic returns a "no known allergies" resource rather than an empty bundle.
func nka(pid string) R {
	return R{
		"resourceType": "AllergyIntolerance", "id": "nka-" + pid,
		"clinicalStatus":     R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", "code": "active"}}},
		"verificationStatus": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-verification", "code": "confirmed"}}},
		"code":               R{"coding": []any{R{"system": "http://snomed.info/sct", "code": "716186003", "display": "No known allergy"}}, "text": "No Known Allergies"},
		"patient":            R{"reference": "Patient/" + pid},
	}
}

func hasSet(q url.Values, sets [][]string) bool {
	for _, set := range sets {
		ok := true
		for _, k := range set {
			if q.Get(k) == "" {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func describeSets(sets [][]string) string {
	var parts []string
	for _, s := range sets {
		parts = append(parts, strings.Join(s, "+"))
	}
	return strings.Join(parts, ", ")
}

// ---------- create (DocumentReference notes) ----------

var epicNoteTypes = map[string]bool{"18842-5": true, "11488-4": true, "34117-2": true, "11506-3": true, "28570-0": true, "34111-5": true, "34746-8": true}

func (s *Server) create(w http.ResponseWriter, r *http.Request, v *Vendor, client, typ string) {
	if !v.Writes[typ] {
		vOutcome(w, v, 405, "not-supported", "", v.Label+" does not support creating "+typ+" for backend apps")
		return
	}
	var res R
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&res); err != nil {
		vOutcome(w, v, 400, "structure", "", "body must be a FHIR JSON resource")
		return
	}
	bad := func(code, epicKey, msg string) { vOutcome(w, v, 422, code, epicKey, msg) }
	if str(res["resourceType"]) != typ {
		bad("structure", "", "resourceType does not match the URL")
		return
	}
	if res["id"] != nil {
		bad("invariant", "", "do not send an id on create")
		return
	}
	subj, _ := res["subject"].(R)
	vid := strings.TrimPrefix(str(subj["reference"]), "Patient/")
	pid, ok := s.canon(v, "Patient", vid)
	if !ok || s.Data.ByID["Patient/"+pid] == nil {
		bad("processing", "reference", "subject must reference an existing Patient")
		return
	}
	content := list(res["content"])
	if len(content) == 0 {
		bad("required", "element", "content with an attachment is required")
		return
	}
	att, _ := content[0].(R)["attachment"].(R)
	ct := str(att["contentType"])
	if att == nil || str(att["data"]) == "" {
		bad("required", "element", "content[0].attachment.data (base64) is required")
		return
	}
	if _, err := base64.StdEncoding.DecodeString(str(att["data"])); err != nil {
		bad("value", "", "attachment.data is not valid base64")
		return
	}
	typeCC, _ := res["type"].(R)
	if typeCC == nil {
		bad("required", "element", "type is required")
		return
	}
	loinc := ""
	for _, c := range list(typeCC["coding"]) {
		if str(c.(R)["system"]) == "http://loinc.org" {
			loinc = str(c.(R)["code"])
		}
	}
	ctx, _ := res["context"].(R)
	switch v.NoteRules {
	case "epic":
		if ct != "text/plain" {
			bad("value", "", "Epic stores clinical notes from attachment contentType text/plain only")
			return
		}
		if !epicNoteTypes[loinc] {
			bad("value", "", "type must be one of LOINC 18842-5, 11488-4, 34117-2, 11506-3, 28570-0, 34111-5, 34746-8")
			return
		}
		encs := list(ctx["encounter"])
		if len(encs) == 0 {
			bad("required", "element", "context.encounter is required: the note must belong to an encounter of the subject")
			return
		}
		evid := strings.TrimPrefix(str(encs[0].(R)["reference"]), "Encounter/")
		eid, ok := s.canon(v, "Encounter", evid)
		if enc := s.Data.ByID["Encounter/"+eid]; !ok || enc == nil || patientOf(enc) != pid {
			bad("processing", "reference", "context.encounter must be an encounter of the subject")
			return
		}
		if ds := str(res["docStatus"]); ds != "" && ds != "final" && ds != "preliminary" {
			bad("value", "", "docStatus must be preliminary or final")
			return
		}
		res["context"] = R{"encounter": []any{R{"reference": "Encounter/" + eid}}}
	case "oracle":
		if str(res["status"]) != "current" {
			bad("value", "", "status must be current")
			return
		}
		if ds := str(res["docStatus"]); ds != "final" && ds != "amended" {
			bad("required", "", "docStatus must be final (or amended for system access)")
			return
		}
		if loinc == "" {
			bad("value", "", "type needs a LOINC coding")
			return
		}
		if !strings.Contains(strings.ReplaceAll(ct, " ", ""), ";charset=") {
			bad("value", "", "contentType needs a charset, e.g. text/plain;charset=utf-8")
			return
		}
		period, _ := ctx["period"].(R)
		if end := str(period["end"]); end == "" || !strings.Contains(end, "T") {
			bad("required", "", "context.period.end is required and needs a time")
			return
		}
		if len(content) != 1 {
			bad("value", "", "exactly one content entry is allowed")
			return
		}
	}
	if !strings.HasPrefix(ct, "text/") {
		bad("value", "", "this sandbox stores text notes only")
		return
	}
	res["status"] = "current"
	if str(res["docStatus"]) == "" {
		res["docStatus"] = "final"
	}
	id := "w-" + randHex(8)
	res["id"] = id
	res["subject"] = R{"reference": "Patient/" + pid}
	if str(res["date"]) == "" {
		res["date"] = time.Now().UTC().Format(time.RFC3339)
	}
	res["meta"] = R{"versionId": "1", "lastUpdated": time.Now().UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(res)
	if err := s.DB.AddWrite(r.Context(), store.Write{ClientID: client, ResourceType: typ, ResourceID: id, Resource: raw, Created: time.Now()}); err != nil {
		log.Printf("write failed: %v", err)
		vOutcome(w, v, 500, "exception", "", "could not store the resource")
		return
	}
	s.mu.Lock()
	s.writes[client] = append(s.writes[client], res)
	s.toCanon[v.ID][typ+"/"+v.IDs(typ, id)] = id
	s.mu.Unlock()
	out := s.render(v, res)
	w.Header().Set("ETag", `W/"1"`)
	switch v.NoteRules {
	case "epic":
		// Relative Location; a body only with Prefer: return=representation.
		w.Header().Set("Location", typ+"/"+str(out["id"]))
		if strings.Contains(r.Header.Get("Prefer"), "return=representation") {
			writeJSON(w, 201, "application/json; charset=utf-8", out)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(201)
	default:
		// Oracle: absolute Location, empty body.
		w.Header().Set("Location", s.Public+v.FHIRPath+"/"+typ+"/"+str(out["id"]))
		w.Header().Set("Content-Type", "application/fhir+json")
		w.WriteHeader(201)
	}
}
