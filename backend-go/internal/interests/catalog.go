package interests

type CatalogCategory struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	PublicKey string `json:"publicKey"`
	FeedLabel string `json:"feedLabel"`
}

type CatalogTopicGroup struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
}

type CatalogTopic struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	Definition string `json:"definition"`
}

type Catalog struct {
	Subject     string              `json:"subject"`
	Categories  []CatalogCategory   `json:"categories"`
	TopicGroups []CatalogTopicGroup `json:"topicGroups"`
	Topics      []CatalogTopic      `json:"topics"`
	ReleaseUnit *string             `json:"releaseUnit,omitempty"`
}

type CatalogService struct {
	catalog Catalog
}

/** NewCatalogService prepares the display-only catalog from runtime configuration. */
func NewCatalogService(keys []string, labels, feedLabels map[string]string, publication PublicationSettings, groups []CatalogTopicGroup, topics []CatalogTopic, releaseUnit ...*string) *CatalogService {
	categories := make([]CatalogCategory, 0, len(keys))
	for _, key := range keys {
		publicKey := key
		if merged, ok := publication.PublicCategories.Merge[key]; ok {
			publicKey = merged
		}
		feedLabel := publication.PublicCategories.FeedLabels[publicKey]
		if feedLabel == "" {
			feedLabel = feedLabels[publicKey]
		}
		if feedLabel == "" {
			feedLabel = labels[publicKey]
		}
		categories = append(categories, CatalogCategory{
			Key:       key,
			Label:     labels[key],
			PublicKey: publicKey,
			FeedLabel: feedLabel,
		})
	}
	var unit *string
	if len(releaseUnit) > 0 {
		unit = releaseUnit[0]
	}
	return &CatalogService{catalog: Catalog{
		Subject:     publication.Subject,
		Categories:  categories,
		TopicGroups: append([]CatalogTopicGroup{}, groups...),
		Topics:      append([]CatalogTopic{}, topics...),
		ReleaseUnit: unit,
	}}
}

/** Display returns the public display fields of the catalog. */
func (s *CatalogService) Display() Catalog {
	return s.catalog
}
