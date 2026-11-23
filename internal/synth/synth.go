// Package synth builds a small, deterministic set of synthetic patients as
// FHIR R4 resources. Nothing here is real: names, ids and histories are
// generated from fixed templates and a fixed seed, so every deploy serves the
// same charts. Resource ids are canonical ("pt-01", "obs-01-0003"); the mock
// rewrites them into each vendor's id format.
package synth

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

type R = map[string]any

// Anchor is "today" for the data set: histories end here and appointments
// start after it. Build sets it, so the same charts (same ids, values and
// spacing) always end at the day the server started.
var Anchor = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

type Store struct {
	Patients []R
	ByType   map[string][]R // every resource, by resourceType
	ByID     map[string]R   // "Type/id"
	Binaries map[string]Binary
}

type Binary struct {
	ContentType string
	Data        []byte
}

type cond struct {
	snomed, icd, display string
	onsetYearsAgo        int
	resolved             bool
}

type med struct{ rxnorm, display, dose string }

type lab struct {
	loinc, display, unit string
	base, spread         float64
	low, high            float64
}

type profile struct {
	given, family, gender, birth, city string
	conds                              []cond
	meds                               []med
	allergies                          [][3]string // rxnorm/snomed code, display, reaction
	labs                               []string    // keys into LABS
	visits                             int
	payer, plan                        string
	note                               string // headline for the latest note
}

var LABS = map[string]lab{
	"a1c":   {"4548-4", "Hemoglobin A1c/Hemoglobin.total in Blood", "%", 7.4, 0.6, 4.0, 5.6},
	"glu":   {"2345-7", "Glucose [Mass/volume] in Serum or Plasma", "mg/dL", 142, 25, 70, 99},
	"ldl":   {"13457-7", "Cholesterol in LDL [Mass/volume] in Serum or Plasma by calculation", "mg/dL", 118, 20, 0, 99},
	"cr":    {"2160-0", "Creatinine [Mass/volume] in Serum or Plasma", "mg/dL", 1.1, 0.2, 0.6, 1.2},
	"egfr":  {"33914-3", "Glomerular filtration rate/1.73 sq M.predicted", "mL/min/{1.73_m2}", 64, 8, 60, 120},
	"k":     {"2823-3", "Potassium [Moles/volume] in Serum or Plasma", "mmol/L", 4.4, 0.3, 3.5, 5.1},
	"bnp":   {"30934-4", "Natriuretic peptide B [Mass/volume] in Serum or Plasma", "pg/mL", 410, 120, 0, 100},
	"hgb":   {"718-7", "Hemoglobin [Mass/volume] in Blood", "g/dL", 12.6, 0.8, 12, 17.5},
	"tsh":   {"3016-3", "Thyrotropin [Units/volume] in Serum or Plasma", "m[IU]/L", 2.1, 0.9, 0.4, 4.0},
	"inr":   {"6301-6", "INR in Platelet poor plasma by Coagulation assay", "{INR}", 2.4, 0.4, 2.0, 3.0},
	"uacr":  {"9318-7", "Albumin/Creatinine [Mass Ratio] in Urine", "mg/g", 48, 15, 0, 30},
	"wbc":   {"6690-2", "Leukocytes [#/volume] in Blood by Automated count", "10*3/uL", 7.1, 1.5, 4.5, 11},
	"alt":   {"1742-6", "Alanine aminotransferase [Enzymatic activity/volume] in Serum or Plasma", "U/L", 28, 9, 7, 56},
	"phq9":  {"44261-6", "Patient Health Questionnaire 9 item (PHQ-9) total score [Reported]", "{score}", 11, 4, 0, 4},
	"hcg":   {"21198-7", "Choriogonadotropin.beta subunit [Units/volume] in Serum or Plasma", "m[IU]/mL", 52000, 9000, 0, 5},
	"vitd":  {"1989-3", "25-hydroxyvitamin D3 [Mass/volume] in Serum or Plasma", "ng/mL", 22, 6, 30, 100},
	"psa":   {"2857-1", "Prostate specific Ag [Mass/volume] in Serum or Plasma", "ng/mL", 1.8, 0.6, 0, 4},
	"fev1":  {"20150-9", "FEV1", "L", 1.9, 0.3, 2.5, 4.5},
	"lead":  {"5671-3", "Lead [Mass/volume] in Blood", "ug/dL", 1.2, 0.5, 0, 3.5},
	"ferr":  {"2276-4", "Ferritin [Mass/volume] in Serum or Plasma", "ng/mL", 14, 5, 15, 150},
}

