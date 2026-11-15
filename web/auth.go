package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

const sessionCookie = "sesh"

func allowGuest(path ...string) func(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
		for _, p := range path {
			if r.URL.Path == p {
				ctx = withBypass(ctx, true)
				return ctx
			}
		}
		return ctx
	}
}

func allowGuestPrefix(prefix string) func(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return withBypass(ctx, true)
		}
		return ctx
	}
}

func appPasswordReadable(path string) bool {
	return strings.TrimSuffix(path, "/") == "/api/v0/tracks" || strings.HasPrefix(path, "/dl/tracks/")
}

func requireLogin(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	maybeRedir := func() context.Context {
		if bypassFrom(ctx) {
			return ctx
		}
		redirectLogin(ctx, w, r)
		return nil
	}

	if isSubsonicReq(r) {
		return subsonicAuth(ctx, w, r)
	}

	if email, secret, ok := r.BasicAuth(); ok && appPasswordReadable(r.URL.Path) {
		user, failed := authenticatePassword(ctx, email, secret)
		if failed != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="music library", charset="UTF-8"`)
			http.Error(w, "wrong e-mail or app password", http.StatusUnauthorized)
			return nil
		}
		return withUser(ctx, user)
	}

	cookieAuth, err := r.Cookie(sessionCookie)
	if err != nil {
		return maybeRedir()
	}

	sesh, keys, err := tube.GetSession(ctx, cookieAuth.Value)
	if err != nil {
		for _, cookie := range expiredAuthCookies() {
			http.SetCookie(w, cookie)
		}
		return maybeRedir()
	}

	user, err := tube.GetUser(ctx, sesh.UserID)
	if err != nil {
		for _, cookie := range expiredAuthCookies() {
			http.SetCookie(w, cookie)
		}
		return maybeRedir()
	}
	user.WithKeys(keys)

	ctx = withUser(ctx, user)
	ctx = withSession(ctx, sesh)
	return ctx
}

func redirectLogin(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var q string
	if param := encodeRedirect(r.URL); param != "" {
		q = "?r=" + encodeRedirect(r.URL)
	}
	http.Redirect(w, r, "/login"+q, http.StatusTemporaryRedirect)
}

func requireAdmin(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	u, _ := userFrom(ctx)
	if !slices.Contains(AdminIDs, u.ID) {
		http.NotFound(w, r)
		return nil
	}
	return ctx
}

type loginFormData struct {
	Jump        string
	Email       string
	MailEnabled bool
	ErrorMsg    string
}

func loginForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	_, loggedIn := userFrom(ctx)
	if loggedIn {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	var data = loginFormData{
		MailEnabled: mailEnabled(),
	}

	if rawJump := r.URL.Query().Get("r"); rawJump != "" {
		data.Jump, _ = decodeRedirect(rawJump)
	}

	renderTemplate(ctx, w, "login", data, http.StatusOK)
}

func login(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	emailaddr := r.FormValue("email")
	pass := r.FormValue("password")
	jump := r.FormValue("jump")

	if jump != "" && (jump[0] != '/' || strings.HasPrefix(jump, `//`)) {
		jump = "/"
	}
	if jump == "" {
		jump = "/"
	}

	renderError := func(msg string) {
		data := loginFormData{
			Email:       emailaddr,
			Jump:        jump,
			MailEnabled: mailEnabled(),
			ErrorMsg:    msg,
		}
		renderTemplate(ctx, w, "login", data, http.StatusOK)
	}

	user, err := tube.GetUserByEmail(ctx, emailaddr)
	switch {
	case errors.Is(err, tube.ErrNotFound):
		renderError("error_bad_login")
		return
	case err != nil:
		panic(err)
	}

	var recovery string
	err = user.Unlock(ctx, pass)
	if errors.Is(err, tube.ErrNoKeys) {
		recovery, err = user.SetupKeys(ctx, pass)
	}
	switch {
	case errors.Is(err, tube.ErrWrongPassword):
		renderError("error_bad_login")
		return
	case err != nil:
		panic(err)
	}

	sesh, err := tube.CreateSession(ctx, user)
	if err != nil {
		panic(err)
	}

	http.SetCookie(w, validAuthCookie(sesh))
	if recovery != "" {
		renderRecoveryKey(ctx, w, recovery, jump)
		return
	}
	http.Redirect(w, r, jump, http.StatusSeeOther)
}

