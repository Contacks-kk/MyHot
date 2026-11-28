package weather

import (
	"context"
	"encoding/json"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Location struct {
	Label string `json:"label"`
	Point
}

type Forecast struct {
	StartsAt       time.Time `json:"startsAt"`
	EndsAt         time.Time `json:"endsAt"`
	Condition      string    `json:"condition"`
	TemperatureMin float64   `json:"temperatureMin"`
	TemperatureMax float64   `json:"temperatureMax"`
}

type Service struct {
	provider *qweatherClient
	geo      *GeoIP
	cache    *redis.Client
}

/** NewService connects the weather provider, offline city lookup and shared forecast cache. */
func NewService(host, key string, geo *GeoIP, cache *redis.Client) (*Service, error) {
	provider, err := newQWeatherClient(host, key)
	if err != nil {
		return nil, err
	}
	return &Service{provider: provider, geo: geo, cache: cache}, nil
}

/** Valid reports whether this point falls within the latitude and longitude bounds. */
func (p Point) Valid() bool {
	return p.Latitude >= -90 && p.Latitude <= 90 && p.Longitude >= -180 && p.Longitude <= 180
}

/** EstimateCity estimates a location using a local public-IP lookup. */
func (s *Service) EstimateCity(ip netip.Addr, language string) (Location, error) {
	return s.geo.city(ip, language)
}

/** SearchCities resolves a user-entered city query for display during the current visit. */
func (s *Service) SearchCities(ctx context.Context, query, language string) ([]Location, error) {
	return s.provider.searchCities(ctx, query, language)
}

/** Today returns a forecast whose interval contains the current time at the selected location. */
func (s *Service) Today(ctx context.Context, point Point, language string) (Forecast, error) {
	latitude := coordinate(point.Latitude)
	longitude := coordinate(point.Longitude)
	key := "myhot:weather:daily:v1:" + latitude + ":" + longitude + ":" + language
	if forecast, ok := s.cachedForecast(ctx, key); ok {
		return forecast, nil
	}
	forecast, err := s.provider.today(ctx, latitude, longitude, language)
	if err != nil {
		return Forecast{}, err
	}
	s.cacheForecast(ctx, key, forecast)
	return forecast, nil
}

/** cachedForecast reads the shared forecast when it still covers the current local day. */
func (s *Service) cachedForecast(ctx context.Context, key string) (Forecast, bool) {
	if s.cache == nil {
		return Forecast{}, false
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	raw, err := s.cache.Get(cacheCtx, key).Bytes()
	var forecast Forecast
	if err != nil || json.Unmarshal(raw, &forecast) != nil {
		return Forecast{}, false
	}
	return forecast, forecast.covers(time.Now()) && forecast.Condition != ""
}

/** cacheForecast shares only the forecast, bounded by its location's local midnight. */
func (s *Service) cacheForecast(ctx context.Context, key string, forecast Forecast) {
	ttl := min(3*time.Hour, time.Until(forecast.EndsAt))
	if s.cache == nil || ttl < time.Millisecond {
		return
	}
	data, err := json.Marshal(forecast)
	if err != nil {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = s.cache.Set(cacheCtx, key, data, ttl).Err()
}

/** covers reports whether the requested instant falls within the forecast day. */
func (f Forecast) covers(now time.Time) bool {
	return !now.Before(f.StartsAt) && now.Before(f.EndsAt)
}

/** coordinate normalizes the upstream request and cache key to the same resolution. */
func coordinate(value float64) string {
	value = math.Round(value*100)/100 + 0
	return strconv.FormatFloat(value, 'f', 2, 64)
}

/** locationLabel formats a disambiguated city label in the reader's locale. */
func locationLabel(language string, labels ...string) string {
	seen := make(map[string]bool, len(labels))
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		key := strings.ToLower(label)
		if label != "" && !seen[key] {
			parts = append(parts, label)
			seen[key] = true
		}
	}
	if language == "zh" {
		for lo, hi := 0, len(parts)-1; lo < hi; lo, hi = lo+1, hi-1 {
			parts[lo], parts[hi] = parts[hi], parts[lo]
		}
		return strings.Join(parts, " · ")
	}
	return strings.Join(parts, ", ")
}
