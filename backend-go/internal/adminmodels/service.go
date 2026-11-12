package adminmodels

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

type Repository interface {
	Overview(context.Context, time.Time) (OverviewRows, error)
	Overrides(context.Context) (map[string]string, error)
	Switch(context.Context, string, *string, string, string, ModelSource, ModelSource) error
}

type Service struct {
	repository  Repository
	catalog     Catalog
	environment map[string]string
}

func NewService(repository Repository, catalog Catalog, modelEnvironment ...map[string]string) *Service {
	service := &Service{repository: repository, catalog: catalog}
	if len(modelEnvironment) > 0 {
		service.environment = modelEnvironment[0]
	}
	return service
}

type ModelSource struct {
	Model  string `json:"model"`
	Source string `json:"source"`
}

type Usage struct {
	Purpose       string        `json:"purpose"`
	Model         *string       `json:"model"`
	PromptVersion *string       `json:"promptVersion"`
	Calls         int           `json:"calls"`
	OK            int           `json:"ok"`
	Failed        int           `json:"failed"`
	Unknown       int           `json:"unknown"`
	P50           *int64        `json:"p50"`
	P95           *int64        `json:"p95"`
	TokensIn      int64         `json:"tokensIn"`
	TokensOut     int64         `json:"tokensOut"`
	ActualCost    *float64      `json:"actualCost"`
	Currency      *string       `json:"currency"`
	Estimate      *CostEstimate `json:"estimate"`
}

type CostEstimate struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type CapabilityView struct {
	Key          string      `json:"key"`
	Label        string      `json:"label"`
	Env          string      `json:"env"`
	DefaultModel string      `json:"defaultModel"`
	Vision       bool        `json:"vision"`
	Current      ModelSource `json:"current"`
	Usage        []Usage     `json:"usage"`
}

type Choice struct {
	Key     string `json:"key"`
	Service string `json:"service"`
	Vision  bool   `json:"vision"`
}

type History struct {
	At      time.Time    `json:"at"`
	Actor   string       `json:"actor"`
	Subject string       `json:"subject"`
	Reason  *string      `json:"reason"`
	Before  *ModelSource `json:"before"`
	After   *ModelSource `json:"after"`
}

type Bench struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	SampleSize    int             `json:"sample_size"`
	PromptVersion *string         `json:"prompt_version"`
	Models        []string        `json:"models"`
	Summary       json.RawMessage `json:"summary"`
	CreatedAt     time.Time       `json:"created_at"`
}

type Overview struct {
	Days         float64          `json:"days"`
	Capabilities []CapabilityView `json:"capabilities"`
	Choices      []Choice         `json:"choices"`
	History      []History        `json:"history"`
	Benches      []Bench          `json:"benches"`
}

func (s *Service) Overview(ctx context.Context, days float64) (Overview, error) {
	since := time.Now().Add(-time.Duration(days * float64(24*time.Hour)))
	rows, err := s.repository.Overview(ctx, since)
	if err != nil {
		return Overview{}, err
	}
	overrides, err := s.repository.Overrides(ctx)
	if err != nil {
		return Overview{}, err
	}
	sources := s.modelSources(overrides)
	choices := []Choice{{Key: "default", Service: "llm", Vision: s.env("LLM_VISION") == "true"}}
	for _, key := range s.catalog.PresetOrder {
		preset := s.catalog.Presets[key]
		choices = append(choices, Choice{Key: key, Service: preset.Service, Vision: preset.Vision})
	}
	steps := make([]CapabilityView, 0, len(s.catalog.Capabilities))
	for _, capability := range s.catalog.Capabilities {
		if configured := s.catalog.Defaults[capability.Key]; configured != "" {
			capability.Default = configured
		}
		usages := make([]Usage, 0)
		for _, row := range rows.Usage {
			if contains(capability.Purposes, row.Purpose) {
				usages = append(usages, s.projectUsage(row, rows.Prices))
			}
		}
		steps = append(steps, CapabilityView{Key: capability.Key, Label: capability.Label, Env: capability.Env, DefaultModel: capability.Default, Vision: capability.Vision, Current: sources[capability.Key], Usage: usages})
	}
	history := make([]History, 0, len(rows.History))
	for _, row := range rows.History {
		entry := History{At: row.At, Actor: row.Actor, Subject: row.Subject, Reason: row.Reason}
		if len(row.Before) > 0 && string(row.Before) != "null" {
			var value ModelSource
			if err := json.Unmarshal(row.Before, &value); err == nil {
				entry.Before = &value
			}
		}
		if len(row.After) > 0 && string(row.After) != "null" {
			var value ModelSource
			if err := json.Unmarshal(row.After, &value); err == nil {
				entry.After = &value
			}
		}
		history = append(history, entry)
	}
	benches := rows.Benches
	if benches == nil {
		benches = make([]Bench, 0)
	}
	return Overview{Days: days, Capabilities: steps, Choices: choices, History: history, Benches: benches}, nil
}

