package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/byteBuilderX/stratum/internal/memory/domain"
	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	storagemilvus "github.com/byteBuilderX/stratum/pkg/storage/milvus"
)

const milvusSourceDocumentMaxLength = 255

type memoryFactSourceDocument struct {
	ConversationID string  `json:"conversation_id"`
	Importance     float64 `json:"importance"`
	Category       string  `json:"category"`
	Confidence     float64 `json:"confidence"`
	Source         string  `json:"source"`
}

type milvusStore interface {
	CreateCollectionWithDim(context.Context, string, int) error
	Insert(context.Context, string, []storagemilvus.DocumentChunk, string) error
	DeleteByPrimaryIDs(context.Context, string, []string) error
	DeleteByFilter(context.Context, string, string) error
	SearchWithFilter(context.Context, string, []float32, int, string, ...string) ([]storagemilvus.SearchResult, error)
	ListCollections(context.Context, string) ([]string, error)
}

// MilvusPortAdapter adapts *storagemilvus.VectorStore to memport.VectorStore.
type MilvusPortAdapter struct{ vs milvusStore }

func NewMilvusPortAdapter(vs milvusStore) *MilvusPortAdapter {
	return &MilvusPortAdapter{vs: vs}
}

func (a *MilvusPortAdapter) Delete(ctx context.Context, collectionName string, ids []string) error {
	return a.vs.DeleteByPrimaryIDs(ctx, collectionName, ids)
}

func (a *MilvusPortAdapter) CreateCollection(ctx context.Context, collectionName string, dim int) error {
	return a.vs.CreateCollectionWithDim(ctx, collectionName, dim)
}

func (a *MilvusPortAdapter) Upsert(ctx context.Context, collectionName string, docs []*memport.VectorDoc) error {
	if len(docs) == 0 {
		return nil
	}
	dim := len(docs[0].Embedding)
	if dim == 0 {
		return fmt.Errorf("milvus upsert: zero-dimension vector")
	}
	if err := a.vs.CreateCollectionWithDim(ctx, collectionName, dim); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	chunks := make([]storagemilvus.DocumentChunk, len(docs))
	for i, d := range docs {
		chunk, err := memoryFactDocumentChunk(d)
		if err != nil {
			return fmt.Errorf("milvus upsert: document %q: %w", d.ID, err)
		}
		chunks[i] = chunk
	}
	return a.vs.Insert(ctx, collectionName, chunks, "")
}

func memoryFactDocumentChunk(doc *memport.VectorDoc) (storagemilvus.DocumentChunk, error) {
	if doc == nil {
		return storagemilvus.DocumentChunk{}, fmt.Errorf("invalid metadata: nil document")
	}
	userID, err := requiredMilvusMetadataString(doc.Metadata, "user_id")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	agentID, err := requiredMilvusMetadataString(doc.Metadata, "agent_id")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	scope, err := requiredMilvusMetadataString(doc.Metadata, "scope")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	content, err := requiredMilvusMetadataString(doc.Metadata, "content")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	conversationID, err := requiredMilvusMetadataString(doc.Metadata, "conversation_id")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	importance, err := requiredMilvusMetadataFloat64(doc.Metadata, "importance")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	category, err := requiredMilvusMetadataString(doc.Metadata, "category")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	confidence, err := requiredMilvusMetadataFloat64(doc.Metadata, "confidence")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}
	source, err := requiredMilvusMetadataString(doc.Metadata, "source")
	if err != nil {
		return storagemilvus.DocumentChunk{}, err
	}

	encoded, err := json.Marshal(memoryFactSourceDocument{
		ConversationID: conversationID,
		Importance:     importance,
		Category:       category,
		Confidence:     confidence,
		Source:         source,
	})
	if err != nil {
		return storagemilvus.DocumentChunk{}, fmt.Errorf("encode source_document metadata: %w", err)
	}
	if len(encoded) > milvusSourceDocumentMaxLength {
		return storagemilvus.DocumentChunk{}, fmt.Errorf("source_document metadata length %d exceeds 255-character limit", len(encoded))
	}

	return storagemilvus.DocumentChunk{
		ID:             doc.ID,
		Vector:         doc.Embedding,
		UserID:         userID,
		AgentID:        agentID,
		Scope:          scope,
		Content:        content,
		SourceDocument: string(encoded),
	}, nil
}

