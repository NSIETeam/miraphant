package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/songquanpeng/one-api/common/render"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/conv"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

const (
	dataPrefix       = "data: "
	done             = "[DONE]"
	dataPrefixLength = len(dataPrefix)
)

const PointsUsageFinalizerKey = "miraphant.points_usage_finalizer"

func finalizePointsUsage(c *gin.Context, usage *model.Usage, complete bool) error {
	value, ok := c.Get(PointsUsageFinalizerKey)
	if !ok {
		return nil
	}
	finalizer, ok := value.(func(*model.Usage, bool) error)
	if !ok {
		return errors.New("invalid points usage finalizer")
	}
	return finalizer(usage, complete)
}

func markAuthoritativeUsage(usage *model.Usage, complete bool) {
	if usage == nil || !complete || !usage.PromptTokensPresent || !usage.CompletionTokensPresent || !usage.TotalTokensPresent || usage.PromptTokens <= 0 || usage.CompletionTokens < 0 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return
	}
	if usage.PromptTokensDetails != nil && (usage.PromptTokensDetails.CachedTokens < 0 || usage.PromptTokensDetails.CachedTokens > usage.PromptTokens) {
		return
	}
	if usage.CompletionTokensDetails != nil && (usage.CompletionTokensDetails.ReasoningTokens < 0 || usage.CompletionTokensDetails.ReasoningTokens > usage.CompletionTokens) {
		return
	}
	usage.UsageSource = "provider_openai_usage"
	usage.UsageAuthoritative = true
}

func StreamHandler(c *gin.Context, resp *http.Response, relayMode int) (*model.ErrorWithStatusCode, string, *model.Usage) {
	defer resp.Body.Close()
	responseText := ""
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	var usage *model.Usage
	responseID := ""
	pointsMode := hasPointsUsageFinalizer(c)
	finalized := false
	finish := func(complete bool) error {
		if finalized || !pointsMode {
			return nil
		}
		finalized = true
		if usage != nil {
			usage.ProviderResponseID = responseID
			markAuthoritativeUsage(usage, complete)
		} else if responseID != "" {
			usage = &model.Usage{ProviderResponseID: responseID}
		}
		return finalizePointsUsage(c, usage, complete)
	}

	common.SetEventStreamHeaders(c)

	doneRendered := false
	for scanner.Scan() {
		data := scanner.Text()
		if len(data) < dataPrefixLength { // ignore blank line or wrong format
			continue
		}
		if data[:dataPrefixLength] != dataPrefix && data[:dataPrefixLength] != done {
			continue
		}
		if data == dataPrefix+done {
			if err := finish(true); err != nil {
				writePointStreamError(c)
				return ErrorWrapper(errors.New("points usage could not be recorded; hold remains available for reconciliation"), "points_usage_settlement_failed", http.StatusInternalServerError), responseText, usage
			}
			render.StringData(c, data)
			doneRendered = true
			break
		}
		switch relayMode {
		case relaymode.ChatCompletions:
			var streamResponse ChatCompletionsStreamResponse
			err := json.Unmarshal([]byte(data[dataPrefixLength:]), &streamResponse)
			if err != nil {
				logger.SysError("error unmarshalling stream response: " + err.Error())
				render.StringData(c, data) // if error happened, pass the data to client
				continue                   // just ignore the error
			}
			if len(streamResponse.Choices) == 0 && streamResponse.Usage == nil {
				// but for empty choice and no usage, we should not pass it to client, this is for azure
				continue // just ignore empty choice
			}
			render.StringData(c, data)
			for _, choice := range streamResponse.Choices {
				responseText += conv.AsString(choice.Delta.Content)
			}
			if streamResponse.Id != "" {
				responseID = streamResponse.Id
			}
			if streamResponse.Usage != nil {
				usage = streamResponse.Usage
			}
		case relaymode.Completions:
			render.StringData(c, data)
			var streamResponse CompletionsStreamResponse
			err := json.Unmarshal([]byte(data[dataPrefixLength:]), &streamResponse)
			if err != nil {
				logger.SysError("error unmarshalling stream response: " + err.Error())
				continue
			}
			for _, choice := range streamResponse.Choices {
				responseText += choice.Text
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.SysError("error reading stream: " + err.Error())
	}

	if pointsMode && !finalized {
		if err := finish(false); err != nil {
			if c.Writer.Written() {
				writePointStreamError(c)
			}
			return ErrorWrapper(errors.New("points usage could not be recorded; hold remains available for reconciliation"), "points_usage_persist_failed", http.StatusInternalServerError), responseText, usage
		}
	}
	if !doneRendered && !pointsMode {
		render.Done(c)
	}

	err := resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), "", nil
	}

	return nil, responseText, usage
}

func Handler(c *gin.Context, resp *http.Response, promptTokens int, modelName string) (*model.ErrorWithStatusCode, *model.Usage) {
	var textResponse SlimTextResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	err = json.Unmarshal(responseBody, &textResponse)
	if err != nil {
		return ErrorWrapper(err, "unmarshal_response_body_failed", http.StatusInternalServerError), nil
	}
	if textResponse.Error.Type != "" {
		return &model.ErrorWithStatusCode{
			Error:      textResponse.Error,
			StatusCode: resp.StatusCode,
		}, nil
	}
	usage := textResponse.Usage
	if pointsMode := hasPointsUsageFinalizer(c); pointsMode {
		if usage == nil && textResponse.Id != "" {
			usage = &model.Usage{ProviderResponseID: textResponse.Id}
		}
		if usage != nil {
			usage.ProviderResponseID = textResponse.Id
			markAuthoritativeUsage(usage, true)
		}
		if err := finalizePointsUsage(c, usage, true); err != nil {
			return ErrorWrapper(errors.New("points usage could not be recorded; hold remains available for reconciliation"), "points_usage_settlement_failed", http.StatusInternalServerError), nil
		}
	}
	// Reset response body
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))

	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the HTTPClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return ErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return ErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}

	if !hasPointsUsageFinalizer(c) && (textResponse.Usage == nil || textResponse.Usage.TotalTokens == 0 || (textResponse.Usage.PromptTokens == 0 && textResponse.Usage.CompletionTokens == 0)) {
		completionTokens := 0
		for _, choice := range textResponse.Choices {
			completionTokens += CountTokenText(choice.Message.StringContent(), modelName)
		}
		usage = &model.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
	}
	return nil, usage
}

func hasPointsUsageFinalizer(c *gin.Context) bool { _, ok := c.Get(PointsUsageFinalizerKey); return ok }

func writePointStreamError(c *gin.Context) {
	_, _ = c.Writer.WriteString("event: error\ndata: {\"error\":{\"message\":\"points usage could not be recorded; funds remain held for reconciliation\",\"type\":\"points_settlement_error\"}}\n\n")
	c.Writer.Flush()
}
