package milvus

type DocumentChunk struct {
	ID             string
	UserID         string
	AgentID        string
	Scope          string
	Content        string
	SourceDocument string
	ChunkIndex     int64
	Vector         []float32
}

type SearchResult struct {
	ID             string
	Content        string
	SourceDocument string
	ChunkIndex     int64
	// Score is a 0-1 normalized similarity, larger = more relevant. It is
	// produced by normalizeScore from the raw Milvus metric score (COSINE by
	// default), so downstream code never sees metric-specific raw distances.
	Score float32
}

type MCPRequest struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type MCPResponse struct {
	Result interface{} `json:"result"`
	Error  string      `json:"error,omitempty"`
}
