// Package topics serves the topic index over the same selected-publication
// boundary used by the site. The industry pack owns the topic definitions.
package topics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"aihot/backend-go/internal/interests"
)

type Group struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
}

type Brand struct {
	Src      *string `json:"src"`
	Monogram string  `json:"monogram"`
	Raster   bool    `json:"raster"`
}

type Summary struct {
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	Group      string  `json:"group"`
	Definition string  `json:"definition"`
	Brand      *Brand  `json:"brand"`
	Total      int     `json:"total"`
	Recent     int     `json:"recent"`
	Indexable  bool    `json:"indexable"`
	Latest     *Latest `json:"latest"`
}

type Latest struct {
	Title string `json:"title"`
	At    string `json:"at"`
}

type Response struct {
	Groups []Group   `json:"groups"`
	Topics []Summary `json:"topics"`
}

type Definition struct {
	Slug       string
	Name       string
	Group      string
	EntityID   string
	Aliases    []string
	Definition string
	Tags       []string
	Pattern    *string
	Position   int
}

type Repository interface {
	Summaries(context.Context, []Definition, time.Time) ([]SummaryRow, error)
}

type SummaryRow struct {
	Slug  string
	ID    *string
	Title *string
	At    *time.Time
}

type Service struct {
	repository Repository
	groups     []Group
	topics     []Definition
	runtime    RuntimeConfig
}

type file struct {
	Groups []Group `json:"groups"`
	Topics []struct {
		Slug       string   `json:"slug"`
		Name       string   `json:"name"`
		Group      string   `json:"group"`
		Definition string   `json:"definition"`
		EntityID   string   `json:"entityId"`
		Aliases    []string `json:"aliases"`
		Tags       []string `json:"tags"`
	} `json:"topics"`
}

func LoadCatalog(entities map[string]interests.Entity) ([]Group, []Definition, error) {
	path := os.Getenv("INDUSTRY_TOPICS_FILE")
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "industry", "topics.json"), filepath.Join("..", "shared", "industry", "topics.json"), filepath.Join("/app", "shared", "industry", "topics.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, nil, errors.New("shared/industry/topics.json not found")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var catalog file
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, nil, err
	}
	if len(catalog.Groups) == 0 || len(catalog.Topics) == 0 {
		return nil, nil, errors.New("shared/industry/topics.json has no groups or topics")
	}
	groups := make(map[string]struct{}, len(catalog.Groups))
	for _, group := range catalog.Groups {
		if group.Key == "" || group.Name == "" {
			return nil, nil, errors.New("shared/industry/topics.json has an incomplete group")
		}
		if _, exists := groups[group.Key]; exists {
			return nil, nil, errors.New("shared/industry/topics.json has a duplicate group")
		}
		groups[group.Key] = struct{}{}
	}
	seen := make(map[string]struct{}, len(catalog.Topics))
	topics := make([]Definition, 0, len(catalog.Topics))
	for position, raw := range catalog.Topics {
		if raw.Slug == "" || raw.Name == "" || raw.Definition == "" {
			return nil, nil, errors.New("shared/industry/topics.json has an incomplete topic")
		}
		if _, exists := seen[raw.Slug]; exists {
			return nil, nil, errors.New("shared/industry/topics.json has a duplicate topic slug")
		}
		if _, exists := groups[raw.Group]; !exists {
			return nil, nil, errors.New("shared/industry/topics.json topic references an unknown group")
		}
		seen[raw.Slug] = struct{}{}
		tags := append([]string(nil), raw.Tags...)
		var pattern *string
		if raw.EntityID != "" {
			tags = []string{"entity:" + raw.EntityID}
			patternValue := topicPattern(raw.Name, raw.EntityID, entities)
			pattern = &patternValue
		} else if tags == nil {
			tags = []string{}
		}
		topics = append(topics, Definition{
			Slug:       raw.Slug,
			Name:       raw.Name,
			Group:      raw.Group,
			EntityID:   raw.EntityID,
			Aliases:    append([]string(nil), raw.Aliases...),
			Definition: raw.Definition,
			Tags:       tags,
			Pattern:    pattern,
			Position:   position})
	}
	return catalog.Groups, topics, nil
}

func topicPattern(name, entityID string, entities map[string]interests.Entity) string {
	terms := map[string]struct{}{name: {}}
	for _, part := range strings.Split(name, "/") {
		terms[strings.TrimSpace(part)] = struct{}{}
	}
	if entity, ok := entities[entityID]; ok {
		terms[entity.Name] = struct{}{}
		for _, term := range entity.Aliases {
			terms[term] = struct{}{}
		}
		for _, term := range entity.OtherNames {
			terms[term] = struct{}{}
		}
	}
	parts := make([]string, 0, len(terms))
	for term := range terms {
		if term != "" {
			parts = append(parts, escapePostgresRegex(term))
		}
	}
	return `(?<![A-Za-z])(` + strings.Join(parts, "|") + `)(?![A-Za-z])`
}

func escapePostgresRegex(value string) string {
	var escaped strings.Builder
	for _, char := range value {
		if strings.ContainsRune(`\.^$|()[]{}*+?`, char) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}

func NewService(repository Repository, groups []Group, definitions []Definition, runtime ...RuntimeConfig) *Service {
	config := RuntimeConfig{XPostIsFullText: true}
	if len(runtime) > 0 {
		config = runtime[0]
	}
	return &Service{repository: repository, groups: groups, topics: definitions, runtime: config}
}

func (s *Service) List(ctx context.Context, now time.Time) (Response, error) {
	rows, err := s.repository.Summaries(ctx, s.topics, now)
	if err != nil {
		return Response{}, err
	}
	bySlug := make(map[string][]SummaryRow, len(s.topics))
	for _, row := range rows {
		bySlug[row.Slug] = append(bySlug[row.Slug], row)
	}
	response := Response{Groups: s.groups, Topics: make([]Summary, 0, len(s.topics))}
	cutoff := now.Add(-30 * 24 * time.Hour)
	for _, topic := range s.topics {
		seats := bySlug[topic.Slug]
		summary := Summary{Slug: topic.Slug, Name: topic.Name, Group: topic.Group, Definition: topic.Definition}
		if topic.Group == "company" {
			summary.Brand = &Brand{Monogram: monogram(topic.Name)}
		}
		for _, seat := range seats {
			if seat.ID == nil {
				continue
			}
			summary.Total++
			if seat.At != nil && seat.At.After(cutoff) {
				summary.Recent++
			}
			if summary.Latest == nil && seat.Title != nil && seat.At != nil {
				summary.Latest = &Latest{Title: *seat.Title, At: seat.At.UTC().Format("2006-01-02T15:04:05.000Z")}
			}
		}
		summary.Indexable = summary.Total >= 50 || summary.Total >= 20 && summary.Recent > 0
		response.Topics = append(response.Topics, summary)
	}
	return response, nil
}

func monogram(name string) string {
	for _, char := range name {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			return strings.ToUpper(string(char))
		}
	}
	return ""
}