var (
	dm2   = cond{"44054006", "E11.9", "Type 2 diabetes mellitus", 9, false}
	htn   = cond{"38341003", "I10", "Essential hypertension", 12, false}
	hld   = cond{"55822004", "E78.5", "Hyperlipidemia", 8, false}
	ckd3  = cond{"433144002", "N18.30", "Chronic kidney disease stage 3", 3, false}
	chf   = cond{"84114007", "I50.9", "Heart failure", 4, false}
	afib  = cond{"49436004", "I48.91", "Atrial fibrillation", 5, false}
	copd  = cond{"13645005", "J44.9", "Chronic obstructive pulmonary disease", 7, false}
	asth  = cond{"195967001", "J45.909", "Asthma", 6, false}
	mdd   = cond{"370143000", "F33.1", "Major depressive disorder, recurrent, moderate", 4, false}
	gad   = cond{"21897009", "F41.1", "Generalized anxiety disorder", 3, false}
	hypo  = cond{"40930008", "E03.9", "Hypothyroidism", 10, false}
	oa    = cond{"239873007", "M17.11", "Osteoarthritis of right knee", 6, false}
	preg  = cond{"77386006", "Z34.82", "Pregnancy, second trimester", 0, false}
	ida   = cond{"87522002", "D50.9", "Iron deficiency anemia", 1, false}
	obes  = cond{"414916001", "E66.9", "Obesity", 8, false}
	gerd  = cond{"235595009", "K21.9", "Gastroesophageal reflux disease", 5, false}
	migr  = cond{"37796009", "G43.909", "Migraine", 9, false}
	sinus = cond{"36971009", "J01.90", "Acute sinusitis", 2, true}
	frac  = cond{"263204007", "S52.501A", "Fracture of distal radius", 4, true}
	adhd  = cond{"406506008", "F90.9", "Attention deficit hyperactivity disorder", 3, false}
	lowbp = cond{"279039007", "M54.50", "Low back pain", 2, false}
	sud   = cond{"7200002", "F10.20", "Alcohol use disorder, moderate", 2, false}

	metformin   = med{"860975", "metformin hydrochloride 500 MG Oral Tablet", "1 tablet by mouth twice daily with meals"}
	lisinopril  = med{"314076", "lisinopril 10 MG Oral Tablet", "1 tablet by mouth daily"}
	atorva      = med{"617312", "atorvastatin 40 MG Oral Tablet", "1 tablet by mouth at bedtime"}
	amlo        = med{"197361", "amlodipine 5 MG Oral Tablet", "1 tablet by mouth daily"}
	furo        = med{"310429", "furosemide 20 MG Oral Tablet", "1 tablet by mouth every morning"}
	carve       = med{"200031", "carvedilol 6.25 MG Oral Tablet", "1 tablet by mouth twice daily"}
	apix        = med{"1364441", "apixaban 5 MG Oral Tablet", "1 tablet by mouth twice daily"}
	warf        = med{"855332", "warfarin sodium 5 MG Oral Tablet", "Take as directed by anticoagulation clinic"}
	tiot        = med{"1552002", "tiotropium 2.5 MCG/ACTUAT Inhalation Spray", "2 puffs once daily"}
	albut       = med{"745679", "albuterol 0.09 MG/ACTUAT Metered Dose Inhaler", "2 puffs every 4 to 6 hours as needed for wheeze"}
	flutic      = med{"896188", "fluticasone propionate 0.11 MG/ACTUAT Inhaler", "2 puffs twice daily"}
	sertr       = med{"312940", "sertraline 50 MG Oral Tablet", "1 tablet by mouth daily"}
	levo        = med{"966222", "levothyroxine sodium 0.075 MG Oral Tablet", "1 tablet by mouth every morning on an empty stomach"}
	prenat      = med{"802882", "prenatal vitamin Oral Tablet", "1 tablet by mouth daily"}
	ferrous     = med{"310325", "ferrous sulfate 325 MG Oral Tablet", "1 tablet by mouth every other day"}
	omep        = med{"198053", "omeprazole 20 MG Delayed Release Oral Capsule", "1 capsule by mouth before breakfast"}
	suma        = med{"313152", "sumatriptan 50 MG Oral Tablet", "1 tablet at migraine onset, may repeat once in 2 hours"}
	empag       = med{"1545658", "empagliflozin 10 MG Oral Tablet", "1 tablet by mouth daily"}
	insulinGlar = med{"285018", "insulin glargine 100 UNT/ML Injectable Solution", "18 units subcutaneously at bedtime"}
	methylph    = med{"1091497", "methylphenidate hydrochloride 18 MG Extended Release Oral Tablet", "1 tablet by mouth every morning"}
	napro       = med{"198013", "naproxen 500 MG Oral Tablet", "1 tablet by mouth twice daily with food as needed"}
	naltrex     = med{"1483744", "naltrexone hydrochloride 50 MG Oral Tablet", "1 tablet by mouth daily"}
	buspir      = med{"104894", "buspirone hydrochloride 10 MG Oral Tablet", "1 tablet by mouth twice daily"}

	pcn   = [3]string{"7980", "Penicillin G", "Hives"}
	sulfa = [3]string{"10180", "Sulfamethoxazole", "Rash"}
	nuts  = [3]string{"91935009", "Peanut", "Anaphylaxis"}
	latex = [3]string{"300916003", "Latex", "Contact dermatitis"}
	codei = [3]string{"2670", "Codeine", "Nausea"}
	shell = [3]string{"300913006", "Shellfish", "Swelling of lips"}
)

