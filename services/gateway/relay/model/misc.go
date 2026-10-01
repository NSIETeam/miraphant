package model

type Usage struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	TotalTokens             int                          `json:"total_tokens"`
	PromptTokensDetails     *UsagePromptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *UsageCompletionTokenDetails `json:"completion_tokens_details,omitempty"`
	UsageSource             string                       `json:"-"`
	UsageAuthoritative      bool                         `json:"-"`
	ProviderResponseID      string                       `json:"-"`
	PromptTokensPresent     bool                         `json:"-"`
	CompletionTokensPresent bool                         `json:"-"`
	TotalTokensPresent      bool                         `json:"-"`
}

type UsagePromptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}
type UsageCompletionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
}

type ErrorWithStatusCode struct {
	Error
	StatusCode int `json:"status_code"`
}
