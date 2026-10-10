package openai

import (
	"encoding/json"
	"testing"
)

func TestPointProviderUsageFieldsArePresenceCheckedWithoutBreakingEmbeddedUsage(t *testing.T) {
	var slim SlimTextResponse
	if err := json.Unmarshal([]byte(`{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"total_tokens":7}}`), &slim); err != nil {
		t.Fatal(err)
	}
	if len(slim.Choices) != 1 || slim.Usage == nil || !slim.Usage.PromptTokensPresent || slim.Usage.CompletionTokensPresent || !slim.Usage.TotalTokensPresent {
		t.Fatalf("point parser failed to retain usage field presence or choices: %+v", slim)
	}
	markAuthoritativeUsage(slim.Usage, true)
	if slim.Usage.UsageAuthoritative {
		t.Fatal("missing completion_tokens was marked authoritative")
	}

	var legacy TextResponse
	if err := json.Unmarshal([]byte(`{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.Choices) != 1 || legacy.Usage.PromptTokens != 5 || legacy.Usage.CompletionTokens != 2 || legacy.Usage.TotalTokens != 7 {
		t.Fatalf("shared anonymous Usage decoding regressed: choices=%d usage=%+v", len(legacy.Choices), legacy.Usage)
	}

	var stream ChatCompletionsStreamResponse
	if err := json.Unmarshal([]byte(`{"id":"stream-r","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`), &stream); err != nil {
		t.Fatal(err)
	}
	if stream.Usage == nil || !stream.Usage.CompletionTokensPresent || stream.Usage.CompletionTokens != 2 {
		t.Fatalf("stream usage field presence missing: %+v", stream.Usage)
	}
}
