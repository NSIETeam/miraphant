package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/helper"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/channeltype"
	relaymeta "github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestPointsRelayNonStreamSettlesAuthoritativeUsageAndRejectsReplay(t *testing.T) {
	oldDB, oldSQLite, oldEnabled, oldClient := dbmodel.DB, common.UsingSQLite, config.PointsBillingEnabled, client.HTTPClient
	t.Cleanup(func() {
		dbmodel.DB, common.UsingSQLite, config.PointsBillingEnabled, client.HTTPClient = oldDB, oldSQLite, oldEnabled, oldClient
	})
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "relay.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	common.UsingSQLite = true
	config.PointsBillingEnabled = true
	client.HTTPClient = &http.Client{}
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.User{Id: 41, Username: "relay-user", Password: "fixture", AccessToken: "relay-user-access", AffCode: "relay-user-code", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.Token{Id: 77, UserId: 41, Key: "relay-test-token", Status: dbmodel.TokenStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointAccount{UserID: 41, AvailableMicro: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointLot{UserID: 41, BusinessKey: "relay-seed", Kind: "grant", InitialMicro: 1_000_000, AvailableMicro: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointTokenBudget{TokenID: 77, UserID: 41, LimitMicro: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	price := dbmodel.PointPriceVersion{ModelID: "public-model", Version: "public-v1", InputMicroPer1K: 1000, CachedInputMicroPer1K: 500, OutputMicroPer1K: 2000}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointActivePrice{ModelID: price.ModelID, Version: price.Version}).Error; err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"mapped-model"`) || strings.Contains(string(body), "unknown_param") {
			t.Errorf("whitelisted request omitted mapped model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "provider-req-1")
		_, _ = w.Write([]byte(`{"id":"provider-resp-1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":10},"total_tokens":140}}`))
	}))
	defer upstream.Close()
	call := func(idem string) (*httptest.ResponseRecorder, *relaymodel.ErrorWithStatusCode) {
		body := fmt.Sprintf(`{"model":"public-model","messages":[{"role":"user","content":%q}],"max_tokens":50,"temperature":0.2,"unknown_param":{"ignored":"must not pass"}}`, strings.Repeat("x", 100))
		var req relaymodel.GeneralOpenAIRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		c.Request.Header.Set("Idempotency-Key", idem)
		c.Set(helper.RequestIdKey, "local-request-"+idem)
		c.Set(ctxkey.Id, 41)
		c.Set(ctxkey.TokenId, 77)
		m := &relaymeta.Meta{Mode: relaymode.ChatCompletions, ChannelType: channeltype.OpenAI, APIType: apitype.OpenAI, ChannelId: 1, TokenId: 77, UserId: 41, BaseURL: upstream.URL, RequestURLPath: "/v1/chat/completions", OriginModelName: "public-model", ActualModelName: "mapped-model"}
		return recorder, RelayPointsText(c, m, &req, 0)
	}
	response, bizErr := call("stable-key")
	if bizErr != nil {
		t.Fatalf("relay failed: %+v", bizErr)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
	}
	var hold dbmodel.PointHold
	if err := db.First(&hold, "logical_request_key LIKE ?", "relay:41:77:%").Error; err != nil {
		t.Fatal(err)
	}
	if hold.State != "settled" || hold.UsageMicro != 170 {
		t.Fatalf("provider usage not precisely settled: %+v", hold)
	}
	var attempt dbmodel.PointHoldAttempt
	if err := db.First(&attempt, "hold_id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !attempt.UsageAuthoritative || attempt.ProviderRequestID != "provider-req-1" || attempt.ProviderResponseID != "provider-resp-1" || attempt.CachedPromptTokens != 20 || attempt.ReasoningTokens != 10 {
		t.Fatalf("provider evidence incomplete: %+v", attempt)
	}
	replay, dupErr := call("stable-key")
	if dupErr == nil || dupErr.StatusCode != http.StatusConflict {
		t.Fatalf("replay should conflict, got %+v", dupErr)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("replay called upstream %d times", upstreamCalls.Load())
	}
	_ = replay
	var account dbmodel.PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 999_830 || account.HeldMicro != 0 || account.SpentMicro != 170 {
		t.Fatalf("wrong ledger settlement: %+v", account)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
}

func newRelayPointsDB(t *testing.T, balance int64) *gorm.DB {
	t.Helper()
	oldDB, oldSQLite, oldEnabled, oldClient := dbmodel.DB, common.UsingSQLite, config.PointsBillingEnabled, client.HTTPClient
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "relay-fixture.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		dbmodel.DB, common.UsingSQLite, config.PointsBillingEnabled, client.HTTPClient = oldDB, oldSQLite, oldEnabled, oldClient
	})
	dbmodel.DB = db
	common.UsingSQLite = true
	config.PointsBillingEnabled = true
	client.HTTPClient = &http.Client{}
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.User{Id: 41, Username: "relay-user", Password: "fixture", AccessToken: "relay-user-access", AffCode: "relay-user-code", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.Token{Id: 77, UserId: 41, Key: "relay-test-token", Status: dbmodel.TokenStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointAccount{UserID: 41, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointLot{UserID: 41, BusinessKey: "relay-seed", Kind: "grant", InitialMicro: balance, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointTokenBudget{TokenID: 77, UserID: 41, LimitMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	price := dbmodel.PointPriceVersion{ModelID: "public-model", Version: "public-v1", InputMicroPer1K: 1000, CachedInputMicroPer1K: 500, OutputMicroPer1K: 2000}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.PointActivePrice{ModelID: price.ModelID, Version: price.Version}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func callPointRelay(t *testing.T, serverURL string, reqBody, idem string) (*httptest.ResponseRecorder, *relaymodel.ErrorWithStatusCode) {
	t.Helper()
	var req relaymodel.GeneralOpenAIRequest
	if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	c.Request.Header.Set("Idempotency-Key", idem)
	c.Set(helper.RequestIdKey, "local-request-"+idem)
	c.Set(ctxkey.Id, 41)
	c.Set(ctxkey.TokenId, 77)
	meta := &relaymeta.Meta{Mode: relaymode.ChatCompletions, ChannelType: channeltype.OpenAI, APIType: apitype.OpenAI, ChannelId: 1, TokenId: 77, UserId: 41, BaseURL: serverURL, RequestURLPath: "/v1/chat/completions", OriginModelName: "public-model", ActualModelName: "mapped-model"}
	return response, RelayPointsText(c, meta, &req, 0)
}

func TestPointsRelayMissingOrPartialProviderUsageStaysFrozen(t *testing.T) {
	db := newRelayPointsDB(t, 1_000_000)
	mode := atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var usage string
		if mode.Load() == 0 {
			usage = ""
		} else {
			usage = `,"usage":{"prompt_tokens":100,"total_tokens":140}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"provider-pending","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]` + usage + `}`))
	}))
	defer upstream.Close()
	requestBody := fmt.Sprintf(`{"model":"public-model","messages":[{"role":"user","content":%q}],"max_tokens":50}`, strings.Repeat("x", 100))
	for _, key := range []string{"no-usage", "partial-usage"} {
		if key == "partial-usage" {
			mode.Store(1)
		}
		response, err := callPointRelay(t, upstream.URL, requestBody, key)
		if err != nil {
			t.Fatalf("call %s failed: %+v", key, err)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("call %s status=%d", key, response.Code)
		}
	}
	var firstAttempt dbmodel.PointHoldAttempt
	if err := db.Where("attempt_key = ?", "primary").Order("id ASC").First(&firstAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if firstAttempt.ProviderRequestID != "" || firstAttempt.LocalRequestID == "" {
		t.Fatalf("local request id was conflated with missing provider request id: %+v", firstAttempt)
	}
	var holds []dbmodel.PointHold
	if err := db.Order("id ASC").Find(&holds).Error; err != nil {
		t.Fatal(err)
	}
	if len(holds) != 2 || holds[0].State != "pending" || holds[1].State != "pending" {
		t.Fatalf("missing/partial usage was not left pending: %+v", holds)
	}
	var account dbmodel.PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.HeldMicro <= 0 || account.SpentMicro != 0 {
		t.Fatalf("estimated or incomplete usage was charged: %+v", account)
	}
}

