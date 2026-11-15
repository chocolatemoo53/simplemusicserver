package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/guregu/intertube/tube"
)

const (
	jamMaxListeners = 20
	jamMaxItems     = 200
	jamLifetime     = 12 * time.Hour
	jamHostTimeout  = 10 * time.Minute
	jamListenerTTL  = 30 * time.Second
	jamPollWait     = 25 * time.Second
	jamEndSlack     = 2 * time.Second
)

var (
	jamNow   = time.Now
	jamLimit = newLimiter(120, time.Minute)
	jamReap  sync.Once
)

var jams = struct {
	sync.Mutex
	byID   map[string]*jam
	byHost map[int]*jam
}{byID: map[string]*jam{}, byHost: map[int]*jam{}}

type jamItem struct {
	Key      int
	Title    string
	Artist   string
	Album    string
	Duration int
	AddedBy  string
	Pic      bool

	object  string
	size    int64
	mime    string
	fileKey []byte
	picObj  string
	picKey  []byte
	picSize int64
}

type jamListener struct {
	name string
	seen time.Time
}

type jam struct {
	mu           sync.Mutex
	id           string
	host         int
	guestControl bool
	items        []*jamItem
	index        int
	playing      bool
	pos          time.Duration
	anchor       time.Time
	version      int
	gen          int
	changed      chan struct{}
	listeners    map[string]jamListener
	nextKey      int
	created      time.Time
	hostSeen     time.Time
	ended        bool
}

func init() {
	handle("GET /jam", jamHome)
	handle("POST /jam", jamStart)
	handle("GET /jam/{id}", jamPage)
	handle("GET /jam/{id}/state", jamStateHandler)
	handle("GET /jam/{id}/track/{key}", jamTrack)
	handle("GET /jam/{id}/pic/{key}", jamPic)
	handle("GET /jam/{id}/search", jamSearch)
	handle("POST /jam/{id}/add", jamAdd)
	handle("POST /jam/{id}/control", jamControl)
	handle("POST /jam/{id}/ended", jamEnded)
	handle("POST /jam/{id}/settings", jamSettings)
}

func newJamID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func findJam(id string) *jam {
	jams.Lock()
	defer jams.Unlock()
	return jams.byID[id]
}

func reapJams() {
	for range time.Tick(time.Minute) {
		now := jamNow()
		jams.Lock()
		var stale []*jam
		for _, j := range jams.byID {
			if now.Sub(j.created) > jamLifetime || now.Sub(j.hostSeen) > jamHostTimeout {
				stale = append(stale, j)
			}
		}
		jams.Unlock()
		for _, j := range stale {
			endJam(j)
		}
	}
}

func endJam(j *jam) {
	jams.Lock()
	if jams.byID[j.id] == j {
		delete(jams.byID, j.id)
	}
	if jams.byHost[j.host] == j {
		delete(jams.byHost, j.host)
	}
	jams.Unlock()

	j.mu.Lock()
	defer j.mu.Unlock()
	for _, it := range j.items {
		clear(it.fileKey)
		clear(it.picKey)
	}
	j.items = nil
	j.ended = true
	j.notify(false)
}

func (j *jam) notify(timeline bool) {
	j.version++
	if timeline {
		j.gen++
		j.schedule()
	}
	close(j.changed)
	j.changed = make(chan struct{})
}

func (j *jam) position(now time.Time) time.Duration {
	if !j.playing {
		return j.pos
	}
	return j.pos + now.Sub(j.anchor)
}

func (j *jam) current() *jamItem {
	if j.index < 0 || j.index >= len(j.items) {
		return nil
	}
	return j.items[j.index]
}

func (j *jam) seek(index int, pos time.Duration, playing bool) {
	j.index = index
	j.pos = pos
	j.anchor = jamNow()
	j.playing = playing && j.current() != nil
	j.notify(true)
}

func (j *jam) schedule() {
	cur := j.current()
	if !j.playing || cur == nil || cur.Duration <= 0 {
		return
	}
	gen := j.gen
	wait := time.Duration(cur.Duration)*time.Second - j.position(jamNow()) + jamEndSlack
	time.AfterFunc(wait, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.gen == gen && j.playing && !j.ended {
			j.seek(j.index+1, 0, true)
		}
	})
}

