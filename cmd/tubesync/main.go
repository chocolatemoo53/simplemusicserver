package main

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/term"

	"github.com/guregu/intertube/tube"
)

var version = "1.0.0"

var defaultHost string

var client *http.Client
var rootPath string

var (
	parallel = flag.Int("parallel", 10, "number of simultaneous downloads")
	host     = flag.String("host", envOr("TUBESYNC_HOST", defaultHost), "server address, for example https://music.example.com (or set TUBESYNC_HOST)")
	workDir  = flag.String("path", "", "path to music library directory, leave blank (default) for current directory")
	help     = flag.Bool("help", false, "show this help message")
)

var ErrSkip = fmt.Errorf("skipped")

var errWrongLogin = errors.New("wrong e-mail or app password (create an app password in Settings, under App passwords)")

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func normalizeHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("invalid server address: %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

type basicAuth struct {
	host, email, password string
}

func (b basicAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host == b.host {
		req = req.Clone(req.Context())
		req.SetBasicAuth(b.email, b.password)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func main() {
	flag.Parse()
	if *help {
		flag.PrintDefaults()
		os.Exit(0)
	}

	if *workDir == "" {
		exe, err := os.Executable()
		maybeDie(err)
		rootPath = filepath.Dir(exe)
	} else {
		rootPath = *workDir
	}

	if *host == "" {
		fmt.Print("server address (for example https://music.example.com): ")
		fmt.Scanln(host)
	}
	server, err := normalizeHost(*host)
	maybeDie(err)
	*host = server

	fmt.Println("welcome to tubesync version", version)
	fmt.Println("   for", *host)
	fmt.Println("library directory:", rootPath)
	if strings.HasPrefix(*host, "http://") {
		fmt.Println("warning: this address isn't HTTPS, so your app password is sent unencrypted")
	}

	email := os.Getenv("TUBESYNC_EMAIL")
	if email == "" {
		fmt.Print("e-mail (blank to quit): ")
		fmt.Scanln(&email)
	}
	if email == "" {
		fmt.Println("e-mail is blank, bye")
		os.Exit(1)
	}

	password := os.Getenv("TUBESYNC_PASSWORD")
	if password == "" {
		fmt.Print("app password (hidden): ")
		raw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		maybeDie(err)
		password = string(raw)
	}
	client = &http.Client{Transport: basicAuth{host: *host, email: email, password: strings.TrimSpace(password)}}

	fmt.Print("getting track metadata (might take a while)")
	tracks, err := getTracks()
	fmt.Println()
	maybeDie(err)
	fmt.Println("got", len(tracks), "tracks")
	fmt.Println("syncing...")

	total := len(tracks)
	progress := new(int64)
	errct := new(int64)

	dlchan := make(chan tube.Track, *parallel)
	var wg sync.WaitGroup
	for n := 0; n < *parallel; n++ {
		wg.Add(1)
		go func() {
			for track := range dlchan {
				var msg string
				name := displayName(track)
				if err := download(track); err != nil {
					if err == ErrSkip {
						msg = fmt.Sprint("skipped: ", name, " (already downloaded)")
					} else {
						msg = fmt.Sprint("download error: ", track.ID, name, " ", err)
						atomic.AddInt64(errct, 1)
					}
				} else {
					msg = "downloaded: " + name
				}
				prog := atomic.AddInt64(progress, 1)
				fmt.Printf("[%d/%d] %s\n", prog, total, msg)
			}
			wg.Done()
		}()
	}

	for _, track := range tracks {
		dlchan <- track
	}
	close(dlchan)
	wg.Wait()

	fmt.Println("done~")
	fmt.Printf("got %d error(s)\n", atomic.LoadInt64(errct))
	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Println("\nPress ENTER to exit")
		fmt.Scanln()
	}

	os.Exit(0)
}

func getTracks() (tube.Tracks, error) {
	var tracks tube.Tracks
	var resp struct {
		Tracks tube.Tracks
		Next   string
	}
	var err error
	for {
		err = get("/api/v0/tracks?start="+resp.Next, &resp)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, resp.Tracks...)
		if resp.Next == "" {
			break
		}
		fmt.Print(".")
	}
	return tracks, nil
}

func download(track tube.Track) error {
	fpath := filepath.Join(rootPath, filepath.FromSlash(track.VirtualPath()))
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		return err
	}

	hash := track.ContentHash
	if hash == "" {
		hash = track.ID
	}
	if ok, err := shouldSkip(fpath, hash); ok && err == nil {
		return ErrSkip
	} else if err != nil {
		return err
	}

	dlURL := track.DL
	if dlURL == "" {
		dlURL = track.FileURL()
	}
	if strings.HasPrefix(dlURL, "/") {
		dlURL = *host + dlURL
	}
	resp, err := client.Get(dlURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", track.ID, resp.Status)
	}

	f, err := os.Create(fpath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

func get(path string, out interface{}) error {
	href := *host + path

	resp, err := client.Get(href)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errWrongLogin
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: %s", path, resp.Status)
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return err
		}
	}
	return nil
}

func shouldSkip(path, hash string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	sum, err := sha1Sum(f)
	if err != nil {
		return false, err
	}
	if sum == hash {
		return true, nil
	}
	return false, nil
}

func sha1Sum(r io.ReadSeeker) (string, error) {
	h := sha1.New()
	_, err := io.Copy(h, r)
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func displayName(track tube.Track) string {
	return fmt.Sprintf("%s - %s - %s", track.Info.Artist, track.Info.Album, track.Info.Title)
}

func maybeDie(err error) {
	if err == nil {
		return
	}
	fmt.Println("error:", err.Error())
	os.Exit(1)
}