func requiredMilvusMetadataString(metadata map[string]interface{}, key string) (string, error) {
	value, ok := metadata[key]
	if !ok {
		return "", fmt.Errorf("invalid metadata: %q is required", key)
	}
	result, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid metadata: %q must be a string", key)
	}
	return result, nil
}

func requiredMilvusMetadataFloat64(metadata map[string]interface{}, key string) (float64, error) {
	value, ok := metadata[key]
	if !ok {
		return 0, fmt.Errorf("invalid metadata: %q is required", key)
	}
	result, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("invalid metadata: %q must be a float64", key)
	}
	return result, nil
}

func (a *MilvusPortAdapter) Search(ctx context.Context, collectionName string, queryVector []float32, topK int, filter memport.VectorSearchFilter) ([]*memport.VectorDoc, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	expr := fmt.Sprintf("user_id == %s", strconv.Quote(filter.UserID))
	switch {
	case filter.IncludeUserScope && filter.IncludeAgentScope:
		expr += fmt.Sprintf(" && (scope == \"user\" || (scope == \"agent\" && agent_id == %s))", strconv.Quote(filter.AgentID))
	case filter.IncludeUserScope:
		expr += " && scope == \"user\""
	case filter.IncludeAgentScope:
		expr += fmt.Sprintf(" && scope == \"agent\" && agent_id == %s", strconv.Quote(filter.AgentID))
	}
	results, err := a.vs.SearchWithFilter(ctx, collectionName, queryVector, topK, expr)
	if err != nil {
		var unavailable *storagemilvus.UnavailableError
		if errors.As(err, &unavailable) {
			return nil, &memport.VectorStoreUnavailableError{Err: err}
		}
		return nil, err
	}
	docs := make([]*memport.VectorDoc, 0, len(results))
	for _, result := range results {
		docs = append(docs, &memport.VectorDoc{
			ID: result.ID,
			Metadata: map[string]interface{}{
				"content": result.Content, "source_document": result.SourceDocument, "chunk_index": result.ChunkIndex,
			},
			Similarity: float64(result.Score),
		})
	}
	return docs, nil
}

func (a *MilvusPortAdapter) DeleteAllByUser(ctx context.Context, tenantID, userID string) error {
	return a.deleteBothMemoryCollections(ctx, tenantID, "user_id", userID)
}

func (a *MilvusPortAdapter) DeleteAllByAgent(ctx context.Context, tenantID, agentID string) error {
	expr := fmt.Sprintf("agent_id == %q and scope == %q", agentID, string(domain.ScopeAgent))
	return a.deleteFromAllCollections(ctx, tenantID, expr)
}

func (a *MilvusPortAdapter) deleteBothMemoryCollections(ctx context.Context, tenantID, field, value string) error {
	expr := fmt.Sprintf("%s == %q", field, value)
	return a.deleteFromAllCollections(ctx, tenantID, expr)
}

// deleteFromAllCollections applies the filter to the tenant's legacy
// (no-model-suffix) collections and to every model-suffixed collection listed
// by prefix, so delete-all paths cover every historical default embedding
// model, not just the current one.
func (a *MilvusPortAdapter) deleteFromAllCollections(ctx context.Context, tenantID, expr string) error {
	var errs []error
	for _, collection := range legacyMemoryCollections(tenantID) {
		if err := a.vs.DeleteByFilter(ctx, collection, expr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", collection, err))
		}
	}
	modelSuffixed, listErr := a.modelSuffixedMemoryCollections(ctx, tenantID)
	if listErr != nil {
		// 列不出来就不能静默当没这回事：失败必须暴露，否则模型后缀集合漏删。
		errs = append(errs, listErr)
	}
	for _, collection := range modelSuffixed {
		if err := a.vs.DeleteByFilter(ctx, collection, expr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", collection, err))
		}
	}
	return errors.Join(errs...)
}