func logout(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := tube.DeleteSession(ctx, cookie.Value); err != nil {
			log.Println("logout:", err)
		}
	}
	for _, cookie := range expiredAuthCookies() {
		http.SetCookie(w, cookie)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func renderRecoveryKey(ctx context.Context, w http.ResponseWriter, recovery, next string) {
	data := struct {
		RecoveryKey string
		Next        string
	}{
		RecoveryKey: recovery,
		Next:        next,
	}
	renderTemplate(ctx, w, "recovery-key", data, http.StatusOK)
}

func registerForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var data = struct {
		Email    string
		Invite   string
		ErrorMsg string
	}{
		Invite: r.URL.Query().Get("invite"),
	}
	renderTemplate(ctx, w, "register", data, http.StatusOK)
}

func register(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	pass := r.FormValue("password")
	conf := r.FormValue("password-confirm")
	agree := r.FormValue("agree") == "on"

	renderError := func(err error) {
		var data = struct {
			Email    string
			Invite   string
			ErrorMsg string
		}{
			Email:    email,
			ErrorMsg: err.Error(),
		}
		renderTemplate(ctx, w, "register", data, http.StatusOK)
	}

	if _, err := mail.ParseAddress(email); err != nil {
		renderError(fmt.Errorf("invalid e-mail address: %w", err))
		return
	}

	if pass != conf {
		renderError(fmt.Errorf("password and confirmation don't match"))
		return
	}

	if !agree {
		renderError(fmt.Errorf("you must agree to the terms"))
		return
	}

	user, err := tube.GetUserByEmail(ctx, email)
	if err == nil {
		renderError(fmt.Errorf("e-mail already registered"))
		return
	}
	if err != tube.ErrNotFound {
		renderError(err)
		return
	}

	if len(pass) < minPasswordLength {
		renderError(fmt.Errorf("use a password of at least %d characters", minPasswordLength))
		return
	}

	user = tube.User{Email: email}
	recovery, err := user.Create(ctx, pass)
	if err != nil {
		renderError(err)
		return
	}

	sesh, err := tube.CreateSession(ctx, user)
	if err != nil {
		renderError(err)
		return
	}

	if err := sendVerification(ctx, &user); err != nil {
		log.Printf("verification mail for user %d: %v", user.ID, err)
	}

	http.SetCookie(w, validAuthCookie(sesh))
	renderRecoveryKey(ctx, w, recovery, "/")
}

const minPasswordLength = 10

func forgotForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var data = struct {
		ErrorMsg string
	}{}
	renderTemplate(ctx, w, "forgot", data, http.StatusOK)
}

func forgot(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")

	renderError := func(err error) {
		var data = struct {
			ErrorMsg string
		}{
			ErrorMsg: err.Error(),
		}
		renderTemplate(ctx, w, "forgot", data, http.StatusOK)
	}

	sent := func() {
		data := struct {
			Email string
		}{
			Email: email,
		}
		renderTemplate(ctx, w, "forgot-sent", data, http.StatusOK)
	}

	if ok, _ := forgotPerMail.allow(strings.ToLower(strings.TrimSpace(email)), time.Now()); !ok {
		sent()
		return
	}

	u, err := tube.GetUserByEmail(ctx, email)
	if err == tube.ErrNotFound {
		sent()
		return
	}
	if err != nil {
		renderError(err)
		return
	}

	token, err := u.StartRecovery(ctx)
	if err != nil {
		renderError(err)
		return
	}

	subject := fmt.Sprintf("Password reset for %s", Domain)
	url := fmt.Sprintf("https://%s/recover?id=%d&token=%s", Domain, u.ID, token)
	body := fmt.Sprintf("A password reset was requested for your account %s at %s.\n\n"+
		"Use this link within the next hour to choose a new password:\n%s\n\n"+
		"If you didn't request this, you can ignore this e-mail.\n", u.Email, Domain, url)

	if err := sendMail(u, subject, body); err != nil {
		log.Println("password reset mail:", err)
		renderError(fmt.Errorf("could not send the e-mail, please try again later"))
		return
	}
	sent()
}

