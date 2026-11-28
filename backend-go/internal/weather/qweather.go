package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var qweatherAPIHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.qweatherapi\.com$`)

type qweatherClient struct {
	baseURL string
	key     string
	http    *http.Client
}

type qweatherDaily struct {
	Start string `json:"forecastStartTime"`
	End   string `json:"forecastEndTime"`
	Min   struct {
		Value *float64 `json:"value"`
	} `json:"temperatureMin"`
	Max struct {
		Value *float64 `json:"value"`
	} `json:"temperatureMax"`
	Day struct {
		Condition struct {
			Text string `json:"text"`
		} `json:"condition"`
	} `json:"daytime"`
}

/** newQWeatherClient confines credential use to the configured HTTPS provider host. */
func newQWeatherClient(host, key string) (*qweatherClient, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	key = strings.TrimSpace(key)
	if !qweatherAPIHost.MatchString(host) || key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("QWEATHER_API_HOST or QWEATHER_API_KEY is invalid")
	}
	return &qweatherClient{
		baseURL: "https://" + host,
		key:     key,
		http: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

/** today selects the forecast covering now from the provider's local-day intervals. */
func (c *qweatherClient) today(ctx context.Context, latitude, longitude, language string) (Forecast, error) {
	query := url.Values{"days": {"2"}, "localTime": {"true"}, "lang": {language}}
	var response struct {
		Days []qweatherDaily `json:"days"`
	}
	if err := c.get(ctx, "/weather/v1/daily/"+latitude+"/"+longitude, query, &response); err != nil {
		return Forecast{}, err
	}
	return currentForecast(response.Days, time.Now())
}

/** currentForecast maps the provider's complete forecast for the current local day. */
func currentForecast(days []qweatherDaily, now time.Time) (Forecast, error) {
	for _, day := range days {
		start, startErr := forecastTime(day.Start)
		end, endErr := forecastTime(day.End)
		forecast := Forecast{StartsAt: start, EndsAt: end, Condition: day.Day.Condition.Text}
		if startErr == nil && endErr == nil && forecast.covers(now) && day.Min.Value != nil && day.Max.Value != nil && forecast.Condition != "" {
			forecast.TemperatureMin = *day.Min.Value
			forecast.TemperatureMax = *day.Max.Value
			return forecast, nil
		}
	}
	return Forecast{}, errors.New("QWeather did not return a complete forecast for today")
}

/** forecastTime accepts the provider's minute-precision timestamps and full RFC3339 timestamps. */
func forecastTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse("2006-01-02T15:04Z07:00", raw); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, raw)
}

/** searchCities resolves and disambiguates a bounded set of matching places worldwide. */
func (c *qweatherClient) searchCities(ctx context.Context, name, language string) ([]Location, error) {
	var response struct {
		Code      string `json:"code"`
		Locations []struct {
			Name      string `json:"name"`
			Country   string `json:"country"`
			Region    string `json:"adm1"`
			AdminArea string `json:"adm2"`
			Latitude  string `json:"lat"`
			Longitude string `json:"lon"`
		} `json:"location"`
	}
	query := url.Values{"location": {name}, "number": {"6"}, "lang": {language}}
	if err := c.get(ctx, "/geo/v2/city/lookup", query, &response); err != nil {
		var status upstreamStatus
		if errors.As(err, &status) && status == http.StatusNotFound {
			return []Location{}, nil
		}
		return nil, err
	}
	locations := make([]Location, 0, len(response.Locations))
	if response.Code == "404" {
		return locations, nil
	}
	if response.Code != "200" {
		return nil, errors.New("QWeather city lookup returned an unsuccessful result")
	}
	for _, item := range response.Locations {
		latitude, latErr := strconv.ParseFloat(item.Latitude, 64)
		longitude, lonErr := strconv.ParseFloat(item.Longitude, 64)
		point := Point{Latitude: latitude, Longitude: longitude}
		if latErr == nil && lonErr == nil && point.Valid() && item.Name != "" {
			label := locationLabel(language, item.Name, item.AdminArea, item.Region, item.Country)
			locations = append(locations, Location{Label: label, Point: point})
		}
	}
	return locations, nil
}

type upstreamStatus int

/** Error reports the upstream status without including request credentials or response content. */
func (status upstreamStatus) Error() string {
	return fmt.Sprintf("QWeather returned HTTP %d", status)
}

/** get reads a bounded JSON response with the credentials confined to a request header. */
func (c *qweatherClient) get(ctx context.Context, path string, query url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return errors.New("could not create QWeather request")
	}
	req.Header.Set("X-QW-Api-Key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("QWeather request timed out")
		}
		return errors.New("QWeather connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamStatus(resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(result); err != nil {
		return errors.New("QWeather returned invalid JSON")
	}
	return nil
}
