package admincontent

import (
	"context"
	"testing"
)

type chainRepository struct{ chain *Chain }

func (chainRepository) ByID(context.Context, string) ([]Row, error)              { return nil, nil }
func (chainRepository) ByURL(context.Context, []string, []string) ([]Row, error) { return nil, nil }
func (chainRepository) ByTitle(context.Context, string) ([]Row, error)           { return nil, nil }
func (r chainRepository) Chain(context.Context, string) (*Chain, error)          { return r.chain, nil }

func TestChainNormalizesEveryCollection(t *testing.T) {
	chain, err := NewService(chainRepository{chain: &Chain{}}).Chain(context.Background(), "fixture")
	if err != nil || chain == nil || chain.Discoveries == nil || chain.Revisions == nil || chain.Analyses == nil || chain.Ledger == nil || chain.Membership == nil || chain.Decisions == nil || chain.Deliveries == nil || chain.History == nil {
		t.Fatalf("empty chain collections must serialize as arrays: chain=%#v err=%v", chain, err)
	}
	missing, err := NewService(chainRepository{}).Chain(context.Background(), "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing content contract changed: chain=%#v err=%v", missing, err)
	}
}
