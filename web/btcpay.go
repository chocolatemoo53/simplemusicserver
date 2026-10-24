package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/tube"
)

type BTCPayConfig struct {
	URL           string
	StoreID       string
	APIKey        string
	WebhookSecret string
}

var (
	btcpay       BTCPayConfig
	btcpayClient = &http.Client{Timeout: 30 * time.Second}
)

const (
	minTopUp       = 5 * tube.MicrosPerDollar
	maxTopUp       = 1000 * tube.MicrosPerDollar
	maxWebhookBody = 1 << 20
)

var topUpPresets = []int{5, 10, 25, 50}

func SetBTCPay(cfg BTCPayConfig) error {
	if cfg.URL == "" {
		return nil
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" && !DebugMode {
		return fmt.Errorf("btcpay.url must be an https URL: %q", cfg.URL)
	}
	if cfg.StoreID == "" || cfg.APIKey == "" || cfg.WebhookSecret == "" {
		return fmt.Errorf("btcpay needs url, store_id, api_key and webhook_secret")
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	btcpay = cfg
	return nil
}

func paymentEnabled() bool {
	return btcpay.URL != ""
}

var ManualPayment string

func canPay() bool {
	return paymentEnabled() || ManualPayment != ""
}

var amountPattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,2})?$`)

func parseAmount(s string) (tube.Micros, error) {
	amount, err := parseUSD(s)
	if err != nil {
		return 0, err
	}
	if amount < minTopUp || amount > maxTopUp {
		return 0, fmt.Errorf("amount must be between %v and %v", minTopUp, maxTopUp)
	}
	return amount, nil
}

func parseUSD(s string) (tube.Micros, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	if !amountPattern.MatchString(s) {
		return 0, fmt.Errorf("invalid amount")
	}
	dollars, cents, _ := strings.Cut(s, ".")
	d, err := strconv.ParseInt(dollars, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount")
	}
	cents = (cents + "00")[:2]
	c, err := strconv.ParseInt(cents, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount")
	}
	amount := tube.Micros(d)*tube.MicrosPerDollar + tube.Micros(c)*10_000
	if amount <= 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	return amount, nil
}

func formatDecimal(m tube.Micros) string {
	return strings.TrimPrefix(m.String(), "$")
}

type btcpayInvoice struct {
	ID           string `json:"id"`
	StoreID      string `json:"storeId"`
	Status       string `json:"status"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	CheckoutLink string `json:"checkoutLink"`
	Metadata     struct {
		UserID int `json:"userId"`
	} `json:"metadata"`
}

func btcpayRequest(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, btcpay.URL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+btcpay.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := btcpayClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("btcpay %s %s: %s: %s", method, path, resp.Status, msg)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxWebhookBody)).Decode(out)
}

func createInvoice(ctx context.Context, u tube.User, amount tube.Micros) (btcpayInvoice, error) {
	body := map[string]any{
		"amount":   formatDecimal(amount),
		"currency": "USD",
		"metadata": map[string]any{
			"userId": u.ID,
		},
		"checkout": map[string]any{
			"redirectURL":           fmt.Sprintf("https://%s/buy/success", Domain),
			"redirectAutomatically": true,
		},
	}
	var inv btcpayInvoice
	err := btcpayRequest(ctx, http.MethodPost, "/api/v1/stores/"+url.PathEscape(btcpay.StoreID)+"/invoices", body, &inv)
	if err != nil {
		return inv, err
	}
	if !strings.HasPrefix(inv.CheckoutLink, btcpay.URL+"/") {
		return inv, fmt.Errorf("unexpected btcpay checkout link: %q", inv.CheckoutLink)
	}
	return inv, nil
}

func getInvoice(ctx context.Context, id string) (btcpayInvoice, error) {
	var inv btcpayInvoice
	path := "/api/v1/stores/" + url.PathEscape(btcpay.StoreID) + "/invoices/" + url.PathEscape(id)
	err := btcpayRequest(ctx, http.MethodGet, path, nil, &inv)
	return inv, err
}

