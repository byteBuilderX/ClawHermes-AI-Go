package model

type CreateSkillRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Type        string `json:"type" binding:"required,oneof=code builtin"`
	Code        string `json:"code"`
	Language    string `json:"language"`
}

type SkillResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	CreatedAt   string `json:"created_at"`
}

type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ExecuteSkillRequest struct {
	Input interface{} `json:"input"`
}

type ExecuteSkillResponse struct {
	Result interface{} `json:"result"`
	Error  string      `json:"error,omitempty"`
}
