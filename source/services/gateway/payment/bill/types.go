package bill

import "time"

type RowKind string

const (
	RowPayment  RowKind = "payment"
	RowRefund   RowKind = "refund"
	RowReversal RowKind = "reversal"
)

// Row is a normalized, immutable representation of one provider bill row.
// Money values are integer fen. The parser preserves provider gross, settlement,
// fee, and net figures separately; callers must not treat one as another.
type Row struct {
	Provider           string
	Kind               RowKind
	Status             string
	RefundStatus       string
	OccurredAt         time.Time
	MerchantID         string
	AppID              string
	OrderKey           string
	TransactionID      string
	MerchantRefundKey  string
	ProviderRefundID   string
	Currency           string
	GrossFen           int64
	SettlementFen      int64
	DiscountFen        int64
	RefundFen          int64
	CouponRefundFen    int64
	RefundRequestedFen int64
	FeeFen             int64
	NetFen             int64
	HasNetFen          bool
	SourceLine         int
	SourceDigest       string
}

type Statement struct {
	Provider             string
	BillDate             string
	Timezone             string
	FormatVersion        string
	SourceSHA256         string
	RequestedMerchantID  string
	RequestedAppID       string
	ProviderHashType     string
	ProviderHashValue    string
	ProviderHashVerified bool
	SourceBytes          []byte
	Rows                 []Row
}

type RawBill struct {
	Provider            string
	BillDate            string
	Timezone            string
	FormatVersion       string
	RequestedMerchantID string
	RequestedAppID      string
	Bytes               []byte
	SHA256              string
	ProviderHashType    string
	ProviderHashValue   string
	ProviderHashOK      bool
}