func validSignature(body []byte, header, secret string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func topUp(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !paymentEnabled() {
		http.Error(w, "payment is disabled", http.StatusForbidden)
		return
	}
	u, _ := userFrom(ctx)
	amount, err := parseAmount(r.FormValue("amount"))
	if err != nil {
		renderBuy(ctx, w, u, err.Error(), http.StatusBadRequest)
		return
	}
	inv, err := createInvoice(ctx, u, amount)
	if err != nil {
		log.Println("btcpay create invoice:", err)
		renderBuy(ctx, w, u, "the payment server is unavailable, please try again later", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, inv.CheckoutLink, http.StatusSeeOther)
}

func topUpDone(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	data := struct {
		User tube.User
	}{
		User: u,
	}
	renderTemplate(ctx, w, "checkout", data, http.StatusOK)
}

func btcpayWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !paymentEnabled() {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validSignature(body, r.Header.Get("BTCPay-Sig"), btcpay.WebhookSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event struct {
		Type      string `json:"type"`
		StoreID   string `json:"storeId"`
		InvoiceID string `json:"invoiceId"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if event.Type != "InvoiceSettled" || event.StoreID != btcpay.StoreID {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := settleInvoice(ctx, event.InvoiceID); err != nil {
		log.Println("btcpay settle", event.InvoiceID, err)
		http.Error(w, "could not settle invoice", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func settleInvoice(ctx context.Context, invoiceID string) error {
	inv, err := getInvoice(ctx, invoiceID)
	if err != nil {
		return err
	}
	if inv.ID != invoiceID || inv.StoreID != btcpay.StoreID || inv.Status != "Settled" || inv.Currency != "USD" {
		return fmt.Errorf("invoice not settled in USD for this store: %+v", inv)
	}
	amount, err := parseInvoiceAmount(inv.Amount)
	if err != nil {
		return err
	}
	applied, err := tube.Credit(ctx, inv.Metadata.UserID, amount, "btcpay:"+inv.ID)
	if err != nil {
		return err
	}
	if applied {
		log.Printf("credited user %d with %v (invoice %s)", inv.Metadata.UserID, amount, inv.ID)
	}
	return nil
}

func parseInvoiceAmount(s string) (tube.Micros, error) {
	dollars, frac, _ := strings.Cut(s, ".")
	d, err := strconv.ParseInt(dollars, 10, 64)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid invoice amount %q", s)
	}
	frac = (frac + "000000")[:6]
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid invoice amount %q", s)
	}
	return tube.Micros(d)*tube.MicrosPerDollar + tube.Micros(f), nil
}

type btcpayPullPayment struct {
	ID       string `json:"id"`
	ViewLink string `json:"viewLink"`
}

func createRefund(ctx context.Context, r tube.Refund) (btcpayPullPayment, error) {
	body := map[string]any{
		"name":           fmt.Sprintf("Refund %d (user %d)", r.ID, r.UserID),
		"description":    fmt.Sprintf("Unused balance refund from %s", Domain),
		"refundVariant":  "Custom",
		"customAmount":   formatDecimal(r.Amount),
		"customCurrency": "USD",
	}
	if fee := tube.Prices.RefundFeePct; fee > 0 {
		body["subtractPercentage"] = strconv.FormatFloat(fee, 'f', -1, 64)
	}
	var pp btcpayPullPayment
	path := "/api/v1/stores/" + url.PathEscape(btcpay.StoreID) + "/invoices/" + url.PathEscape(r.InvoiceID) + "/refund"
	if err := btcpayRequest(ctx, http.MethodPost, path, body, &pp); err != nil {
		return pp, err
	}
	if pp.ID == "" {
		return pp, fmt.Errorf("btcpay returned no pull payment id")
	}
	if !strings.HasPrefix(pp.ViewLink, btcpay.URL+"/") {
		log.Printf("btcpay refund %d: unexpected claim link %q", r.ID, pp.ViewLink)
		pp.ViewLink = ""
	}
	return pp, nil
}

func requestRefund(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !paymentEnabled() {
		http.Error(w, "payment is disabled", http.StatusForbidden)
		return
	}
	u, _ := userFrom(ctx)
	if !u.ValidPassword(r.FormValue("password")) {
		renderBuy(ctx, w, u, "wrong password", http.StatusBadRequest)
		return
	}
	amount, err := parseUSD(r.FormValue("amount"))
	if err != nil {
		renderBuy(ctx, w, u, err.Error(), http.StatusBadRequest)
		return
	}

	parts, err := tube.RequestRefund(ctx, u.ID, amount, time.Now().UTC())
	switch {
	case errors.Is(err, tube.ErrRefundTooLarge), errors.Is(err, tube.ErrRefundTooSoon):
		renderBuy(ctx, w, u, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		panic(err)
	}

	failed := false
	for _, part := range parts {
		pp, err := createRefund(ctx, part)
		if err != nil {
			log.Printf("btcpay refund %d for invoice %s: %v", part.ID, part.InvoiceID, err)
			if err := tube.FailRefund(ctx, part.ID); err != nil {
				log.Printf("refund %d: could not return %v to user %d: %v", part.ID, part.Amount, u.ID, err)
			}
			failed = true
			continue
		}
		if err := tube.CompleteRefund(ctx, part.ID, pp.ID, pp.ViewLink); err != nil {
			log.Printf("refund %d: created pull payment %s but could not record it: %v", part.ID, pp.ID, err)
		}
	}

	u, err = tube.GetUser(ctx, u.ID)
	if err != nil {
		panic(err)
	}
	if failed {
		renderBuy(ctx, w, u, "part of the refund couldn't be created and was returned to your balance", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/buy/#refunds", http.StatusSeeOther)
}
