package mock

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// Vendor describes how one EHR's FHIR R4 API behaves on the wire. Every
// behavior here comes from public developer documentation; the sources and
// what is confirmed versus assumed are in acyclic-sdk-spec/vendors.md.
type Vendor struct {
	ID, Label string
	FHIRPath  string // mirrors the real base URL's path
	TokenPath string
	// Auth methods the token endpoint accepts; Auth[0] is the default the
	// playground registers with.
	Auth []string
	// SMART scope grammar: "v1" (.read/.write), "v2" (.rs/.c) or "both".
	Scopes   string
	Wildcard bool // system/*.read allowed
	// Extra non-FHIR scopes granted when requested (athena's proprietary API).
	ExtraScopes []string

	TokenTTL int
	// TokenShape names the token response quirk: "standard", "epic", "athena".
	TokenShape string

	PageSize map[string]int // per type; "" is the default
	MaxCount int
	NoCount  bool // the vendor has no _count parameter (eCW)
	// Paging names the next-link style: "epic-session", "oracle-context",
	// "athena-cursor", "meditech-continue", "ecw-page".
	Paging string

	// Required search parameter sets per type: one full set must be present.
	Required map[string][][]string
	// Forbidden parameter combinations per type (Epic: category with code).
	Exclusive map[string][2]string
	// Values a parameter must have when present ("intent": "order").
	IgnoreUnknownParams bool // Epic answers unknown params with a warning instead of 400
	NeedsAccept         string // "406": no JSON Accept gets 406 (Epic defaults to XML, Oracle requires Accept)

	Writes     map[string]bool
	NoteRules  string // "epic" | "oracle" | ""
	NKA        bool   // returns a "no known allergies" resource instead of an empty bundle
	HTMLNotes  bool   // notes are served as text/html (+ text/rtf) Binaries
	PracticeID string
	MustHave   map[string]map[string]string // type -> param -> required value

	// Rate limit: Limit requests per Window; FixedMinute blocks until the
	// minute ends (eCW).
	Limit       int
	Window      time.Duration
	FixedMinute bool

	// OutcomeStyle: "epic" (Epic error codes in details.coding), "athena"
	// (details as a plain string, as athena's live 401 sends), "standard".
	OutcomeStyle string
	EmptyAuthErr bool // 401 with an empty body (Epic)

	Fidelity string
	CORSNote string
	IDs      func(resourceType, canonical string) string
}

func h64(parts ...string) uint64 {
	s := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return binary.BigEndian.Uint64(s[:8])
}

