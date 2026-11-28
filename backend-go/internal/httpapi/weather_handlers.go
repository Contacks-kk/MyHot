package httpapi

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/weather"
)

type weatherView struct {
	Enabled   bool              `json:"enabled"`
	Location  string            `json:"location,omitempty"`
	Estimated bool              `json:"estimated,omitempty"`
	Forecast  *weather.Forecast `json:"forecast,omitempty"`
}

/** getWeather reads today's forecast for the selected city or the estimated visitor city. */
func (s *Server) getWeather(w http.ResponseWriter, r *http.Request, user userContext) {
	view := weatherView{Enabled: s.Weather != nil}
	if !view.Enabled {
		writeJSON(w, http.StatusOK, view)
		return
	}
	if s.limiter.limited("weather:"+strconv.FormatInt(user.ID, 10), 20, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "天气查询过于频繁")
		return
	}
	query := r.URL.Query()
	language := weatherLanguage(query.Get("lang"))
	var point weather.Point
	if query.Has("lat") || query.Has("lon") {
		latitude, latErr := strconv.ParseFloat(query.Get("lat"), 64)
		longitude, lonErr := strconv.ParseFloat(query.Get("lon"), 64)
		point = weather.Point{Latitude: latitude, Longitude: longitude}
		if latErr != nil || lonErr != nil || !point.Valid() {
			writeError(w, http.StatusBadRequest, "天气查询坐标无效")
			return
		}
	} else {
		// Reuse the API's trusted-proxy boundary for visitor addresses.
		ip, _ := netip.ParseAddr(clientKey(r, s.Config.TrustProxy))
		location, err := s.Weather.EstimateCity(ip, language)
		if err != nil {
			s.Logger.Warn("Weather IP estimation failed")
		}
		if location.Label == "" || !location.Point.Valid() {
			writeJSON(w, http.StatusOK, view)
			return
		}
		view.Location = location.Label
		view.Estimated = true
		point = location.Point
	}

	forecast, err := s.Weather.Today(r.Context(), point, language)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.Logger.Warn("Weather forecast unavailable", "error", err)
		writeError(w, http.StatusBadGateway, "天气暂不可用")
		return
	}
	view.Forecast = &forecast
	writeJSON(w, http.StatusOK, view)
}

/** searchWeatherLocations looks up a city only on a reader's explicit search. */
func (s *Server) searchWeatherLocations(w http.ResponseWriter, r *http.Request, user userContext) {
	if s.Weather == nil {
		writeError(w, http.StatusServiceUnavailable, "天气服务未配置")
		return
	}
	query := r.URL.Query()
	name := strings.TrimSpace(query.Get("q"))
	if n := utf8.RuneCountInString(name); n == 0 || n > 80 {
		writeError(w, http.StatusBadRequest, "城市名称无效")
		return
	}
	if s.limiter.limited("weather-search:"+strconv.FormatInt(user.ID, 10), 8, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "城市搜索过于频繁")
		return
	}
	locations, err := s.Weather.SearchCities(r.Context(), name, weatherLanguage(query.Get("lang")))
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.Logger.Warn("Weather city search unavailable", "error", err)
		writeError(w, http.StatusBadGateway, "城市搜索暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, locations)
}

/** weatherLanguage keeps provider requests and cache variants aligned with supported UI languages. */
func weatherLanguage(language string) string {
	if language == "en" {
		return "en"
	}
	return "zh"
}
