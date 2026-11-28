package weather

import (
	"net/netip"

	"github.com/oschwald/maxminddb-golang/v2"
)

type GeoIP struct {
	db *maxminddb.Reader
}

var carrierNAT = netip.MustParsePrefix("100.64.0.0/10")
var benchmarking = netip.MustParsePrefix("198.18.0.0/15")

/** OpenGeoIP opens the local DB-IP City Lite database. */
func OpenGeoIP(path string) (*GeoIP, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &GeoIP{db: db}, nil
}

/** city estimates a city from a public address without sending the address off-host. */
func (g *GeoIP) city(ip netip.Addr, language string) (Location, error) {
	ip = ip.Unmap()
	if g == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || carrierNAT.Contains(ip) || benchmarking.Contains(ip) {
		return Location{}, nil
	}
	var record struct {
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
		Subdivisions []struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"subdivisions"`
		Country struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
		Location struct {
			Latitude  *float64 `maxminddb:"latitude"`
			Longitude *float64 `maxminddb:"longitude"`
		} `maxminddb:"location"`
	}
	if err := g.db.Lookup(ip).Decode(&record); err != nil {
		return Location{}, err
	}
	city := geoName(record.City.Names, language)
	if city == "" || record.Location.Latitude == nil || record.Location.Longitude == nil {
		return Location{}, nil
	}
	labels := []string{city}
	for _, division := range record.Subdivisions {
		labels = append(labels, geoName(division.Names, language))
	}
	labels = append(labels, geoName(record.Country.Names, language))
	return Location{
		Label: locationLabel(language, labels...),
		Point: Point{Latitude: *record.Location.Latitude, Longitude: *record.Location.Longitude},
	}, nil
}

/** geoName selects the requested DB-IP language, with an English fallback. */
func geoName(names map[string]string, language string) string {
	if language == "zh" && names["zh-CN"] != "" {
		return names["zh-CN"]
	}
	if name := names[language]; name != "" {
		return name
	}
	return names["en"]
}
