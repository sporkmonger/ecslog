package httpmw

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/sporkmonger/ecsevent"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(b)
	rw.size += size
	return size, err
}

// Unfortunately, we can't implement Flusher and Hijacker only if the parent
// response writer does.

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("ResponseWriter does not implement the Hijacker interface")
}

var (
	// This is a compile-time check to make sure our types correctly
	// implement the interface:
	// https://medium.com/@matryer/c167afed3aae
	_ http.ResponseWriter = &responseWriter{}
	_ http.Hijacker       = &responseWriter{}
	_ http.Flusher        = &responseWriter{}
)

// FromRequest gets a SpanMonitor from the request context.
//
// Middleware never puts a global monitor in a context. If needed,
// the global monitor can be obtained by asking the span monitor for
// its parent.
func FromRequest(r *http.Request) *ecsevent.SpanMonitor {
	monitor, ok := ecsevent.MonitorFromContext(r.Context()).(*ecsevent.SpanMonitor)
	if !ok {
		return nil
	}
	return monitor
}

// NewHandler uses a Monitor to inject SpanMonitors into request
// contexts.
func NewHandler(monitor ecsevent.Monitor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			span := ecsevent.NewSpanMonitorFromParent(monitor)
			fullURL := &url.URL{
				Host: r.Host,
			}
			if r.URL != nil {
				fullURL.Path = r.URL.Path
				fullURL.RawQuery = r.URL.RawQuery
			}
			if scheme := r.Header.Get("X-Forwarded-Proto"); scheme != "" {
				fullURL.Scheme = scheme
			}
			span.UpdateFields(map[string]interface{}{
				ecsevent.FieldHTTPRequestMethod:    r.Method,
				ecsevent.FieldHTTPRequestBodyBytes: int64(r.ContentLength),
				ecsevent.FieldHTTPVersion:          fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor),
				ecsevent.FieldECSVersion:           "1.0.1",
			})
			if r.RemoteAddr != "" {
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldClientIP: r.RemoteAddr,
				})
			}
			if r.Host != "" {
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldURLDomain: r.Host,
				})
			}
			if r.URL != nil {
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldURLOriginal: r.URL.String(),
					ecsevent.FieldURLFull:     fullURL.String(),
				})
				if r.URL.Path != "" {
					span.UpdateFields(map[string]interface{}{
						ecsevent.FieldURLPath: r.URL.Path,
					})
				}
				if r.URL.RawQuery != "" {
					span.UpdateFields(map[string]interface{}{
						ecsevent.FieldURLQuery: r.URL.RawQuery,
					})
				}
			}
			if ref := r.Header.Get("Referer"); ref != "" {
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldHTTPRequestReferrer: ref,
				})
			}
			if ua := r.Header.Get("User-Agent"); ua != "" {
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldUserAgentOriginal: ua,
				})
			}
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				ips := []string{}
				for _, rawIP := range strings.Split(xff, ",") {
					ip := net.ParseIP(rawIP)
					if ip != nil {
						ips = append(ips, ip.String())
					}
				}
				span.UpdateFields(map[string]interface{}{
					ecsevent.FieldRelatedIP: ips,
				})
			}

			r = r.WithContext(span.WithContext(r.Context()))
			// Record status and size, using 200 as our default status
			// Passes everything through to the parent response writer after
			// recording status and size.
			wrw := &responseWriter{
				ResponseWriter: w,
				status:         200,
			}
			next.ServeHTTP(wrw, r)
			span.UpdateFields(map[string]interface{}{
				ecsevent.FieldHTTPResponseStatusCode: wrw.status,
				ecsevent.FieldHTTPResponseBodyBytes:  int64(wrw.size),
			})
			span.Finish()
		})
	}
}