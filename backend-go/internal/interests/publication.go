package interests

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PublicationSettings is shared by the React site and Go public-content exits.
type PublicationSettings struct {
	Subject          string           `json:"subject"`
	AllFeedLeavesOut []string         `json:"allFeedLeavesOut"`
	CommunityFeeds   CommunityFeeds   `json:"communityFeeds"`
	PublicCategories PublicCategories `json:"publicCategories"`
}

type CommunityFeeds struct {
	Dev []string `json:"dev"`
	HN  []string `json:"hn"`
}

type PublicCategories struct {
	Merge      map[string]string `json:"merge"`
	FeedLabels map[string]string `json:"feedLabels"`
}

func LoadPublicationSettings() (PublicationSettings, error) {
	path := os.Getenv("SITE_PUBLICATION_FILE")
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "site", "publication.json"), filepath.Join("..", "shared", "site", "publication.json"), filepath.Join("/app", "shared", "site", "publication.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return PublicationSettings{}, errors.New("shared/site/publication.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PublicationSettings{}, err
	}
	var settings PublicationSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return PublicationSettings{}, err
	}
	if settings.Subject == "" {
		return PublicationSettings{}, errors.New("shared/site/publication.json has an empty subject")
	}
	if settings.AllFeedLeavesOut == nil {
		settings.AllFeedLeavesOut = []string{}
	}
	if settings.CommunityFeeds.Dev == nil {
		settings.CommunityFeeds.Dev = []string{}
	}
	if settings.CommunityFeeds.HN == nil {
		settings.CommunityFeeds.HN = []string{}
	}
	if settings.PublicCategories.Merge == nil {
		settings.PublicCategories.Merge = map[string]string{}
	}
	if settings.PublicCategories.FeedLabels == nil {
		settings.PublicCategories.FeedLabels = map[string]string{}
	}
	return settings, nil
}
