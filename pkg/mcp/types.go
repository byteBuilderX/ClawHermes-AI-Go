package mcp

type MCPRequest struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type MCPResponse struct {
	Result interface{} `json:"result"`
	Error  string      `json:"error,omitempty"`
}
