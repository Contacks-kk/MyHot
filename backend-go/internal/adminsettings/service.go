package adminsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Repository interface {
	Overview(context.Context) (Overview, error)
	Contact(context.Context) (Contact, error)
	SetTargetEnabled(context.Context, string, bool, string, string) (map[string]any, bool, error)
	UpdateBudget(context.Context, string, BudgetInput, string) (map[string]any, error)
	ReplaceContactQR(context.Context, string, string, string) (Contact, error)
}

type Config struct {
	FeishuContentPushEnabled bool
	DataDir                  string
}

type Service struct {
	repository Repository
	config     Config
}

func NewService(repository Repository, cfg Config) *Service {
	return &Service{repository: repository, config: cfg}
}

type Contact struct {
	WechatQR *string                    `json:"wechatQr"`
	FeishuQR *string                    `json:"feishuQr"`
	Extra    map[string]json.RawMessage `json:"-"`
}

func (c *Contact) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if value, ok := fields["wechatQr"]; ok {
		if err := json.Unmarshal(value, &c.WechatQR); err != nil {
			return err
		}
		delete(fields, "wechatQr")
	}
	if value, ok := fields["feishuQr"]; ok {
		if err := json.Unmarshal(value, &c.FeishuQR); err != nil {
			return err
		}
		delete(fields, "feishuQr")
	}
	c.Extra = fields
	return nil
}

func (c Contact) MarshalJSON() ([]byte, error) {
	wechatQR, err := json.Marshal(c.WechatQR)
	if err != nil {
		return nil, err
	}
	feishuQR, err := json.Marshal(c.FeishuQR)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage, len(c.Extra)+2)
	for key, value := range c.Extra {
		if key != "wechatQr" && key != "feishuQr" {
			fields[key] = value
		}
	}
	fields["wechatQr"] = wechatQR
	fields["feishuQr"] = feishuQR
	return json.Marshal(fields)
}

type Target struct {
	Key          string     `json:"key"`
	Purpose      string     `json:"purpose"`
	Kind         string     `json:"kind"`
	Enabled      bool       `json:"enabled"`
	EnabledAt    *time.Time `json:"enabled_at"`
	ConfigRef    *string    `json:"config_ref"`
	Note         *string    `json:"note"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Deliveries7d int        `json:"deliveries_7d" gorm:"column:deliveries_7d"`
	LastSentAt   *time.Time `json:"last_sent_at"`
}

type Budget struct {
	Service   string    `json:"service"`
	PerMinute int       `json:"per_minute"`
	PerHour   int       `json:"per_hour"`
	PerDay    int       `json:"per_day"`
	Note      *string   `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
	UsedDay   int       `json:"used_day" gorm:"column:used_day"`
	UsedHour  int       `json:"used_hour" gorm:"column:used_hour"`
}

type Overview struct {
	Contact Contact  `json:"contact"`
	Targets []Target `json:"targets"`
	Budgets []Budget `json:"budgets"`
}

type BudgetInput struct {
	PerMinute int    `json:"perMinute"`
	PerHour   int    `json:"perHour"`
	PerDay    int    `json:"perDay"`
	Reason    string `json:"reason"`
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	return s.repository.Overview(ctx)
}

func (s *Service) Contact(ctx context.Context) (Contact, error) {
	return s.repository.Contact(ctx)
}

func (s *Service) SetTargetEnabled(ctx context.Context, key string, enabled bool, reason, actor string) (map[string]any, bool, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, false, errors.New("reason is required")
	}
	result, found, err := s.repository.SetTargetEnabled(ctx, key, enabled, reason, actor)
	if err != nil || !found {
		return result, found, err
	}
	result["pushEnabledHere"] = s.config.FeishuContentPushEnabled
	return result, true, nil
}

func (s *Service) UpdateBudget(ctx context.Context, service string, input BudgetInput, actor string) (map[string]any, error) {
	if strings.TrimSpace(input.Reason) == "" {
		return nil, errors.New("reason is required")
	}
	if input.PerMinute < 0 || input.PerHour < 0 || input.PerDay < 0 {
		return nil, errors.New("budgets are non-negative integers (0 stops the service)")
	}
	return s.repository.UpdateBudget(ctx, service, input, actor)
}

func (s *Service) ReplaceContactQR(ctx context.Context, slot string, data []byte, actor string) (Contact, error) {
	if slot != "wechatQr" && slot != "feishuQr" {
		return Contact{}, errors.New("unknown slot")
	}
	if len(data) > 2<<20 {
		return Contact{}, errors.New("二维码图片最大 2MB")
	}
	format, width, height, err := qrImageConfig(data)
	if err != nil || (format != "png" && format != "jpeg" && format != "webp") {
		return Contact{}, errors.New("需要 PNG、JPG 或 WebP 图片")
	}
	if width < 120 || height < 120 {
		return Contact{}, errors.New("图片太小，二维码可能扫不出来")
	}
	ext := format
	if ext == "jpeg" {
		ext = "jpg"
	}
	slotName := "wechat"
	if slot == "feishuQr" {
		slotName = "feishu"
	}
	digest := sha256.Sum256(data)
	name := fmt.Sprintf("qr-%s-%s.%s", slotName, hex.EncodeToString(digest[:])[:8], ext)
	dir := filepath.Join(s.config.DataDir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Contact{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return Contact{}, err
	}
	imageURL := "/contact/" + name
	return s.repository.ReplaceContactQR(ctx, slot, imageURL, actor)
}

func qrImageConfig(data []byte) (format string, width, height int, err error) {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		width, height, err = webPDimensions(data)
		return "webp", width, height, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, err
	}
	return format, config.Width, config.Height, nil
}

func webPDimensions(data []byte) (int, int, error) {
	for offset := 12; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		size := int(data[offset+4]) | int(data[offset+5])<<8 | int(data[offset+6])<<16 | int(data[offset+7])<<24
		start, end := offset+8, offset+8+size
		if size < 0 || end > len(data) {
			return 0, 0, errors.New("invalid webp image")
		}
		chunk := data[start:end]
		switch kind {
		case "VP8X":
			if len(chunk) < 10 {
				return 0, 0, errors.New("invalid webp image")
			}
			return 1 + little24(chunk[4:7]), 1 + little24(chunk[7:10]), nil
		case "VP8 ":
			if len(chunk) < 10 || chunk[3] != 0x9d || chunk[4] != 0x01 || chunk[5] != 0x2a {
				return 0, 0, errors.New("invalid webp image")
			}
			return int(chunk[6]) | int(chunk[7]&0x3f)<<8, int(chunk[8]) | int(chunk[9]&0x3f)<<8, nil
		case "VP8L":
			if len(chunk) < 5 || chunk[0] != 0x2f {
				return 0, 0, errors.New("invalid webp image")
			}
			width := 1 + int(chunk[1]) + (int(chunk[2]&0x3f) << 8)
			height := 1 + int(chunk[2]>>6) + (int(chunk[3]) << 2) + (int(chunk[4]&0x0f) << 10)
			return width, height, nil
		}
		offset = end + size%2
	}
	return 0, 0, errors.New("invalid webp image")
}

func little24(value []byte) int { return int(value[0]) | int(value[1])<<8 | int(value[2])<<16 }