var PROFILES = []profile{
	{"Josefina", "Diaz", "female", "1961-04-02", "Worcester", []cond{dm2, htn, hld, ckd3}, []med{metformin, lisinopril, atorva, empag}, [][3]string{pcn}, []string{"a1c", "glu", "ldl", "cr", "egfr", "k", "uacr"}, 9, "Blue Harbor Health Plan", "Harbor PPO 500", "Diabetes follow-up, A1c above goal"},
	{"Samuel", "Okafor", "male", "1948-11-19", "Springfield", []cond{chf, afib, htn, ckd3}, []med{furo, carve, apix, lisinopril}, nil, []string{"bnp", "cr", "egfr", "k", "hgb"}, 11, "Medicare", "Medicare Part B", "Heart failure clinic visit, mild volume overload"},
	{"Mei", "Tanaka", "female", "1987-07-30", "Lowell", []cond{preg, ida}, []med{prenat, ferrous}, [][3]string{latex}, []string{"hgb", "ferr", "hcg", "tsh"}, 6, "Commonwealth Care Alliance", "CCA Standard", "Prenatal visit, 22 weeks, iron supplementation"},
	{"Robert", "Kowalski", "male", "1955-02-14", "Pittsfield", []cond{copd, htn, hld}, []med{tiot, albut, amlo, atorva}, [][3]string{sulfa}, []string{"fev1", "wbc", "ldl", "glu"}, 8, "Medicare Advantage North", "MA Gold HMO", "COPD follow-up after winter exacerbation"},
	{"Aaliyah", "Johnson", "female", "2014-05-09", "Boston", []cond{asth, adhd}, []med{flutic, albut, methylph}, [][3]string{nuts}, []string{"lead", "hgb", "vitd"}, 7, "MassHealth", "MassHealth Standard", "Asthma check, well controlled on inhaled steroid"},
	{"Priya", "Raman", "female", "1979-09-12", "Cambridge", []cond{mdd, gad, migr}, []med{sertr, buspir, suma}, nil, []string{"phq9", "tsh", "vitd"}, 8, "Blue Harbor Health Plan", "Harbor HMO Select", "Behavioral health follow-up, PHQ-9 improving"},
	{"Walter", "Brennan", "male", "1940-01-25", "Quincy", []cond{afib, hypo, oa, ckd3}, []med{warf, levo, napro}, [][3]string{codei}, []string{"inr", "tsh", "cr", "egfr", "hgb"}, 12, "Medicare", "Medicare Part B", "Anticoagulation review, INR in range"},
	{"Carmen", "Alvarez", "female", "1969-03-03", "Lawrence", []cond{dm2, obes, gerd, htn}, []med{insulinGlar, metformin, omep, lisinopril}, [][3]string{shell}, []string{"a1c", "glu", "alt", "ldl", "cr"}, 9, "Fallon Community Health", "Fallon Select Care", "Insulin titration visit"},
	{"Daniel", "Nguyen", "male", "1992-12-01", "Somerville", []cond{lowbp, sinus, frac}, []med{napro}, nil, []string{"wbc", "vitd"}, 4, "Tufts Health Direct", "Tufts Bronze", "Low back pain, physical therapy referral"},
	{"Grace", "O'Connell", "female", "1958-06-21", "Brockton", []cond{hypo, hld, oa, gerd}, []med{levo, atorva, omep}, [][3]string{pcn, codei}, []string{"tsh", "ldl", "vitd", "alt"}, 7, "Blue Harbor Health Plan", "Harbor PPO 500", "Annual wellness visit"},
	{"Marcus", "Bell", "male", "1974-10-17", "New Bedford", []cond{sud, htn, gerd, mdd}, []med{naltrex, amlo, omep, sertr}, nil, []string{"alt", "glu", "phq9", "hgb"}, 9, "MassHealth", "MassHealth CarePlus", "Addiction medicine follow-up, 90 days without alcohol"},
	{"Helen", "Fitzgerald", "female", "1951-08-08", "Fall River", []cond{chf, dm2, ckd3, obes}, []med{furo, carve, metformin, empag}, [][3]string{sulfa}, []string{"bnp", "a1c", "cr", "egfr", "k", "uacr"}, 10, "Medicare Advantage North", "MA Gold HMO", "Hospital follow-up after heart failure admission"},
}