func (s *Service) Switch(ctx context.Context, capabilityKey string, model *string, reason, actor string) (map[string]any, error) {
	capability, ok := s.catalog.Capability(capabilityKey)
	if !ok {
		return nil, invalid("unknown capability")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, invalid("a reason is required")
	}
	if model != nil {
		preset, exists := s.catalog.Presets[*model]
		if *model != "default" && !exists {
			return nil, invalid("unknown model")
		}
		vision := preset.Vision
		if *model == "default" {
			vision = s.env("LLM_VISION") == "true"
		}
		if capability.Vision != vision {
			if capability.Vision {
				return nil, invalid("this capability needs a vision model")
			}
			return nil, invalid("a vision-only model cannot do this")
		}
	}
	overrides, err := s.repository.Overrides(ctx)
	if err != nil {
		return nil, err
	}
	before := s.modelSources(overrides)[capabilityKey]
	updated := make(map[string]string, len(overrides)+1)
	for key, value := range overrides {
		updated[key] = value
	}
	if model == nil {
		delete(updated, capabilityKey)
	} else {
		updated[capabilityKey] = *model
	}
	after := s.modelSources(updated)[capabilityKey]
	if err := s.repository.Switch(ctx, capabilityKey, model, actor, reason, before, after); err != nil {
		return nil, err
	}
	return map[string]any{"capability": capabilityKey, "before": before, "after": after}, nil
}

func (s *Service) modelSources(overrides map[string]string) map[string]ModelSource {
	out := make(map[string]ModelSource, len(s.catalog.Capabilities))
	for _, capability := range s.catalog.Capabilities {
		model := capability.Default
		if configured := s.catalog.Defaults[capability.Key]; configured != "" {
			model = configured
		}
		if !s.catalog.validModel(model) {
			model = "default"
		}
		if value := overrides[capability.Key]; s.catalog.validModel(value) && value != "" {
			out[capability.Key] = ModelSource{Model: value, Source: "admin"}
			continue
		}
		if value := s.env(capability.Env); value != "" && s.catalog.validModel(value) {
			out[capability.Key] = ModelSource{Model: value, Source: "env"}
			continue
		}
		out[capability.Key] = ModelSource{Model: model, Source: "default"}
	}
	return out
}

func (s *Service) env(key string) string {
	if value, ok := s.environment[key]; ok {
		return value
	}
	return os.Getenv(key)
}

func (s *Service) projectUsage(row UsageRow, prices []Price) Usage {
	out := Usage{Purpose: row.Purpose, Model: row.Model, PromptVersion: row.PromptVersion, Calls: row.Calls, OK: row.OK, Failed: row.Failed, Unknown: row.Unknown, P50: row.P50, P95: row.P95,
		TokensIn: valueInt(row.TokensIn), TokensOut: valueInt(row.TokensOut), ActualCost: row.ActualCost, Currency: row.Currency}
	if row.Model == nil {
		return out
	}
	service, model := s.providerModel(*row.Model)
	if service == "" {
		return out
	}
	var price *Price
	for i := range prices {
		if prices[i].Service == service && prices[i].Model == model {
			price = &prices[i]
			break
		}
	}
	if price == nil {
		for i := range prices {
			if prices[i].Service == service && prices[i].Model == "" {
				price = &prices[i]
				break
			}
		}
	}
	if price == nil || (valueFloat(price.InputPerMTok) == 0 && valueFloat(price.OutputPerMTok) == 0) {
		return out
	}
	cached := float64(valueInt(row.TokensCached))
	fresh := float64(valueInt(row.TokensInUncosted)) - cached
	amount := fresh/1e6*valueFloat(price.InputPerMTok) + cached/1e6*valueFloat(first(price.CachedPerMTok, price.InputPerMTok)) + float64(valueInt(row.TokensOutUncosted))/1e6*valueFloat(price.OutputPerMTok)
	out.Estimate = &CostEstimate{Amount: amount, Currency: price.Currency}
	return out
}

func (s *Service) providerModel(key string) (string, string) {
	defaultModel := s.env("LLM_MODEL")
	if key == "default" || (defaultModel != "" && key == defaultModel) {
		return "llm", defaultModel
	}
	for _, presetKey := range s.catalog.PresetOrder {
		preset := s.catalog.Presets[presetKey]
		if presetKey == key || preset.Model == key {
			return preset.Service, preset.Model
		}
	}
	return "", ""
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func valueInt(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func valueFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func first(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

type invalidError string

func (e invalidError) Error() string { return string(e) }
func invalid(message string) error   { return invalidError(message) }

func IsInvalid(err error) bool { _, ok := err.(invalidError); return ok }
