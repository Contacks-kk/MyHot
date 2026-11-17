package admindeliveries

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"aihot/backend-go/internal/publication"
)

type shareImageSource struct {
	Name string `json:"name"`
}

type shareImageItem struct {
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Summary       *string          `json:"summary"`
	TimelineAt    string           `json:"timelineAt"`
	Category      *string          `json:"category"`
	CategoryLabel string           `json:"categoryLabel,omitempty"`
	Score         *int             `json:"score"`
	Selected      bool             `json:"selected"`
	Source        shareImageSource `json:"source"`
}

type shareImageRequest struct {
	ID   string         `json:"id"`
	Item shareImageItem `json:"item"`
}

// HTTPShareImageWarmer asks the Web presentation process to render the article's Open Graph image.
// The card data is signed so this private rendering endpoint cannot be used by public callers.
type HTTPShareImageWarmer struct {
	URL            string
	Secret         string
	Client         *http.Client
	CategoryLabels map[string]string
}

func (w HTTPShareImageWarmer) Warm(ctx context.Context, content SelectedContent) error {
	if strings.TrimSpace(w.URL) == "" || strings.TrimSpace(w.Secret) == "" {
		return errors.New("share image prewarm is not configured")
	}
	item := shareImageItem{ID: content.ArticleID, Title: content.Title, Summary: content.Summary,
		TimelineAt: content.TimelineAt.In(beijingLocation()).Format(time.RFC3339Nano), Category: content.Category,
		Selected: content.Selected, Source: shareImageSource{Name: publication.PublicSourceName(content.SourceName)}}
	if content.Category != nil {
		item.CategoryLabel = w.CategoryLabels[*content.Category]
	}
	if content.Selected && content.Score != nil {
		score := int(math.Round(*content.Score))
		item.Score = &score
	}
	body, err := json.Marshal(shareImageRequest{ID: content.ArticleID, Item: item})
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(w.Secret))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-OG-Prewarm-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("share image prewarm returned HTTP %d", response.StatusCode)
	}
	return nil
}

func beijingLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return location
}
