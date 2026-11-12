package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"aihot/backend-go/internal/mediaurl"
)

const siteHotFaceLimit = 6

type SiteHotResponse struct {
	ComputedAt  *string        `json:"computedAt"`
	WindowHours int            `json:"windowHours"`
	Entries     []SiteHotEntry `json:"entries"`
}

type SiteHotEntry struct {
	Rank             int                  `json:"rank"`
	Story            SiteHotStory         `json:"story"`
	Heat             float64              `json:"heat"`
	Trend            string               `json:"trend"`
	TrendPct         *float64             `json:"trendPct"`
	Badges           []string             `json:"badges"`
	ParticipantCount int                  `json:"participantCount"`
	SourceCount      int                  `json:"sourceCount"`
	SourceNames      []string             `json:"sourceNames"`
	Participants     []SiteHotParticipant `json:"participants"`
	Spark            []*float64           `json:"spark"`
	Summary          *string              `json:"summary"`
	Latest           *string              `json:"latest"`
	Cover            *SiteHotCover        `json:"cover"`
	StoryID          int64                `json:"-"`
	ItemID           *string              `json:"-"`
}

type SiteHotStory struct {
	PublicID string `json:"publicId"`
	Title    string `json:"title"`
}

type SiteHotParticipant struct {
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	IconURL    json.RawMessage `json:"iconUrl,omitempty"`
	IconSrcSet string          `json:"iconSrcSet,omitempty"`
}

