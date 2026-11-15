package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// AbortReadsOnCancel makes request-body reads fail promptly once the request
// context is cancelled.
//
// net/http does not interrupt a handler blocked reading the body when the
// request context is cancelled by the server (e.g. BaseContext cancelled on
// shutdown); only a client disconnect does. A stalled upload would therefore
// outlive shutdown. This middleware sets an immediate read deadline on the
// connection on cancellation, so the blocked Read returns an error.
//
// It must be the outermost middleware so that w is the server's own
// ResponseWriter, which supports SetReadDeadline.
func AbortReadsOnCancel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		fired := make(chan struct{})
		stop := context.AfterFunc(r.Context(), func() {
			defer close(fired)
			if err := rc.SetReadDeadline(time.Now()); err != nil && !errors.Is(err, http.ErrNotSupported) {
				log.Printf("abort body read: %s %q: %v", r.Method, r.URL.Path, err) //nolint:gosec // G706: %q escapes control chars
			}
		})
		defer func() {
			// If the callback already started, let it finish before the
			// connection can be reused; net/http resets the read deadline
			// when it reads the next request.
			if !stop() {
				<-fired
			}
		}()
		next.ServeHTTP(w, r)
	})
}
