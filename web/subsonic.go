package web

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/cors"

	"github.com/guregu/intertube/tube"
)

const (
	subsonicAPIPrefix      = "/rest/"
	subsonicAPIVersion     = "1.12.0"
	subsonicXMLNS          = "http://subsonic.org/restapi"
	subsonicTimeLayout     = "2006-01-02T15:04:05"
	subsonicIgnoreArticles = "The El La Los Las Le Les"
	subsonicMaxSize        = 500
)

func init() {
	laxCORS := cors.AllowAll()
	add := func(path string, h handlerFunc) {
		capitalized := strings.ToUpper(path[:1]) + path[1:]
		for _, href := range []string{path + ".view", capitalized + ".view", path} {
			mux.Handle(subsonicAPIPrefix+href, laxCORS.Handler(wrap(h)))
		}
	}

	add("ping", subsonicPing)
	add("getLicense", subsonicGetLicense)
	add("getUser", subsonicGetUser)
	add("getMusicFolders", subsonicGetMusicFolders)
	add("getMusicDirectory", subsonicGetMusicDirectory)
	add("getAlbumList", subsonicGetAlbumList1)
	add("getAlbumList2", subsonicGetAlbumList2)
	add("getAlbum", subsonicGetAlbum)
	add("getArtists", subsonicGetArtists)
	add("getIndexes", subsonicGetIndexes)
	add("getGenres", subsonicGetGenres)
	add("getArtist", subsonicGetArtist)
	add("getRandomSongs", subsonicGetRandomSongs)
	add("getSongsByGenre", subsonicGetSongsByGenre)
	add("getStarred", subsonicGetStarred)
	add("getStarred2", subsonicGetStarred)
	add("search2", subsonicSearch2)
	add("search3", subsonicSearch3)
	add("getSong", subsonicGetSong)
	add("getLyricsBySongId", subsonicGetLyricsBySongID)
	add("getCoverArt", subsonicGetCoverArt)
	add("stream", subsonicStream)
	add("download", subsonicStream)
	add("scrobble", subsonicScrobble)
	add("star", subsonicStar)
	add("unstar", subsonicUnstar)
	add("getPlaylists", subsonicGetPlaylists)
	add("getPlaylist", subsonicGetPlaylist)
	add("createPlaylist", subsonicCreatePlaylist)
	add("updatePlaylist", subsonicUpdatePlaylist)
	add("deletePlaylist", subsonicDeletePlaylist)
	add("savePlayQueue", subsonicSavePlayQueue)
	add("getPlayQueue", subsonicGetPlayQueue)
	add("getArtistInfo", subsonicGetArtistInfo)
	add("getArtistInfo2", subsonicGetArtistInfo)
	add("getLyrics", subsonicGetLyrics)
	add("getNowPlaying", subsonicGetNowPlaying)
	add("tokenInfo", subsonicGetTokenInfo)
	add("getOpenSubsonicExtensions", subsonicGetOpenSubsonicExtensions)

}

type subsonicResponse struct {
	XMLName       xml.Name `xml:"http://subsonic.org/restapi subsonic-response" json:"-"`
	Status        string   `xml:"status,attr" json:"status"`
	Version       string   `xml:"version,attr" json:"version"`
	Type          string   `xml:"type,attr" json:"type"`
	ServerVersion string   `xml:"serverVersion,attr" json:"serverVersion"`
	OpenSubsonic  bool     `xml:"openSubsonic,attr" json:"openSubsonic"`
}
type subsonicError struct {
	subsonicResponse
	Error struct {
		Code    int    `xml:"code,attr" json:"code"`
		Msg     string `xml:"message,attr" json:"message"`
		HelpURL string `xml:"helpUrl,attr,omitempty" json:"helpUrl,omitempty"`
	} `xml:"error" json:"error"`
}

const subsonicServerType = "simplemusicserver"

func newSubsonicResponse(status string) subsonicResponse {
	return subsonicResponse{
		Status:        status,
		Version:       subsonicAPIVersion,
		Type:          subsonicServerType,
		ServerVersion: Deployed.UTC().Format("2006.01.02"),
		OpenSubsonic:  true,
	}
}

func subOK() subsonicResponse {
	return newSubsonicResponse("ok")
}

func subErr(code int, msg string) subsonicError {
	resp := subsonicError{
		subsonicResponse: newSubsonicResponse("failed"),
	}
	resp.Error.Code = code
	resp.Error.Msg = msg
	return resp
}

type subsonicUserFolder struct {
	XMLName xml.Name `xml:"folder" json:"-"`
	ID      int      `xml:",chardata" json:"value"`
}

