package httpapi

import (
	"net/http"
	"strings"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

func (s *Server) v1Dailies(w http.ResponseWriter, r *http.Request) {
	s.v1ReportIndex(w, r, "daily")
}

func (s *Server) v1Periods(w http.ResponseWriter, r *http.Request, kind string) {
	s.v1ReportIndex(w, r, kind)
}

func (s *Server) v1ReportIndex(w http.ResponseWriter, r *http.Request, kind string) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	query, err := strictSelectedQuery(r, "limit")
	if err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	limit, maximum, fallback := 30, 180, 30
	etagPrefix := "v1-dailies"
	if kind != "daily" {
		limit, maximum, fallback = 12, 60, 12
		etagPrefix = "v1-" + map[string]string{"weekly": "weeklies", "monthly": "monthlies"}[kind]
	}
	if raw, ok := query["limit"]; ok {
		limit, err = parsePublicV1Integer(raw, 1, maximum, "limit")
		if err != nil {
			writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
			return
		}
	} else {
		limit = fallback
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	var body publiccontent.V1ReportIndexResponse
	if kind == "daily" {
		body, err = s.PublicContent.V1Dailies(r.Context(), limit, time.Now(), s.Config.SiteURL, s.Config.SiteName)
	} else {
		body, err = s.PublicContent.V1Periods(r.Context(), kind, limit, time.Now(), s.Config.SiteURL, s.Config.SiteName)
	}
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	writeV1JSONWithETag(w, r, etagPrefix, body, body)
}

func (s *Server) v1DailyLatest(w http.ResponseWriter, r *http.Request) {
	s.v1Daily(w, r, "latest", true)
}

func (s *Server) v1DailyByDate(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date {
		s.v1ReportInvalid(w, r, "date must be a real YYYY-MM-DD calendar date.")
		return
	}
	s.v1Daily(w, r, date, false)
}

func (s *Server) v1Daily(w http.ResponseWriter, r *http.Request, key string, latest bool) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if query, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	} else if len(query) != 0 {
		// strictSelectedQuery with no allow-list rejects all query parameters.
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", "Unknown query parameter.", 0)
		return
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, err := s.PublicContent.V1Daily(r.Context(), key, latest, time.Now(), s.Config.SiteURL, s.Config.SiteName)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	if body == nil {
		detail := "No daily report has been published yet."
		if !latest {
			detail = "No daily report exists for " + key + "."
		}
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", detail, 0)
		return
	}
	writeV1JSONWithETag(w, r, "v1-daily", body, body)
}

func (s *Server) v1PeriodLatest(w http.ResponseWriter, r *http.Request, kind string) {
	s.v1Period(w, r, kind, "latest", true)
}

func (s *Server) v1PeriodByKey(w http.ResponseWriter, r *http.Request, kind, key string) {
	if _, _, valid := publiccontent.ReportPeriodRange(kind, key); !valid {
		name, form := "week", "a real ISO week such as 2026-W39"
		if kind == "monthly" {
			name, form = "month", "a real month such as 2026-09"
		}
		s.v1ReportInvalid(w, r, name+" must be "+form+".")
		return
	}
	s.v1Period(w, r, kind, key, false)
}

func (s *Server) v1Period(w http.ResponseWriter, r *http.Request, kind, key string, latest bool) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	if s.PublicContent == nil {
		writeV1HotUnavailable(w)
		return
	}
	body, err := s.PublicContent.V1Period(r.Context(), kind, key, latest, time.Now(), s.Config.SiteURL, s.Config.SiteName, s.Config.SiteReportEntryMeasure, s.Config.SiteReportEntryNoun)
	if err != nil {
		writeV1HotUnavailable(w)
		return
	}
	if body == nil {
		name := "weekly"
		if kind == "monthly" {
			name = "monthly"
		}
		detail := "No " + name + " report has been published yet."
		if !latest {
			label := key
			if kind == "monthly" {
				detail = "No monthly report exists for " + label + "."
			} else {
				detail = "No weekly report exists for " + label + "."
			}
		}
		writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", detail, 0)
		return
	}
	etagPrefix := "v1-weekly"
	if kind == "monthly" {
		etagPrefix = "v1-monthly"
	}
	writeV1JSONWithETag(w, r, etagPrefix, body, body)
}

func (s *Server) v1ReportInvalid(w http.ResponseWriter, r *http.Request, detail string) {
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	if !s.requireSiteUser(w, r) {
		return
	}
	if _, err := strictSelectedQuery(r); err != nil {
		writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error(), 0)
		return
	}
	writeV1HotProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", detail, 0)
}

func registerV1ReportRoutes(mux *http.ServeMux, server *Server) {
	mux.HandleFunc("GET /api/v1/dailies", server.v1Dailies)
	mux.HandleFunc("GET /api/v1/dailies/latest", server.v1DailyLatest)
	mux.HandleFunc("GET /api/v1/dailies/{date}", server.v1DailyByDate)
	mux.HandleFunc("GET /api/v1/weeklies", func(w http.ResponseWriter, r *http.Request) { server.v1Periods(w, r, "weekly") })
	mux.HandleFunc("GET /api/v1/weeklies/latest", func(w http.ResponseWriter, r *http.Request) { server.v1PeriodLatest(w, r, "weekly") })
	mux.HandleFunc("GET /api/v1/weeklies/{week}", func(w http.ResponseWriter, r *http.Request) {
		server.v1PeriodByKey(w, r, "weekly", r.PathValue("week"))
	})
	mux.HandleFunc("GET /api/v1/monthlies", func(w http.ResponseWriter, r *http.Request) { server.v1Periods(w, r, "monthly") })
	mux.HandleFunc("GET /api/v1/monthlies/latest", func(w http.ResponseWriter, r *http.Request) { server.v1PeriodLatest(w, r, "monthly") })
	mux.HandleFunc("GET /api/v1/monthlies/{month}", func(w http.ResponseWriter, r *http.Request) {
		server.v1PeriodByKey(w, r, "monthly", r.PathValue("month"))
	})
	for _, path := range []string{"/api/v1/dailies", "/api/v1/weeklies", "/api/v1/monthlies"} {
		mux.HandleFunc("OPTIONS "+path, v1SelectedPreflight)
		mux.HandleFunc(path, v1SelectedMethodNotAllowed)
		mux.HandleFunc("OPTIONS "+path+"/{key}", v1SelectedPreflight)
		mux.HandleFunc(path+"/", server.v1ReportSubtreeFallback)
	}
}

func (s *Server) v1ReportSubtreeFallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		v1SelectedPreflight(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		v1SelectedMethodNotAllowed(w, r)
		return
	}
	setPublicV1Headers(w)
	setPrivateSiteHeaders(w)
	path := r.URL.Path
	writeV1HotProblem(w, http.StatusNotFound, "not_found", "Not found", "No public API v1 operation exists at "+path+". Recent items are at /api/v1/items; every operation is listed at "+strings.TrimRight(s.Config.SiteURL, "/")+"/openapi-v1.json", 0)
}
