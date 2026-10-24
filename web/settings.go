package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/guregu/intertube/email"
	"github.com/guregu/intertube/tube"
)

var validThemes = map[string]bool{
	"":         true,
	"Light":    true,
	"Dark":     true,
	"Terminal": true,
}

type settingsFormData struct {
	User         tube.User
	Refundable   tube.Micros
	AppPasswords []tube.AppPassword
	NewAppPass   string
	NewAppName   string
	Notice       string
	VerifySent   bool
	ErrorMsg     string
}

func renderSettings(ctx context.Context, w http.ResponseWriter, data settingsFormData, code int) {
	list, err := tube.ListAppPasswords(ctx, data.User.ID)
	if err != nil {
		panic(err)
	}
	data.AppPasswords = list
	if refundable, err := tube.Refundable(ctx, data.User, time.Now().UTC()); err == nil {
		data.Refundable = refundable
	}
	renderTemplate(ctx, w, "settings", data, code)
}

func settingsForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	renderSettings(ctx, w, settingsFormData{User: u}, http.StatusOK)
}

func settings(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	renderError := func(err error) {
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: err.Error()}, http.StatusOK)
	}

	email := r.FormValue("email")
	if email != "" && !strings.EqualFold(u.Email, email) {
		if err := u.SetEmail(ctx, email); err != nil {
			renderError(err)
			return
		}
		if err := sendVerification(ctx, &u); err != nil {
			log.Printf("verification mail for user %d: %v", u.ID, err)
		}
	}

	theme := r.FormValue("theme")
	if !validThemes[theme] {
		renderError(fmt.Errorf("unknown theme: %q", theme))
		return
	}
	if u.Theme != theme {
		if err := u.SetTheme(ctx, theme); err != nil {
			renderError(err)
			return
		}
	}

	disp := tube.DisplayOptions{}
	disp.Stretch = r.FormValue("display-stretch") == "on"
	disp.LyricsLookup = r.FormValue("lyrics-lookup") == "on"
	switch r.FormValue("trackselect") {
	case "ctrl":
		disp.TrackSelect = tube.TrackSelCtrlKey
	default:
		disp.TrackSelect = tube.TrackSelDefault
	}
	if u.Display != disp {
		if err := u.SetDisplayOpt(ctx, disp); err != nil {
			renderError(err)
			return
		}
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func changePasswordForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	data := struct {
		User     tube.User
		ErrorMsg string
		Success  bool
	}{
		User: u,
	}
	renderTemplate(ctx, w, "settings-password", data, http.StatusOK)
}

func changePassword(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	renderError := func(err error) {
		data := struct {
			User     tube.User
			ErrorMsg string
			Success  bool
		}{
			User:     u,
			ErrorMsg: err.Error(),
		}
		renderTemplate(ctx, w, "settings-password", data, http.StatusOK)
	}

	oldpw := r.FormValue("old-password")
	newpw := r.FormValue("new-password")
	confirm := r.FormValue("new-password-confirm")

	if newpw == "" || confirm == "" {
		renderError(fmt.Errorf("missing input"))
		return
	}
	if newpw != confirm {
		renderError(fmt.Errorf("new password and confirmation don't match"))
		return
	}
	if len(newpw) < minPasswordLength {
		renderError(fmt.Errorf("use a password of at least %d characters", minPasswordLength))
		return
	}
	if err := u.ChangePassword(ctx, oldpw, newpw); err != nil {
		if errors.Is(err, tube.ErrWrongPassword) {
			renderError(fmt.Errorf("current password is incorrect"))
			return
		}
		renderError(err)
		return
	}
	if sesh, ok := sessionFrom(ctx); ok {
		if err := tube.DeleteOtherSessions(ctx, u.ID, sesh.Token); err != nil {
			log.Println("deleting other sessions:", err)
		}
	}

	data := struct {
		User     tube.User
		ErrorMsg string
		Success  bool
	}{
		User:    u,
		Success: true,
	}
	renderTemplate(ctx, w, "settings-password", data, http.StatusOK)
}

func deleteOwnAccount(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	renderError := func(msg string) {
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: msg}, http.StatusBadRequest)
	}
	if !u.ValidPassword(r.FormValue("password")) {
		renderError("wrong password")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), u.Email) {
		renderError("type your e-mail address exactly to confirm")
		return
	}
	if err := deleteAccount(ctx, u); err != nil {
		log.Printf("user %d deleting own account: %v", u.ID, err)
		renderError("deletion stopped partway, please try again")
		return
	}
	log.Printf("user %d deleted their account", u.ID)
	for _, cookie := range expiredAuthCookies() {
		http.SetCookie(w, cookie)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func savePGPKey(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	if r.FormValue("remove") != "" {
		if err := u.SetPGPKey(ctx, "", ""); err != nil {
			panic(err)
		}
		http.Redirect(w, r, "/settings#pgp", http.StatusSeeOther)
		return
	}
	armored := strings.TrimSpace(r.FormValue("key"))
	fingerprint, err := email.ParsePublicKey(armored)
	if err != nil {
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: err.Error()}, http.StatusBadRequest)
		return
	}
	if err := u.SetPGPKey(ctx, armored, fingerprint); err != nil {
		panic(err)
	}
	http.Redirect(w, r, "/settings#pgp", http.StatusSeeOther)
}

func sendTestMail(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	if !mailEnabled() {
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: "e-mail isn't set up on this server"}, http.StatusBadRequest)
		return
	}
	body := fmt.Sprintf("This is a test e-mail from %s.\n\nIf you can read this, e-mails to you are working", Domain)
	if u.PGPKey != "" {
		body += " and were encrypted to your key " + u.PGPFingerprint
	}
	if err := sendMail(u, "Test e-mail from "+Domain, body+".\n"); err != nil {
		log.Printf("test mail for user %d: %v", u.ID, err)
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: "couldn't send the e-mail, try again later"}, http.StatusBadGateway)
		return
	}
	renderSettings(ctx, w, settingsFormData{User: u, Notice: "test e-mail sent"}, http.StatusOK)
}