type subsonicUser struct {
	XMLName           xml.Name `xml:"user" json:"-"`
	Username          string   `xml:"username,attr" json:"username"`
	Email             string   `xml:"email,attr" json:"email"`
	ScrobblingEnabled bool     `xml:"scrobblingEnabled,attr" json:"scrobblingEnabled"`
	AdminRole         bool     `xml:"adminRole,attr" json:"adminRole"`
	SettingsRole      bool     `xml:"settingsRole,attr" json:"settingsRole"`
	DownloadRole      bool     `xml:"downloadRole,attr" json:"downloadRole"`
	UploadRole        bool     `xml:"uploadRole,attr" json:"uploadRole"`
	PlaylistRole      bool     `xml:"playlistRole,attr" json:"playlistRole"`
	CoverArtRole      bool     `xml:"coverArtRole,attr" json:"coverArtRole"`
	CommentRole       bool     `xml:"commentRole,attr" json:"commentRole"`
	PodcastRole       bool     `xml:"podcastRole,attr" json:"podcastRole"`
	StreamRole        bool     `xml:"streamRole,attr" json:"streamRole"`
	JukeboxRole       bool     `xml:"jukeboxRole,attr" json:"jukeboxRole"`
	ShareRole         bool     `xml:"shareRole,attr" json:"shareRole"`

	Folders []subsonicUserFolder `json:"folder,omitempty"`
}

func subsonicPanicHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, ex any) {
	var msg string
	var code int
	if err, ok := ex.(error); ok {
		msg = err.Error()
		switch err {
		case tube.ErrNotFound:
			code = 70
		case strconv.ErrSyntax:
			code = 10
		}
	} else {
		msg = fmt.Sprint(ex)
	}

	resp := subErr(code, msg)
	writeSubsonic(ctx, w, r, resp)
}

func subsonicPing(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp := subOK()
	writeSubsonic(ctx, w, r, resp)
}

func subsonicGetUser(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	name := r.FormValue("username")
	if name != "" && name != u.Email {
		writeSubsonic(ctx, w, r, subErr(50, "User is not authorized for the given operation."))
		return
	}

	type userResponse struct {
		subsonicResponse
		User subsonicUser `json:"user"`
	}

	resp := userResponse{
		subsonicResponse: subOK(),

		User: subsonicUser{
			Username:          u.Email,
			Email:             u.Email,
			ScrobblingEnabled: true,
			AdminRole:         false,
			SettingsRole:      false,
			DownloadRole:      true,
			UploadRole:        true,
			PlaylistRole:      true,
			CoverArtRole:      true,
			CommentRole:       false,
			PodcastRole:       false,
			StreamRole:        true,
			JukeboxRole:       false,
			ShareRole:         false,

			Folders: []subsonicUserFolder{{ID: 1}},
		},
	}

	writeSubsonic(ctx, w, r, resp)
}

func subsonicAuth(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	if DebugMode {
		fmt.Println(r.Method, r.URL.EscapedPath(), "?", redactedQuery(r))
	}

	if f := r.FormValue("f"); f != "" {
		ctx = withFormat(ctx, f)
	}

	endpoint := subsonicEndpoint(r.URL.Path)
	if endpoint == "ping" || endpoint == "getopensubsonicextensions" {
		return ctx
	}
	if endpoint == "getlicense" && !hasSubsonicCredentials(r) {
		return ctx
	}

	user, failure := authenticateSubsonic(ctx, r)
	if failure != nil {
		writeSubsonic(ctx, w, r, *failure)
		return nil
	}
	return withUser(ctx, user)
}

func subsonicEndpoint(path string) string {
	name := strings.TrimPrefix(path, subsonicAPIPrefix)
	return strings.ToLower(strings.TrimSuffix(name, ".view"))
}

func hasSubsonicCredentials(r *http.Request) bool {
	for _, k := range []string{"u", "p", "t", "s", "apiKey"} {
		if r.FormValue(k) != "" {
			return true
		}
	}
	return false
}

func subsonicHelpURL() string {
	return "https://" + Domain + "/settings#apps"
}

func authFailure(code int, msg string) *subsonicError {
	resp := subErr(code, msg)
	switch code {
	case subErrWrongCredentials, subErrInvalidAPIKey:
		resp.Error.HelpURL = subsonicHelpURL()
	}
	return &resp
}

const (
	subErrMissingParam       = 10
	subErrWrongCredentials   = 40
	subErrTokenUnsupported   = 41
	subErrConflictingAuth    = 43
	subErrInvalidAPIKey      = 44
	subErrMsgConflictingAuth = "Multiple conflicting authentication mechanisms provided"
)

