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
	requests []receipts.Request
	response json.RawMessage
	rejected int64
	err      error
}

/** Claim records the receipt identity used by each embedding request. */
func (r *embeddingReceiptRepositoryStub) Claim(_ context.Context, request receipts.Request, _ string) (receipts.Claim, error) {
	r.request = request
	r.requests = append(r.requests, request)
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
	calls   int
	texts   [][]string
	configs []EmbeddingConfig
	result  func([]string) EmbeddingResponse
	err     error
}

/** Embed records the requested dimensions and returns the configured fake provider response. */
func (p *embeddingProviderStub) Embed(_ context.Context, config EmbeddingConfig, texts []string) (EmbeddingResponse, error) {
	p.calls++
	p.configs = append(p.configs, config)
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

/** TestEmbeddingRequestDimensionsFollowProviderConfiguration keeps provider-selected sizing explicit. */
func TestEmbeddingRequestDimensionsFollowProviderConfiguration(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		dimensions int
		wantField  bool
	}{
		{name: "provider selected", dimensions: 0},
		{name: "configured", dimensions: 4, wantField: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			encoded, err := json.Marshal(embeddingRequestBody(EmbeddingConfig{Model: "model", Dimensions: fixture.dimensions}, []string{"text"}))
			if err != nil {
				t.Fatal(err)
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			_, hasDimensions := request["dimensions"]
			if hasDimensions != fixture.wantField {
				t.Fatalf("dimensions field presence = %t, want %t: %s", hasDimensions, fixture.wantField, encoded)
			}
			if fixture.wantField && string(request["dimensions"]) != "4" {
				t.Fatalf("configured dimensions = %s, want 4", request["dimensions"])
			}
		})
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

/** TestEmbeddingStoreRejectsMalformedReceivedResponse keeps invalid paid vectors out of storage. */
func TestEmbeddingStoreRejectsMalformedReceivedResponse(t *testing.T) {
	invalidResponses := []struct {
		name string
		body string
	}{
		{name: "null response", body: `null`},
		{name: "wrong response type", body: `"not a response object"`},
		{name: "empty data", body: `{"data":[]}`},
		{name: "duplicate indices", body: `{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`},
		{name: "wrong dimensions", body: `{"data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[1]}]}`},
		{name: "null coordinate", body: `{"data":[{"index":0,"embedding":[1,null]},{"index":1,"embedding":[0,1]}]}`},
	}
	for _, fixture := range invalidResponses {
		t.Run(fixture.name, func(t *testing.T) {
			stored := &embeddingRepositoryStub{}
			receiptRepository := &embeddingReceiptRepositoryStub{}
			provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
				return EmbeddingResponse{Body: json.RawMessage(fixture.body)}
			}}
			store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
				Enabled: true, Service: "embedding", Model: "m", APIKey: "key", Dimensions: 2,
			}, provider)
			_, err := store.Ensure(context.Background(), []VectorInput{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}})
			var outputError *EmbeddingOutputError
			if !errors.As(err, &outputError) || outputError.ReceiptID != 42 || receiptRepository.rejected != 42 || len(receiptRepository.response) == 0 || stored.saves != 0 {
				t.Fatalf("malformed embeddings were accepted: err=%v rejected=%d response=%s saves=%d", err, receiptRepository.rejected, receiptRepository.response, stored.saves)
			}
		})
	}
}

/** TestEmbeddingStoreRecomputesStaleDimensionsAcrossConfiguredStores rejects vectors from another configured dimension. */
func TestEmbeddingStoreRecomputesStaleDimensionsAcrossConfiguredStores(t *testing.T) {
	text := "same embedding input"
	stored := &embeddingRepositoryStub{stored: map[string]StoredEmbedding{
		"article": {ID: "article", Hash: hashEmbeddingText(text), Vector: []float32{1, 0}},
	}}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
		encoded, _ := json.Marshal(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{0, 1, 0, 1}}}})
		return EmbeddingResponse{Body: encoded}
	}}
	fourDimensions := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
		Enabled:    true,
		Service:    "embedding",
		Model:      "m",
		APIKey:     "key",
		Dimensions: 4,
	}, provider)
	first, err := fourDimensions.Ensure(context.Background(), []VectorInput{{ID: "article", Text: text}})
	if err != nil || len(first["article"]) != 4 || provider.calls != 1 {
		t.Fatalf("stale stored dimensions were reused: vector=%v calls=%d err=%v", first["article"], provider.calls, err)
	}
	provider.result = func([]string) EmbeddingResponse {
		encoded, _ := json.Marshal(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}}})
		return EmbeddingResponse{Body: encoded}
	}
	twoDimensions := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
		Enabled:    true,
		Service:    "embedding",
		Model:      "m",
		APIKey:     "key",
		Dimensions: 2,
	}, provider)
	second, err := twoDimensions.Ensure(context.Background(), []VectorInput{{ID: "article", Text: text}})
	if err != nil || len(second["article"]) != 2 || provider.calls != 2 || stored.saves != 2 {
		t.Fatalf("dimension change did not refresh the stored vector: vector=%v calls=%d saves=%d err=%v", second["article"], provider.calls, stored.saves, err)
	}
	if len(provider.configs) != 2 || provider.configs[0].Dimensions != 4 || provider.configs[1].Dimensions != 2 {
		t.Fatalf("provider dimensions = %#v", provider.configs)
	}
	if len(receiptRepository.requests) != 2 {
		t.Fatalf("unexpected embedding receipt count: %#v", receiptRepository.requests)
	}
	firstIdentity, firstOK := receiptRepository.requests[0].Identity.(map[string]any)
	secondIdentity, secondOK := receiptRepository.requests[1].Identity.(map[string]any)
	if !firstOK || !secondOK || firstIdentity["dims"] != 4 || secondIdentity["dims"] != 2 {
		t.Fatalf("dimension-specific paid receipt identity changed: %#v", receiptRepository.requests)
	}
}

