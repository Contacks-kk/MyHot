package grouping

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/modelconfig"
	"aihot/backend-go/internal/receipts"
)

const embeddingBatchSize = 10

type EmbeddingConfig struct {
	Enabled    bool
	Service    string
	Model      string
	BaseURL    string
	APIKey     string
	Dimensions int
}

type EmbeddingProvider interface {
	Embed(context.Context, EmbeddingConfig, []string) (EmbeddingResponse, error)
}

type EmbeddingResponse struct {
	Body      json.RawMessage
	RequestID string
}

type StoredEmbedding struct {
	ID     string
	Hash   string
	Vector []float32
}

type EmbeddingRepository interface {
	LoadEmbeddings(context.Context, string, []string) (map[string]StoredEmbedding, error)
	SaveEmbeddingsAndComplete(context.Context, string, []StoredEmbedding, int64) error
}

type GormEmbeddingStore struct {
	repository EmbeddingRepository
	receipts   *receipts.Service
	provider   EmbeddingProvider
	config     EmbeddingConfig
	cacheMu    sync.RWMutex
	cache      map[string]embeddingCacheEntry
}

type embeddingCacheEntry struct {
	hash     string
	vector   []float32
	revision *int
}

func NewEmbeddingStore(db EmbeddingRepository, receiptService *receipts.Service, cfg EmbeddingConfig, provider EmbeddingProvider) *GormEmbeddingStore {
	if provider == nil {
		provider = HTTPEmbeddingProvider{}
	}
	return &GormEmbeddingStore{repository: db, receipts: receiptService, provider: provider, config: cfg, cache: make(map[string]embeddingCacheEntry)}
}

func EmbeddingConfigFromRuntime(enabled bool, credential func(string) string) EmbeddingConfig {
	if credential == nil {
		credential = func(string) string { return "" }
	}
	apiKey := strings.TrimSpace(credential("EMBEDDING_API_KEY"))
	service, model, baseURL, dimensions := "embedding", "text-embedding-3-small", "https://api.openai.com/v1", 0
	if apiKey == "" {
		apiKey = strings.TrimSpace(credential("DASHSCOPE_API_KEY"))
		service, model, baseURL, dimensions = "dashscope", "text-embedding-v4", "https://dashscope.aliyuncs.com/compatible-mode/v1", 1024
		if value := strings.TrimSpace(credential("DASHSCOPE_BASE_URL")); value != "" {
			baseURL = value
		}
	} else if value := strings.TrimSpace(credential("EMBEDDING_BASE_URL")); value != "" {
		baseURL = value
	}
	if value := strings.TrimSpace(credential("EMBEDDING_MODEL")); value != "" {
		model = value
	}
	if value := strings.TrimSpace(credential("EMBEDDING_DIMS")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			dimensions = parsed
		} else {
			dimensions = 0
		}
	}
	return EmbeddingConfig{Enabled: enabled, Service: service, Model: model, BaseURL: baseURL, APIKey: apiKey, Dimensions: dimensions}
}

func (s *GormEmbeddingStore) Available(context.Context) bool {
	return s != nil && s.config.Enabled && s.repository != nil && s.receipts != nil && s.config.Service != "" && s.config.Model != "" && s.config.APIKey != ""
}

func (s *GormEmbeddingStore) NeedsText(articleID string, revision int) bool {
	if s == nil {
		return true
	}
	s.cacheMu.RLock()
	entry, ok := s.cache[articleID]
	s.cacheMu.RUnlock()
	return !ok || entry.revision == nil || *entry.revision != revision
}

func (s *GormEmbeddingStore) Cached(articleIDs []string) map[string][]float32 {
	result := make(map[string][]float32, len(articleIDs))
	if s == nil {
		return result
	}
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	for _, id := range articleIDs {
		if entry, ok := s.cache[id]; ok {
			result[id] = append([]float32(nil), entry.vector...)
		}
	}
	return result
}

