// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-049)
// Tests for the per-connection listen-submission opt-in: the connect-form
// checkbox and the provider-tile toggle endpoint. Submission must default OFF.
package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"spotter/ent"
	"spotter/ent/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenBrainzAuthFor(t *testing.T, client *ent.Client, u *ent.User) *ent.ListenBrainzAuth {
	t.Helper()
	refreshed, err := client.User.Query().
		Where(user.ID(u.ID)).
		WithListenbrainzAuth().
		Only(context.Background())
	require.NoError(t, err)
	return refreshed.Edges.ListenbrainzAuth
}

func TestToggleListenBrainzSubmitListens_EnableAndDisable(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)
	_, err := client.ListenBrainzAuth.Create().
		SetUser(u).
		SetToken("tok").
		SetUsername("lb-user").
		Save(context.Background())
	require.NoError(t, err)

	// Enable: checkbox present in the form.
	w := httptest.NewRecorder()
	h.ToggleListenBrainzSubmitListens(w, prefsPostForm(
		"/preferences/listenbrainz/submit-listens", u,
		url.Values{"submit_listens": {"on"}}))
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.True(t, listenBrainzAuthFor(t, client, u).SubmitListens)

	// Disable: an unchecked htmx checkbox submits no value at all.
	w = httptest.NewRecorder()
	h.ToggleListenBrainzSubmitListens(w, prefsPostForm(
		"/preferences/listenbrainz/submit-listens", u, url.Values{}))
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.False(t, listenBrainzAuthFor(t, client, u).SubmitListens)
}

func TestToggleListenBrainzSubmitListens_NotConnected(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	w := httptest.NewRecorder()
	h.ToggleListenBrainzSubmitListens(w, prefsPostForm(
		"/preferences/listenbrainz/submit-listens", u,
		url.Values{"submit_listens": {"on"}}))
	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

// The connect form's opt-in checkbox is persisted with the new connection and
// defaults OFF when absent.
func TestListenBrainzConnect_SubmitListensCheckbox(t *testing.T) {
	client, h := setupPrefsHandler(t)
	u := createPrefsTestUser(t, client)

	// Fake ListenBrainz validate-token endpoint that accepts everything.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "message": "Token valid.", "valid": true, "user_name": "lb-user",
		})
	}))
	defer server.Close()
	h.Config.ListenBrainz.APIURL = server.URL

	// Connect WITH the checkbox checked.
	w := httptest.NewRecorder()
	h.ListenBrainzConnect(w, prefsPostForm("/auth/listenbrainz/connect", u,
		url.Values{"token": {"tok"}, "submit_listens": {"on"}}))
	require.Equal(t, http.StatusSeeOther, w.Result().StatusCode)
	require.Equal(t, "/preferences/providers", w.Result().Header.Get("Location"))
	auth := listenBrainzAuthFor(t, client, u)
	require.NotNil(t, auth)
	assert.True(t, auth.SubmitListens)

	// Re-connect WITHOUT the checkbox: opt-in is cleared (default OFF).
	w = httptest.NewRecorder()
	h.ListenBrainzConnect(w, prefsPostForm("/auth/listenbrainz/connect", u,
		url.Values{"token": {"tok2"}}))
	require.Equal(t, http.StatusSeeOther, w.Result().StatusCode)
	auth = listenBrainzAuthFor(t, client, u)
	require.NotNil(t, auth)
	assert.False(t, auth.SubmitListens, "submission is opt-in and must default OFF")
}
