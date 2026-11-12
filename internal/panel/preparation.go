package panel

import "net/http"

// Limit peak memory and concurrent SSH probes before parsing large uploads.
func (a *App) limitPreparation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case a.preparationSlots <- struct{}{}:
			defer func() { <-a.preparationSlots }()
		default:
			w.Header().Set("Retry-After", "2")
			fail(w, 429, "Ya hay dos revisiones en preparación. Espera unos segundos y vuelve a intentar")
			return
		}
		next(w, r)
	}
}