func authenticateSubsonic(ctx context.Context, r *http.Request) (tube.User, *subsonicError) {
	username := strings.ReplaceAll(r.FormValue("u"), " ", "+")
	password := r.FormValue("p")
	token, salt := strings.ToLower(r.FormValue("t")), r.FormValue("s")
	apiKey := r.FormValue("apiKey")

	passwordAuth := password != ""
	tokenAuth := token != "" || salt != ""

	switch {
	case passwordAuth && tokenAuth:
		return tube.User{}, authFailure(subErrConflictingAuth, subErrMsgConflictingAuth)
	case (passwordAuth || tokenAuth) && apiKey != "":
		return tube.User{}, authFailure(subErrConflictingAuth, subErrMsgConflictingAuth)
	case apiKey != "" && username != "":
		return tube.User{}, authFailure(subErrConflictingAuth, "Don't send u together with apiKey")
	case tokenAuth && (token == "" || salt == ""):
		return tube.User{}, authFailure(subErrMissingParam, "Token login needs both t and s")
	case (passwordAuth || tokenAuth) && username == "":
		return tube.User{}, authFailure(subErrMissingParam, "Missing required parameter u")
	case apiKey != "":
		return authenticateAPIKey(ctx, apiKey)
	case passwordAuth:
		return authenticatePassword(ctx, username, password)
	case tokenAuth:
		return authenticateToken(ctx, username, token, salt)
	case username != "":
		return tube.User{}, authFailure(subErrMissingParam, "Missing required parameter p, or t and s")
	default:
		return tube.User{}, authFailure(subErrMissingParam, "Missing required parameter apiKey")
	}
}

func authenticateAPIKey(ctx context.Context, key string) (tube.User, *subsonicError) {
	ap, keys, err := tube.UnlockWithAppPassword(ctx, key)
	if err != nil {
		if err != tube.ErrNotFound {
			log.Println("subsonic api key:", err)
		}
		return tube.User{}, authFailure(subErrInvalidAPIKey, "Invalid API key")
	}
	user, err := tube.GetUser(ctx, ap.UserID)
	if err != nil {
		return tube.User{}, authFailure(subErrInvalidAPIKey, "Invalid API key")
	}
	user.WithKeys(keys)
	return user, nil
}

func wrongCredentials() *subsonicError {
	return authFailure(subErrWrongCredentials, "Wrong username or password. Use an app password from your settings page.")
}

func authenticatePassword(ctx context.Context, username, password string) (tube.User, *subsonicError) {
	if enc, ok := strings.CutPrefix(password, "enc:"); ok {
		decoded, err := hex.DecodeString(enc)
		if err != nil {
			return tube.User{}, wrongCredentials()
		}
		password = string(decoded)
	}
	user, err := tube.GetUserByEmail(ctx, username)
	if err != nil {
		return tube.User{}, wrongCredentials()
	}
	ap, keys, err := tube.UnlockWithAppPassword(ctx, password)
	if err != nil || ap.UserID != user.ID {
		if err != nil && err != tube.ErrNotFound {
			log.Println("subsonic auth for user", user.ID, err)
		}
		return tube.User{}, wrongCredentials()
	}
	user.WithKeys(keys)
	return user, nil
}

func authenticateToken(ctx context.Context, username, token, salt string) (tube.User, *subsonicError) {
	user, err := tube.GetUserByEmail(ctx, username)
	if err != nil {
		return tube.User{}, wrongCredentials()
	}
	_, keys, err := tube.UnlockWithAppPasswordToken(ctx, user.ID, matchToken(token, salt))
	switch {
	case err == tube.ErrStrictAppPassword:
		resp := authFailure(subErrTokenUnsupported, "Strict app passwords can't use token login. Turn on plaintext password in your app, or use it as an API key.")
		resp.Error.HelpURL = subsonicHelpURL()
		return tube.User{}, resp
	case err != nil:
		if err != tube.ErrNotFound {
			log.Println("subsonic token auth for user", user.ID, err)
		}
		return tube.User{}, wrongCredentials()
	}
	user.WithKeys(keys)
	return user, nil
}

func matchToken(token, salt string) func(string) bool {
	return func(secret string) bool {
		sum := md5.Sum([]byte(secret + salt))
		return tube.EqualSecret(token, hex.EncodeToString(sum[:]))
	}
}

type subsonicTokenInfo struct {
	subsonicResponse
	TokenInfo struct {
		Username string `xml:"username,attr" json:"username"`
	} `xml:"tokenInfo" json:"tokenInfo"`
}

