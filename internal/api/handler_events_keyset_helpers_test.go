package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// getList is extracted from upstream's internal/api/handler_lists_keyset_test.go
// (S2 of the keyset-cursor track: convoys/mail/sessions), which is not ported
// here — out of scope for the S3 events-only fix (ga-punx6/ga-jfl96) and its
// other cases (TestConvoyList*, TestMailList*, TestSessionList*) exercise
// endpoints this cherry-pick does not touch. handler_events_keyset_test.go
// (S3) uses only this one shared helper.
func getList(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
