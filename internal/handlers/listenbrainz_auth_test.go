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
	"regexp"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// submitListensCheckboxChecked reports whether the rendered connect form's
// submit_listens checkbox carries the checked attribute.
var submitListensCheckboxChecked = regexp.MustCompile(`<input[^>]*name="submit_listens"[^>]*\schecked`)

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

	// Re-connect WITHOUT the checkbox: the user explicitly unchecked the
	// pre-checked box (the GET form pre-fills the saved state, see
	// TestListenBrainzConnectForm_Regression_ReflectsSavedSubmitListens), so
	// absence means an intentional opt-out.
	w = httptest.NewRecorder()
	h.ListenBrainzConnect(w, prefsPostForm("/auth/listenbrainz/connect", u,
		url.Values{"token": {"tok2"}}))
	require.Equal(t, http.StatusSeeOther, w.Result().StatusCode)
	auth = listenBrainzAuthFor(t, client, u)
	require.NotNil(t, auth)
	assert.False(t, auth.SubmitListens, "an explicitly unchecked box disables submission")
}

// Regression: PR #55 adversarial review MINOR 3 — the connect form always
// rendered the submit_listens checkbox unchecked, so reconnecting (e.g. to
// rotate a token) silently reset the opt-in on save. The GET form must
// pre-check the checkbox from the saved auth state.
func TestListenBrainzConnectForm_Regression_ReflectsSavedSubmitListens(t *testing.T) {
	client, h := setupPrefsHandler(t)

	// Not connected: the checkbox renders unchecked (opt-in defaults OFF).
	u := createPrefsTestUser(t, client)
	w := httptest.NewRecorder()
	h.ListenBrainzConnectForm(w, prefsGet("/auth/listenbrainz/connect", u))
	require.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.False(t, submitListensCheckboxChecked.MatchString(w.Body.String()),
		"a fresh connect form must render the opt-in unchecked")

	// Connected with submit_listens enabled: the checkbox must render checked
	// so re-saving the form preserves the opt-in.
	_, err := client.ListenBrainzAuth.Create().
		SetUser(u).
		SetToken("tok").
		SetUsername("lb-user").
		SetSubmitListens(true).
		Save(context.Background())
	require.NoError(t, err)

	w = httptest.NewRecorder()
	h.ListenBrainzConnectForm(w, prefsGet("/auth/listenbrainz/connect", u))
	require.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.True(t, submitListensCheckboxChecked.MatchString(w.Body.String()),
		"the connect form must pre-check the opt-in from the saved auth state")
}

// Regression: PR #55 adversarial review MINOR 5 — disconnecting left
// submitted_to_listenbrainz_at stamps in place, so reconnecting a DIFFERENT
// ListenBrainz account would never receive the pre-existing history.
// Disconnect must clear all of the user's stamps (same-account reconnect
// resubmission is absorbed by ListenBrainz's server-side dedup) while leaving
// other users' stamps untouched.
func TestDisconnectListenBrainz_Regression_ClearsSubmittedFlags(t *testing.T) {
	client, h := setupPrefsHandler(t)
	ctx := context.Background()
	u := createPrefsTestUser(t, client)
	other := createPrefsTestUser(t, client)

	_, err := client.ListenBrainzAuth.Create().
		SetUser(u).
		SetToken("tok").
		SetUsername("lb-user").
		Save(ctx)
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seed := func(owner *ent.User, track string, playedAt time.Time, stamped bool) {
		builder := client.Listen.Create().
			SetUser(owner).
			SetTrackName(track).
			SetArtistName("Artist").
			SetAlbumName("Album").
			SetSource("spotify").
			SetPlayedAt(playedAt)
		if stamped {
			builder.SetSubmittedToListenbrainzAt(base.Add(time.Hour))
		}
		_, err := builder.Save(ctx)
		require.NoError(t, err)
	}
	seed(u, "Stamped A", base, true)
	seed(u, "Stamped B", base.Add(time.Minute), true)
	seed(u, "Unstamped", base.Add(2*time.Minute), false)
	seed(other, "Other User Stamped", base, true)

	w := httptest.NewRecorder()
	h.DisconnectListenBrainz(w, prefsPostForm("/preferences/listenbrainz/disconnect", u, url.Values{}))
	require.Equal(t, http.StatusOK, w.Result().StatusCode)

	// The auth row is gone.
	assert.Nil(t, listenBrainzAuthFor(t, client, u))

	// All of the user's stamps are cleared so a reconnect resubmits history.
	remaining, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.SubmittedToListenbrainzAtNotNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, remaining, "disconnect must clear the user's submission stamps")

	// The other user's stamps are untouched.
	otherRemaining, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(other.ID)), listen.SubmittedToListenbrainzAtNotNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, otherRemaining)
}