/** TestEmbeddingStoreAcceptsProviderSelectedDimensions keeps old vectors and rejects cross-size similarity. */
func TestEmbeddingStoreAcceptsProviderSelectedDimensions(t *testing.T) {
	oldText := "stored provider-sized vector"
	stored := &embeddingRepositoryStub{stored: map[string]StoredEmbedding{
		"old": {ID: "old", Hash: hashEmbeddingText(oldText), Vector: []float32{1, 0}},
	}}
	receiptRepository := &embeddingReceiptRepositoryStub{}
	provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
		encoded, _ := json.Marshal(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0, 0, 0, 0}}}})
		return EmbeddingResponse{Body: encoded}
	}}
	store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
		Enabled: true, Service: "embedding", Model: "m", APIKey: "key", Dimensions: 0,
	}, provider)
	vectors, err := store.Ensure(context.Background(), []VectorInput{
		{ID: "old", Text: oldText},
		{ID: "fresh", Text: "fresh provider-sized vector"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors["old"]) != 2 || len(vectors["fresh"]) != 6 || provider.calls != 1 || len(provider.texts[0]) != 1 {
		t.Fatalf("provider-selected vector sizes changed: vectors=%v calls=%d texts=%v", vectors, provider.calls, provider.texts)
	}
	if Cosine32(vectors["old"], vectors["fresh"]) != 0 {
		t.Fatalf("different provider-selected dimensions compared as similar: old=%v fresh=%v", vectors["old"], vectors["fresh"])
	}
	if len(receiptRepository.requests) != 1 {
		t.Fatalf("unexpected provider receipt count: %#v", receiptRepository.requests)
	}
	identity, ok := receiptRepository.requests[0].Identity.(map[string]any)
	if !ok || identity["dims"] != 0 {
		t.Fatalf("provider-selected dimensions are missing from receipt identity: %#v", receiptRepository.requests[0].Identity)
	}
}

/** TestEmbeddingStoreRecomputesStoredNonFiniteVectors rejects NaN and infinity from persisted embeddings. */
func TestEmbeddingStoreRecomputesStoredNonFiniteVectors(t *testing.T) {
	for _, test := range []struct {
		name   string
		vector []float32
	}{
		{name: "nan", vector: []float32{1, math.Float32frombits(0x7fc00000), 0, 0}},
		{name: "positive infinity", vector: []float32{1, math.Float32frombits(0x7f800000), 0, 0}},
		{name: "negative infinity", vector: []float32{1, math.Float32frombits(0xff800000), 0, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := "persisted embedding"
			stored := &embeddingRepositoryStub{stored: map[string]StoredEmbedding{
				"article": {ID: "article", Hash: hashEmbeddingText(text), Vector: test.vector},
			}}
			receiptRepository := &embeddingReceiptRepositoryStub{}
			provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
				encoded, _ := json.Marshal(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{0, 1, 0, 0}}}})
				return EmbeddingResponse{Body: encoded}
			}}
			store := NewEmbeddingStore(stored, receipts.NewService(receiptRepository), EmbeddingConfig{
				Enabled:    true,
				Service:    "embedding",
				Model:      "m",
				APIKey:     "key",
				Dimensions: 4,
			}, provider)
			vectors, err := store.Ensure(context.Background(), []VectorInput{{ID: "article", Text: text}})
			if err != nil || provider.calls != 1 || stored.saves != 1 || !validVector(vectors["article"], 4) {
				t.Fatalf("non-finite vector was reused: vectors=%v calls=%d saves=%d err=%v", vectors, provider.calls, stored.saves, err)
			}
		})
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
	vector := []float32{1.5, -2, math.Float32frombits(0x80000000), math.Float32frombits(1)}
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
	if !ok || len(decoded) != len(vector) {
		t.Fatalf("PostgreSQL real[] decode changed: %v %t", decoded, ok)
	}
	for index, value := range vector {
		if math.Float32bits(decoded[index]) != math.Float32bits(value) {
			t.Fatalf("PostgreSQL real[] coordinate %d changed bits: got %08x want %08x", index, math.Float32bits(decoded[index]), math.Float32bits(value))
		}
	}
	if _, ok := decodePostgresFloatArray(data[:len(data)-1]); ok {
		t.Fatal("truncated PostgreSQL real[] was accepted")
	}
	wrongLowerBound := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(wrongLowerBound[16:20], 0)
	if _, ok := decodePostgresFloatArray(wrongLowerBound); ok {
		t.Fatal("PostgreSQL real[] with a non-one lower bound was accepted")
	}
	empty := append([]byte(nil), data[:20]...)
	binary.BigEndian.PutUint32(empty[12:16], 0)
	if _, ok := decodePostgresFloatArray(empty); ok {
		t.Fatal("empty PostgreSQL real[] was accepted")
	}
	multidimensional := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(multidimensional[0:4], 2)
	if _, ok := decodePostgresFloatArray(multidimensional); ok {
		t.Fatal("multidimensional PostgreSQL real[] was accepted")
	}
}