// Name is the patient's display name.
func (p profile) Name() string { return p.given + " " + p.family }

// Conds lists the patient's unresolved problems, for pickers.
func (p profile) Conds() []string {
	var out []string
	for _, c := range p.conds {
		if !c.resolved {
			out = append(out, c.display)
		}
	}
	return out
}

// Chart is the pagination stress patient: four years of monthly vitals and
// labs so any client that ignores Bundle next links visibly loses data.
const chartPatient = 2 // Samuel Okafor

// Build generates the data set ending at anchor (truncated to a UTC day).
func Build(anchor time.Time) *Store {
	Anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	s := &Store{ByType: map[string][]R{}, ByID: map[string]R{}, Binaries: map[string]Binary{}}
	for i, p := range PROFILES {
		buildPatient(s, i+1, p)
	}
	for _, list := range s.ByType {
		sort.SliceStable(list, func(i, j int) bool { return fmt.Sprint(list[i]["id"]) < fmt.Sprint(list[j]["id"]) })
	}
	return s
}

func (s *Store) add(r R) {
	t := r["resourceType"].(string)
	s.ByType[t] = append(s.ByType[t], r)
	s.ByID[t+"/"+r["id"].(string)] = r
	if t == "Patient" {
		s.Patients = append(s.Patients, r)
	}
}

func day(t time.Time) string { return t.Format("2006-01-02") }
func ts(t time.Time) string  { return t.Format(time.RFC3339) }
func ref(t, id, display string) R {
	r := R{"reference": t + "/" + id}
	if display != "" {
		r["display"] = display
	}
	return r
}
func cc(system, code, display string) R {
	return R{"coding": []any{R{"system": system, "code": code, "display": display}}, "text": display}
}

var (
	clinicians = []string{"Ana Reyes, MD", "David Chen, MD", "Fatima Malik, NP", "Thomas Byrne, MD", "Keisha Grant, PA-C", "Elena Petrova, MD"}
	clinics    = []string{"Main Street Primary Care", "Riverside Cardiology", "Harbor Women's Health", "Northfield Pulmonary", "Children's Care Collaborative", "Elm Behavioral Health"}
)

