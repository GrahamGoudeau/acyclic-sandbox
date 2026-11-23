package mock

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/synth"
)

type R = synth.R

// Search parameters the engine understands, per resource type. Anything not
// listed is rejected by the vendor layer the way that vendor would.
var searchParams = map[string]map[string]bool{
	"Patient":            set("_id", "identifier", "family", "given", "birthdate", "gender", "name"),
	"Condition":          set("patient", "subject", "category", "clinical-status", "code", "onset-date", "encounter", "_id"),
	"MedicationRequest":  set("patient", "subject", "status", "intent", "authoredon", "_id"),
	"AllergyIntolerance": set("patient", "clinical-status", "_id"),
	"Observation":        set("patient", "subject", "category", "code", "date", "_id"),
	"Encounter":          set("patient", "subject", "date", "status", "class", "_id"),
	"Appointment":        set("patient", "actor", "date", "status", "service-category", "_id"),
	"DocumentReference":  set("patient", "subject", "category", "type", "date", "_id"),
	"Coverage":           set("patient", "beneficiary", "_id"),
}

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	m["_count"] = true
	return m
}

// match reports whether resource r satisfies every query parameter. q holds
// canonical ids (the vendor layer has already translated them).
func match(r R, q url.Values) bool {
	for key, vals := range q {
		if strings.HasPrefix(key, "_") && key != "_id" {
			continue
		}
		for _, v := range vals {
			if !matchOne(r, key, v) {
				return false
			}
		}
	}
	return true
}

func matchOne(r R, key, v string) bool {
	switch key {
	case "_id":
		return anyEq(strings.Split(v, ","), str(r["id"]))
	case "patient", "subject", "beneficiary", "actor":
		id := strings.TrimPrefix(v, "Patient/")
		return patientOf(r) == id
	case "identifier":
		val := v
		if i := strings.LastIndex(v, "|"); i >= 0 {
			val = v[i+1:]
		}
		for _, x := range list(r["identifier"]) {
			if strings.EqualFold(str(x.(R)["value"]), val) {
				return true
			}
		}
		return false
	case "family", "given", "name":
		for _, x := range list(r["name"]) {
			n := x.(R)
			var cands []string
			if key != "given" {
				cands = append(cands, str(n["family"]))
			}
			if key != "family" {
				for _, g := range list(n["given"]) {
					cands = append(cands, str(g))
				}
			}
			for _, c := range cands {
				if strings.HasPrefix(strings.ToLower(c), strings.ToLower(v)) {
					return true
				}
			}
		}
		return false
	case "birthdate":
		return str(r["birthDate"]) == v
	case "gender":
		return str(r["gender"]) == v
	case "status":
		return anyEq(strings.Split(v, ","), str(r["status"]))
	case "intent":
		return anyEq(strings.Split(v, ","), str(r["intent"]))
	case "service-category":
		return v == "appointment"
	case "encounter":
		e, _ := r["encounter"].(R)
		if e == nil {
			if c, ok := r["context"].(R); ok {
				if l := list(c["encounter"]); len(l) > 0 {
					e = l[0].(R)
				}
			}
		}
		return e != nil && strings.TrimPrefix(str(e["reference"]), "Encounter/") == v
	case "clinical-status":
		return anyEq(strings.Split(v, ","), firstCode(r["clinicalStatus"]))
	case "category":
		for _, c := range strings.Split(v, ",") {
			c = afterBar(c)
			for _, cat := range list(r["category"]) {
				if s, ok := cat.(string); ok && s == c {
					return true
				}
				if cm, ok := cat.(R); ok && hasCode(cm, c) {
					return true
				}
			}
		}
		return false
	case "class":
		cl, _ := r["class"].(R)
		return cl != nil && str(cl["code"]) == afterBar(v)
	case "code", "type":
		field := r["code"]
		if key == "type" {
			field = r["type"]
		}
		cm, _ := field.(R)
		for _, c := range strings.Split(v, ",") {
			sys, code := "", c
			if i := strings.LastIndex(c, "|"); i >= 0 {
				sys, code = c[:i], c[i+1:]
			}
			for _, x := range list(cm["coding"]) {
				co := x.(R)
				if str(co["code"]) == code && (sys == "" || str(co["system"]) == sys) {
					return true
				}
			}
		}
		return false
	case "date", "onset-date", "authoredon":
		return matchDate(dateOf(r), v)
	}
	return true
}

// dateOf picks the date a resource is searched and sorted by.
func dateOf(r R) string {
	for _, k := range []string{"effectiveDateTime", "date", "start", "onsetDateTime", "authoredOn", "issued", "recordedDate"} {
		if s := str(r[k]); s != "" {
			return s
		}
	}
	if p, ok := r["period"].(R); ok {
		return str(p["start"])
	}
	if p, ok := r["effectivePeriod"].(R); ok {
		return str(p["start"])
	}
	return ""
}

// FHIR date prefixes on the date part only, which is what clients send.
func matchDate(have, want string) bool {
	if have == "" {
		return false
	}
	prefix := "eq"
	if len(want) > 2 && strings.ContainsAny(want[:1], "glne") && !strings.ContainsAny(want[:2], "0123456789") {
		prefix, want = want[:2], want[2:]
	}
	n := len(want)
	if n > len(have) {
		n = len(have)
	}
	h := have[:n]
	switch prefix {
	case "ge", "sa":
		return h >= want
	case "gt":
		return h > want
	case "le", "eb":
		return h <= want
	case "lt":
		return h < want
	case "ne":
		return h != want
	default:
		return h == want
	}
}

func sortDesc(rs []R) {
	sort.SliceStable(rs, func(i, j int) bool { return dateOf(rs[i]) > dateOf(rs[j]) })
}

func patientOf(r R) string {
	if str(r["resourceType"]) == "Patient" {
		return str(r["id"])
	}
	for _, k := range []string{"subject", "patient", "beneficiary"} {
		if m, ok := r[k].(R); ok {
			return strings.TrimPrefix(str(m["reference"]), "Patient/")
		}
	}
	for _, p := range list(r["participant"]) {
		if a, ok := p.(R)["actor"].(R); ok && strings.HasPrefix(str(a["reference"]), "Patient/") {
			return strings.TrimPrefix(str(a["reference"]), "Patient/")
		}
	}
	return ""
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func firstCode(v any) string {
	m, _ := v.(R)
	for _, c := range list(m["coding"]) {
		if s := str(c.(R)["code"]); s != "" {
			return s
		}
	}
	return ""
}

func hasCode(m R, code string) bool {
	for _, c := range list(m["coding"]) {
		if str(c.(R)["code"]) == code {
			return true
		}
	}
	return false
}

func anyEq(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func afterBar(s string) string {
	if i := strings.LastIndex(s, "|"); i >= 0 {
		return s[i+1:]
	}
	return s
}
