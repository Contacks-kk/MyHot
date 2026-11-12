package editorial

import (
	"context"

	"aihot/backend-go/internal/articlebody"
	"aihot/backend-go/internal/interests"
)

type ProcessingService struct {
	repository Repository
	analysis   *AnalysisService
	entities   map[string]interests.Entity
}

func NewProcessingService(repository Repository, analysis *AnalysisService, entities map[string]interests.Entity) *ProcessingService {
	return &ProcessingService{repository: repository, analysis: analysis, entities: entities}
}

// Analyze loads one current article revision, runs the shared analysis stages,
// and commits its judgement and paid-call receipts atomically.
func (s *ProcessingService) Analyze(ctx context.Context, articleID string, options AnalysisOptions) (*AnalyzeResult, error) {
	input, err := s.repository.LoadInput(ctx, articleID)
	if err != nil || input == nil {
		return nil, err
	}
	if WaitsForPage(*input) {
		return &AnalyzeResult{NeedsBody: true, ReceiptIDs: []int64{}, Reused: true}, nil
	}
	run, err := s.analysis.Run(ctx, *input, options)
	if err != nil {
		return nil, err
	}
	output := NormalizeAnalysis(run, s.entities)
	result, err := s.repository.SaveAnalysis(ctx, *input, run, output, s.analysis.Versions.Analysis)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// WaitsForPage keeps title-only feed items out of model analysis while an
// eligible article body can still be fetched.
func WaitsForPage(input Input) bool {
	return input.BodyStatus == "pending" && (input.BodyText == nil || *input.BodyText == "") &&
		input.XPost == nil && articlebody.PageFetchable(input.URL, input.Source.Kind)
}
