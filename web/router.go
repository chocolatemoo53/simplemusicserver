package web

import (
	"context"
	"log"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
)

type handlerFunc func(context.Context, http.ResponseWriter, *http.Request)

type middlewareFunc func(context.Context, http.ResponseWriter, *http.Request) context.Context

type middleware struct {
	prefix string
	fn     middlewareFunc
}

var (
	mux         = http.NewServeMux()
	middlewares []middleware
)

func Handler() http.Handler {
	return mux
}

func use(prefix string, fn middlewareFunc) {
	middlewares = append(middlewares, middleware{prefix: prefix, fn: fn})
	sort.SliceStable(middlewares, func(i, j int) bool {
		return len(middlewares[i].prefix) < len(middlewares[j].prefix)
	})
}

func (m middleware) matches(path string) bool {
	if strings.HasSuffix(m.prefix, "/") {
		return strings.HasPrefix(path, m.prefix)
	}
	return path == m.prefix
}

func handle(pattern string, h handlerFunc) {
	mux.Handle(pattern, wrap(h))
}

func wrap(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		defer func() {
			if ex := recover(); ex != nil {
				if ex == http.ErrAbortHandler {
					panic(ex)
				}
				panicHandler(ctx, w, r, ex)
			}
		}()
		for _, m := range middlewares {
			if !m.matches(r.URL.Path) {
				continue
			}
			if ctx = m.fn(ctx, w, r); ctx == nil {
				return
			}
		}
		h(ctx, w, r)
	})
}

func panicHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, ex any) {
	if isSubsonicReq(r) {
		subsonicPanicHandler(ctx, w, r, ex)
		return
	}
	log.Println("Panic!", ex)
	debug.PrintStack()
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