func (j *jam) itemIndex(key int) int {
	return slices.IndexFunc(j.items, func(it *jamItem) bool { return it.Key == key })
}

type jamStateView struct {
	Version      int
	Now          int64
	Items        []jamItem
	Index        int
	Playing      bool
	Pos          float64
	Anchor       int64
	GuestControl bool
	Host         bool
	CanControl   bool
	CanAdd       bool
	Listeners    []string
	Ended        bool
	Full         bool
}

func (j *jam) view(ctx context.Context) jamStateView {
	u, signedIn := userFrom(ctx)
	host := signedIn && u.ID == j.host
	now := jamNow()
	var names []string
	for id, l := range j.listeners {
		if now.Sub(l.seen) > jamListenerTTL {
			delete(j.listeners, id)
			continue
		}
		names = append(names, l.name)
	}
	slices.Sort(names)
	items := make([]jamItem, len(j.items))
	for i, it := range j.items {
		items[i] = *it
	}
	return jamStateView{
		Version:      j.version,
		Now:          now.UnixMilli(),
		Items:        items,
		Index:        j.index,
		Playing:      j.playing,
		Pos:          j.pos.Seconds(),
		Anchor:       j.anchor.UnixMilli(),
		GuestControl: j.guestControl,
		Host:         host,
		CanControl:   host || j.guestControl,
		CanAdd:       signedIn,
		Listeners:    names,
		Ended:        j.ended,
	}
}

func jamName(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	for utf8.RuneCountInString(s) > 32 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	if s == "" {
		return "Guest"
	}
	return s
}

func jamHome(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	jams.Lock()
	var id string
	if j := jams.byHost[u.ID]; j != nil {
		id = j.id
	}
	jams.Unlock()
	if id != "" {
		http.Redirect(w, r, "/jam/"+id, http.StatusSeeOther)
		return
	}
	renderTemplate(ctx, w, "jam", struct {
		User tube.User
		ID   string
		Gone bool
	}{User: u}, http.StatusOK)
}

func jamStart(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	jamReap.Do(func() { go reapJams() })
	now := jamNow()
	jams.Lock()
	j := jams.byHost[u.ID]
	if j == nil {
		j = &jam{
			id:        newJamID(),
			host:      u.ID,
			changed:   make(chan struct{}),
			listeners: map[string]jamListener{},
			created:   now,
			hostSeen:  now,
			anchor:    now,
		}
		jams.byID[j.id] = j
		jams.byHost[u.ID] = j
	}
	id := j.id
	jams.Unlock()
	http.Redirect(w, r, "/jam/"+id, http.StatusSeeOther)
}

func jamPage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	id := r.PathValue("id")
	if findJam(id) == nil {
		renderTemplate(ctx, w, "jam", struct {
			User tube.User
			ID   string
			Gone bool
		}{User: u, Gone: true}, http.StatusNotFound)
		return
	}
	renderTemplate(ctx, w, "jam", struct {
		User tube.User
		ID   string
		Gone bool
	}{User: u, ID: id}, http.StatusOK)
}

