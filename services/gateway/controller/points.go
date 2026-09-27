package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/middleware"
	dbmodel "github.com/songquanpeng/one-api/model"
	"gorm.io/gorm"
)

func PointsCSRF(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"csrf_token": middleware.PointsCSRFToken(c.GetInt(ctxkey.Id))})
}

func PaymentsCSRF(c *gin.Context) { PointsCSRF(c) }

func PointsWallet(c *gin.Context) {
	wallet, err := dbmodel.GetPointWallet(c.GetInt(ctxkey.Id))
	if err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, wallet)
}

func PointsPrices(c *gin.Context) {
	prices, err := dbmodel.GetActivePointPrices()
	if err != nil {
		pointsError(c, err)
		return
	}
	out := make([]gin.H, 0, len(prices))
	for _, p := range prices {
		out = append(out, gin.H{"model": p.ModelID, "version": p.Version, "input_micro_per_1k": p.InputMicroPer1K, "cached_input_micro_per_1k": p.CachedInputMicroPer1K, "output_micro_per_1k": p.OutputMicroPer1K, "extra_micro": p.ExtraMicro})
	}
	c.JSON(http.StatusOK, gin.H{"prices": out})
}

func PointsEstimate(c *gin.Context) {
	maxInput := config.PointsMaxInputBytes
	if maxInput < 1024 {
		maxInput = 1024
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(maxInput))
	var req struct {
		Model     string `json:"model"`
		Prompt    string `json:"prompt"`
		MaxOutput int64  `json:"max_output_tokens"`
	}
	maxOutput := config.PointsMaxOutputTokens
	if maxOutput < 1 {
		maxOutput = 8192
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Model == "" || req.MaxOutput <= 0 || req.MaxOutput > int64(maxOutput) || req.Prompt == "" || len(req.Prompt) > maxInput {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model, prompt, and positive max_output_tokens are required"})
		return
	}
	price, err := dbmodel.GetActivePointPrice(req.Model)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "model is not priced"})
		return
	}
	// UTF-8 byte length is used as a conservative, deterministic local reserve estimate.
	// It is not provider usage and is never used for settlement.
	promptTokens := int64(len(req.Prompt))
	amount, err := dbmodel.CalculatePointUsage(promptTokens, 0, req.MaxOutput, *price)
	if err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"model": req.Model, "price_version": price.Version, "estimated_prompt_tokens": promptTokens, "max_output_tokens": req.MaxOutput, "budget_micro": amount, "final_bill": false, "assumptions": "prompt token count is a local estimate; cached input is charged at the regular input rate; output reserves the requested maximum; actual billing requires authoritative provider usage"})
}

func PointsUsage(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := dbmodel.ListPointUsage(c.GetInt(ctxkey.Id), limit)
	if err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"usage": rows})
}

func PointsTokens(c *gin.Context) {
	tokens, err := dbmodel.ListPointTokens(c.GetInt(ctxkey.Id))
	if err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}

func SetPointsTokenBudget(c *gin.Context) {
	tokenID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}
	var req struct {
		Limit string `json:"limit_points"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit_points is required"})
		return
	}
	limit, err := dbmodel.ParseMicroPoints(req.Limit)
	if err != nil || limit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit_points must be a positive decimal points amount"})
		return
	}
	if err = dbmodel.SetPointTokenBudget(c.GetInt(ctxkey.Id), tokenID, limit); err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func SetPointsTokenSettings(c *gin.Context) {
	tokenID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}
	var req struct {
		ExpiredAt int64 `json:"expired_time"`
		Status    int   `json:"status"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token settings"})
		return
	}
	if err = dbmodel.SetPointTokenSettings(c.GetInt(ctxkey.Id), tokenID, req.ExpiredAt, req.Status); err != nil {
		if errors.Is(err, dbmodel.ErrPointsInvalidTokenSettings) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled tokens need a future expiry or no expiry; status must be enabled or disabled"})
			return
		}
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func AdminPublishPointPrice(c *gin.Context) {
	var req struct {
		Model   string `json:"model"`
		Version string `json:"version"`
		Source  string `json:"source"`
		Input   string `json:"input_points_per_1k"`
		Cached  string `json:"cached_input_points_per_1k"`
		Output  string `json:"output_points_per_1k"`
		Extra   string `json:"extra_points"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid price request"})
		return
	}
	input, e1 := dbmodel.ParseMicroPoints(req.Input)
	cached, e2 := dbmodel.ParseMicroPoints(req.Cached)
	output, e3 := dbmodel.ParseMicroPoints(req.Output)
	extra, e4 := dbmodel.ParseMicroPoints(req.Extra)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prices must be non-negative points decimals with at most six places"})
		return
	}
	p := &dbmodel.PointPriceVersion{ModelID: strings.TrimSpace(req.Model), Version: strings.TrimSpace(req.Version), Source: strings.TrimSpace(req.Source), InputMicroPer1K: input, CachedInputMicroPer1K: cached, OutputMicroPer1K: output, ExtraMicro: extra}
	if err := dbmodel.PublishPointPriceVersion(c.GetInt(ctxkey.Id), p); err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "version": p.Version})
}

func AdminGrantPoints(c *gin.Context) {
	var req struct {
		UserID      int    `json:"user_id"`
		Amount      string `json:"amount_points"`
		BusinessKey string `json:"business_key"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid grant request"})
		return
	}
	amount, err := dbmodel.ParseMicroPoints(req.Amount)
	if err != nil || amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount_points must be positive"})
		return
	}
	if err = dbmodel.AdjustPoints(c.GetInt(ctxkey.Id), req.UserID, amount, req.BusinessKey, req.Reason); err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true})
}

func AdminPointPending(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	holds, err := dbmodel.ListPointRecoveryHoldsWithAttempts(limit)
	if err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"holds": holds})
}

func AdminResolvePointHold(c *gin.Context) {
	var req struct {
		DecisionKey string `json:"decision_key"`
		Action      string `json:"action"`
		Usage       string `json:"usage_points"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid hold resolution"})
		return
	}
	usage, err := dbmodel.ParseMicroPoints(req.Usage)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage_points must be a non-negative points amount"})
		return
	}
	if err = dbmodel.ResolvePointHoldBy(c.GetInt(ctxkey.Id), c.Param("key"), req.DecisionKey, req.Action, usage, req.Reason); err != nil {
		pointsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func pointsError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "points operation failed"
	if err == dbmodel.ErrPointsInsufficient || err == dbmodel.ErrPointsConflict {
		status = http.StatusConflict
		message = err.Error()
	} else if err == gorm.ErrRecordNotFound {
		status = http.StatusNotFound
		message = "resource not found"
	} else if err == dbmodel.ErrPointsDisabled {
		status = http.StatusServiceUnavailable
		message = err.Error()
	}
	c.JSON(status, gin.H{"error": message})
}
