package grouping

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrSuperseded = errors.New("grouping input was superseded by a newer revision")

type ArticleInput struct {
	ID                string
	Revision          int
	Title             string
	URL               string
	PublishedAt       *time.Time
	DiscoveredAt      time.Time
	Backfill          bool
	BodyText          *string
	XPost             XPost `gorm:"-"`
	SourceID          string
	SourceName        string
	SignalGroupID     *string
	FirstParty        bool
	ParticipationMode string
}

type XPost struct {
	TweetID string  `json:"tweetId"`
	ReplyTo *string `json:"replyTo"`
	Quoted  *struct {
		URL string `json:"url"`
	} `json:"quoted"`
}

type AnalysisInput struct {
	InputRevision int
	Relevance     string
	Title         *string
	Summary       *string
	Output        map[string]any
	Composite     bool
}

type GroupingStart struct {
	Revision           int
	WasComplete        bool
	SelectionAddsValue *bool
}

type ManualState struct {
	Found  bool
	FactID *int64
}

type CurrentMembership struct {
	FactID  int64
	StoryID int64
}

type DecisionCandidate struct {
	ID         int64    `json:"id"`
	Score      float64  `json:"score"`
	Relation   Relation `json:"relation,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type CommitInput struct {
	Article         ArticleInput
	Verdict         string
	Selection       *SelectionValue
	Candidates      []DecisionCandidate
	FactID          *int64
	StoryID         *int64
	CreateStory     bool
	CreateFact      bool
	FactTitle       string
	Frame           *Frame
	Evidence        *string
	Conditions      *string
	ReceiptID       *int64
	ObservedAt      time.Time
	FirstParty      bool
	SignalGroupID   *string
	MentionFacts    []int64
	RemoveAutomatic bool
	SkipDecision    bool
	AddMembership   bool
	Signal          bool
}

type CommitResult struct {
	FactID      *int64
	StoryID     *int64
	Manual      ManualState
	LeftStories []int64
}

func (r *GormRepository) BeginGrouping(ctx context.Context, articleID string) (GroupingStart, bool, error) {
	if err := validateRepository(r); err != nil {
		return GroupingStart{}, false, err
	}
	var result GroupingStart
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			Revision  int    `gorm:"column:revision"`
			Status    string `gorm:"column:grouping_status"`
			Selection *bool  `gorm:"column:selection_adds_value"`
		}
		if err := tx.Raw(`SELECT revision, grouping_status, selection_adds_value FROM articles WHERE id = ? FOR UPDATE`, articleID).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		found = true
		result = GroupingStart{Revision: rows[0].Revision, WasComplete: rows[0].Status == "complete", SelectionAddsValue: rows[0].Selection}
		return tx.Exec(`UPDATE articles SET grouping_status='pending', grouped_at=NULL, grouping_error=NULL, grouping_receipt_id=NULL WHERE id=?`, articleID).Error
	})
	return result, found, err
}

func (r *GormRepository) ArticleForGrouping(ctx context.Context, articleID string) (*ArticleInput, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []struct {
		ArticleInput
		XPostJSON json.RawMessage `gorm:"column:x_post"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT a.id, a.revision, a.title, a.url, a.published_at, a.discovered_at, a.backfill,
		a.body_text, a.x_post, s.id AS source_id, s.name AS source_name, s.signal_group_id,
		(s.tier='T1') AS first_party, s.participation_mode
		FROM articles a JOIN sources s ON s.id=a.source_id WHERE a.id=?`, articleID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	result := rows[0].ArticleInput
	if len(rows[0].XPostJSON) != 0 && string(rows[0].XPostJSON) != "null" {
		if err := json.Unmarshal(rows[0].XPostJSON, &result.XPost); err != nil {
			return nil, fmt.Errorf("decode article social identity: %w", err)
		}
	}
	return &result, nil
}

func (r *GormRepository) LatestAnalysis(ctx context.Context, articleID string) (*AnalysisInput, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []struct {
		InputRevision int             `gorm:"column:input_revision"`
		Relevance     *string         `gorm:"column:relevance"`
		Title         *string         `gorm:"column:title_zh"`
		Summary       *string         `gorm:"column:summary_zh"`
		Output        json.RawMessage `gorm:"column:output"`
		Composite     bool            `gorm:"column:composite"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT input_revision, relevance, title_zh, summary_zh, output,
		coalesce(output->>'scope'='composite', false) AS composite FROM analyses WHERE article_id=?
		ORDER BY input_revision DESC, id DESC LIMIT 1`, articleID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	result := &AnalysisInput{InputRevision: rows[0].InputRevision, Title: rows[0].Title, Summary: rows[0].Summary, Composite: rows[0].Composite}
	if rows[0].Relevance != nil {
		result.Relevance = *rows[0].Relevance
	}
	if len(rows[0].Output) > 0 && string(rows[0].Output) != "null" {
		if err := json.Unmarshal(rows[0].Output, &result.Output); err != nil {
			return nil, fmt.Errorf("decode latest analysis output: %w", err)
		}
	}
	return result, nil
}

func (r *GormRepository) ManualDecision(ctx context.Context, articleID string) (ManualState, error) {
	if err := validateRepository(r); err != nil {
		return ManualState{}, err
	}
	var rows []struct {
		FactID int64 `gorm:"column:fact_id"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT fact_id FROM fact_articles WHERE article_id=? AND manual ORDER BY created_at LIMIT 1`, articleID).Scan(&rows).Error; err != nil {
		return ManualState{}, err
	}
	if len(rows) > 0 {
		id := rows[0].FactID
		return ManualState{Found: true, FactID: &id}, nil
	}
	var overrides []struct {
		Exists bool `gorm:"column:exists"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM grouping_overrides WHERE article_id=?) AS exists`, articleID).Scan(&overrides).Error; err != nil {
		return ManualState{}, err
	}
	return ManualState{Found: len(overrides) > 0 && overrides[0].Exists}, nil
}

func (r *GormRepository) CurrentMembership(ctx context.Context, articleID string) (*CurrentMembership, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	var rows []CurrentMembership
	if err := r.db.WithContext(ctx).Raw(`SELECT fa.fact_id, f.story_id FROM fact_articles fa JOIN facts f ON f.id=fa.fact_id
		JOIN stories st ON st.id=f.story_id WHERE fa.article_id=? AND fa.role IN ('primary','report')
		AND st.merged_into IS NULL ORDER BY (fa.role='primary') DESC, fa.created_at LIMIT 1`, articleID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (r *GormRepository) FailGrouping(ctx context.Context, articleID string, revision int, receiptID *int64, failure error, shutdown bool) error {
	if err := validateRepository(r); err != nil {
		return err
	}
	status := "failed"
	if shutdown {
		status = "pending"
	}
	return r.db.WithContext(ctx).Exec(`UPDATE articles SET grouping_status=?, grouped_at=NULL, grouping_receipt_id=?, grouping_error=?
		WHERE id=? AND revision=? AND grouping_status<>'complete'`, status, receiptID, truncateUTF16(fmt.Sprint(failure), 2000), articleID, revision).Error
}

func (r *GormRepository) CommitGrouping(ctx context.Context, input CommitInput) (CommitResult, error) {
	if err := validateRepository(r); err != nil {
		return CommitResult{}, err
	}
	var result CommitResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current []struct {
			Revision int `gorm:"column:revision"`
		}
		if err := tx.Raw(`SELECT revision FROM articles WHERE id=? FOR UPDATE`, input.Article.ID).Scan(&current).Error; err != nil {
			return err
		}
		if len(current) == 0 || current[0].Revision != input.Article.Revision {
			return ErrSuperseded
		}
		manual, err := manualDecisionTx(tx, input.Article.ID)
		if err != nil {
			return err
		}
		if manual.Found {
			result.Manual = manual
			return markGroupingComplete(tx, input.Article.ID, input.Article.Revision, &SelectionValue{AddsValue: true, Reason: "人工确认的归属"})
		}
		if input.RemoveAutomatic {
			if err := tx.Raw(`SELECT DISTINCT f.story_id FROM fact_articles fa JOIN facts f ON f.id=fa.fact_id
				WHERE fa.article_id=? AND NOT fa.manual AND fa.role IN ('primary','report') AND f.story_id IS NOT NULL`, input.Article.ID).Scan(&result.LeftStories).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM fact_articles WHERE article_id=? AND NOT manual`, input.Article.ID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM story_signals WHERE article_id=?`, input.Article.ID).Error; err != nil {
				return err
			}
		}
		storyID, factID := input.StoryID, input.FactID
		if input.CreateStory && storyID == nil {
			publicID, err := newUUID()
			if err != nil {
				return err
			}
			createdAt := input.ObservedAt
			story := model.Story{PublicID: publicID, Title: input.FactTitle, Status: "active", FirstReportAt: &createdAt,
				LatestAt: &createdAt, Version: 1, Origin: "model"}
			if err := tx.Create(&story).Error; err != nil {
				return err
			}
			storyID = &story.ID
		} else if storyID != nil {
			var live []struct {
				ID int64 `gorm:"column:id"`
			}
			if err := tx.Raw(`SELECT id FROM stories WHERE id=? AND merged_into IS NULL FOR UPDATE`, *storyID).Scan(&live).Error; err != nil {
				return err
			}
			if len(live) == 0 {
				return errors.New("grouping target changed; retry against the current story")
			}
		}
		if input.CreateFact && factID == nil {
			publicID, err := newFactID()
			if err != nil {
				return err
			}
			frame := input.Frame
			var occurredAt *time.Time
			if frame != nil && frame.OccurredAt != nil {
				if parsed, err := time.Parse("2006-01-02", *frame.OccurredAt); err == nil {
					value := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.FixedZone("UTC+08", 8*60*60))
					occurredAt = &value
				}
			}
			var subject, action, object, conditions *string
			if frame != nil {
				subject, action, object = frame.Subject, frame.Action, frame.Object
			}
			conditions = input.Conditions
			fact := model.Fact{PublicID: publicID, StoryID: storyID, Title: input.FactTitle, Subject: subject, Action: action, Object: object,
				Conditions: conditions, OccurredAt: occurredAt, CreatedAt: input.ObservedAt}
			if err := tx.Create(&fact).Error; err != nil {
				return err
			}
			factID = &fact.ID
		}
		if input.AddMembership && factID != nil && storyID != nil && input.Verdict != "kept" {
			var primary []struct {
				Count int `gorm:"column:count"`
			}
			if err := tx.Raw(`SELECT count(*)::int AS count FROM fact_articles WHERE fact_id=? AND role='primary'`, *factID).Scan(&primary).Error; err != nil {
				return err
			}
			role := "report"
			if input.FirstParty && (len(primary) == 0 || primary[0].Count == 0) {
				role = "primary"
			}
			membership := model.FactArticle{FactID: *factID, ArticleID: input.Article.ID, Role: role, Evidence: input.Evidence, CreatedAt: input.ObservedAt}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&membership).Error; err != nil {
				return err
			}
			if err := updateStoryAndSignal(tx, *storyID, input); err != nil {
				return err
			}
		}
		if input.Signal && storyID != nil {
			if err := updateStoryAndSignal(tx, *storyID, input); err != nil {
				return err
			}
		}
		for _, mentionID := range input.MentionFacts {
			mention := model.FactArticle{FactID: mentionID, ArticleID: input.Article.ID, Role: "mention", CreatedAt: input.ObservedAt}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention).Error; err != nil {
				return err
			}
		}
		if !input.SkipDecision {
			candidateRows := input.Candidates
			if candidateRows == nil {
				candidateRows = []DecisionCandidate{}
			}
			candidates, err := json.Marshal(candidateRows)
			if err != nil {
				return err
			}
			decision := model.GroupingDecision{ArticleID: input.Article.ID, FactID: factID, StoryID: storyID,
				Verdict: input.Verdict, Candidates: json.RawMessage(candidates), ReceiptID: input.ReceiptID}
			if err := tx.Create(&decision).Error; err != nil {
				return err
			}
		}
		if err := markGroupingComplete(tx, input.Article.ID, input.Article.Revision, input.Selection); err != nil {
			return err
		}
		result.FactID, result.StoryID = factID, storyID
		return nil
	})
	return result, err
}

func manualDecisionTx(tx *gorm.DB, articleID string) (ManualState, error) {
	var rows []struct {
		FactID int64 `gorm:"column:fact_id"`
	}
	if err := tx.Raw(`SELECT fact_id FROM fact_articles WHERE article_id=? AND manual ORDER BY created_at LIMIT 1`, articleID).Scan(&rows).Error; err != nil {
		return ManualState{}, err
	}
	if len(rows) > 0 {
		id := rows[0].FactID
		return ManualState{Found: true, FactID: &id}, nil
	}
	var exists []struct {
		Value bool `gorm:"column:exists"`
	}
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM grouping_overrides WHERE article_id=?) AS exists`, articleID).Scan(&exists).Error; err != nil {
		return ManualState{}, err
	}
	return ManualState{Found: len(exists) > 0 && exists[0].Value}, nil
}

