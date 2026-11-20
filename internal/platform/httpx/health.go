package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
)

// Check is one dependency's health. Name is stable; the acceptance test and the
// Kubernetes probes both key on it.
type Check struct {
	Name string
	// Probe returns nil when the dependency is usable. It must be cheap and it
	// must have its own timeout, because a readiness probe that hangs is worse
	// than one that fails.
	Probe func(r *http.Request) error
}

type healthResponse struct {
	Status string            `json:"status"`
	Build  buildinfo.Info    `json:"build"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Health serves liveness and readiness.
//
// Liveness answers "is this process wedged", so it must not touch dependencies:
// a Postgres blip should never cause Kubernetes to kill an otherwise fine pod.
// Readiness answers "should traffic come here", so it runs the checks.
func Health(checks ...Check) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{
			Status: "ok",
			Build:  buildinfo.Get(),
		})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		results := make(map[string]string, len(checks))
		status, code := "ok", http.StatusOK
		for _, c := range checks {
			if err := c.Probe(r); err != nil {
				results[c.Name] = err.Error()
				status, code = "degraded", http.StatusServiceUnavailable
				continue
			}
			results[c.Name] = "ok"
		}
		writeJSON(w, code, healthResponse{
			Status: status,
			Build:  buildinfo.Get(),
			Checks: results,
		})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
