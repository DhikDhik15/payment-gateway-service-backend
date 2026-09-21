package service

import (
	"bytes"
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MidtransProvider implements the legacy Snap API. It deliberately has no
// retries: a timed-out create may already have been accepted remotely.
type MidtransProvider struct {
	baseURL, serverKey string
	client             *http.Client
}

func NewMidtransProvider(baseURL, serverKey string, timeout time.Duration, client *http.Client) (*MidtransProvider, error) {
	if serverKey == "" {
		return nil, errors.New("midtrans server key is required")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("invalid Midtrans base URL: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &MidtransProvider{baseURL: strings.TrimRight(baseURL, "/"), serverKey: serverKey, client: client}, nil
}
func (p *MidtransProvider) Name() string { return "MIDTRANS" }

type midtransCreateRequest struct {
	TransactionDetails struct {
		OrderID     string `json:"order_id"`
		GrossAmount int64  `json:"gross_amount"`
	} `json:"transaction_details"`
}
type midtransResponse struct {
	Token             string `json:"token"`
	RedirectURL       string `json:"redirect_url"`
	TransactionID     string `json:"transaction_id"`
	TransactionStatus string `json:"transaction_status"`
	StatusCode        string `json:"status_code"`
	StatusMessage     string `json:"status_message"`
	GrossAmount       string `json:"gross_amount"`
	Currency          string `json:"currency"`
	OrderID           string `json:"order_id"`
	SignatureKey      string `json:"signature_key"`
}

func (p *MidtransProvider) CreatePayment(ctx context.Context, req ProviderCreateRequest) (*ProviderPaymentResponse, error) {
	var body midtransCreateRequest
	body.TransactionDetails.OrderID = req.TransactionID
	body.TransactionDetails.GrossAmount = req.Amount
	var out midtransResponse
	if err := p.do(ctx, http.MethodPost, "/snap/v1/transactions", body, &out); err != nil {
		return nil, err
	}
	if out.Token == "" || out.RedirectURL == "" {
		return nil, ErrProviderFailure
	}
	return &ProviderPaymentResponse{Provider: p.Name(), ProviderTransactionID: req.TransactionID, Status: "PENDING", PaymentURL: out.RedirectURL}, nil
}
func (p *MidtransProvider) GetPayment(ctx context.Context, id string) (*ProviderPaymentResponse, error) {
	var out midtransResponse
	if err := p.do(ctx, http.MethodGet, "/v2/"+url.PathEscape(id)+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &ProviderPaymentResponse{Provider: p.Name(), ProviderTransactionID: out.TransactionID, Status: out.TransactionStatus}, nil
}
func (p *MidtransProvider) CancelPayment(ctx context.Context, id string) error {
	var out midtransResponse
	return p.do(ctx, http.MethodPost, "/v2/"+url.PathEscape(id)+"/cancel", nil, &out)
}
func (p *MidtransProvider) do(ctx context.Context, method, path string, in, out any) error {
	var r io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return ErrProviderFailure
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, r)
	if err != nil {
		return ErrProviderFailure
	}
	req.SetBasicAuth(p.serverKey, "")
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return ErrProviderTimeout
		}
		return ErrProviderFailure
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ErrProviderFailure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrProviderFailure
	}
	if out != nil && json.Unmarshal(body, out) != nil {
		return ErrProviderFailure
	}
	return nil
}
func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var t timeout
	return errors.As(err, &t) && t.Timeout()
}

// MidtransWebhookParser validates the documented SHA-512 signature embedded in
// notification JSON: order_id + status_code + gross_amount + server_key.
type MidtransWebhookParser struct{ serverKey string }

func NewMidtransWebhookParser(serverKey string) *MidtransWebhookParser {
	return &MidtransWebhookParser{serverKey: serverKey}
}
func (p *MidtransWebhookParser) ProviderName() string { return "MIDTRANS" }
func (p *MidtransWebhookParser) VerifySignature(payload []byte, _ string) error {
	var v midtransResponse
	if json.Unmarshal(payload, &v) != nil {
		return ErrWebhookMalformedPayload
	}
	sum := sha512.Sum512([]byte(v.OrderID + v.StatusCode + v.GrossAmount + p.serverKey))
	expected := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(v.SignatureKey)) != 1 {
		return ErrWebhookInvalidSignature
	}
	return nil
}
func (p *MidtransWebhookParser) ParseEvent(payload []byte) (*ParsedWebhookEvent, error) {
	var v midtransResponse
	if json.Unmarshal(payload, &v) != nil {
		return nil, ErrWebhookMalformedPayload
	}
	if v.OrderID == "" || v.TransactionStatus == "" || v.StatusCode == "" {
		return nil, ErrWebhookMissingFields
	}
	amount := int64(0)
	fmt.Sscan(v.GrossAmount, &amount)
	status := strings.ToLower(v.TransactionStatus)
	typ := "UNKNOWN"
	switch status {
	case "settlement", "capture":
		typ = "PAYMENT_PAID"
	case "deny", "cancel", "failure":
		typ = "PAYMENT_FAILED"
	case "expire":
		typ = "PAYMENT_EXPIRED"
	}
	return &ParsedWebhookEvent{EventID: v.TransactionID + ":" + status + ":" + v.StatusCode, EventType: typ, ProviderTransactionID: v.OrderID, MerchantOrderID: v.OrderID, Status: status, Amount: amount, Currency: v.Currency}, nil
}
