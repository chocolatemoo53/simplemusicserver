package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/guregu/intertube/tube"
)

const errVerifyEmail = "Confirm your e-mail address to upload music. Check your inbox, or resend the link from settings."

func init() {
	handle("GET /verify", verifyEmail)
	handle("POST /settings/verify", resendVerification)
}

func sendVerification(ctx context.Context, u *tube.User) error {
	if !mailEnabled() || u.Verified() {
		return nil
	}
	token, err := u.StartVerification(ctx)
	if err != nil {
		return err
	}
	link := fmt.Sprintf("https://%s/verify?id=%d&token=%s", Domain, u.ID, token)
	body := fmt.Sprintf("Confirm the e-mail address for your account at %s by opening this link within 7 days:\n%s\n\n"+
		"If you didn't sign up, you can ignore this e-mail.\n", Domain, link)
	return sendMail(*u, "Confirm your e-mail for "+Domain, body)
}

func verifyEmail(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	err := tube.VerifyEmail(ctx, id, r.URL.Query().Get("token"), time.Now())
	if errors.Is(err, tube.ErrBadVerifyToken) {
		renderTemplate(ctx, w, "verified", struct{ OK bool }{false}, http.StatusNotFound)
		return
	}
	if err != nil {
		panic(err)
	}
	renderTemplate(ctx, w, "verified", struct{ OK bool }{true}, http.StatusOK)
}

func resendVerification(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	if u.Verified() {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if ok, wait := verifyPerUser.allow(strconv.Itoa(u.ID), time.Now()); !ok {
		tooManyRequests(w, wait)
		return
	}
	if err := sendVerification(ctx, &u); err != nil {
		log.Printf("verification mail for user %d: %v", u.ID, err)
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: "couldn't send the e-mail, try again later"}, http.StatusBadGateway)
		return
	}
	renderSettings(ctx, w, settingsFormData{User: u, VerifySent: true}, http.StatusOK)
}
