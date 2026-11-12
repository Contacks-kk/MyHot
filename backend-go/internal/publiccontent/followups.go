package publiccontent

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/interests"
	"golang.org/x/text/unicode/norm"
)

const followupLimit = 8

var storyPublicIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var factSubjectSeparators = regexp.MustCompile(`[,，、;；+＋&＆/]`)

type FollowupMember struct {
	FactID        int64
	FactPublicID  string
	ID            string
	Title         string
	SourceName    string
	BodyMode      string
	Score         *float64
	TimelineAt    time.Time
	SortAt        time.Time
	SourceTier    string
	PublisherRole *string
	OwnerEntityID *string
	FactSubject   *string
}

type StoryFollowup struct {
	FactID         string `json:"factId"`
	Representative struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Source struct {
			Name string `json:"name"`
		} `json:"source"`
		TimelineAt string `json:"timelineAt"`
	} `json:"representative"`
}

type StoryFollowupsResponse struct {
	Items []StoryFollowup `json:"items"`
	More  bool            `json:"more"`
}

type followupFact struct {
	id             int64
	publicID       string
	first          time.Time
	representative FollowupMember
}

func (s *Service) StoryFollowups(ctx context.Context, publicID string, now time.Time) (*StoryFollowupsResponse, error) {
	if !storyPublicIDPattern.MatchString(publicID) {
		return nil, nil
	}
	members, err := s.repository.StoryFollowups(ctx, publicID, now)
	if err != nil || len(members) == 0 {
		return nil, err
	}
	byFact := make(map[int64][]FollowupMember)
	for _, member := range members {
		byFact[member.FactID] = append(byFact[member.FactID], member)
	}
	facts := make([]followupFact, 0, len(byFact))
	for id, reports := range byFact {
		representative := reports[0]
		first := reports[0].SortAt
		for _, report := range reports[1:] {
			if report.SortAt.Before(first) {
				first = report.SortAt
			}
			if preferredFollowup(report, representative, s.entities) {
				representative = report
			}
		}
		facts = append(facts, followupFact{id: id, publicID: reports[0].FactPublicID, first: first, representative: representative})
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].first.Equal(facts[j].first) {
			return facts[i].publicID < facts[j].publicID
		}
		return facts[i].first.After(facts[j].first)
	})
	more := len(facts) > followupLimit
	if more {
		facts = facts[:followupLimit]
	}
	items := make([]StoryFollowup, 0, len(facts))
	for _, fact := range facts {
		representative := fact.representative
		item := StoryFollowup{FactID: fact.publicID}
		item.Representative.ID = representative.ID
		item.Representative.Title = representative.Title
		item.Representative.Source.Name = publicSourceName(representative.SourceName)
		item.Representative.TimelineAt = representative.TimelineAt.UTC().Format("2006-01-02T15:04:05.000Z")
		items = append(items, item)
	}
	return &StoryFollowupsResponse{Items: items, More: more}, nil
}

func preferredFollowup(candidate, current FollowupMember, entities map[string]interests.Entity) bool {
	candidatePriority := followupPriority(candidate, entities)
	currentPriority := followupPriority(current, entities)
	if candidatePriority != currentPriority {
		return candidatePriority < currentPriority
	}
	if (candidate.BodyMode == "full") != (current.BodyMode == "full") {
		return candidate.BodyMode == "full"
	}
	candidateScore, currentScore := scoreValue(candidate.Score), scoreValue(current.Score)
	if candidateScore != currentScore {
		return candidateScore > currentScore
	}
	if !candidate.TimelineAt.Equal(current.TimelineAt) {
		return candidate.TimelineAt.Before(current.TimelineAt)
	}
	return candidate.ID < current.ID
}

func followupPriority(member FollowupMember, entities map[string]interests.Entity) int {
	if member.SourceTier == "T1" {
		return 0
	}
	if member.OwnerEntityID == nil || strings.TrimSpace(*member.OwnerEntityID) == "" || member.FactSubject == nil {
		return 3
	}
	owner := entityIdentity(*member.OwnerEntityID, entities)
	if owner == "" {
		return 3
	}
	subjects := factSubjectSeparators.Split(*member.FactSubject, -1)
	matched := false
	for _, value := range subjects {
		identity := entityIdentity(value, entities)
		if identity == "" {
			return 3
		}
		matched = matched || identity == owner
	}
	if !matched {
		return 3
	}
	if member.PublisherRole != nil {
		switch *member.PublisherRole {
		case "organization":
			return 1
		case "person":
			return 2
		}
	}
	return 3
}

func entityIdentity(value string, entities map[string]interests.Entity) string {
	want := normalizedIdentity(value)
	if want == "" {
		return ""
	}
	matched := ""
	for id, entity := range entities {
		aliases := append([]string{id, entity.Name}, entity.Aliases...)
		aliases = append(aliases, entity.OtherNames...)
		for _, alias := range aliases {
			if normalizedIdentity(alias) != want {
				continue
			}
			if matched != "" && matched != id {
				return ""
			}
			matched = id
		}
	}
	return matched
}

func normalizedIdentity(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
	return strings.Join(strings.Fields(value), "")
}

func scoreValue(score *float64) float64 {
	if score == nil {
		return 0
	}
	return *score
}
