package bandwidth

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"golang.org/x/time/rate"
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("bandwidth", parseCaddyfile)
}

type Middleware struct {
	Limit     int    `json:"limit,omitempty"`
	LimitStr  string `json:"limit_str,omitempty"`
	limiter   *rate.Limiter
}

func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.bandwidth",
		New: func() caddy.Module { return new(Middleware) },
	}
}

func (m *Middleware) Provision(ctx caddy.Context) error {
	// If LimitStr is set (potentially containing placeholders), we'll resolve it at request time
	// If Limit is set directly, we can create the limiter now
	if m.Limit > 0 && m.LimitStr == "" {
		m.limiter = rate.NewLimiter(rate.Limit(m.Limit), m.Limit)
	}
	return nil
}

func (m Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	var limiter *rate.Limiter
	
	// If we have a static limiter, use it
	if m.limiter != nil {
		limiter = m.limiter
	} else if m.LimitStr != "" {
		// Resolve placeholder and create limiter per request
		repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
		limitStr := repl.ReplaceAll(m.LimitStr, "")
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			limiter = rate.NewLimiter(rate.Limit(limit), limit)
		}
	}
	
	if limiter != nil {
		w = &limitedResponseWriter{
			ResponseWriter: w,
			limiter:        limiter,
			r:              r,
		}
	}
	return next.ServeHTTP(w, r)
}

type limitedResponseWriter struct {
	http.ResponseWriter
	limiter *rate.Limiter
	r       *http.Request
}

func (l *limitedResponseWriter) Write(p []byte) (int, error) {
   total := 0
   for len(p) > 0 {
	   // Determine chunk size based on limiter burst (minimum 1)
	   chunk := l.limiter.Burst()
	   if chunk <= 0 {
		   chunk = 1
	   }
	   if len(p) < chunk {
		   chunk = len(p)
	   }
	   // Wait for permission to send this chunk
	   if err := l.limiter.WaitN(l.r.Context(), chunk); err != nil {
		   return total, err
	   }
	   // Write the chunk
	   n, err := l.ResponseWriter.Write(p[:chunk])
	   total += n
	   if err != nil {
		   return total, err
	   }
	   // Advance the buffer
	   p = p[chunk:]
   }
   return total, nil
}

// containsPlaceholders checks if the string contains Caddy placeholder syntax {key}
func containsPlaceholders(s string) bool {
	openIdx := strings.Index(s, "{")
	if openIdx == -1 {
		return false
	}
	closeIdx := strings.Index(s[openIdx+1:], "}")
	if closeIdx == -1 {
		return false
	}
	// Make sure there is content between the brackets
	return closeIdx > 0
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Middleware

	for h.Next() {
		for h.NextBlock(0) {
			switch h.Val() {
			case "limit":
				limitStr := h.RemainingArgs()
				if len(limitStr) != 1 {
					return nil, h.ArgErr()
				}
				
				// Check if the limit contains placeholders
				limitValue := limitStr[0]
				if containsPlaceholders(limitValue) {
					// Store as string for runtime resolution
					m.LimitStr = limitValue
				} else {
					// Parse as integer immediately
					var err error
					m.Limit, err = strconv.Atoi(limitValue)
					if err != nil {
						return nil, h.Errf("parsing limit value: %v", err)
					}
				}
			default:
				return nil, h.Errf("unrecognized parameter '%s'", h.Val())
			}
		}
	}

	return m, nil
}