func (s *GormEmbeddingStore) Ensure(ctx context.Context, inputs []VectorInput) (map[string][]float32, error) {
	result := make(map[string][]float32, len(inputs))
	if len(inputs) == 0 {
		return result, nil
	}
	if !s.Available(ctx) {
		return nil, ErrEmbeddingUnavailable
	}
	hashes := make(map[string]string, len(inputs))
	for _, input := range inputs {
		hash := hashEmbeddingText(input.Text)
		hashes[input.ID] = hash
	}
	missing := make([]VectorInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	lookupIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if _, ok := seen[input.ID]; ok {
			continue
		}
		seen[input.ID] = struct{}{}
		if entry, ok := s.cached(input.ID); ok && entry.hash == hashes[input.ID] && validVector(entry.vector, s.config.Dimensions) {
			if input.Revision != nil {
				entry.revision = cloneInt(input.Revision)
				s.remember(input.ID, entry)
			}
			result[input.ID] = entry.vector
			continue
		}
		lookupIDs = append(lookupIDs, input.ID)
	}
	stored := make(map[string]StoredEmbedding)
	if len(lookupIDs) > 0 {
		loaded, loadErr := s.repository.LoadEmbeddings(ctx, s.config.Model, lookupIDs)
		if loadErr != nil {
			return nil, loadErr
		}
		stored = loaded
	}
	for _, input := range inputs {
		if _, already := result[input.ID]; already {
			continue
		}
		if row, ok := stored[input.ID]; ok && row.Hash == hashes[input.ID] && validVector(row.Vector, s.config.Dimensions) {
			result[input.ID] = row.Vector
			s.remember(input.ID, embeddingCacheEntry{hash: row.Hash, vector: append([]float32(nil), row.Vector...), revision: cloneInt(input.Revision)})
			continue
		}
		missing = append(missing, input)
	}
	for start := 0; start < len(missing); start += embeddingBatchSize {
		batch := missing[start:min(start+embeddingBatchSize, len(missing))]
		vectors, receiptID, err := s.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		rows := make([]StoredEmbedding, 0, len(batch))
		for index, input := range batch {
			rows = append(rows, StoredEmbedding{ID: input.ID, Hash: hashes[input.ID], Vector: vectors[index]})
		}
		if err := s.repository.SaveEmbeddingsAndComplete(ctx, s.config.Model, rows, receiptID); err != nil {
			return nil, err
		}
		for index, input := range batch {
			result[input.ID] = vectors[index]
			s.remember(input.ID, embeddingCacheEntry{hash: hashes[input.ID], vector: append([]float32(nil), vectors[index]...), revision: cloneInt(input.Revision)})
		}
	}
	return result, nil
}

func (s *GormEmbeddingStore) cached(articleID string) (embeddingCacheEntry, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	entry, ok := s.cache[articleID]
	return entry, ok
}

