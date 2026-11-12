package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/model"
)

func TestAdminPaymentOrderRoutesFiltersPaginationAndRedaction(t *testing.T) {
	env := paymentHTTPFixture(t, false) // Historical order management must work while billing is disabled.
	adminCookie, customerCookie := env.Login(42), env.Login(41)
	baseTime := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 105; i++ {
		channel, state := "wechat", "pending"
		if i%2 == 0 {
			channel = "alipay"
		}
		if i%3 == 0 {
			state = "credited"
		}
		order := model.PointPurchaseOrder{
			OrderKey: fmt.Sprintf("admin-filter-order-%03d", i), UserID: 41, Channel: channel,
			PackageID: "starter", PackageVersion: "v1", PackageSnapshot: `{"package_id":"starter","version":"v1","name":"充值套餐","amount_fen":500,"currency":"CNY","purchase_micro":500000000}`,
			Currency: "CNY", AmountFen: 500, PurchaseMicro: 500_000_000,
			ProviderMerchantID: "merchant-secret-do-not-return", ProviderAppID: "app-secret-do-not-return",
			CheckoutSnapshot: `{"code_url":"checkout-secret-do-not-return"}`, State: state,
			CreatedAt: baseTime.Add(time.Duration(i) * time.Second),
		}
		if err := env.DB.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	query := fmt.Sprintf("/api/admin/orders?user_id=41&channel=alipay&state=credited&from=%s&to=%s&limit=10", baseTime.Format(time.RFC3339), baseTime.Add(200*time.Second).Format(time.RFC3339))
	first := env.Request(http.MethodGet, query, adminCookie, "", 0, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("admin order list status=%d body=%s", first.Code, first.Body.String())
	}
	var page struct {
		Orders     []map[string]any `json:"orders"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 10 || page.NextCursor == "" {
		t.Fatalf("expected a full filtered page with cursor, got len=%d cursor=%q", len(page.Orders), page.NextCursor)
	}
	if strings.Contains(first.Body.String(), "merchant-secret") || strings.Contains(first.Body.String(), "app-secret") || strings.Contains(first.Body.String(), "checkout-secret") {
		t.Fatalf("order list leaked payment material: %s", first.Body.String())
	}
	secondURL := query + "&before_id=" + page.NextCursor
	second := env.Request(http.MethodGet, secondURL, adminCookie, "", 0, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second order page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPage struct {
		Orders []map[string]any `json:"orders"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Orders) != 8 {
		t.Fatalf("filtered keyset page should contain remaining 8 rows, got %d", len(secondPage.Orders))
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders?channel=card", adminCookie, "", 0, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("unknown channel filter status=%d", got)
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders?from=2026-09-27T10:00:00%2B08:00", adminCookie, "", 0, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("non-UTC time filter status=%d", got)
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders?from=2026-09-27T12:00:00Z&to=2026-09-27T11:00:00Z", adminCookie, "", 0, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("reversed time filter status=%d", got)
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders", customerCookie, "", 0, nil).Code; got != http.StatusForbidden {
		t.Fatalf("customer admin order list status=%d", got)
	}
	if err := env.DB.Model(&model.User{}).Where("id = ?", 42).Update("role", model.RoleCommonUser).Error; err != nil {
		t.Fatal(err)
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders", adminCookie, "", 0, nil).Code; got != http.StatusForbidden {
		t.Fatalf("revoked admin order list status=%d", got)
	}
}

func TestAdminPaymentOrderDetailHasExactCreditAndSafeEventSummary(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	adminCookie := env.Login(42)
	expires := time.Now().UTC().Add(time.Hour).Unix()
	order := model.PointPurchaseOrder{
		OrderKey: "admin-detail-order-0001", UserID: 41, Channel: "wechat", PackageID: "starter", PackageVersion: "v1",
		PackageSnapshot: `{"package_id":"starter","version":"v1","name":"标准套餐","amount_fen":500,"currency":"CNY","purchase_micro":500000000}`,
		Currency:        "CNY", AmountFen: 500, PurchaseMicro: 500_000_000, BonusMicro: 12_000_000,
		ProviderMerchantID: "merchant-private", ProviderAppID: "app-private", ProviderTransactionID: "wx-transaction-abcdef123456",
		CheckoutSnapshot: `{"code_url":"private-checkout-material"}`, ExpiresAt: &expires, State: "credited",
	}
	if err := env.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&[]model.PointLedger{
		{UserID: 41, BusinessKey: "refund:" + order.OrderKey, Kind: "refund", AvailableDelta: -12, AvailableAfter: 0},
		{UserID: 41, BusinessKey: "credit:" + order.OrderKey, Kind: "purchase_credit", AvailableDelta: order.PurchaseMicro + order.BonusMicro, AvailableAfter: order.PurchaseMicro + order.BonusMicro},
	}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 23; i++ {
		payload := fmt.Sprintf(`{"provider":"wechat","provider_event_id":"notify-secret-%d","transaction_id":"transaction-secret-%d","amount_fen":501,"currency":"CNY","status":"SUCCESS","provider_occurred_at":"2026-09-27T01:02:03Z","signature":"raw-signature-secret"}`, i, i)
		if err := env.DB.Create(&model.PaymentEvent{Provider: "wechat", ProviderEventID: fmt.Sprintf("notify-secret-%d", i), OrderKey: order.OrderKey, ProviderTransactionID: fmt.Sprintf("transaction-secret-%d", i), Digest: "digest-secret", Payload: payload, Verification: "verified", State: "quarantined", ErrorCode: "amount_mismatch"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := env.Request(http.MethodGet, "/api/admin/orders/"+order.OrderKey+"?event_limit=20", adminCookie, "", 0, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("order detail status=%d body=%s", first.Code, first.Body.String())
	}
	var detail struct {
		Order      map[string]any   `json:"order"`
		Credit     map[string]any   `json:"credit_ledger"`
		Events     []map[string]any `json:"events"`
		NextCursor string           `json:"events_next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Credit["kind"] != "purchase_credit" || detail.Credit["available_delta"] != float64(order.PurchaseMicro+order.BonusMicro) {
		t.Fatalf("wrong credit ledger selected: %#v", detail.Credit)
	}
	if len(detail.Events) != 20 || detail.NextCursor == "" {
		t.Fatalf("expected paged event summaries, got %d cursor=%q", len(detail.Events), detail.NextCursor)
	}
	firstEvent := detail.Events[0]
	if firstEvent["status"] != "SUCCESS" || firstEvent["amount_fen"] != float64(501) || firstEvent["currency"] != "CNY" || firstEvent["provider_occurred_at"] != "2026-09-27T01:02:03Z" {
		t.Fatalf("event summary omitted normalized payment facts: %#v", firstEvent)
	}
	for _, forbidden := range []string{"merchant-private", "app-private", "private-checkout-material", "digest-secret", "raw-signature-secret", "notify-secret-", "transaction-secret-"} {
		if strings.Contains(first.Body.String(), forbidden) {
			t.Fatalf("order detail leaked %q: %s", forbidden, first.Body.String())
		}
	}
	second := env.Request(http.MethodGet, "/api/admin/orders/"+order.OrderKey+"?event_limit=20&event_before_id="+detail.NextCursor, adminCookie, "", 0, nil)
	var secondDetail struct {
		Events []map[string]any `json:"events"`
		Next   string           `json:"events_next_cursor"`
	}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondDetail) != nil || len(secondDetail.Events) != 3 || secondDetail.Next != "" {
		t.Fatalf("event cursor page status=%d body=%s", second.Code, second.Body.String())
	}
	if got := env.Request(http.MethodGet, "/api/admin/orders/other-user-order", nil, "", 0, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated order detail status=%d", got)
	}
}

func TestAdminAuditRoutesFilterAssociationsAndPaginate(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	adminCookie, customerCookie := env.Login(42), env.Login(41)
	baseTime := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 105; i++ {
		details := fmt.Sprintf(`{"package_id":"pkg-%03d","version":"v1","activate":true}`, i)
		if err := env.DB.Create(&model.PointAdminAudit{ActorUserID: 42, Action: "package_publish", BusinessKey: fmt.Sprintf("package-%03d", i), Details: details, CreatedAt: baseTime.Add(time.Duration(i) * time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := env.DB.Create(&model.PointAdminAudit{ActorUserID: 0, Action: "offline_hold_recovery", BusinessKey: "offline-recovery-hold:batch:1", TargetUserID: 41, Details: `{"batch_key":"batch","reason":"operator note","logical_request_key":"request-link-001"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&model.PointHold{ID: 808, UserID: 41, TokenID: 77, LogicalRequestKey: "hold-link-002", BudgetMicro: 100, State: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&model.PointHoldDecision{HoldID: 808, DecisionKey: "decision-link-002", Action: "release", Reason: "fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&model.PointAdminAudit{ActorUserID: 42, Action: "hold_resolution", BusinessKey: "hold-decision:decision-link-002", TargetUserID: 41, Details: `{"decision_key":"decision-link-002","action":"release","usage_micro":0,"reason":"fixture"}`}).Error; err != nil {
		t.Fatal(err)
	}
	filtered := env.Request(http.MethodGet, "/api/admin/audit?request_key=request-link-001", adminCookie, "", 0, nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("request-linked audit status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	var linked struct {
		Audits []map[string]any `json:"audits"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &linked); err != nil || len(linked.Audits) != 1 || linked.Audits[0]["related_request_key"] != "request-link-001" {
		t.Fatalf("offline audit request association failed: %s err=%v", filtered.Body.String(), err)
	}
	if linked.Audits[0]["reason"] != "operator note" || linked.Audits[0]["business_reference"] != "offline-recovery-hold:batch:1" || linked.Audits[0]["actor_label"] != "系统" {
		t.Fatalf("offline audit safe projection omitted reason/reference/actor: %#v", linked.Audits[0])
	}
	holdLinked := env.Request(http.MethodGet, "/api/admin/audit?request_key=hold-link-002", adminCookie, "", 0, nil)
	if holdLinked.Code != http.StatusOK || !strings.Contains(holdLinked.Body.String(), "hold_resolution") {
		t.Fatalf("hold resolution request association failed: %d %s", holdLinked.Code, holdLinked.Body.String())
	}
	first := env.Request(http.MethodGet, fmt.Sprintf("/api/admin/audit?action=package_publish&actor_id=42&limit=10&from=%s&to=%s", baseTime.Add(-time.Hour).Format(time.RFC3339), baseTime.Add(2*time.Hour).Format(time.RFC3339)), adminCookie, "", 0, nil)
	var page struct {
		Audits []map[string]any `json:"audits"`
		Next   string           `json:"next_cursor"`
	}
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Audits) != 10 || page.Next == "" {
		t.Fatalf("audit keyset first page failed: %d %s", first.Code, first.Body.String())
	}
	second := env.Request(http.MethodGet, "/api/admin/audit?action=package_publish&actor_id=42&limit=10&before_id="+page.Next, adminCookie, "", 0, nil)
	var page2 struct {
		Audits []map[string]any `json:"audits"`
	}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &page2) != nil || len(page2.Audits) != 10 {
		t.Fatalf("audit keyset second page failed: %d %s", second.Code, second.Body.String())
	}
	if strings.Contains(first.Body.String(), "business_key") || strings.Contains(first.Body.String(), "reason") || strings.Contains(first.Body.String(), "details") {
		t.Fatalf("audit endpoint leaked raw evidence: %s", first.Body.String())
	}
	if got := env.Request(http.MethodGet, "/api/admin/audit?action=unknown", adminCookie, "", 0, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("unknown audit filter status=%d", got)
	}
	if got := env.Request(http.MethodGet, "/api/admin/audit?order_key=nonexistent-order", adminCookie, "", 0, nil).Body.String(); !strings.Contains(got, `"audits":[]`) {
		t.Fatalf("unlinked audit order filter must be empty: %s", got)
	}
	if err := env.DB.Create(&model.PointAdminAudit{ActorUserID: 0, Action: "payment_event_quarantined", BusinessKey: "system-quarantine:event-1", Details: `{"order_key":"linked-order-1","reason":"amount_mismatch","transaction_id":"never-return-this"}`}).Error; err != nil {
		t.Fatal(err)
	}
	orderAudit := env.Request(http.MethodGet, "/api/admin/audit?order_key=linked-order-1", adminCookie, "", 0, nil)
	if orderAudit.Code != http.StatusOK || !strings.Contains(orderAudit.Body.String(), `"actor_label":"系统"`) || strings.Contains(orderAudit.Body.String(), "never-return-this") {
		t.Fatalf("system payment audit association/redaction failed: %d %s", orderAudit.Code, orderAudit.Body.String())
	}
	if got := env.Request(http.MethodGet, "/api/admin/audit", customerCookie, "", 0, nil).Code; got != http.StatusForbidden {
		t.Fatalf("customer audit access status=%d", got)
	}
}

func TestAdminAuditSparseAssociationScanReturnsContinuation(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	adminCookie := env.Login(42)
	target := model.PointAdminAudit{ActorUserID: 0, Action: "offline_hold_recovery", BusinessKey: "offline-recovery-hold:sparse:1", TargetUserID: 41, Details: `{"batch_key":"sparse","reason":"fixture","logical_request_key":"deep-match"}`}
	if err := env.DB.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]model.PointAdminAudit, 10000)
	for i := range rows {
		rows[i] = model.PointAdminAudit{ActorUserID: 42, Action: "package_publish", BusinessKey: fmt.Sprintf("sparse-package-%05d", i), Details: `{"package_id":"fixture","version":"v1","activate":false}`}
	}
	if err := env.DB.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	first := env.Request(http.MethodGet, "/api/admin/audit?request_key=deep-match&limit=10", adminCookie, "", 0, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("sparse first scan status=%d body=%s", first.Code, first.Body.String())
	}
	var page struct {
		Audits []map[string]any `json:"audits"`
		Next   string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Audits) != 0 || page.Next == "" {
		t.Fatalf("sparse audit scan must expose continuation: %s err=%v", first.Body.String(), err)
	}
	second := env.Request(http.MethodGet, "/api/admin/audit?request_key=deep-match&limit=10&before_id="+page.Next, adminCookie, "", 0, nil)
	var next struct {
		Audits []map[string]any `json:"audits"`
		Cursor string           `json:"next_cursor"`
	}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &next) != nil || len(next.Audits) != 1 || next.Audits[0]["related_request_key"] != "deep-match" || next.Cursor != "" {
		t.Fatalf("sparse audit continuation missed association: %d %s", second.Code, second.Body.String())
	}
}

func TestAdminAuditAssociationDatabaseFailureReturnsServerError(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	adminCookie := env.Login(42)
	if err := env.DB.Create(&model.PointHold{ID: 808, UserID: 41, TokenID: 77, LogicalRequestKey: "db-error-link", BudgetMicro: 10, State: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&model.PointHoldDecision{HoldID: 808, DecisionKey: "db-error-decision", Action: "release", Reason: "fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&model.PointAdminAudit{ActorUserID: 42, Action: "hold_resolution", BusinessKey: "hold-decision:db-error-decision", TargetUserID: 41, Details: `{"decision_key":"db-error-decision","action":"release","usage_micro":0,"reason":"fixture"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Migrator().DropTable(&model.PointHoldDecision{}); err != nil {
		t.Fatal(err)
	}
	res := env.Request(http.MethodGet, "/api/admin/audit?request_key=db-error-link", adminCookie, "", 0, nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("association storage failure must not look like empty result: %d %s", res.Code, res.Body.String())
	}
}