type recoverFormData struct {
	UserID   int
	Token    string
	Email    string
	ErrorMsg string
}

func recoveryUser(ctx context.Context, userID int, token string) (tube.User, bool) {
	u, err := tube.GetUser(ctx, userID)
	if err != nil || !u.ValidRecovery(token, time.Now()) {
		return tube.User{}, false
	}
	return u, true
}

func recoverForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	token := r.URL.Query().Get("token")
	u, ok := recoveryUser(ctx, id, token)
	if !ok {
		http.Error(w, "this reset link is invalid or has already been used", http.StatusNotFound)
		return
	}
	renderTemplate(ctx, w, "recover", recoverFormData{UserID: id, Token: token, Email: u.Email}, http.StatusOK)
}

const startOverConfirmation = "delete my library"

func doRecover(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	userID, _ := strconv.Atoi(r.FormValue("userid"))
	token := r.FormValue("token")
	pass := r.FormValue("password")
	conf := r.FormValue("password-confirm")
	recoveryKey := strings.TrimSpace(r.FormValue("recovery-key"))
	startOver := strings.EqualFold(strings.TrimSpace(r.FormValue("start-over")), startOverConfirmation)

	u, ok := recoveryUser(ctx, userID, token)
	if !ok {
		http.Error(w, "this reset link is invalid or has already been used", http.StatusNotFound)
		return
	}
	renderError := func(err error) {
		renderTemplate(ctx, w, "recover", recoverFormData{UserID: userID, Token: token, Email: u.Email, ErrorMsg: err.Error()}, http.StatusOK)
	}

	switch {
	case pass != conf:
		renderError(fmt.Errorf("password and confirmation don't match"))
		return
	case len(pass) < minPasswordLength:
		renderError(fmt.Errorf("use a password of at least %d characters", minPasswordLength))
		return
	}

	var newRecovery string
	switch {
	case recoveryKey != "":
		err := u.RecoverWithKey(ctx, recoveryKey, pass)
		if errors.Is(err, tube.ErrWrongRecoveryKey) {
			renderError(fmt.Errorf("that recovery key doesn't match this account"))
			return
		}
		if err != nil {
			panic(err)
		}
	case startOver:
		recovery, objects, err := u.StartOver(ctx, pass)
		if err != nil {
			panic(err)
		}
		deleteObjects(ctx, u.ID, objects)
		newRecovery = recovery
	default:
		renderError(fmt.Errorf("enter your recovery key, or type %q to start over with an empty library", startOverConfirmation))
		return
	}

	sesh, err := tube.CreateSession(ctx, u)
	if err != nil {
		panic(err)
	}
	http.SetCookie(w, validAuthCookie(sesh))
	if newRecovery != "" {
		renderRecoveryKey(ctx, w, newRecovery, "/")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func deleteObjects(ctx context.Context, userID int, objects []string) {
	for _, key := range objects {
		if err := storage.Delete(ctx, key); err != nil {
			log.Printf("deleting object %s for user %d: %v", key, userID, err)
		}
	}
}

func validAuthCookie(sesh tube.Session) *http.Cookie {
	domain := "." + Domain
	if DebugMode {
		domain = ""
	}
	return &http.Cookie{
		Name:     sessionCookie,
		Domain:   domain,
		Path:     "/",
		Value:    sesh.Token,
		Expires:  sesh.Expires,
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   !DebugMode,
	}
}

func expiredAuthCookies() []*http.Cookie {
	expired := func(domain string) *http.Cookie {
		return &http.Cookie{
			Name:     sessionCookie,
			Domain:   domain,
			Path:     "/",
			Value:    "",
			MaxAge:   -1,
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
			Secure:   !DebugMode,
		}
	}
	cookies := []*http.Cookie{expired("")}
	if !DebugMode && Domain != "" {
		cookies = append(cookies, expired("."+Domain))
	}
	return cookies
}

func encodeRedirect(href *url.URL) string {
	uri := strings.TrimPrefix(href.RequestURI(), "/")
	return base64.RawURLEncoding.EncodeToString([]byte(uri))
}

func decodeRedirect(param string) (string, error) {
	uri, err := base64.RawURLEncoding.DecodeString(param)
	if err != nil {
		return "", err
	}
	return "/" + string(uri), nil
}