func markGroupingComplete(tx *gorm.DB, articleID string, revision int, selection *SelectionValue) error {
	values := map[string]any{"grouped_at": gorm.Expr("coalesce(grouped_at, now())"), "grouping_status": "complete", "grouping_receipt_id": nil, "grouping_error": nil}
	if selection != nil {
		values["selection_adds_value"], values["selection_value_reason"] = selection.AddsValue, selection.Reason
	}
	result := tx.Table("articles").Where("id=? AND revision=?", articleID, revision).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSuperseded
	}
	return nil
}

func updateStoryAndSignal(tx *gorm.DB, storyID int64, input CommitInput) error {
	if err := tx.Exec(`UPDATE stories SET latest_at=GREATEST(coalesce(latest_at,?),?), first_report_at=LEAST(coalesce(first_report_at,?),?),updated_at=now() WHERE id=? AND merged_into IS NULL`, input.ObservedAt, input.ObservedAt, input.ObservedAt, input.ObservedAt, storyID).Error; err != nil {
		return err
	}
	participant := "source:" + input.Article.SourceID
	if input.SignalGroupID != nil && strings.TrimSpace(*input.SignalGroupID) != "" {
		participant = "group:" + *input.SignalGroupID
	}
	kind := "editorial"
	if input.Signal {
		kind = "signal"
	}
	signal := model.StorySignal{StoryID: storyID, ArticleID: input.Article.ID, ParticipantKey: participant,
		SourceID: input.Article.SourceID, Kind: kind, ObservedAt: input.ObservedAt}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&signal).Error
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func newFactID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "f" + hex.EncodeToString(value[:]), nil
}