// modelSuffixedMemoryCollections lists the tenant's model-suffixed memory
// collections (memory_<t>_<model> / memory_facts_<t>_<model>). The trailing
// underscore keeps tenant t1 from matching t10. A listing failure is returned
// so the delete path surfaces it instead of leaking collections.
func (a *MilvusPortAdapter) modelSuffixedMemoryCollections(ctx context.Context, tenantID string) ([]string, error) {
	tid := strings.ReplaceAll(tenantID, "-", "_")
	var cols []string
	var errs []error
	for _, prefix := range []string{"memory_" + tid + "_", "memory_facts_" + tid + "_"} {
		listed, err := a.vs.ListCollections(ctx, prefix)
		if err != nil {
			errs = append(errs, fmt.Errorf("list collections %q: %w", prefix, err))
			continue
		}
		cols = append(cols, listed...)
	}
	return cols, errors.Join(errs...)
}

// memoryFactsCollectionLegacyName / memoryCollectionLegacyName 是无模型后缀的
// 存量 collection 名（升级前数据）。删除路径统一经此拼写，与 pipeline 查询回退
// （memoryFactsCollectionLegacyName）保持一致，避免两处命名漂移。
func memoryFactsCollectionLegacyName(tenantID string) string {
	return "memory_facts_" + strings.ReplaceAll(tenantID, "-", "_")
}

func memoryCollectionLegacyName(tenantID string) string {
	return "memory_" + strings.ReplaceAll(tenantID, "-", "_")
}

// legacyMemoryCollections 返回租户的存量（无模型后缀）collection 名列表，
// 删除路径统一经此枚举。模型后缀 collection 不在此列——它们由
// modelSuffixedMemoryCollections 按前缀枚举，两者共同构成 delete-all 全集。
func legacyMemoryCollections(tenantID string) []string {
	return []string{memoryFactsCollectionLegacyName(tenantID), memoryCollectionLegacyName(tenantID)}
}

// DeleteEntryVectors removes the given raw-turn entry ids from the tenant's
// memory_ collections (legacy + every model-suffixed). The episodic TTL GC
// drives deletion from PG ids, so rows and vectors stay consistent.
func (a *MilvusPortAdapter) DeleteEntryVectors(ctx context.Context, tenantID string, ids []string) error {
	return a.deleteByIDsFromFamily(ctx, tenantID, "memory_", memoryCollectionLegacyName(tenantID), ids)
}

// DeleteFactVectors removes the given fact ids from the tenant's
// memory_facts_ collections (legacy + every model-suffixed). Called when a
// fact leaves the active set and by the GC reconcile so vectors converge to
// PG status even if an immediate deletion failed.
func (a *MilvusPortAdapter) DeleteFactVectors(ctx context.Context, tenantID string, ids []string) error {
	return a.deleteByIDsFromFamily(ctx, tenantID, "memory_facts_", memoryFactsCollectionLegacyName(tenantID), ids)
}

// deleteByIDsFromFamily deletes the given ids from the tenant's legacy
// collection plus every model-suffixed collection matching the family prefix.
// DeleteByPrimaryIDs tolerates missing collections; listing failures surface
// instead of silently leaking model-suffixed collections (same rule as
// deleteFromAllCollections).
func (a *MilvusPortAdapter) deleteByIDsFromFamily(ctx context.Context, tenantID, prefix, legacy string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var errs []error
	if err := a.vs.DeleteByPrimaryIDs(ctx, legacy, ids); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", legacy, err))
	}
	tid := strings.ReplaceAll(tenantID, "-", "_")
	listedPrefix := prefix + tid + "_"
	modelSuffixed, listErr := a.vs.ListCollections(ctx, listedPrefix)
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list collections %q: %w", listedPrefix, listErr))
	}
	for _, collection := range modelSuffixed {
		if err := a.vs.DeleteByPrimaryIDs(ctx, collection, ids); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", collection, err))
		}
	}
	return errors.Join(errs...)
}
