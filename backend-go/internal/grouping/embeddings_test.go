package grouping

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
)

type embeddingRepositoryStub struct {
	stored    map[string]StoredEmbedding
	loads     int
	saves     int
	saved     []StoredEmbedding
	receiptID int64
	saveErr   error
}

func (r *embeddingRepositoryStub) LoadEmbeddings(_ context.Context, _ string, ids []string) (map[string]StoredEmbedding, error) {
	r.loads++
	result := make(map[string]StoredEmbedding, len(ids))
	for _, id := range ids {
		if row, ok := r.stored[id]; ok {
			result[id] = row
		}
	}
	return result, nil
}
func (r *embeddingRepositoryStub) SaveEmbeddingsAndComplete(_ context.Context, _ string, rows []StoredEmbedding, receiptID int64) error {
	r.saves++
	r.saved = append(r.saved, rows...)
	r.receiptID = receiptID
	if r.saveErr != nil {
		return r.saveErr
	}
	if r.stored == nil {
		r.stored = make(map[string]StoredEmbedding)
	}
	for _, row := range rows {
		r.stored[row.ID] = row
	}
	return nil
}

type embeddingReceiptRepositoryStub struct {
	request  receipts.Request
	response json.RawMessage
	rejected int64
	err      error
}

func (r *embeddingReceiptRepositoryStub) Claim(_ context.Context, request receipts.Request, _ string) (receipts.Claim, error) {
	r.request = request
	return receipts.Claim{ReceiptID: 42, AttemptID: 43}, r.err
}
func (*embeddingReceiptRepositoryStub) RecordFailure(context.Context, receipts.Claim, string, string, time.Duration) error {
	return nil
}
func (r *embeddingReceiptRepositoryStub) RecordReceived(_ context.Context, _ receipts.Claim, result receipts.Outcome, _ time.Duration) error {
	r.response = result.Response
	return nil
}
func (*embeddingReceiptRepositoryStub) Complete(context.Context, int64) error { return nil }
func (r *embeddingReceiptRepositoryStub) Reject(_ context.Context, id int64, _ string) error {
	r.rejected = id
	return nil
}

type embeddingProviderStub struct {
	calls  int
	texts  [][]string
	result func([]string) EmbeddingResponse
	err    error
}

func (p *embeddingProviderStub) Embed(_ context.Context, _ EmbeddingConfig, texts []string) (EmbeddingResponse, error) {
	p.calls++
	p.texts = append(p.texts, append([]string(nil), texts...))
	if p.err != nil {
		return EmbeddingResponse{}, p.err
	}
	return p.result(texts), nil
}

func TestEmbeddingConfigMatchesOpenAIAndDashScopeDefaults(t *testing.T) {
	values := map[string]string{"EMBEDDING_API_KEY": "openai-key", "EMBEDDING_BASE_URL": "https://embedding.example/v1", "EMBEDDING_DIMS": "1536"}
	openAI := EmbeddingConfigFromRuntime(true, func(key string) string { return values[key] })
	if openAI.Service != "embedding" || openAI.Model != "text-embedding-3-small" || openAI.BaseURL != "https://embedding.example/v1" || openAI.Dimensions != 1536 || !openAI.Enabled {
		t.Fatalf("OpenAI embedding defaults changed: %#v", openAI)
	}
	values = map[string]string{"DASHSCOPE_API_KEY": "dashscope-key"}
	dashscope := EmbeddingConfigFromRuntime(false, func(key string) string { return values[key] })
	if dashscope.Service != "dashscope" || dashscope.Model != "text-embedding-v4" || dashscope.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" || dashscope.Dimensions != 1024 || dashscope.Enabled {
		t.Fatalf("DashScope defaults changed: %#v", dashscope)
	}
}

func TestEmbeddingStorePersistsAndReusesVectorsWithPaidReceipt(t *testing.T) {
	stored := &embeddingRepositoryStub{}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func(texts []string) EmbeddingResponse {
		rows := make([]map[string]any, len(texts))
		for index := range texts {
			rows[index] = map[string]any{"index": len(texts) - index - 1, "embedding": []float32{float32(len(texts) - index), 0}}
		}
		encoded, _ := json.Marshal(map[string]any{"data": rows, "usage": map[string]int{"total_tokens": len(texts)}})
		return EmbeddingResponse{Body: encoded, RequestID: "embedding-request"}
	}}
	store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
		Enabled: true, Service: "embedding", Model: "fixture-model", BaseURL: "https://model.example/v1", APIKey: "secret", Dimensions: 2,
	}, provider)
	inputs := []VectorInput{{ID: "article-a", Text: strings.Repeat("😀", 1001)}, {ID: "article-b", Text: "second"}}
	vectors, err := store.Ensure(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if vectors["article-a"][0] != 1 || vectors["article-b"][0] != 2 || provider.calls != 1 || stored.saves != 1 || stored.receiptID != 42 {
		t.Fatalf("embedding persistence/order changed: vectors=%v provider=%#v repository=%#v", vectors, provider, stored)
	}
	if receiptRepository.request.Service != "embedding" || receiptRepository.request.Purpose != "embedding" || receiptRepository.request.Subject != "article:article-a" || receiptRepository.request.Summary["count"] != 2 {
		t.Fatalf("paid request contract changed: %#v", receiptRepository.request)
	}
	if utf16Length(provider.texts[0][0]) != 2000 {
		t.Fatalf("provider text truncation changed: got %d UTF-16 units", utf16Length(provider.texts[0][0]))
	}
	encoded, _ := json.Marshal(receiptRepository.request)
	if strings.Contains(string(encoded), "😀") || strings.Contains(string(encoded), "second") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("receipt persisted text or credentials: %s", encoded)
	}
	_, err = store.Ensure(context.Background(), inputs)
	if err != nil || provider.calls != 1 || stored.saves != 1 || stored.loads != 1 {
		t.Fatalf("stored vectors were not reused: calls=%d saves=%d err=%v", provider.calls, stored.saves, err)
	}
}