type SiteHotCover struct {
	URL    string   `json:"url"`
	SrcSet string   `json:"srcSet,omitempty"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
}

type SiteHotRanking struct {
	ComputedAt time.Time
	Entries    []SiteHotRankingRecord
}

type SiteHotRankingRecord struct {
	ComputedAt           time.Time
	Rank                 int
	StoryID              int64
	Entry                json.RawMessage
	StoryPublicID        string
	StoryTitle           string
	RepresentativeURL    string
	RepresentativeSource *string
	LatestAt             time.Time
}

type SiteHotFaceRecord struct {
	Name string
	URL  *string
}

type SiteHotSparkRecord struct {
	StoryID int64
	Hour    time.Time
	Heat    float64
}

type SiteHotCoverRecord struct {
	StoryID int64
	URL     string
	Width   *float64
	Height  *float64
}

type SiteHotTexts struct {
	Summary *string
	Latest  *string
}

type SiteHotDetails struct {
	Sparks map[int64][]SiteHotSparkRecord
	Faces  []SiteHotFaceRecord
	Texts  map[int64]SiteHotTexts
	Covers map[int64]SiteHotCoverRecord
}

type SiteHotRepository interface {
	SiteHotRanking(context.Context, time.Time) (*SiteHotRanking, error)
	SiteHotDetails(context.Context, []SiteHotRankingRecord, time.Time, time.Time) (SiteHotDetails, error)
}

type storedSiteHotEntry struct {
	Heat             float64                `json:"heat"`
	Trend            string                 `json:"trend"`
	TrendPct         *float64               `json:"trendPct"`
	Badges           []string               `json:"badges"`
	ParticipantCount int                    `json:"participantCount"`
	SourceCount      int                    `json:"sourceCount"`
	SourceNames      []string               `json:"sourceNames"`
	Participants     []storedHotParticipant `json:"participants"`
}

type storedHotParticipant struct {
	Name string  `json:"name"`
	Kind string  `json:"kind"`
	Tier *string `json:"tier"`
}

func (s *Service) SiteHot(ctx context.Context, now time.Time, imageProxySecret string) (SiteHotResponse, error) {
	repository, ok := s.repository.(SiteHotRepository)
	if !ok {
		return SiteHotResponse{}, errors.New("site hot repository is unavailable")
	}
	now = now.UTC().Truncate(time.Millisecond)
	ranking, err := repository.SiteHotRanking(ctx, now)
	if err != nil {
		return SiteHotResponse{}, err
	}
	if ranking == nil {
		return SiteHotResponse{WindowHours: 48, Entries: []SiteHotEntry{}}, nil
	}
	details, err := repository.SiteHotDetails(ctx, ranking.Entries, ranking.ComputedAt, now)
	if err != nil {
		return SiteHotResponse{}, err
	}
	computedAt := formatV1Time(ranking.ComputedAt)
	result := SiteHotResponse{ComputedAt: &computedAt, WindowHours: 48, Entries: make([]SiteHotEntry, 0, len(ranking.Entries))}
	for _, entryRecord := range ranking.Entries {
		var stored storedSiteHotEntry
		if err := json.Unmarshal(entryRecord.Entry, &stored); err != nil {
			return SiteHotResponse{}, errors.New("invalid stored site hot entry")
		}
		entry := SiteHotEntry{
			Rank: entryRecord.Rank, Story: SiteHotStory{PublicID: entryRecord.StoryPublicID, Title: entryRecord.StoryTitle},
			Heat: stored.Heat, Trend: stored.Trend, TrendPct: stored.TrendPct,
			Badges: nonNilStrings(stored.Badges), ParticipantCount: stored.ParticipantCount,
			SourceCount: stored.SourceCount, SourceNames: siteHotSourceNames(stored.SourceNames),
			Spark:        siteHotSpark(details.Sparks[entryRecord.StoryID], ranking.ComputedAt),
			Participants: make([]SiteHotParticipant, 0, len(stored.Participants)),
			StoryID:      entryRecord.StoryID,
		}
		var identity struct {
			RepresentativeItemID *string `json:"representativeItemId"`
		}
		if err := json.Unmarshal(entryRecord.Entry, &identity); err != nil {
			return SiteHotResponse{}, errors.New("invalid stored site hot entry")
		}
		entry.ItemID = identity.RepresentativeItemID
		if text, found := details.Texts[entryRecord.StoryID]; found {
			entry.Summary, entry.Latest = text.Summary, text.Latest
		}
		participants, err := siteHotParticipants(stored.Participants, details.Faces, imageProxySecret, now)
		if err != nil {
			return SiteHotResponse{}, err
		}
		for _, person := range participants {
			entry.Participants = append(entry.Participants, person)
		}
		if cover, found := details.Covers[entryRecord.StoryID]; found {
			raw := cover.URL
			image, err := mediaurl.ProxyURL(&raw, "full", now, 48*time.Hour, imageProxySecret)
			if err != nil {
				return SiteHotResponse{}, err
			}
			if image != nil {
				set, err := mediaurl.ProxyURLSet(&raw, []string{"image-720", "image-1200", "image-1600"}, []int{720, 1200, 1600}, now, 48*time.Hour, imageProxySecret)
				if err != nil {
					return SiteHotResponse{}, err
				}
				entry.Cover = &SiteHotCover{URL: *image, Width: cover.Width, Height: cover.Height}
				if set != nil {
					entry.Cover.SrcSet = *set
				}
			}
		}
		if entry.Story.PublicID == "" {
			return SiteHotResponse{}, errors.New("invalid stored site hot story")
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func siteHotSourceNames(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, raw := range input {
		name := publicSourceName(raw)
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

func nonNilStrings(input []string) []string {
	if input == nil {
		return []string{}
	}
	return input
}

func siteHotSpark(rows []SiteHotSparkRecord, computedAt time.Time) []*float64 {
	end := time.Unix(computedAt.Unix()/3600*3600, 0).UTC()
	start := end.Add(-24 * time.Hour)
	result := make([]*float64, 25)
	for _, row := range rows {
		slot := int(mathRoundHour(row.Hour.Sub(start)))
		if slot < 0 || slot >= len(result) {
			continue
		}
		value := row.Heat
		result[slot] = &value
	}
	return result
}

func mathRoundHour(value time.Duration) int {
	return int((value + 30*time.Minute) / time.Hour)
}

func siteHotParticipants(input []storedHotParticipant, faceRows []SiteHotFaceRecord, secret string, now time.Time) ([]SiteHotParticipant, error) {
	faces := make(map[string]*string, len(faceRows))
	for _, face := range faceRows {
		faces[face.Name] = face.URL
	}
	type orderedPerson struct {
		person storedHotParticipant
		index  int
		face   *string
	}
	people := make([]orderedPerson, 0, len(input))
	for index, person := range input {
		people = append(people, orderedPerson{person: person, index: index, face: faces[person.Name]})
	}
	sort.SliceStable(people, func(i, j int) bool {
		a, b := people[i], people[j]
		if (a.person.Kind == "editorial") != (b.person.Kind == "editorial") {
			return a.person.Kind == "editorial"
		}
		if rank := siteHotTierRank(a.person.Tier); rank != siteHotTierRank(b.person.Tier) {
			return rank < siteHotTierRank(b.person.Tier)
		}
		if (a.face != nil) != (b.face != nil) {
			return a.face != nil
		}
		return a.index < b.index
	})
	seen := make(map[string]struct{}, len(people))
	result := make([]SiteHotParticipant, 0, len(people))
	for _, person := range people {
		name := publicSourceName(person.person.Name)
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		value := SiteHotParticipant{Name: name, Kind: person.person.Kind}
		if person.person.Kind == "editorial" && len(result) < siteHotFaceLimit {
			value.IconURL = json.RawMessage("null")
			if person.face != nil {
				proxy, err := mediaurl.ProxyURL(person.face, "avatar", now, 48*time.Hour, secret)
				if err != nil {
					return nil, err
				}
				if proxy != nil {
					encoded, _ := json.Marshal(*proxy)
					value.IconURL = encoded
				}
				set, err := mediaurl.ProxyURLSet(person.face, []string{"avatar-48", "avatar-96"}, []int{48, 96}, now, 48*time.Hour, secret)
				if err != nil {
					return nil, err
				}
				if set != nil {
					value.IconSrcSet = *set
				}
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func siteHotTierRank(tier *string) int {
	if tier == nil {
		return 3
	}
	switch *tier {
	case "T1":
		return 0
	case "T1_5":
		return 1
	case "T2":
		return 2
	default:
		return 3
	}
}
