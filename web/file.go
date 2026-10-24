package web

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

const (
	maxFileSize = 1024 * 1024 * 1024
)

func downloadTrack(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	id := r.PathValue("id")
	if ext := path.Ext(id); ext != "" {
		id = id[:len(id)-len(ext)]
	}

	t, err := u.GetTrack(ctx, id)
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		panic(err)
	}
	fileKey, err := u.TrackFileKey(t)
	if err != nil {
		panic(err)
	}
	reader, err := tube.NewDecryptingReader(int64(t.Size), fileKey, t.Object, objectFetcher(ctx, t.Object))
	if err != nil {
		panic(err)
	}
	if mime := t.MIMEType(); mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", encodeContentDisp(t.Filename))
	}
	http.ServeContent(w, r, "", t.LastModOrDate(), reader)
}

func objectFetcher(ctx context.Context, object string) tube.FetchFunc {
	return func(off, n int64) ([]byte, error) {
		return storage.ReadRange(ctx, object, off, n)
	}
}

var pictureIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func showPicture(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	servePicture(ctx, w, r, u, r.PathValue("id"))
}

func servePicture(ctx context.Context, w http.ResponseWriter, r *http.Request, u tube.User, id string) {
	if !pictureIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	object := tube.PictureObject(u.ID, id)
	size, err := tube.ObjectSize(ctx, u.ID, object)
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		panic(err)
	}
	reader, err := tube.NewDecryptingReader(size, u.PictureKey(id), object, objectFetcher(ctx, object))
	if err != nil {
		panic(err)
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, reader)
}

func uploadStart(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	name := r.FormValue("name")
	filetype := r.FormValue("type")
	size, err := strconv.ParseInt(r.FormValue("size"), 10, 64)
	if err != nil || size <= 0 {
		renderText(w, "Missing file size", http.StatusBadRequest)
		return
	}
	var localMod int64
	if msec, err := strconv.ParseInt(r.FormValue("lastmod"), 10, 64); err == nil {
		localMod = msec
	}

	w.Header().Set("Tube-Upload-Usage", strconv.FormatInt(u.Usage, 10))
	if size > maxFileSize {
		renderText(w, "File too big. The maximum size is "+strconv.FormatInt(maxFileSize/1000/1000, 10)+"MB", http.StatusBadRequest)
		return
	}
	if !u.CanStore(size, time.Now()) {
		if u.NeedsVerification() {
			renderText(w, errVerifyEmail, http.StatusForbidden)
			return
		}
		renderText(w, errStorageFull, http.StatusBadRequest)
		return
	}

	f := tube.NewFile(u.ID, name, size)
	f.Type = filetype
	f.LocalMod = localMod
	if err := u.CreateFile(ctx, f); err != nil {
		panic(err)
	}

	data := struct {
		ID  string
		CD  string
		URL string
	}{
		ID:  f.ID,
		CD:  encodeContentDisp(name),
		URL: "/upload/data/" + f.ID,
	}
	w.Header().Set("Tube-Upload-ID", f.ID)
	renderJSON(w, data, http.StatusOK)
}

func uploadData(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	f, err := u.GetFile(ctx, r.PathValue("id"))
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		panic(err)
	}
	if f.TrackID != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != f.Size {
		renderText(w, "Upload size doesn't match", http.StatusBadRequest)
		return
	}

	select {
	case uploadSlots <- struct{}{}:
		defer func() { <-uploadSlots }()
	case <-ctx.Done():
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, f.Size+1))
	if err != nil {
		renderText(w, "Upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(data)) != f.Size {
		renderText(w, "Upload size doesn't match", http.StatusBadRequest)
		return
	}
	track, err := processUpload(ctx, u, f, data)
	if err != nil {
		log.Printf("upload %s for user %d: %v", f.ID, u.ID, err)
		renderText(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := u.SetFileTrack(ctx, f.ID, track.ID); err != nil {
		panic(err)
	}
	w.WriteHeader(http.StatusOK)
}

func uploadFinish(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	f, err := u.GetFile(ctx, r.PathValue("id"))
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		panic(err)
	}
	if f.TrackID == "" {
		w.Header().Set("Tube-Upload-Status", f.Status())
		renderText(w, "Upload not received yet", http.StatusConflict)
		return
	}
	track, err := u.GetTrack(ctx, f.TrackID)
	if err != nil {
		panic(err)
	}
	renderJSON(w, track, http.StatusOK)
}

var uploadSlots = make(chan struct{}, 2)

func SetUploadConcurrency(n int) {
	if n > 0 {
		uploadSlots = make(chan struct{}, n)
	}
}

var TempDir string

func encryptToBucket(ctx context.Context, object string, key, plain []byte) error {
	tmp, err := os.CreateTemp(TempDir, "upload-*.enc")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tube.EncryptStream(tmp, bytes.NewReader(plain), key, object); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return storage.FilesBucket.PutFile(ctx, object, tmp, tube.EncryptedSize(int64(len(plain))))
}

func encodeContentDisp(filename string) string {
	ext := path.Ext(filename)
	escaped := url.QueryEscape(filename)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	return "attachment; filename=\"file" + ext + "\"; filename*=UTF-8''" + escaped
}