func (s *GormEmbeddingStore) remember(articleID string, entry embeddingCacheEntry) {
	s.cacheMu.Lock()
	s.cache[articleID] = entry
	if len(s.cache) > 30_000 {
		s.cache = make(map[string]embeddingCacheEntry)
	}
	s.cacheMu.Unlock()
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (s *GormEmbeddingStore) embedBatch(ctx context.Context, batch []VectorInput) ([][]float32, int64, error) {
	texts := make([]string, len(batch))
	hashes := make([]string, len(batch))
	for index, input := range batch {
		texts[index] = truncateUTF16(input.Text, 2000)
		hashes[index] = hashEmbeddingText(input.Text)
	}
	request := receipts.Request{
		Service: s.config.Service, Model: s.config.Model, Purpose: "embedding", Subject: "article:" + batch[0].ID,
		Identity: map[string]any{"model": s.config.Model, "dims": s.config.Dimensions, "texts": hashes},
		Summary:  map[string]any{"count": len(batch)},
	}
	paid, err := s.receipts.Do(ctx, request, func(callCtx context.Context) (receipts.Outcome, error) {
		response, callErr := s.provider.Embed(callCtx, s.config, texts)
		if callErr != nil {
			return receipts.Outcome{}, callErr
		}
		if len(response.Body) == 0 {
			return receipts.Outcome{}, &embeddingProviderError{message: "embedding provider returned an empty response", rejected: true}
		}
		usage := embeddingUsage(response.Body)
		return receipts.Outcome{Response: response.Body, RequestID: response.RequestID, Usage: usage}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	vectors, parseErr := parseEmbeddingResponse(paid.Response, len(batch), s.config.Dimensions)
	if parseErr != nil {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		rejectErr := s.receipts.Reject(persistCtx, paid.ReceiptID, "unusable embeddings: "+parseErr.Error())
		cancel()
		if rejectErr != nil {
			return nil, 0, rejectErr
		}
		return nil, 0, &EmbeddingOutputError{ReceiptID: paid.ReceiptID, Message: "embedding response is unusable"}
	}
	return vectors, paid.ReceiptID, nil
}

type EmbeddingOutputError struct {
	ReceiptID int64
	Message   string
}

func (e *EmbeddingOutputError) Error() string { return e.Message }

type embeddingProviderError struct {
	message  string
	rejected bool
}

func (e *embeddingProviderError) Error() string          { return e.message }
func (e *embeddingProviderError) SafeMessage() string    { return e.message }
func (e *embeddingProviderError) ProviderRejected() bool { return e.rejected }

type HTTPEmbeddingProvider struct{}

func (HTTPEmbeddingProvider) Embed(ctx context.Context, cfg EmbeddingConfig, texts []string) (EmbeddingResponse, error) {
	base, err := modelconfig.ValidateBaseURL(ctx, cfg.BaseURL)
	if err != nil {
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding base URL is invalid", rejected: true}
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/embeddings"
	endpoint.RawPath = ""
	payload, err := json.Marshal(embeddingRequestBody(cfg, texts))
	if err != nil {
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding request could not be encoded", rejected: true}
	}
	transport := &http.Transport{DialContext: modelconfig.DialPublic, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 45 * time.Second, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding request is invalid", rejected: true}
	}
	request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		var operationError *net.OpError
		var dnsError *net.DNSError
		notSent := errors.As(err, &dnsError) || errors.As(err, &operationError) && operationError.Op == "dial"
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding provider connection failed", rejected: notSent}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding provider response could not be read"}
	}
	if len(data) > 2<<20 {
		return EmbeddingResponse{}, &embeddingProviderError{message: "embedding provider response is too large", rejected: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return EmbeddingResponse{}, &embeddingProviderError{message: fmt.Sprintf("embedding provider returned HTTP %d", response.StatusCode), rejected: true}
	}
	var raw json.RawMessage
	if json.Valid(data) {
		raw = append(json.RawMessage(nil), data...)
	} else {
		raw, _ = json.Marshal(map[string]string{"unparsable": string(data[:min(len(data), 20000)])})
	}
	return EmbeddingResponse{Body: raw, RequestID: response.Header.Get("x-request-id")}, nil
}

/** embeddingRequestBody omits dimensions when the provider selects its own vector size. */
func embeddingRequestBody(cfg EmbeddingConfig, texts []string) map[string]any {
	body := map[string]any{"model": cfg.Model, "input": texts, "encoding_format": "float"}
	if cfg.Dimensions > 0 {
		body["dimensions"] = cfg.Dimensions
	}
	return body
}

/** parseEmbeddingResponse validates provider vectors and restores their input order. */
func parseEmbeddingResponse(raw []byte, count, dimensions int) ([][]float32, error) {
	var response struct {
		Data []struct {
			Index     *int       `json:"index"`
			Embedding []*float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || len(response.Data) != count {
		return nil, errors.New("embedding count or JSON shape is invalid")
	}
	ordered := make([][]float32, count)
	seen := make([]bool, count)
	for _, row := range response.Data {
		if row.Index == nil || *row.Index < 0 || *row.Index >= count {
			return nil, errors.New("embedding index or dimensions are invalid")
		}
		index := *row.Index
		if seen[index] {
			return nil, errors.New("embedding indices are duplicated")
		}
		vector := make([]float32, len(row.Embedding))
		for coordinate, value := range row.Embedding {
			if value == nil {
				return nil, errors.New("embedding index or dimensions are invalid")
			}
			vector[coordinate] = *value
		}
		if !validVector(vector, dimensions) {
			return nil, errors.New("embedding index or dimensions are invalid")
		}
		seen[index] = true
		ordered[index] = vector
	}
	for _, found := range seen {
		if !found {
			return nil, errors.New("embedding indices are incomplete")
		}
	}
	return ordered, nil
}

func embeddingUsage(raw []byte) json.RawMessage {
	var response struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil || !json.Valid(response.Usage) || string(response.Usage) == "null" {
		return nil
	}
	return response.Usage
}

/** validVector rejects empty, mismatched, and non-finite embedding coordinates. */
func validVector(vector []float32, dimensions int) bool {
	if len(vector) == 0 || (dimensions > 0 && len(vector) != dimensions) {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func hashEmbeddingText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func decodePostgresFloatArray(raw []byte) ([]float32, bool) {
	if len(raw) < 20 || int32(binary.BigEndian.Uint32(raw[0:4])) != 1 || int32(binary.BigEndian.Uint32(raw[16:20])) != 1 {
		return nil, false
	}
	count := int(int32(binary.BigEndian.Uint32(raw[12:16])))
	if count < 1 || count > 100_000 || len(raw) != 20+count*8 {
		return nil, false
	}
	vector := make([]float32, count)
	for i := range vector {
		if int32(binary.BigEndian.Uint32(raw[20+i*8:24+i*8])) != 4 {
			return nil, false
		}
		vector[i] = math.Float32frombits(binary.BigEndian.Uint32(raw[24+i*8 : 28+i*8]))
	}
	return vector, true
}
