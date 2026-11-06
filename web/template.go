package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dustin/go-humanize"
	"github.com/fsnotify/fsnotify"

	"github.com/guregu/intertube/tube"
)

var templates *template.Template

func getTemplate(ctx context.Context, name string) *template.Template {
	t := templates.Lookup(name + ".gohtml")
	if t == nil {
		panic("no template: " + name)
	}

	t, err := t.Clone()
	if err != nil {
		panic(err)
	}
	t.Funcs(templateFuncs(ctx))
	return t
}

func templateFuncs(ctx context.Context) template.FuncMap {
	m := make(template.FuncMap)

	localizer := localizerFrom(ctx)
	lang := languageFrom(ctx)
	user, loggedIn := userFrom(ctx)

	m["render"] = renderFunc(ctx)
	m["stylesheet"] = renderCSSFunc(ctx, user.Theme)
	m["opts"] = func() tube.DisplayOptions { return user.Display }
	m["tr"] = translateFunc(localizer)
	m["tc"] = translateCountFunc(localizer)
	m["lang"] = func() string { return lang }
	m["path"] = func() string { return pathFrom(ctx) }
	m["hasprefix"] = strings.HasPrefix
	m["loggedin"] = func() bool { return loggedIn }
	m["me"] = func() tube.User { return user }
	m["isadmin"] = func() bool { return loggedIn && slices.Contains(AdminIDs, user.ID) }

	return m
}

func parseTemplates() *template.Template {
	assets, err := assetsDir()
	if err != nil {
		panic(err)
	}

	globs := []string{
		filepath.Join(assets, "templates", "*.gohtml"),
		filepath.Join(assets, "templates", "*.gojs"),
	}

	t := template.New("root").Funcs(template.FuncMap{
		"render":     renderFunc(context.Background()),
		"stylesheet": renderCSSFunc(context.Background(), "default"),
		"opts":       func() tube.DisplayOptions { return tube.DisplayOptions{} },

		"timestamp": func(t time.Time) template.HTML {
			dateFmt := "2006-01-02 15:04"
			rfc := t.Format(time.RFC3339)
			return template.HTML(
				fmt.Sprintf(`<time datetime="%s">%s</time>`, rfc, t.Format(dateFmt)))
		},
		"date": func(t time.Time) template.HTML {
			dateFmt := "2006-01-02"
			rfc := t.Format(time.RFC3339)
			return template.HTML(
				fmt.Sprintf(`<time datetime="%s">%s</time>`, rfc, t.Format(dateFmt)))
		},
		"erasedays":    eraseDays,
		"replicadelay": replicaDelayDays,
		"mmss": func(secs int) string {
			return fmt.Sprintf("%d:%02d", secs/60, secs%60)
		},
		"bespace": func(v []string) string {
			return strings.Join(v, " ")
		},
		"bytesize": func(size int64) string {
			return humanize.Bytes(uint64(max(size, 0)))
		},

		"tr":        translateFunc(defaultLocalizer),
		"tc":        translateCountFunc(defaultLocalizer),
		"lang":      func() string { return "en" },
		"path":      func() string { return "" },
		"hasprefix": strings.HasPrefix,
		"loggedin":  func() bool { return false },
		"me":        func() tube.User { return tube.User{} },
		"isadmin":   func() bool { return false },

		"ucfirst": ucfirst,
		"domain": func() string {
			return Domain
		},

		"blankzero": func(i int) string {
			if i == 0 {
				return ""
			}
			return strconv.Itoa(i)
		},
	})

	for _, glob := range globs {
		template.Must(t.ParseGlob(glob))
	}

	return t
}

func renderFunc(ctx context.Context) func(string, any) (template.HTML, error) {
	return func(name string, data any) (template.HTML, error) {
		target := getTemplate(ctx, name)
		if target == nil {
			return "", fmt.Errorf("render: missing template: %s", name)
		}
		var buf bytes.Buffer
		err := target.Execute(&buf, data)
		if err != nil {
			fmt.Println("ERR!!", err)
			return "", err
		}
		return template.HTML(buf.String()), nil
	}
}

func renderCSSFunc(ctx context.Context, active string) func(string, any) (template.CSS, error) {
	if active == "" || !validThemes[active] {
		active = "default"
	}
	return func(name string, data any) (template.CSS, error) {
		if name == "@" {
			name = active
		}
		name = "_style-" + name
		target := getTemplate(ctx, name)
		if target == nil {
			return "", fmt.Errorf("render: missing template: %s", name)
		}
		var buf bytes.Buffer
		err := target.Execute(&buf, data)
		if err != nil {
			fmt.Println("ERR!!", err)
			return "", err
		}
		return template.CSS(buf.String()), nil
	}
}

func WatchFiles() func() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			select {
			case ev := <-watcher.Events:
				log.Println("watch event:", ev)
				switch filepath.Ext(ev.Name) {
				case ".gohtml", ".gojs":
					log.Println("Reloading templates...", filepath.Base(ev.Name))
					templates = parseTemplates()
				case ".toml":
					log.Println("Reloading translations...", filepath.Base(ev.Name))
					loadTranslations()
				}
			case err := <-watcher.Errors:
				log.Println("watch error:", err)
			}
		}
	}()

	assets, err := assetsDir()
	if err != nil {
		panic(err)
	}
	if err := watcher.Add(filepath.Join(assets, "templates")); err != nil {
		panic(err)
	}
	if err := watcher.Add(filepath.Join(assets, "text")); err != nil {
		panic(err)
	}
	log.Println("Hot reloading enabled")
	return watcher.Close
}

var AssetsDir string

func assetsDir() (string, error) {
	if AssetsDir != "" {
		return AssetsDir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "assets"), nil
}

func ucfirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
