package interests

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Entity struct {
	Name       string   `json:"name"`
	DisplayTag *string  `json:"displayTag"`
	Aliases    []string `json:"aliases"`
	OtherNames []string `json:"otherNames"`
}

func LoadEntities() (map[string]Entity, error) {
	path := os.Getenv("INDUSTRY_ENTITIES_FILE")
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "industry", "entities.json"), filepath.Join("..", "shared", "industry", "entities.json"), filepath.Join("/app", "shared", "industry", "entities.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("shared/industry/entities.json not found")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entities map[string]Entity
	if err := json.Unmarshal(b, &entities); err != nil {
		return nil, err
	}
	return entities, nil
}

type EntityEntry struct {
	ID     string
	Entity Entity
}

// LoadEntityEntries preserves entities.json order for prompts whose receipt identity depends on it.
func LoadEntityEntries() ([]EntityEntry, error) {
	path := os.Getenv("INDUSTRY_ENTITIES_FILE")
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "industry", "entities.json"), filepath.Join("..", "shared", "industry", "entities.json"), filepath.Join("/app", "shared", "industry", "entities.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("shared/industry/entities.json not found")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("shared/industry/entities.json must be an object")
	}
	entries := make([]EntityEntry, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := token.(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, errors.New("shared/industry/entities.json contains an invalid entity id")
		}
		var entity Entity
		if err := decoder.Decode(&entity); err != nil {
			return nil, err
		}
		entries = append(entries, EntityEntry{ID: id, Entity: entity})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return entries, nil
}

func LoadCategoryKeys() ([]string, error) {
	categories, err := LoadCategories()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(categories))
	for _, category := range categories {
		keys = append(keys, category.Key)
	}
	return keys, nil
}

type Category struct {
	Key        string
	Label      string
	FeedLabel  string
	Section    string
	Guide      string
	Commentary bool
}

func LoadCategories() ([]Category, error) {
	path := os.Getenv("INDUSTRY_CATEGORIES_FILE")
	if path == "" {
		for _, candidate := range []string{filepath.Join("shared", "industry", "categories.json"), filepath.Join("..", "shared", "industry", "categories.json"), filepath.Join("/app", "shared", "industry", "categories.json")} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("shared/industry/categories.json not found")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var categories []struct {
		Key        string `json:"key"`
		Label      string `json:"label"`
		FeedLabel  string `json:"feedLabel"`
		Section    string `json:"section"`
		Guide      string `json:"guide"`
		Commentary bool   `json:"commentary"`
	}
	if err := json.Unmarshal(b, &categories); err != nil {
		return nil, err
	}
	if len(categories) == 0 {
		return nil, errors.New("shared/industry/categories.json has no categories")
	}
	loaded := make([]Category, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		if strings.TrimSpace(category.Key) == "" {
			return nil, errors.New("shared/industry/categories.json has an empty category key")
		}
		if _, exists := seen[category.Key]; exists {
			return nil, errors.New("shared/industry/categories.json has duplicate category keys")
		}
		seen[category.Key] = struct{}{}
		loaded = append(loaded, Category{Key: category.Key, Label: category.Label, FeedLabel: category.FeedLabel, Section: category.Section, Guide: category.Guide, Commentary: category.Commentary})
	}
	return loaded, nil
}

func Match(title, body string, keywords, entityIDs []string, entities map[string]Entity) []string {
	text := strings.ToLower(title + "\n" + body)
	var hits []string
	for _, keyword := range keywords {
		if keyword = strings.TrimSpace(keyword); keyword != "" && strings.Contains(text, strings.ToLower(keyword)) {
			hits = append(hits, "关键词："+keyword)
		}
	}
	for _, id := range entityIDs {
		entity, ok := entities[id]
		if !ok {
			continue
		}
		terms := append(append([]string{}, entity.Aliases...), entity.Name)
		terms = append(terms, entity.OtherNames...)
		for _, term := range terms {
			if term = strings.TrimSpace(term); term != "" && strings.Contains(text, strings.ToLower(term)) {
				hits = append(hits, "实体："+entity.Name)
				break
			}
		}
	}
	return hits
}