func subsonicGetTokenInfo(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	resp := subsonicTokenInfo{subsonicResponse: subOK()}
	resp.TokenInfo.Username = u.Email
	writeSubsonic(ctx, w, r, resp)
}

type subsonicExtension struct {
	Name     string `xml:"name,attr" json:"name"`
	Versions []int  `xml:"versions" json:"versions"`
}

type subsonicExtensions struct {
	subsonicResponse
	Extensions []subsonicExtension `xml:"openSubsonicExtensions" json:"openSubsonicExtensions"`
}

func subsonicGetOpenSubsonicExtensions(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	writeSubsonic(ctx, w, r, subsonicExtensions{
		subsonicResponse: subOK(),
		Extensions: []subsonicExtension{
			{Name: "apiKeyAuthentication", Versions: []int{1}},
			{Name: "songLyrics", Versions: []int{1}},
		},
	})
}

type subsonicLicense struct {
	subsonicResponse
	License struct {
		Valid   bool   `xml:"valid,attr" json:"valid"`
		Expires string `xml:"licenseExpires,attr" json:"licenseExpires"`
		Email   string `xml:"email,attr,omitempty" json:"email,omitempty"`
	} `xml:"license" json:"license"`
}

func subsonicGetLicense(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	expire := time.Now().UTC().Add(24 * time.Hour * 30)
	resp := subsonicLicense{
		subsonicResponse: subOK(),
	}
	resp.License.Valid = expire.After(time.Now())
	resp.License.Expires = expire.Format(subsonicTimeLayout)
	resp.License.Email = u.Email
	writeSubsonic(ctx, w, r, resp)
}

func subsonicSavePlayQueue(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	writeSubsonic(ctx, w, r, subOK())
}

func subsonicGetPlayQueue(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	writeSubsonic(ctx, w, r, subOK())
}

func subsonicGetNowPlaying(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	writeSubsonic(ctx, w, r, subOK())
}

func writeSubsonic(ctx context.Context, w http.ResponseWriter, r *http.Request, resp any) {
	f := formatFrom(ctx)
	switch f {
	case "json":
		w.Header().Set("Content-Type", "application/json")

		wrap := struct {
			Resp any `json:"subsonic-response"`
		}{
			Resp: resp,
		}

		if DebugMode {
			fmt.Println("\n>>>>>>>>>>>>>>> "+r.URL.EscapedPath(), " ? ", redactedQuery(r))
			raw, err := json.Marshal(wrap)
			fmt.Println("=================\n"+string(raw)+"\n~~~~~~~err", err, "~~~~~~~")
		}

		renderJSON(w, wrap, http.StatusOK)
	case "jsonp":
		w.Header().Set("Content-Type", "application/javascript")
		cb := r.FormValue("callback")
		if cb == "" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"subsonic-response":{"status":"failed","version":"1.12.0","error":{"code":10,"message":"missing callback param"}}}`)
			return
		}

		wrap := struct {
			Resp any `json:"subsonic-response"`
		}{
			Resp: resp,
		}

		fmt.Fprintf(w, "%s(", cb)
		js, err := json.Marshal(wrap)
		if err != nil {
			panic(err)
		}
		fmt.Fprint(w, string(js))
		fmt.Fprint(w, ");")
	case "xml":
		if DebugMode {
			raw, err := xml.MarshalIndent(resp, "  ", "	")
			fmt.Println("\n>>>>>>>>>>>>>>> "+r.URL.EscapedPath(), " ? ", redactedQuery(r))
			fmt.Println("=================\n"+string(raw)+"\n~~~~~~~~ err:", err, "~~~~")
		}

		renderXML(w, resp, http.StatusOK)
	default:
		panic("unknown format: " + f)
	}
}

func isSubsonicReq(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, subsonicAPIPrefix)
}

func sec2msec(sec float64) int {
	return int(sec * 1000)
}

type formatkey struct{}

func withFormat(ctx context.Context, f string) context.Context {
	return context.WithValue(ctx, formatkey{}, f)
}

func formatFrom(ctx context.Context) string {
	f, _ := ctx.Value(formatkey{}).(string)
	if f == "" {
		return "xml"
	}
	return f
}

var subsonicSecretParams = []string{"p", "t", "s", "apiKey"}

func redactedQuery(r *http.Request) string {
	q := r.URL.Query()
	for _, k := range subsonicSecretParams {
		if q.Has(k) {
			q.Set(k, "REDACTED")
		}
	}
	return q.Encode()
}
