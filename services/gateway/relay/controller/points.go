package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

type pointsChatRequest struct {
	Model               string                     `json:"model"`
	Messages            []relaymodel.Message       `json:"messages"`
	MaxTokens           int                        `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                       `json:"max_completion_tokens,omitempty"`
	Stream              bool                       `json:"stream,omitempty"`
	StreamOptions       *relaymodel.StreamOptions  `json:"stream_options,omitempty"`
	Temperature         *float64                   `json:"temperature,omitempty"`
	TopP                *float64                   `json:"top_p,omitempty"`
	FrequencyPenalty    *float64                   `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64                   `json:"presence_penalty,omitempty"`
	Stop                any                        `json:"stop,omitempty"`
	ResponseFormat      *relaymodel.ResponseFormat `json:"response_format,omitempty"`
	Seed                *float64                   `json:"seed,omitempty"`
	User                string                     `json:"user,omitempty"`
}

func RelayPointsText(c *gin.Context, meta *meta.Meta, req *relaymodel.GeneralOpenAIRequest, promptTokens int) *relaymodel.ErrorWithStatusCode {
	if c.Request.URL.Path != "/v1/chat/completions" || meta.Mode != relaymode.ChatCompletions || meta.APIType != apitype.OpenAI {
		return openai.ErrorWrapper(errors.New("points billing currently supports only OpenAI-compatible text chat completions"), "points_route_unsupported", http.StatusNotImplemented)
	}
	if req.MaxTokens > 0 && req.MaxCompletionTokens != nil {
		return openai.ErrorWrapper(errors.New("specify only one of max_tokens or max_completion_tokens"), "invalid_output_limit", http.StatusBadRequest)
	}
	maxOutput := req.MaxTokens
	if req.MaxCompletionTokens != nil {
		maxOutput = *req.MaxCompletionTokens
	}
	maxOutputLimit := config.PointsMaxOutputTokens
	if maxOutputLimit < 1 {
		maxOutputLimit = 8192
	}
	if maxOutput <= 0 || maxOutput > maxOutputLimit {
		return openai.ErrorWrapper(fmt.Errorf("max_tokens or max_completion_tokens must be between 1 and %d", maxOutputLimit), "invalid_output_limit", http.StatusBadRequest)
	}
	if req.N > 1 || len(req.Tools) > 0 || req.ToolChoice != nil || req.FunctionCall != nil || req.Functions != nil || req.Audio != nil {
		return openai.ErrorWrapper(errors.New("points mode supports one text completion without tools or audio"), "unsupported_request_feature", http.StatusBadRequest)
	}
	if req.Store != nil || req.Metadata != nil || req.LogitBias != nil || req.Logprobs != nil || req.TopLogprobs != nil || req.Prediction != nil || req.ServiceTier != nil || req.TopK != 0 || req.ParallelTooCalls != nil {
		return openai.ErrorWrapper(errors.New("request contains a feature not supported by points text chat"), "unsupported_request_feature", http.StatusBadRequest)
	}
	for _, modality := range req.Modalities {
		if modality != "text" {
			return openai.ErrorWrapper(errors.New("points mode supports text modality only"), "unsupported_modality", http.StatusBadRequest)
		}
	}
	for _, message := range req.Messages {
		if !message.IsStringContent() || len(message.ToolCalls) > 0 || message.ToolCallId != "" {
			return openai.ErrorWrapper(errors.New("points mode supports plain text messages only"), "unsupported_message_content", http.StatusBadRequest)
		}
	}
	if len(req.Messages) == 0 {
		return openai.ErrorWrapper(errors.New("messages are required"), "invalid_text_request", http.StatusBadRequest)
	}

	priceModelID := meta.OriginModelName
	if priceModelID == "" {
		priceModelID = meta.ActualModelName
	}
	price, err := model.GetActivePointPrice(priceModelID)
	if err != nil {
		return openai.ErrorWrapper(errors.New("model has no active points price"), "model_not_priced", http.StatusUnprocessableEntity)
	}
	if price.CachedInputMicroPer1K > price.InputMicroPer1K {
		return openai.ErrorWrapper(errors.New("cached input price must not exceed regular input price"), "invalid_price", http.StatusUnprocessableEntity)
	}
	// This byte-count estimate is deliberately conservative and is used only to reserve funds.
	// Settlement always depends on complete, validated provider usage.
	promptEstimate := int64(0)
	for _, msg := range req.Messages {
		promptEstimate += int64(len(msg.Role) + len(msg.StringContent()) + 4)
	}
	if promptTokens > 0 && int64(promptTokens) > promptEstimate {
		promptEstimate = int64(promptTokens)
	}
	budget, err := model.CalculatePointUsage(promptEstimate, 0, int64(maxOutput), *price)
	if err != nil || budget <= 0 {
		return openai.ErrorWrapper(errors.New("points reserve calculation failed"), "invalid_points_reserve", http.StatusBadRequest)
	}

	requestID := c.GetString(helper.RequestIdKey)
	if requestID == "" {
		requestID = helper.GenRequestID()
		c.Set(helper.RequestIdKey, requestID)
	}
	idemKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idemKey == "" {
		idemKey = requestID
	}
	keyHash := sha256.Sum256([]byte(idemKey))
	logicalKey := fmt.Sprintf("relay:%d:%d:%s", meta.UserId, meta.TokenId, hex.EncodeToString(keyHash[:]))
	bodyFingerprint, err := json.Marshal(req)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_text_request", http.StatusBadRequest)
	}
	fingerprintHash := sha256.Sum256(bodyFingerprint)
	fingerprint := hex.EncodeToString(fingerprintHash[:])
	_, err = model.ReservePoints(model.PointReserveRequest{UserID: meta.UserId, TokenID: meta.TokenId, LogicalRequestKey: logicalKey, AttemptKey: "primary", LocalRequestID: requestID, AttemptFingerprint: fingerprint, BudgetMicro: budget, PriceVersion: price.Version, RejectExisting: true})
	if err != nil {
		status := http.StatusInternalServerError
		message := "points reserve could not be completed"
		if errors.Is(err, model.ErrPointsInsufficient) || errors.Is(err, model.ErrPointsConflict) {
			status = http.StatusConflict
			if errors.Is(err, model.ErrPointsInsufficient) {
				message = "insufficient available points or token budget"
			} else {
				message = "idempotency key has already been used"
			}
		}
		return openai.ErrorWrapper(errors.New(message), "points_reserve_failed", status)
	}

	providerRequestID := ""
	var once sync.Once
	var finalErr error
	finalize := func(usage *relaymodel.Usage, complete bool) error {
		once.Do(func() {
			report := model.PointAttemptUsage{LogicalRequestKey: logicalKey, AttemptKey: "primary", State: "unknown", UsageSource: "missing_or_untrusted", Authoritative: false, Billable: true, ProviderRequestID: providerRequestID}
			if usage != nil {
				report.ProviderResponseID = usage.ProviderResponseID
				report.PromptTokens = int64(usage.PromptTokens)
				report.CompletionTokens = int64(usage.CompletionTokens)
				if usage.PromptTokensDetails != nil {
					report.CachedPromptTokens = int64(usage.PromptTokensDetails.CachedTokens)
				}
				if usage.CompletionTokensDetails != nil {
					report.ReasoningTokens = int64(usage.CompletionTokensDetails.ReasoningTokens)
				}
				if complete && usage.UsageAuthoritative {
					report.State = "succeeded"
					report.UsageSource = usage.UsageSource
					report.Authoritative = true
					value, calcErr := model.CalculatePointUsage(report.PromptTokens, report.CachedPromptTokens, report.CompletionTokens, *price)
					if calcErr != nil {
						finalErr = errors.New("provider usage calculation failed")
						return
					}
					report.UsageMicro = value
					serialized, _ := json.Marshal(usage)
					sum := sha256.Sum256(serialized)
					report.Fingerprint = hex.EncodeToString(sum[:])
				}
			}
			_, recordErr := model.RecordPointAttemptUsage(report)
			if recordErr != nil {
				finalErr = errors.New("points usage could not be recorded; hold remains available for reconciliation")
			}
		})
		return finalErr
	}
	c.Set(openai.PointsUsageFinalizerKey, finalize)

	markUnsent := func(reason string) {
		if err := model.ReleaseUnsentPointHold(logicalKey, reason); err != nil {
			_ = finalize(nil, false)
		}
	}
	upstream := relay.GetAdaptor(meta.APIType)
	if upstream == nil {
		markUnsent("no adaptor before upstream send")
		return openai.ErrorWrapper(errors.New("invalid api type"), "invalid_api_type", http.StatusBadRequest)
	}
	upstream.Init(meta)
	pointReq := pointsChatRequest{Model: meta.ActualModelName, Messages: req.Messages, MaxTokens: req.MaxTokens, MaxCompletionTokens: req.MaxCompletionTokens, Stream: req.Stream, Temperature: req.Temperature, TopP: req.TopP, FrequencyPenalty: req.FrequencyPenalty, PresencePenalty: req.PresencePenalty, Stop: req.Stop, ResponseFormat: req.ResponseFormat, User: req.User}
	if req.Seed != 0 {
		seed := req.Seed
		pointReq.Seed = &seed
	}
	if req.Stream {
		pointReq.StreamOptions = &relaymodel.StreamOptions{IncludeUsage: true}
	}
	requestJSON, err := json.Marshal(pointReq)
	if err != nil {
		markUnsent("request conversion failed before upstream send")
		return openai.ErrorWrapper(errors.New("request conversion failed"), "convert_request_failed", http.StatusInternalServerError)
	}
	timeoutSeconds := config.PointsRelayTimeout
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	requestContext, cancel := context.WithTimeout(c.Request.Context(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(requestContext)
	resp, err := upstream.DoRequest(c, meta, strings.NewReader(string(requestJSON)))
	if err != nil {
		_ = finalize(nil, false)
		return openai.ErrorWrapper(errors.New("upstream request status is unknown; points remain held for reconciliation"), "do_request_failed", http.StatusBadGateway)
	}
	if resp != nil && resp.Header.Get("X-Request-Id") != "" {
		providerRequestID = resp.Header.Get("X-Request-Id")
	}
	if isErrorHappened(meta, resp) {
		_ = finalize(nil, false)
		return RelayErrorHandler(resp)
	}
	usage, respErr := upstream.DoResponse(c, resp, meta)
	if respErr != nil {
		_ = finalize(usage, false)
		return respErr
	}
	if !meta.IsStream && usage == nil {
		_ = finalize(nil, false)
	}
	return nil
}