func b64(parts ...string) string {
	s := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

var patientOrSubject = [][]string{{"patient"}, {"subject"}, {"_id"}}
var patientOrID = [][]string{{"patient"}, {"_id"}}

var Vendors = []*Vendor{
	{
		ID: "epic", Label: "Epic",
		FHIRPath: "/epic/interconnect-fhir-oauth/api/FHIR/R4", TokenPath: "/epic/interconnect-fhir-oauth/oauth2/token",
		Auth: []string{"private_key_jwt"}, Scopes: "both", Wildcard: true,
		TokenTTL: 3600, TokenShape: "epic",
		PageSize: map[string]int{"": 50}, MaxCount: 999, Paging: "epic-session",
		Required: map[string][][]string{
			"Patient":            {{"_id"}, {"identifier"}, {"given", "family", "birthdate"}},
			"Condition":          patientOrSubject,
			"MedicationRequest":  patientOrSubject,
			"AllergyIntolerance": {{"patient"}, {"_id"}},
			"Observation":        {{"patient", "category"}, {"patient", "code"}, {"subject", "category"}, {"subject", "code"}, {"_id"}},
			"Encounter":          patientOrSubject,
			"Appointment":        {{"patient"}, {"_id"}},
			"DocumentReference":  {{"patient", "category"}, {"patient", "type"}, {"subject", "category"}, {"subject", "type"}, {"_id"}},
			"Coverage":           {{"patient"}, {"beneficiary"}, {"_id"}},
		},
		Exclusive:           map[string][2]string{"Observation": {"category", "code"}},
		IgnoreUnknownParams: true, NeedsAccept: "406",
		Writes: map[string]bool{"DocumentReference": true}, NoteRules: "epic", NKA: true, HTMLNotes: true,
		Limit: 40, Window: 10 * time.Second, OutcomeStyle: "epic", EmptyAuthErr: true,
		IDs: func(t, c string) string {
			// Epic R4 ids start with "e" and end with "3"; length varies by type.
			n := 22
			if t != "Patient" {
				n = 40
			}
			s := strings.NewReplacer("_", ".").Replace(b64("epic", t, c) + b64("epic2", t, c))[:n]
			if t == "Binary" {
				return "f" + s + "4"
			}
			return "e" + s + "3"
		},
		Fidelity: "Epic-compatible mock built from Epic's public FHIR R4 documentation (fhir.epic.com): backend OAuth with a signed JWT, category or code but not both on Observation searches, Epic error codes in OperationOutcome entries, sessionID paging, HTML notes, and DocumentReference.Create rules.",
	},
	{
		ID: "oracle", Label: "Oracle Health",
		FHIRPath: "/oracle/r4/ec2458f2-1e24-41c8-b71b-0e701af7583d", TokenPath: "/oracle/tenants/ec2458f2-1e24-41c8-b71b-0e701af7583d/protocols/oauth2/profiles/smart-v1/token",
		Auth: []string{"client_secret_basic", "private_key_jwt"}, Scopes: "both", Wildcard: false,
		TokenTTL: 570, TokenShape: "standard",
		PageSize: map[string]int{"": 50, "DocumentReference": 10, "Patient": 20}, MaxCount: 200, Paging: "oracle-context",
		Required: map[string][][]string{
			"Patient":            {{"_id"}, {"identifier"}, {"family"}, {"birthdate"}, {"name"}},
			"Condition":          patientOrSubject,
			"MedicationRequest":  patientOrID,
			"AllergyIntolerance": patientOrID,
			"Observation":        patientOrSubject,
			"Encounter":          patientOrSubject,
			"Appointment":        {{"patient", "date"}, {"actor", "date"}, {"_id"}},
			"DocumentReference":  patientOrID,
			"Coverage":           patientOrID,
		},
		NeedsAccept: "406",
		Writes:      map[string]bool{"DocumentReference": true}, NoteRules: "oracle",
		Limit: 40, Window: 10 * time.Second, OutcomeStyle: "standard",
		IDs: func(t, c string) string {
			n := h64("oracle", t, c)%90000000 + 10000000
			switch t {
			case "Observation":
				if strings.HasPrefix(c, "lab-") {
					return fmt.Sprintf("L-%d", n*10+1)
				}
				return fmt.Sprintf("VS-%d", n)
			case "Condition":
				return fmt.Sprintf("p%d", n)
			case "Binary":
				return fmt.Sprintf("TR-%d", n)
			case "Coverage":
				return fmt.Sprintf("%d-%d-%d", h64("oracle", "Patient", strings.Replace(c, "cov-", "pt-", 1))%90000000+10000000, n, n)
			}
			return fmt.Sprint(n)
		},
		Fidelity: "Oracle Health (Millennium) compatible mock built from Oracle's public Millennium R4 documentation and OpenAPI file: tenant id in the path, system accounts with client secret or signed JWT, no wildcard scopes, 570-second tokens, -pageContext paging, a required date on Appointment searches, and empty-body 201 creates.",
	},
	{
		ID: "athena", Label: "athenahealth",
		FHIRPath: "/athena/fhir/r4", TokenPath: "/athena/oauth2/v1/token",
		Auth: []string{"client_secret_basic", "private_key_jwt"}, Scopes: "both", Wildcard: false,
		ExtraScopes: []string{"athena/service/Athenanet.MDP.*"},
		TokenTTL:    3600, TokenShape: "athena",
		PageSize: map[string]int{"": 20}, MaxCount: 100, Paging: "athena-cursor",
		PracticeID: "195900",
		Required: map[string][][]string{
			"Patient":            {{"_id"}, {"identifier"}, {"name"}, {"family", "birthdate"}, {"family", "gender"}, {"family", "given"}},
			"Condition":          {{"patient"}, {"encounter"}, {"_id"}},
			"MedicationRequest":  {{"patient", "intent"}, {"_id"}},
			"AllergyIntolerance": {{"patient"}},
			"Observation":        patientOrID,
			"Encounter":          patientOrID,
			"DocumentReference":  patientOrID,
			"Coverage":           {{"patient"}},
		},
		MustHave: map[string]map[string]string{"Patient": {"ah-practice": "Organization/a-1.Practice-195900"}},
		Limit:    15, Window: time.Second, OutcomeStyle: "athena",
		IDs: func(t, c string) string {
			n := h64("athena", t, c)%9000000 + 1000000
			switch t {
			case "Patient":
				return fmt.Sprintf("a-195900.E-%d", 60200+h64("athena", c)%9000)
			case "Condition":
				return fmt.Sprintf("a-195900.Problem-%d", n)
			case "Observation":
				if strings.HasPrefix(c, "lab-") {
					return fmt.Sprintf("a-195900.labresult-%d", n)
				}
				return fmt.Sprintf("a-195900.vitalamb-%d", n)
			}
			return fmt.Sprintf("a-195900.%s-%d", strings.ToLower(t), n)
		},
		Fidelity: "athenahealth-compatible mock built from athenaOne's public docs (docs.athenahealth.com): practice-prefixed ids, ah-practice on Patient searches, intent on MedicationRequest, 15 calls per second, and appointments through the proprietary /v1/{practiceid} API rather than FHIR. FHIR writes are not available to system apps by default, so none here.",
	},
	{
		ID: "meditech", Label: "MEDITECH",
		FHIRPath: "/meditech/v1/uscore/R4", TokenPath: "/meditech/oauth/token",
		Auth: []string{"client_secret_basic", "private_key_jwt"}, Scopes: "v2", Wildcard: false,
		TokenTTL: 3600, TokenShape: "standard",
		PageSize: map[string]int{"": 25}, MaxCount: 100, Paging: "meditech-continue",
		Required: map[string][][]string{
			"Patient":            {{"_id"}, {"identifier"}, {"family", "given"}, {"family", "birthdate"}, {"name"}},
			"Condition":          patientOrID,
			"MedicationRequest":  patientOrID,
			"AllergyIntolerance": patientOrID,
			"Observation":        patientOrID,
			"Encounter":          patientOrID,
			"Appointment":        patientOrID,
			"DocumentReference":  patientOrID,
			"Coverage":           patientOrID,
		},
		Limit: 40, Window: 10 * time.Second, OutcomeStyle: "standard",
		IDs: func(t, c string) string {
			s := sha256.Sum256([]byte("meditech|" + t + "|" + c))
			return fmt.Sprintf("%x-%x-%x-%x-%x", s[0:4], s[4:6], s[6:8], s[8:10], s[10:16])
		},
		Fidelity: "MEDITECH Expanse compatible mock built only from MEDITECH's public CapabilityStatement and SMART configuration. Lower fidelity: MEDITECH's detailed API docs need a Greenfield login, which this mock does not use. SMART v2 system scopes only, continue-token paging, read-only.",
	},
	{
		ID: "ecw", Label: "eClinicalWorks",
		FHIRPath: "/ecw/fhir/r4/SBXACY", TokenPath: "/ecw/oauth/oauth2/token",
		Auth: []string{"private_key_jwt"}, Scopes: "both", Wildcard: false,
		TokenTTL: 300, TokenShape: "standard",
		PageSize: map[string]int{"": 20}, NoCount: true, Paging: "ecw-page",
		Required: map[string][][]string{
			"Patient":            {{"_id"}, {"identifier"}, {"name"}, {"family", "given", "birthdate"}, {"family", "birthdate"}, {"family", "gender"}},
			"Condition":          patientOrID,
			"MedicationRequest":  {{"patient", "intent"}, {"_id"}},
			"AllergyIntolerance": patientOrID,
			"Observation":        {{"patient", "category"}, {"patient", "code"}, {"_id"}},
			"Encounter":          patientOrID,
			"DocumentReference":  patientOrID,
			"Coverage":           patientOrID,
		},
		Limit: 500, Window: time.Minute, FixedMinute: true, OutcomeStyle: "standard",
		IDs: func(t, c string) string {
			// eCW ids share a per-practice prefix, then 22 to 43 more characters.
			n := 22
			if t != "Patient" && t != "Coverage" && t != "Binary" {
				n = 43
			}
			return "U4AqQsgbW6hNV8HfIb2RT" + strings.NewReplacer("_", ".").Replace(b64("ecw", t, c)+b64("ecw2", t, c))[:n]
		},
		Fidelity: "eClinicalWorks-compatible mock built from eCW's public developer docs: 6-letter practice code in the path, signed JWT auth with 5-minute tokens, intent=order on MedicationRequest, page-number paging, 500 calls per minute, and no Appointment API.",
		CORSNote: "Real eCW FHIR APIs send no CORS headers, so browser apps must call them from a server. This mock allows CORS so the playground can run in your browser.",
	},
}

func VendorByID(id string) *Vendor {
	for _, v := range Vendors {
		if v.ID == id {
			return v
		}
	}
	return nil
}

func (v *Vendor) accepts(method string) bool {
	for _, a := range v.Auth {
		if a == method {
			return true
		}
	}
	return false
}

func (v *Vendor) pageSize(typ string) int {
	if n, ok := v.PageSize[typ]; ok {
		return n
	}
	return v.PageSize[""]
}
