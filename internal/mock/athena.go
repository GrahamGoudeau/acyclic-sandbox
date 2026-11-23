package mock

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// athenaOne schedules through its proprietary API, not FHIR: system apps get
// no Appointment FHIR search. This covers the one read the SDK needs.
//
//	GET /v1/{practiceid}/patients/{patientid}/appointments
//
// Shape follows athena's documented conventions (mm/dd/yyyy dates, HH:MM
// department-local times, string ids, {"error","detailedmessage"} errors).
// The exact path is from athena's API reference index and is marked
// UNCONFIRMED in vendors.md.
func (s *Server) athenaProprietary(w http.ResponseWriter, r *http.Request) {
	v := VendorByID("athena")
	perr := func(status int, e, detail string) {
		writeJSON(w, status, "application/json; charset=utf-8", R{"error": e, "detailedmessage": detail})
	}
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		perr(401, "Access Token Missing", "A bearer access token was not found in the authorization header.")
		return
	}
	client, scopes, ok := s.Auth.Check(strings.TrimPrefix(authz, "Bearer "), "athena")
	if !ok {
		perr(401, "Invalid Access Token.", "The access token provided is not valid. Please verify the token is correct and provided as a bearer token in the authorization header.")
		return
	}
	mdp := false
	for _, sc := range scopes {
		if sc == "athena/service/Athenanet.MDP.*" {
			mdp = true
		}
	}
	if !mdp {
		perr(403, "One or more required access scopes are missing.", "Request the athena/service/Athenanet.MDP.* scope.")
		return
	}
	if s.rateLimited(w, v, client) {
		perr(429, "This endpoint has been called too often or too recently. Please wait a while and try again.", "")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/athena/v1/"), "/"), "/")
	if len(parts) != 4 || parts[1] != "patients" || parts[3] != "appointments" || r.Method != http.MethodGet {
		perr(404, "An unknown API path was called.", "")
		return
	}
	if parts[0] != v.PracticeID {
		perr(403, "This call is not allowed against this practice ID.", "")
		return
	}
	pid, ok := s.canon(v, "Patient", "a-195900.E-"+parts[2])
	if !ok {
		perr(404, "Patient ID is invalid.", "")
		return
	}
	showPast := r.URL.Query().Get("showpast") == "true"
	status := map[string]string{"booked": "f", "noshow": "x", "cancelled": "x", "fulfilled": "4", "arrived": "2"}
	var out []any
	for _, a := range s.all(client, "Appointment") {
		if patientOf(a) != pid {
			continue
		}
		start, err := time.Parse(time.RFC3339, str(a["start"]))
		if err != nil || (!showPast && start.Before(time.Now().Truncate(24*time.Hour))) {
			continue
		}
		typ := ""
		if st := list(a["serviceType"]); len(st) > 0 {
			typ = str(st[0].(R)["text"])
		}
		id := v.IDs("Appointment", str(a["id"]))
		num := id[strings.LastIndex(id, "-")+1:]
		out = append(out, R{
			"appointmentid": num, "appointmentstatus": status[str(a["status"])],
			"appointmenttype": typ, "appointmenttypeid": strconv.Itoa(int(h64(typ)%900 + 100)),
			"date": start.Format("01/02/2006"), "starttime": start.Format("15:04"),
			"duration": a["minutesDuration"], "departmentid": "1", "providerid": "71",
			"patientid": parts[2], "patientappointmenttypename": typ,
		})
	}
	if out == nil {
		out = []any{}
	}
	writeJSON(w, 200, "application/json; charset=utf-8", R{"appointments": out, "totalcount": len(out)})
}
