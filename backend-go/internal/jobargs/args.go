package jobargs

type SharedBodyArgs struct {
	ArticleID string `json:"articleId" river:"unique"`
}

/** Kind returns the stable River kind for shared-body extraction jobs. */
func (SharedBodyArgs) Kind() string { return "myhot_shared_body_extract" }

type ContentAnalyzeArgs struct {
	ArticleID  string `json:"articleId" river:"unique"`
	AttemptTag string `json:"attemptTag,omitempty" river:"unique"`
}

/** Kind returns the stable River kind for shared-content analysis jobs. */
func (ContentAnalyzeArgs) Kind() string { return "content.analyze" }

type EventGroupArgs struct {
	ArticleID  string `json:"articleId" river:"unique"`
	SignalOnly bool   `json:"signalOnly,omitempty"`
}

/** Kind returns the stable River kind for event-grouping jobs. */
func (EventGroupArgs) Kind() string { return "events.group" }

type StoryDigestArgs struct {
	StoryID int64 `json:"storyId" river:"unique"`
}

/** Kind returns the stable River kind for story-digest jobs. */
func (StoryDigestArgs) Kind() string { return "myhot_story_digest" }

type HotRankingArgs struct{}

/** Kind returns the stable River kind for hot-ranking jobs. */
func (HotRankingArgs) Kind() string { return "myhot_hot_ranking" }