func TestEmbeddingMemoryCacheTracksArticleRevision(t *testing.T) {
	stored := &embeddingRepositoryStub{}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func(texts []string) EmbeddingResponse {
		encoded, _ := json.Marshal(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1}}}})
		return EmbeddingResponse{Body: encoded}
	}}
	store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{Enabled: true, Service: "embedding", Model: "m", APIKey: "key"}, provider)
	revision := 7
	input := []VectorInput{{ID: "article", Text: "same report text", Revision: &revision}}
	if _, err := store.Ensure(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if store.NeedsText("article", revision) || len(store.Cached([]string{"article"})["article"]) != 1 {
		t.Fatal("current revision was not memoized")
	}
	revision++
	if !store.NeedsText("article", revision) {
		t.Fatal("revision change must refresh the report text before comparing its hash")
	}
	input[0].Revision = &revision
	if _, err := store.Ensure(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if store.NeedsText("article", revision) || provider.calls != 1 || stored.loads != 1 {
		t.Fatalf("same text at a new revision should reuse its vector: calls=%d loads=%d", provider.calls, stored.loads)
	}
}

func TestEmbeddingStoreRejectsMalformedReceivedResponse(t *testing.T) {
	stored := &embeddingRepositoryStub{}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
		return EmbeddingResponse{Body: json.RawMessage(`{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`)}
	}}
	store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{Enabled: true, Service: "embedding", Model: "m", APIKey: "key"}, provider)
	_, err := store.Ensure(context.Background(), []VectorInput{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}})
	var outputError *EmbeddingOutputError
	if !errors.As(err, &outputError) || outputError.ReceiptID != 42 || receiptRepository.rejected != 42 || stored.saves != 0 {
		t.Fatalf("malformed embeddings were accepted: err=%v rejected=%d saves=%d", err, receiptRepository.rejected, stored.saves)
	}
}

func TestEmbeddingStoreBatchesAtProviderLimit(t *testing.T) {
	stored := &embeddingRepositoryStub{}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func(texts []string) EmbeddingResponse {
		rows := make([]map[string]any, len(texts))
		for index := range texts {
			rows[index] = map[string]any{"index": index, "embedding": []float32{1}}
		}
		encoded, _ := json.Marshal(map[string]any{"data": rows})
		return EmbeddingResponse{Body: encoded}
	}}
	store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{Enabled: true, Service: "embedding", Model: "m", APIKey: "key"}, provider)
	inputs := make([]VectorInput, 11)
	for index := range inputs {
		inputs[index] = VectorInput{ID: fmt.Sprintf("article-%02d", index), Text: "text"}
	}
	result, err := store.Ensure(context.Background(), inputs)
	if err != nil || len(result) != 11 || provider.calls != 2 || len(provider.texts[0]) != 10 || len(provider.texts[1]) != 1 || stored.saves != 2 {
		t.Fatalf("embedding batch bound changed: count=%d provider=%#v repository=%#v err=%v", len(result), provider, stored, err)
	}
}

func TestDecodePostgresFloatArray(t *testing.T) {
	vector := []float32{1.5, -2}
	data := make([]byte, 20+len(vector)*8)
	binary.BigEndian.PutUint32(data[0:4], 1)
	binary.BigEndian.PutUint32(data[4:8], 0)
	binary.BigEndian.PutUint32(data[8:12], 700)
	binary.BigEndian.PutUint32(data[12:16], uint32(len(vector)))
	binary.BigEndian.PutUint32(data[16:20], 1)
	for index, value := range vector {
		binary.BigEndian.PutUint32(data[20+index*8:24+index*8], 4)
		binary.BigEndian.PutUint32(data[24+index*8:28+index*8], math.Float32bits(value))
	}
	decoded, ok := decodePostgresFloatArray(data)
	if !ok || len(decoded) != len(vector) || decoded[0] != vector[0] || decoded[1] != vector[1] {
		t.Fatalf("PostgreSQL real[] decode changed: %v %t", decoded, ok)
	}
	if _, ok := decodePostgresFloatArray(data[:len(data)-1]); ok {
		t.Fatal("truncated PostgreSQL real[] was accepted")
	}
}
