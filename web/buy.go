package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/guregu/intertube/tube"
)

const ledgerHistory = 30

func buyForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	renderBuy(ctx, w, u, "", http.StatusOK)
}

func renderBuy(ctx context.Context, w http.ResponseWriter, u tube.User, errMsg string, code int) {
	var (
		ledger     []tube.LedgerEntry
		refunds    []tube.Refund
		refundable tube.Micros
	)
	if u.ID != 0 {
		var err error
		if ledger, err = tube.GetLedger(ctx, u.ID, ledgerHistory); err != nil {
			log.Println("ledger for user", u.ID, err)
		}
		if refunds, err = tube.GetRefunds(ctx, u.ID, ledgerHistory); err != nil {
			log.Println("refunds for user", u.ID, err)
		}
		if refundable, err = tube.Refundable(ctx, u, time.Now().UTC()); err != nil {
			log.Println("refundable for user", u.ID, err)
		}
	}
	data := struct {
		User       tube.User
		Pricing    tube.Pricing
		Presets    []int
		Ledger     []tube.LedgerEntry
		Refunds    []tube.Refund
		Refundable tube.Micros
		ErrorMsg   string
	}{
		User:       u,
		Pricing:    tube.Prices,
		Presets:    topUpPresets,
		Ledger:     ledger,
		Refunds:    refunds,
		Refundable: refundable,
		ErrorMsg:   errMsg,
	}
	renderTemplate(ctx, w, "buy", data, code)
}
