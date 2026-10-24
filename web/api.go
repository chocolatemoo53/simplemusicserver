package web

import (
	"context"
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

var (
	Domain    = "inter.tube"
	Deployed  time.Time
	DebugMode = false
	AdminIDs  []int
)

func init() {
	use("/", discover)
	use("/", allowGuest(
		"/login", "/register", "/forgot", "/recover", "/verify",
		"/terms", "/privacy", "/buy/", "/subsonic",
		"/favicon.svg", "/apple-touch-icon.png",
		"/api/v0/login",
		"/external/btcpay"))
	use("/", allowGuestPrefix("/fonts/"))
	use("/", requireLogin)

	handle("GET /{$}", homepage)
	handle("GET /terms", termsOfService)
	handle("GET /privacy", privacyPolicy)
	handle("GET /fonts/{name}", serveFont)
	handle("GET /favicon.svg", serveIcon("favicon.svg"))
	handle("GET /apple-touch-icon.png", serveIcon("apple-touch-icon.png"))

	handle("GET /login", loginForm)
	handle("POST /login", login)
	handle("POST /logout", logout)

	handle("GET /register", registerForm)
	handle("POST /register", register)

	handle("GET /forgot", forgotForm)
	handle("POST /forgot", forgot)

	handle("GET /recover", recoverForm)
	handle("POST /recover", doRecover)

	handle("GET /upload", uploadForm)
	handle("POST /upload/track", uploadStart)
	handle("POST /upload/track/{id}", uploadFinish)
	handle("PUT /upload/data/{id}", uploadData)
	handle("GET /pic/{id}", showPicture)

	handle("GET /sync", syncForm)
	handle("GET /export/library.zip", exportLibrary)

	use("/music", requireStanding)
	use("/music/", requireStanding)
	use("/rest/", requireStanding)
	use("/music", cacheHeaders)
	use("/music/", cacheHeaders)
	handle("GET /music", showMusic)
	handle("HEAD /music", showMusicHead)
	handle("GET /music/{$}", showMusic)
	handle("HEAD /music/{$}", showMusicHead)
	handle("GET /music/{kind}", showMusic)
	handle("HEAD /music/{kind}", showMusicHead)

	handle("DELETE /track/{id}", deleteTrack)
	handle("POST /track/{id}/played", incPlays)
	handle("POST /track/{id}/resume", setResume)
	handle("GET /track/{id}/lyrics", showLyrics)
	handle("GET /track/{id}/edit", editTrackForm)
	handle("POST /track/{id}/edit", editTrack)

	handle("GET /dl/tracks/{id}", downloadTrack)

	use("/playlist", requireStanding)
	use("/playlist/", requireStanding)
	use("/playlists", requireStanding)
	use("/playlists/", requireStanding)
	handle("GET /playlist", func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/music/playlists", http.StatusFound)
	})
	handle("GET /playlists", func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/music/playlists", http.StatusFound)
	})
	handle("GET /playlists/{$}", func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/music/playlists", http.StatusFound)
	})
	handle("GET /playlist/{$}", createPlaylistForm)
	handle("POST /playlist/{$}", createPlaylist)
	handle("GET /playlist/{id}", createPlaylistForm)
	handle("POST /playlist/{id}", createPlaylist)
	handle("DELETE /playlist/{id}", deletePlaylist)

	handle("GET /subsonic", subsonicHelp)

	handle("GET /settings", settingsForm)
	handle("POST /settings", settings)
	handle("GET /settings/password", changePasswordForm)
	handle("POST /settings/password", changePassword)
	handle("POST /settings/app-passwords", createAppPassword)
	handle("POST /settings/delete", deleteOwnAccount)
	handle("POST /settings/pgp", savePGPKey)
	handle("POST /settings/mail-test", sendTestMail)
	handle("POST /settings/app-passwords/{id}/revoke", revokeAppPassword)

	handle("GET /buy/{$}", buyForm)
	handle("POST /buy/checkout", topUp)
	handle("POST /buy/refund", requestRefund)
	handle("GET /buy/success", topUpDone)

	use("/admin/", requireAdmin)
	handle("GET /admin/{$}", adminIndex)
	handle("GET /admin/user/{id}", adminUser)
	handle("POST /admin/user/{id}/credit", adminCredit)
	handle("POST /admin/user/{id}/exempt", adminExempt)
	handle("POST /admin/user/{id}/delete", adminDelete)

	handle("POST /external/btcpay", btcpayWebhook)
}

func init() {
	var dirty bool
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.time":
				var err error
				Deployed, err = time.Parse(time.RFC3339, kv.Value)
				if err != nil {
					panic(err)
				}
			case "vcs.modified":
				dirty = kv.Value == "true"
			}
		}
	}
	if !dirty && !Deployed.IsZero() {
		return
	}
	Deployed = time.Now().UTC()
}

func Load() {
	log.Println("Loading templates...")
	templates = parseTemplates()

	log.Println("Loading translations...")
	loadTranslations()

	log.Println("Loaded up")
}
