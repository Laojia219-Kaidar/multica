package handler

import (
	"errors"
	"net/http"
	"strconv"
)

// parseLimitOffset parses the shared list pagination query parameters.
// Missing values use limit=100 and offset=0. Limit is capped at 200; malformed
// or negative values fail closed instead of silently changing the query.
func parseLimitOffset(r *http.Request) (limit, offset int, err error) {
	limit, offset = 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n < 0 {
			return 0, 0, errors.New("invalid limit")
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n < 0 {
			return 0, 0, errors.New("invalid offset")
		}
		offset = n
	}
	return limit, offset, nil
}
