package handler

import (
	"net/http/httptest"
	"testing"
)

func TestParseLimitOffset(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
		wantErr    bool
	}{
		{name: "defaults", wantLimit: 100},
		{name: "explicit", query: "?limit=25&offset=50", wantLimit: 25, wantOffset: 50},
		{name: "limit capped", query: "?limit=999", wantLimit: 200},
		{name: "zero allowed", query: "?limit=0&offset=0", wantLimit: 0},
		{name: "negative limit", query: "?limit=-1", wantErr: true},
		{name: "negative offset", query: "?offset=-1", wantErr: true},
		{name: "invalid limit", query: "?limit=many", wantErr: true},
		{name: "invalid offset", query: "?offset=later", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/inbox"+tc.query, nil)
			limit, offset, err := parseLimitOffset(req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if limit != tc.wantLimit || offset != tc.wantOffset {
				t.Fatalf("got limit=%d offset=%d, want limit=%d offset=%d", limit, offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}
