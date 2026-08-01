package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// logRequests records one line per request, including the ones that succeed —
// fail only ever sees failures, so without this the log shows what broke and
// never what the server actually did. It logs at Debug rather than Info so
// `--log-level debug` is the server's verbose mode and a default run stays
// quiet; failures still reach the log at Warn or Error through fail.
//
// The status comes from a wrapped ResponseWriter because net/http keeps no
// record of what a handler wrote.
func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(recorder, r)

		a.logger.DebugContext(r.Context(), "request completed",
			"method", r.Method,
			"url", r.URL.Path,
			"code", recorder.Status(),
			"bytes", recorder.BytesWritten(),
			"duration", time.Since(start).String(),
			"requestID", middleware.GetReqID(r.Context()),
		)
	})
}