func TestPointsRelayStreamFlushesChunksAndSettlesBeforeDone(t *testing.T) {
	db := newRelayPointsDB(t, 1_000_000)
	firstChunk := make(chan struct{})
	allowUsage := make(chan struct{})
	var releaseOnce sync.Once
	releaseUsage := func() { releaseOnce.Do(func() { close(allowUsage) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamRequest map[string]any
		_ = json.NewDecoder(r.Body).Decode(&upstreamRequest)
		options, _ := upstreamRequest["stream_options"].(map[string]any)
		if options["include_usage"] != true {
			t.Errorf("stream usage was not requested: %v", upstreamRequest)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "stream-provider-request")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"id\":\"stream-response-id\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first chunk\"}}]}\n\n"))
		flusher.Flush()
		close(firstChunk)
		<-allowUsage
		_, _ = w.Write([]byte("data: {\"id\":\"stream-response-id\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":20},\"completion_tokens\":40,\"completion_tokens_details\":{\"reasoning_tokens\":10},\"total_tokens\":140}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()
	defer releaseUsage()
	app := gin.New()
	app.POST("/v1/chat/completions", func(c *gin.Context) {
		data, _ := io.ReadAll(c.Request.Body)
		var req relaymodel.GeneralOpenAIRequest
		if err := json.Unmarshal(data, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid"})
			return
		}
		c.Set(helper.RequestIdKey, "stream-local-request")
		meta := &relaymeta.Meta{Mode: relaymode.ChatCompletions, ChannelType: channeltype.OpenAI, APIType: apitype.OpenAI, ChannelId: 1, TokenId: 77, UserId: 41, BaseURL: upstream.URL, RequestURLPath: "/v1/chat/completions", OriginModelName: "public-model", ActualModelName: "mapped-model", IsStream: req.Stream}
		if err := RelayPointsText(c, meta, &req, 0); err != nil {
			c.JSON(err.StatusCode, gin.H{"error": err.Error.Message})
		}
	})
	appServer := httptest.NewServer(app)
	defer appServer.Close()
	body := fmt.Sprintf(`{"model":"public-model","messages":[{"role":"user","content":%q}],"max_tokens":100,"stream":true}`, strings.Repeat("x", 100))
	req, err := http.NewRequest(http.MethodPost, appServer.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "stream-key")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	var firstLine string
	for !strings.Contains(firstLine, "first chunk") {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("first stream chunk was not flushed: line=%q err=%v", line, readErr)
		}
		firstLine += line
	}
	<-firstChunk
	var hold dbmodel.PointHold
	if err := db.First(&hold, "logical_request_key LIKE ?", "relay:41:77:%").Error; err != nil {
		t.Fatal(err)
	}
	if hold.State != "held" {
		t.Fatalf("hold settled before provider usage: %+v", hold)
	}
	releaseUsage()
	remainder, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	streamText := firstLine + string(remainder)
	if !strings.Contains(streamText, "[DONE]") {
		t.Fatalf("finished stream omitted DONE: %s", streamText)
	}
	if err := db.First(&hold, "id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if hold.State != "settled" || hold.UsageMicro != 170 {
		t.Fatalf("stream did not settle before DONE: %+v", hold)
	}
	var attempt dbmodel.PointHoldAttempt
	if err := db.First(&attempt, "hold_id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.ProviderResponseID != "stream-response-id" || attempt.ProviderRequestID != "stream-provider-request" || attempt.LocalRequestID != "stream-local-request" || !attempt.UsageAuthoritative {
		t.Fatalf("stream provider evidence missing: %+v", attempt)
	}
}

func TestPointsRelayClientCancellationLeavesPendingHoldWithoutDone(t *testing.T) {
	db := newRelayPointsDB(t, 1_000_000)
	firstChunk := make(chan struct{})
	providerCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "cancel-provider-request")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"id\":\"cancel-response-id\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n"))
		flusher.Flush()
		close(firstChunk)
		<-r.Context().Done()
		close(providerCanceled)
	}))
	defer upstream.Close()
	app := gin.New()
	handlerDone := make(chan struct{})
	app.POST("/v1/chat/completions", func(c *gin.Context) {
		defer close(handlerDone)
		data, _ := io.ReadAll(c.Request.Body)
		var req relaymodel.GeneralOpenAIRequest
		if err := json.Unmarshal(data, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid"})
			return
		}
		c.Set(helper.RequestIdKey, "cancel-local-request")
		meta := &relaymeta.Meta{Mode: relaymode.ChatCompletions, ChannelType: channeltype.OpenAI, APIType: apitype.OpenAI, ChannelId: 1, TokenId: 77, UserId: 41, BaseURL: upstream.URL, RequestURLPath: "/v1/chat/completions", OriginModelName: "public-model", ActualModelName: "mapped-model", IsStream: req.Stream}
		if err := RelayPointsText(c, meta, &req, 0); err != nil && !c.Writer.Written() {
			c.JSON(err.StatusCode, gin.H{"error": err.Error.Message})
		}
	})
	appServer := httptest.NewServer(app)
	defer appServer.Close()
	body := fmt.Sprintf(`{"model":"public-model","messages":[{"role":"user","content":%q}],"max_tokens":100,"stream":true}`, strings.Repeat("x", 100))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, appServer.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cancel-key")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	var firstText string
	for !strings.Contains(firstText, "first") {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = resp.Body.Close()
			t.Fatalf("first stream chunk was not flushed: %q %v", line, readErr)
		}
		firstText += line
	}
	<-firstChunk
	cancel()
	_ = resp.Body.Close()
	select {
	case <-providerCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach the upstream request")
	}
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("relay handler did not finish after client cancellation")
	}
	if strings.Contains(firstText, "[DONE]") {
		t.Fatalf("interrupted stream was marked complete: %q", firstText)
	}
	var hold dbmodel.PointHold
	if err := db.First(&hold, "logical_request_key LIKE ?", "relay:41:77:%").Error; err != nil {
		t.Fatal(err)
	}
	if hold.State != "pending" || hold.UsageMicro != 0 {
		t.Fatalf("interrupted stream did not retain a pending hold: %+v", hold)
	}
	var attempt dbmodel.PointHoldAttempt
	if err := db.First(&attempt, "hold_id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != "unknown" || attempt.ProviderRequestID != "cancel-provider-request" || attempt.ProviderResponseID != "cancel-response-id" || attempt.LocalRequestID != "cancel-local-request" {
		t.Fatalf("cancellation provider evidence missing: %+v", attempt)
	}
}