func buildPatient(s *Store, n int, p profile) {
	rng := rand.New(rand.NewSource(int64(n) * 7919))
	pid := fmt.Sprintf("pt-%02d", n)
	name := p.given + " " + p.family
	doc := clinicians[n%len(clinicians)]
	docID := fmt.Sprintf("pr-%02d", n%len(clinicians)+1)
	clinic := clinics[n%len(clinics)]
	locID := fmt.Sprintf("loc-%02d", n%len(clinics)+1)

	s.add(R{
		"resourceType": "Patient", "id": pid,
		"identifier": []any{R{
			"use": "usual", "type": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MR", "display": "Medical Record Number"}}, "text": "MRN"},
			"system": "urn:oid:2.16.840.1.113883.3.0000.1", "value": fmt.Sprintf("SYN%06d", 104000+n*37),
		}},
		"active": true,
		"name":   []any{R{"use": "official", "family": p.family, "given": []any{p.given}, "text": name}},
		"telecom": []any{R{"system": "phone", "value": fmt.Sprintf("555-01%02d", n), "use": "home"}},
		"gender": p.gender, "birthDate": p.birth,
		"address": []any{R{"use": "home", "city": p.city, "state": "MA", "postalCode": "0" + fmt.Sprint(1000+n*13), "country": "US"}},
		"generalPractitioner": []any{ref("Practitioner", docID, doc)},
	})

	for i, c := range p.conds {
		onset := Anchor.AddDate(-c.onsetYearsAgo, -rng.Intn(11), -rng.Intn(27))
		if c.onsetYearsAgo == 0 {
			onset = Anchor.AddDate(0, -5, 0)
		}
		status := "active"
		r := R{
			"resourceType": "Condition", "id": fmt.Sprintf("cond-%02d-%02d", n, i+1),
			"clinicalStatus": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": status}}},
			"verificationStatus": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/condition-ver-status", "code": "confirmed"}}},
			"category": []any{R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/condition-category", "code": "problem-list-item", "display": "Problem List Item"}}}},
			"code": R{"coding": []any{
				R{"system": "http://snomed.info/sct", "code": c.snomed, "display": c.display},
				R{"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": c.icd, "display": c.display},
			}, "text": c.display},
			"subject": ref("Patient", pid, name), "onsetDateTime": day(onset), "recordedDate": day(onset.AddDate(0, 0, 3)),
		}
		if c.resolved {
			r["clinicalStatus"] = R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": "resolved"}}}
			r["abatementDateTime"] = day(onset.AddDate(0, 2, 0))
		}
		s.add(r)
	}

	for i, m := range p.meds {
		authored := Anchor.AddDate(-1-rng.Intn(4), -rng.Intn(11), 0)
		s.add(R{
			"resourceType": "MedicationRequest", "id": fmt.Sprintf("med-%02d-%02d", n, i+1),
			"status": "active", "intent": "order",
			"medicationCodeableConcept": cc("http://www.nlm.nih.gov/research/umls/rxnorm", m.rxnorm, m.display),
			"subject": ref("Patient", pid, name), "authoredOn": day(authored),
			"requester": ref("Practitioner", docID, doc),
			"dosageInstruction": []any{R{"text": m.dose}},
		})
	}
	// One stopped medication per patient, so status filters matter.
	s.add(R{
		"resourceType": "MedicationRequest", "id": fmt.Sprintf("med-%02d-90", n), "status": "stopped", "intent": "order",
		"medicationCodeableConcept": cc("http://www.nlm.nih.gov/research/umls/rxnorm", "308182", "amoxicillin 500 MG Oral Capsule"),
		"subject": ref("Patient", pid, name), "authoredOn": day(Anchor.AddDate(-2, -3, 0)),
		"requester": ref("Practitioner", docID, doc), "dosageInstruction": []any{R{"text": "1 capsule by mouth three times daily for 10 days"}},
	})

	for i, a := range p.allergies {
		system := "http://www.nlm.nih.gov/research/umls/rxnorm"
		cat := "medication"
		if len(a[0]) > 6 {
			system, cat = "http://snomed.info/sct", "food"
			if a[1] == "Latex" {
				cat = "environment"
			}
		}
		crit := "low"
		if a[2] == "Anaphylaxis" || a[2] == "Swelling of lips" {
			crit = "high"
		}
		s.add(R{
			"resourceType": "AllergyIntolerance", "id": fmt.Sprintf("alg-%02d-%02d", n, i+1),
			"clinicalStatus": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", "code": "active"}}},
			"verificationStatus": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-verification", "code": "confirmed"}}},
			"category": []any{cat}, "criticality": crit,
			"code": cc(system, a[0], a[1]), "patient": ref("Patient", pid, name),
			"recordedDate": day(Anchor.AddDate(-6-rng.Intn(8), 0, 0)),
			"reaction": []any{R{"manifestation": []any{R{"text": a[2], "coding": []any{R{"system": "http://snomed.info/sct", "display": a[2]}}}}}},
		})
	}

	// Encounters, each with vitals and labs.
	visits := p.visits
	monthsApart := 4
	if n == chartPatient {
		visits, monthsApart = 48, 1
	}
	var encs []R
	for v := 0; v < visits; v++ {
		start := Anchor.AddDate(0, -monthsApart*(visits-v), rng.Intn(20)).Add(time.Duration(8+rng.Intn(8)) * time.Hour)
		class, typ := "AMB", "Office Visit"
		if v == visits-3 && len(p.conds) > 3 {
			class, typ = "EMER", "Emergency department visit"
		}
		eid := fmt.Sprintf("enc-%02d-%03d", n, v+1)
		reason := p.conds[v%len(p.conds)].display
		e := R{
			"resourceType": "Encounter", "id": eid, "status": "finished",
			"class": R{"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": class},
			"type":  []any{R{"text": typ, "coding": []any{R{"system": "http://snomed.info/sct", "code": map[string]string{"AMB": "185349003", "EMER": "50849002"}[class], "display": typ}}}},
			"subject": ref("Patient", pid, name),
			"participant": []any{R{"individual": ref("Practitioner", docID, doc)}},
			"period":      R{"start": ts(start), "end": ts(start.Add(35 * time.Minute))},
			"reasonCode":  []any{R{"text": reason}},
			"location":    []any{R{"location": ref("Location", locID, clinic)}},
		}
		s.add(e)
		encs = append(encs, e)
		addVitals(s, rng, n, v, pid, name, eid, start, p)
		if v%2 == 1 || v == visits-1 || n == chartPatient {
			for li, key := range p.labs {
				addLab(s, rng, n, v, li, pid, name, eid, start.Add(40*time.Minute), LABS[key], v == visits-1)
			}
		}
	}

	// Notes for the three most recent encounters.
	for k := 0; k < 3 && k < len(encs); k++ {
		e := encs[len(encs)-1-k]
		start, _ := time.Parse(time.RFC3339, e["period"].(R)["start"].(string))
		did := fmt.Sprintf("doc-%02d-%02d", n, k+1)
		text := noteText(p, name, doc, start, k, e)
		bid := "bin-" + did[4:]
		s.Binaries[bid] = Binary{ContentType: "text/plain", Data: []byte(text)}
		title := p.note
		if k > 0 {
			title = fmt.Sprintf("%s visit note", e["type"].([]any)[0].(R)["text"])
		}
		s.add(R{
			"resourceType": "DocumentReference", "id": did, "status": "current", "docStatus": "final",
			"type":     R{"coding": []any{R{"system": "http://loinc.org", "code": "11506-3", "display": "Progress note"}}, "text": "Progress Note"},
			"category": []any{R{"coding": []any{R{"system": "http://hl7.org/fhir/us/core/CodeSystem/us-core-documentreference-category", "code": "clinical-note", "display": "Clinical Note"}}}},
			"subject":  ref("Patient", pid, name), "date": ts(start.Add(2 * time.Hour)),
			"author":   []any{ref("Practitioner", docID, doc)},
			"description": title,
			"content":     []any{R{"attachment": R{"contentType": "text/plain", "url": "Binary/" + bid, "title": title}, "format": R{"system": "http://ihe.net/fhir/ihe.formatcode.fhir/CodeSystem/formatcode", "code": "urn:ihe:iti:xds:2017:mimeTypeSufficient"}}},
			"context":     R{"encounter": []any{ref("Encounter", e["id"].(string), "")}, "period": e["period"]},
		})
	}

	// Two upcoming appointments and one past no-show.
	for k, a := range []struct {
		days       int
		status, ty string
	}{{12 + n%9, "booked", "Follow-up"}, {74 + n*3, "booked", "Lab only"}, {-30, "noshow", "Follow-up"}} {
		start := Anchor.AddDate(0, 0, a.days).Add(time.Duration(9+(n+k)%7) * time.Hour)
		dur := 20
		if a.ty == "Lab only" {
			dur = 10
		}
		s.add(R{
			"resourceType": "Appointment", "id": fmt.Sprintf("appt-%02d-%02d", n, k+1), "status": a.status,
			"serviceType":  []any{R{"text": a.ty}},
			"description":  fmt.Sprintf("%s: %s", a.ty, p.conds[0].display),
			"start":        ts(start), "end": ts(start.Add(time.Duration(dur) * time.Minute)), "minutesDuration": dur,
			"participant": []any{
				R{"actor": ref("Patient", pid, name), "status": "accepted"},
				R{"actor": ref("Practitioner", docID, doc), "status": "accepted"},
				R{"actor": ref("Location", locID, clinic), "status": "accepted"},
			},
		})
	}

	s.add(R{
		"resourceType": "Coverage", "id": fmt.Sprintf("cov-%02d", n), "status": "active",
		"subscriberId": fmt.Sprintf("%s%07d", string(p.payer[0]), 3000000+n*4111),
		"beneficiary":  ref("Patient", pid, name),
		"relationship": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/subscriber-relationship", "code": "self", "display": "Self"}}},
		"period":       R{"start": fmt.Sprintf("%d-01-01", Anchor.Year()), "end": fmt.Sprintf("%d-12-31", Anchor.Year())},
		"payor":        []any{R{"display": p.payer}},
		"class": []any{
			R{"type": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/coverage-class", "code": "group"}}}, "value": fmt.Sprintf("GRP-%03d", 100+n)},
			R{"type": R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/coverage-class", "code": "plan"}}}, "value": p.plan, "name": p.plan},
		},
	})
}

func round(x float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(x*p+0.5*sign(x))) / p
}

func sign(x float64) float64 {
	if x < 0 {
		return -1
	}
	return 1
}

func vitalObs(id, pid, name, eid string, at time.Time, loinc, display string) R {
	return R{
		"resourceType": "Observation", "id": id, "status": "final",
		"category": []any{R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs", "display": "Vital Signs"}}}},
		"code":     R{"coding": []any{R{"system": "http://loinc.org", "code": loinc, "display": display}}, "text": display},
		"subject":  ref("Patient", pid, name), "encounter": ref("Encounter", eid, ""),
		"effectiveDateTime": ts(at), "issued": ts(at.Add(5 * time.Minute)),
	}
}

func q(v float64, unit, code string) R {
	return R{"value": v, "unit": unit, "system": "http://unitsofmeasure.org", "code": code}
}

func addVitals(s *Store, rng *rand.Rand, n, v int, pid, name, eid string, at time.Time, p profile) {
	base := fmt.Sprintf("obs-%02d-%03d", n, v+1)
	sys, dia := 124+rng.Intn(18), 76+rng.Intn(10)
	for _, c := range p.conds {
		if c.snomed == htn.snomed {
			sys += 8
		}
	}
	bp := vitalObs(base+"-bp", pid, name, eid, at, "85354-9", "Blood pressure panel with all children optional")
	bp["component"] = []any{
		R{"code": cc("http://loinc.org", "8480-6", "Systolic blood pressure"), "valueQuantity": q(float64(sys), "mmHg", "mm[Hg]")},
		R{"code": cc("http://loinc.org", "8462-4", "Diastolic blood pressure"), "valueQuantity": q(float64(dia), "mmHg", "mm[Hg]")},
	}
	s.add(bp)
	hr := vitalObs(base+"-hr", pid, name, eid, at, "8867-4", "Heart rate")
	hr["valueQuantity"] = q(float64(62+rng.Intn(24)), "beats/minute", "/min")
	s.add(hr)
	wt := vitalObs(base+"-wt", pid, name, eid, at, "29463-7", "Body weight")
	kg := 58.0 + float64(n*3%40) + rng.Float64()*2
	birth, _ := time.Parse("2006-01-02", p.birth)
	if at.Sub(birth).Hours() < 24*365*16 {
		kg = 28 + float64(at.Year()-birth.Year())*2 + rng.Float64()
	}
	wt["valueQuantity"] = q(round(kg, 1), "kg", "kg")
	s.add(wt)
	spo2 := vitalObs(base+"-o2", pid, name, eid, at, "59408-5", "Oxygen saturation in Arterial blood by Pulse oximetry")
	spo2["valueQuantity"] = q(float64(93+rng.Intn(6)), "%", "%")
	s.add(spo2)
}

func addLab(s *Store, rng *rand.Rand, n, v, li int, pid, name, eid string, at time.Time, l lab, latest bool) {
	val := l.base + (rng.Float64()*2-1)*l.spread
	if val < 0 {
		val = 0
	}
	places := 1
	if l.base >= 100 {
		places = 0
	}
	val = round(val, places)
	r := R{
		"resourceType": "Observation", "id": fmt.Sprintf("lab-%02d-%03d-%02d", n, v+1, li+1), "status": "final",
		"category": []any{R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "laboratory", "display": "Laboratory"}}}},
		"code":     R{"coding": []any{R{"system": "http://loinc.org", "code": l.loinc, "display": l.display}}, "text": l.display},
		"subject":  ref("Patient", pid, name), "encounter": ref("Encounter", eid, ""),
		"effectiveDateTime": ts(at), "issued": ts(at.Add(6 * time.Hour)),
		"valueQuantity":     q(val, l.unit, l.unit),
		"referenceRange":    []any{R{"low": q(l.low, l.unit, l.unit), "high": q(l.high, l.unit, l.unit)}},
	}
	interp := ""
	switch {
	case val > l.high:
		interp = "H"
	case val < l.low:
		interp = "L"
	}
	if interp != "" {
		d := map[string]string{"H": "High", "L": "Low"}[interp]
		r["interpretation"] = []any{R{"coding": []any{R{"system": "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation", "code": interp, "display": d}}, "text": d}}
	}
	s.add(r)
}

