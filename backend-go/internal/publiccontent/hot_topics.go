package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type HotTopicRecord struct {
	Rank                 int
	Entry                json.RawMessage
	StoryPublicID        string
	StoryTitle           string
	RepresentativeURL    string
	RepresentativeSource *string
	LatestAt             time.Time
}

type HotTopicsResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Count         int            `json:"count"`
	Items         []HotTopicItem `json:"items"`
}

type HotTopicItem struct {
	Rank             int       `json:"rank"`
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Source           HotSource `json:"source"`
	Links            HotLinks  `json:"links"`
	SourceCount      int       `json:"sourceCount"`
	SignalCount      int       `json:"signalCount"`
	ParticipantCount int       `json:"participantCount"`
	SourceNames      []string  `json:"sourceNames"`
	LatestAt         string    `json:"latestAt"`
}

type HotSource struct {
	Name string `json:"name"`
}

type HotLinks struct {
	AIHOT    string `json:"aihot"`
	Original string `json:"original"`
	Story    string `json:"story"`
}

type storedHotTopic struct {
	StoryPublicID        string   `json:"storyPublicId"`
	RepresentativeItemID *string  `json:"representativeItemId"`
	SourceCount          int      `json:"sourceCount"`
	SignalCount          int      `json:"signalCount"`
	ParticipantCount     int      `json:"participantCount"`
	SourceNames          []string `json:"sourceNames"`
}

func (s *Service) HotTopics(ctx context.Context, now time.Time, siteURL, siteName string) (HotTopicsResponse, error) {
	repository, ok := s.repository.(HotTopicRepository)
	if !ok {
		return HotTopicsResponse{}, errors.New("hot topics repository is unavailable")
	}
	records, err := repository.HotTopics(ctx, now)
	if err != nil {
		return HotTopicsResponse{}, err
	}
	base := strings.TrimRight(siteURL, "/")
	items := make([]HotTopicItem, 0, len(records))
	for _, record := range records {
		var entry storedHotTopic
		if err := json.Unmarshal(record.Entry, &entry); err != nil {
			return HotTopicsResponse{}, errors.New("invalid stored hot topic")
		}
		// The legacy publication layer drops a saved hot entry when its representative no longer
		// has current public evidence. The repository applies that live check before returning it.
		if entry.RepresentativeItemID == nil || *entry.RepresentativeItemID == "" {
			continue
		}
		id := *entry.RepresentativeItemID
		storyURL := base + "/story/" + record.StoryPublicID
		sourceName := siteName
		if len(entry.SourceNames) > 0 {
			sourceName = entry.SourceNames[0]
		}
		if record.RepresentativeSource != nil {
			sourceName = *record.RepresentativeSource
		}
		sourceNames := entry.SourceNames
		if sourceNames == nil {
			sourceNames = []string{}
		}
		items = append(items, HotTopicItem{
			Rank: record.Rank, ID: id, Title: record.StoryTitle, Source: HotSource{Name: sourceName},
			Links:       HotLinks{AIHOT: base + "/items/" + id, Original: record.RepresentativeURL, Story: storyURL},
			SourceCount: entry.SourceCount, SignalCount: entry.SignalCount, ParticipantCount: entry.ParticipantCount,
			SourceNames: sourceNames, LatestAt: record.LatestAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
		})
	}
	return HotTopicsResponse{SchemaVersion: 1, Count: len(items), Items: items}, nil
}