func jamStateHandler(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	j := findJam(r.PathValue("id"))
	if j == nil {
		renderJSON(w, jamStateView{Ended: true}, http.StatusNotFound)
		return
	}
	u, signedIn := userFrom(ctx)
	listener := r.FormValue("l")
	since, _ := strconv.Atoi(r.FormValue("v"))

	j.mu.Lock()
	now := jamNow()
	if signedIn && u.ID == j.host {
		j.hostSeen = now
	}
	if listener != "" && len(listener) <= 64 {
		l, known := j.listeners[listener]
		if !known {
			active := 0
			for _, l := range j.listeners {
				if now.Sub(l.seen) <= jamListenerTTL {
					active++
				}
			}
			if active >= jamMaxListeners {
				j.mu.Unlock()
				renderJSON(w, jamStateView{Full: true}, http.StatusServiceUnavailable)
				return
			}
		}
		name := jamName(r.FormValue("name"))
		j.listeners[listener] = jamListener{name: name, seen: now}
		if !known || l.name != name {
			j.notify(false)
		}
	}
	changed := j.changed
	waiting := since == j.version
	j.mu.Unlock()

	if waiting {
		timer := time.NewTimer(jamPollWait)
		select {
		case <-changed:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		timer.Stop()
	}

	j.mu.Lock()
	view := j.view(ctx)
	j.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	renderJSON(w, view, http.StatusOK)
}

func jamItemFor(r *http.Request) (jamItem, bool) {
	j := findJam(r.PathValue("id"))
	key, err := strconv.Atoi(r.PathValue("key"))
	if j == nil || err != nil {
		return jamItem{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	i := j.itemIndex(key)
	if i < 0 {
		return jamItem{}, false
	}
	it := *j.items[i]
	it.fileKey = bytes.Clone(it.fileKey)
	it.picKey = bytes.Clone(it.picKey)
	return it, true
}

func jamTrack(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	it, ok := jamItemFor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	reader, err := tube.NewDecryptingReader(it.size, it.fileKey, it.object, objectFetcher(ctx, it.object))
	if err != nil {
		panic(err)
	}
	if it.mime != "" {
		w.Header().Set("Content-Type", it.mime)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, reader)
}

func jamPic(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	it, ok := jamItemFor(r)
	if !ok || !it.Pic {
		http.NotFound(w, r)
		return
	}
	reader, err := tube.NewDecryptingReader(it.picSize, it.picKey, it.picObj, objectFetcher(ctx, it.picObj))
	if err != nil {
		panic(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, reader)
}

type jamSearchResult struct {
	ID     string
	Title  string
	Artist string
	Album  string
}

func jamSearch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, signedIn := userFrom(ctx)
	if !signedIn || findJam(r.PathValue("id")) == nil {
		http.NotFound(w, r)
		return
	}
	tracks, err := u.GetTracks(ctx)
	if err != nil {
		panic(err)
	}
	words := strings.Fields(strings.ToLower(r.FormValue("q")))
	results := []jamSearchResult{}
	for _, t := range NewLibrary(tracks, nil).Tracks(organize{}) {
		text := strings.ToLower(t.Info.Title + " " + t.Info.Artist + " " + t.Info.Album)
		if !containsAll(text, words) {
			continue
		}
		results = append(results, jamSearchResult{ID: t.ID, Title: t.Info.Title, Artist: t.Info.Artist, Album: t.Info.Album})
		if len(results) == 50 {
			break
		}
	}
	renderJSON(w, results, http.StatusOK)
}

func containsAll(text string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func jamPost(w http.ResponseWriter, r *http.Request) (*jam, bool) {
	if ok, wait := jamLimit.allow(clientIP(r), jamNow()); !ok {
		tooManyRequests(w, wait)
		return nil, false
	}
	j := findJam(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return j, true
}

func jamAdd(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, signedIn := userFrom(ctx)
	j, ok := jamPost(w, r)
	if !ok {
		return
	}
	if !signedIn {
		http.Error(w, "Sign in to add music", http.StatusUnauthorized)
		return
	}
	ids := strings.Split(r.FormValue("ids"), ",")
	if len(ids) > jamMaxItems {
		ids = ids[:jamMaxItems]
	}
	tracks, err := u.GetTracksBatch(ctx, ids)
	if err != nil {
		panic(err)
	}
	by := jamName(r.FormValue("name"))
	var items []*jamItem
	for _, t := range tracks {
		fileKey, err := u.TrackFileKey(t)
		if err != nil {
			panic(err)
		}
		it := &jamItem{
			Title:    t.Info.Title,
			Artist:   t.Info.Artist,
			Album:    t.Info.Album,
			Duration: t.Duration,
			AddedBy:  by,
			object:   t.Object,
			size:     int64(t.Size),
			mime:     t.MIMEType(),
			fileKey:  fileKey,
		}
		if t.Picture.ID != "" {
			it.Pic = true
			it.picObj = t.Picture.Object
			it.picKey = u.PictureKey(t.Picture.ID)
			it.picSize = t.Picture.Size
		}
		items = append(items, it)
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		http.NotFound(w, r)
		return
	}
	if len(j.items)+len(items) > jamMaxItems {
		http.Error(w, "The queue is full", http.StatusConflict)
		return
	}
	idle := j.current() == nil
	for _, it := range items {
		j.nextKey++
		it.Key = j.nextKey
		j.items = append(j.items, it)
	}
	if idle && len(items) > 0 {
		j.seek(len(j.items)-len(items), 0, true)
	} else {
		j.notify(false)
	}
	w.WriteHeader(http.StatusNoContent)
}

func jamControl(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, signedIn := userFrom(ctx)
	j, ok := jamPost(w, r)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !(signedIn && u.ID == j.host) && !j.guestControl {
		http.Error(w, "Only the host can do that", http.StatusForbidden)
		return
	}
	now := jamNow()
	key, _ := strconv.Atoi(r.FormValue("key"))
	i := j.itemIndex(key)
	switch r.FormValue("action") {
	case "play":
		if j.current() != nil && !j.playing {
			j.seek(j.index, j.pos, true)
		}
	case "pause":
		if j.playing {
			j.seek(j.index, j.position(now), false)
		}
	case "seek":
		secs, err := strconv.ParseFloat(r.FormValue("pos"), 64)
		if err != nil || secs < 0 || j.current() == nil {
			http.Error(w, "Bad position", http.StatusBadRequest)
			return
		}
		j.seek(j.index, time.Duration(secs*float64(time.Second)), j.playing)
	case "next":
		j.seek(min(j.index+1, len(j.items)), 0, j.playing || j.current() == nil)
	case "prev":
		if j.position(now) > 3*time.Second || j.index == 0 {
			j.seek(j.index, 0, j.playing)
		} else {
			j.seek(j.index-1, 0, j.playing)
		}
	case "jump":
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		j.seek(i, 0, true)
	case "remove":
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		clear(j.items[i].fileKey)
		clear(j.items[i].picKey)
		j.items = slices.Delete(j.items, i, i+1)
		switch {
		case i < j.index:
			j.index--
			j.notify(false)
		case i == j.index:
			j.seek(j.index, 0, j.playing)
		default:
			j.notify(false)
		}
	case "up", "down":
		to := i - 1
		if r.FormValue("action") == "down" {
			to = i + 1
		}
		if i <= j.index || to <= j.index || to >= len(j.items) {
			http.Error(w, "Can't move that", http.StatusBadRequest)
			return
		}
		j.items[i], j.items[to] = j.items[to], j.items[i]
		j.notify(false)
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func jamEnded(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	j, ok := jamPost(w, r)
	if !ok {
		return
	}
	key, _ := strconv.Atoi(r.FormValue("key"))
	j.mu.Lock()
	defer j.mu.Unlock()
	cur := j.current()
	if cur != nil && cur.Key == key && j.playing {
		end := time.Duration(cur.Duration) * time.Second
		if j.position(jamNow()) >= end-jamEndSlack {
			j.seek(j.index+1, 0, true)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func jamSettings(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, signedIn := userFrom(ctx)
	j, ok := jamPost(w, r)
	if !ok {
		return
	}
	if !signedIn || u.ID != j.host {
		http.Error(w, "Only the host can do that", http.StatusForbidden)
		return
	}
	switch r.FormValue("action") {
	case "guests":
		j.mu.Lock()
		j.guestControl = r.FormValue("control") == "on"
		j.notify(false)
		j.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "relink":
		jams.Lock()
		delete(jams.byID, j.id)
		j.mu.Lock()
		j.id = newJamID()
		j.listeners = map[string]jamListener{}
		id := j.id
		j.mu.Unlock()
		jams.byID[id] = j
		jams.Unlock()
		renderJSON(w, struct{ ID string }{id}, http.StatusOK)
	case "end":
		endJam(j)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
	}
}