func noteText(p profile, name, doc string, at time.Time, k int, e R) string {
	headline := p.note
	if k > 0 {
		headline = "Routine follow-up"
	}
	var probs, meds string
	for _, c := range p.conds {
		if !c.resolved {
			probs += "- " + c.display + "\n"
		}
	}
	for _, m := range p.meds {
		meds += "- " + m.display + ": " + m.dose + "\n"
	}
	allergies := "No known drug allergies."
	if len(p.allergies) > 0 {
		allergies = ""
		for i, a := range p.allergies {
			if i > 0 {
				allergies += "; "
			}
			allergies += a[1] + " (" + a[2] + ")"
		}
	}
	return fmt.Sprintf(`SYNTHETIC RECORD. NOT A REAL PATIENT.

Progress Note
Patient: %s
Date: %s
Clinician: %s
Visit: %s

Chief concern: %s.

History: %s returns for %s. Reports taking medications as prescribed most days. No new hospitalizations since last visit.

Active problems:
%s
Medications:
%s
Allergies: %s

Assessment and plan:
1. %s: continue current regimen; recheck labs before next visit.
2. Reviewed warning signs and when to seek urgent care.
3. Follow up in %d weeks or sooner as needed.

Electronically signed by %s
`, name, at.Format("January 2, 2006"), doc, e["type"].([]any)[0].(R)["text"], headline, p.given, e["reasonCode"].([]any)[0].(R)["text"], probs, meds, allergies, p.conds[0].display, 4+k*4, doc)
}
