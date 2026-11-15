package music

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"pikmelo-server/internal/online"
)

type onlineFixtureTransport func(*http.Request) (*http.Response, error)

func TestOnlineIntegrationReportsActualAudioWhenSourceDowngradesFLAC(t *testing.T) {
	a, admin, _ := onlineIntegrationFixture(t)
	source := onlineFixtureSource(t, a, admin)
	if err := readOnlineValue(a.store.db, onlineSourcePrefix+source.ID, &source); err != nil {
		t.Fatal(err)
	}
	capability := source.Sources["kw"]
	capability.Qualities = append(capability.Qualities, "flac")
	source.Sources["kw"] = capability
	if err := a.store.transaction(func(tx *sql.Tx) error { return saveOnlineValue(tx, onlineSourcePrefix+source.ID, source) }); err != nil {
		t.Fatal(err)
	}
	response := onlineFixtureRequest(t, a, admin, "POST", "jobs", map[string]any{"source_id": source.ID, "quality": "flac", "item": online.Item{Platform: "kw", ID: "fixture", Title: "Fixture quality", MusicInfo: map[string]any{"songmid": "fixture"}}}, 202)
	var job onlineJob
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	job = onlineAwaitFixtureJob(t, a, job.ID, "completed")
	if job.Warning == "" || job.ActualFormat != "wav" || job.ActualBitrate == 0 {
		t.Fatal("source audio mismatch was hidden", job.Warning, job.ActualFormat)
	}
	view := onlineFixtureRequest(t, a, admin, "GET", "jobs", nil, 200)
	if !strings.Contains(view.Body.String(), "actual_format") || !strings.Contains(view.Body.String(), "warning") {
		t.Fatal("actual format not exposed to task owner")
	}
}

func TestOnlineBrokerCapabilityWithInternalDockerHost(t *testing.T) {
	a, _ := testAPI(t)
	a.SetAuthConfig(AuthConfig{LANHTTP: true})
	client := &http.Client{Transport: onlineFixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})}
	server := httptest.NewServer(a)
	defer server.Close()
	a.store.online.broker = online.NewBroker(client, server.URL+"/api/v2/online/broker")
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Broker struct{ URL, Token string } }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		request, _ := http.NewRequest("POST", payload.Broker.URL, strings.NewReader(`{"url":"https://example.com/test","method":"GET"}`))
		request.Host = "pikmelo:8080"
		request.Header.Set("Authorization", "Bearer "+payload.Broker.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Errorf("capability broker rejected internal service host: %d", response.StatusCode)
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"sources":{"kw":{"type":"music","actions":["musicUrl"],"qualitys":["128k"]}}}`)
	}))
	defer worker.Close()
	if _, err := a.store.online.broker.Execute(context.Background(), worker.URL, "fixture", "init", nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v2/online/broker", "/api/v2/online/sources"} {
		request := httptest.NewRequest("POST", "http://pikmelo:8080"+path, strings.NewReader(`{}`))
		request.RemoteAddr = "172.22.0.3:12345"
		response := httptest.NewRecorder()
		a.ServeHTTP(response, request)
		expected := 401
		if path != "/api/v2/online/broker" {
			expected = 403
		}
		if response.Code != expected {
			t.Fatalf("path %s status %d expected %d", path, response.Code, expected)
		}
	}
}

func TestOnlineIntegrationCancelAfterReloginAndEmailPolicyChange(t *testing.T) {
	a, _, _ := onlineIntegrationFixture(t)
	user, oldSession, _ := loginTestAccount(t, a.store, "admin")
	auth := authContext{session: true, userID: user.ID, keyID: oldSession.ID, authVersion: user.AuthVersion, role: user.Role}
	task, err := a.store.createResume(auth, "cancel.wav", 1000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	job := onlineJob{ID: uuid.NewString(), Owner: user.ID, Actor: MutationOptions{UserID: user.ID, ActorType: "session", CredentialID: oldSession.ID}, ResumeID: task.ID, Status: "failed"}
	if err := a.store.transaction(func(tx *sql.Tx) error { return saveOnlineValue(tx, onlineJobPrefix+job.ID, job) }); err != nil {
		t.Fatal(err)
	}
	_, _, freshToken := loginTestAccount(t, a.store, "admin")
	if _, err := a.store.db.Exec("UPDATE sessions SET revoked_at='2026-11-15T00:00:00Z' WHERE id=?", oldSession.ID); err != nil {
		t.Fatal(err)
	}
	setEmailTestRequirement(t, a.store, user.ID, true)
	onlineFixtureRequest(t, a, freshToken, "POST", "jobs/"+job.ID+"/cancel", nil, 200)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.store.online.mu.Lock()
		_, active := a.store.online.active[job.ID]
		a.store.online.mu.Unlock()
		if !active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	restored, err := readResume(a.store.db, task.ID)
	if err != nil || restored.State != "canceled" {
		t.Fatal("cancel failed to release checkpoint after account policy change", err)
	}
	if _, err := os.Stat(filepath.Join(a.store.mediaRoot, filepath.FromSlash(resumeFilePath(task.ID)))); !os.IsNotExist(err) {
		t.Fatal("cancelled partial retained", err)
	}
}

func TestOnlineIntegrationUnknownContentLength(t *testing.T) {
	a, admin, _ := onlineIntegrationFixture(t)
	previous := a.store.online.client.Transport
	a.store.online.client.Transport = onlineFixtureTransport(func(r *http.Request) (*http.Response, error) {
		response, err := previous.RoundTrip(r)
		if err == nil && r.URL.Hostname() == "audio.example.test" {
			response.ContentLength = -1
		}
		return response, err
	})
	source := onlineFixtureSource(t, a, admin)
	job := onlineFixtureJob(t, a, admin, source.ID, "")
	completed := onlineAwaitFixtureJob(t, a, job.ID, "completed")
	track, ok := a.store.Track(completed.TrackID)
	if !ok || completed.Downloaded != track.Size || track.Size == 0 {
		t.Fatal("unknown-length download did not retain actual bytes")
	}
}

func (f onlineFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type onlineFixtureDownload struct {
	atomic.Bool
	entered chan struct{}
}

func onlineIntegrationFixture(t *testing.T) (*API, string, *onlineFixtureDownload) {
	t.Helper()
	api, _ := testAPI(t)
	_, _, adminToken := loginTestAccount(t, api.store, "admin")
	audio := validTestAudio(t)
	slow := &onlineFixtureDownload{entered: make(chan struct{}, 1)}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		var payload struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if payload.Action == "init" {
			_, _ = io.WriteString(w, `{"sources":{"kw":{"name":"Fixture platform","type":"music","actions":["musicUrl"],"qualitys":["128k"]}}}`)
		} else {
			_, _ = io.WriteString(w, `{"result":"https://audio.example.test/song.wav"}`)
		}
	}))
	t.Cleanup(worker.Close)
	client := &http.Client{Transport: onlineFixtureTransport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		mime := "application/json"
		headers := make(http.Header)
		switch r.URL.Hostname() {
		case "source.example.test":
			body = []byte("/**\n * @name Integration fixture\n * @version 1.0\n */\n// private-script-token\n")
			mime = "text/javascript"
		case "audio.example.test":
			if slow.Load() {
				select {
				case slow.entered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			body = audio
			mime = "audio/wav"
			headers.Set("ETag", `"fixture-audio"`)
		default:
			body = []byte(`{}`)
		}
		headers.Set("Content-Type", mime)
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	})}
	api.store.online.client = client
	api.store.online.broker = online.NewBroker(client, "http://unused.example.test/api/v2/online/broker")
	api.store.online.workerURL = worker.URL
	// End online workers before the fixture worker HTTP server is closed.
	t.Cleanup(api.store.online.close)
	return api, adminToken, slow
}

func onlineFixtureRequest(t *testing.T, api *API, token, method, path string, payload any, expected int) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
	}
	r := sessionRequest(api, token, method, "/api/v2/online/"+path, body)
	if r.Code != expected {
		t.Fatalf("%s %s: expected %d, got %d %s", method, path, expected, r.Code, r.Body.String())
	}
	return r
}

func onlineFixtureSource(t *testing.T, api *API, admin string) onlineSource {
	t.Helper()
	r := onlineFixtureRequest(t, api, admin, "POST", "sources", map[string]string{"url": "https://source.example.test/source.js?token=private-url-token"}, 201)
	var source onlineSource
	if err := json.Unmarshal(r.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if source.ID == "" || source.Name != "Integration fixture" || source.Version != "1.0" || !source.Enabled || len(source.Sources) != 1 || source.Script != "" {
		t.Fatalf("invalid imported source view: %+v", source)
	}
	return source
}

func onlineFixtureJob(t *testing.T, api *API, token, source, target string) onlineJob {
	t.Helper()
	payload := map[string]any{"source_id": source, "quality": "128k", "target_playlist_id": target,
		"item": online.Item{Platform: "kw", ID: "remote-one", Title: "Imported fixture", Artist: "Fixture artist", Album: "Fixture album", MusicInfo: map[string]any{"songmid": "remote-one"}}}
	r := onlineFixtureRequest(t, api, token, "POST", "jobs", payload, 202)
	var job onlineJob
	if err := json.Unmarshal(r.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatal("invalid created job", r.Body.String(), err)
	}
	return job
}

func onlineAwaitFixtureJob(t *testing.T, api *API, id, status string) onlineJob {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var job onlineJob
	for time.Now().Before(deadline) {
		if err := readOnlineValue(api.store.db, onlineJobPrefix+id, &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == status {
			return job
		}
		if job.Status == "failed" && status != "failed" {
			t.Fatalf("task failed before %s: %+v", status, job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task did not reach %s: %+v", status, job)
	return job
}

func TestOnlineIntegrationSourcePermissionsAndVisibility(t *testing.T) {
	api, admin, _ := onlineIntegrationFixture(t)
	_, member := readyMember(t, api, "online-member")
	onlineFixtureRequest(t, api, member, "POST", "sources", map[string]string{"url": "https://source.example.test/source.js"}, 403)
	source := onlineFixtureSource(t, api, admin)
	r := onlineFixtureRequest(t, api, member, "GET", "sources", nil, 200)
	if !strings.Contains(r.Body.String(), source.ID) || strings.Contains(r.Body.String(), "private-url-token") || strings.Contains(r.Body.String(), "private-script-token") || strings.Contains(r.Body.String(), `"url"`) || strings.Contains(r.Body.String(), `"script"`) {
		t.Fatal("member source view leaks script or credentials", r.Body.String())
	}
	onlineFixtureRequest(t, api, member, "PATCH", "sources/"+source.ID, map[string]bool{"enabled": false}, 403)
	key := communityKey(t, api, member, "library")
	if response := communityPlayer(api, key, "online/sources"); response.Code != 403 {
		t.Fatalf("player key accessed online management: %d %s", response.Code, response.Body.String())
	}
	onlineFixtureRequest(t, api, admin, "PATCH", "sources/"+source.ID, map[string]bool{"enabled": false}, 200)
	r = onlineFixtureRequest(t, api, member, "GET", "sources", nil, 200)
	if strings.Contains(r.Body.String(), source.ID) {
		t.Fatal("disabled source still exposed to member")
	}
	onlineFixtureRequest(t, api, admin, "DELETE", "sources/"+source.ID, nil, 200)
}

func TestOnlineIntegrationDownloadCommitPlaylistAndDuplicate(t *testing.T) {
	api, admin, _ := onlineIntegrationFixture(t)
	member, token := readyMember(t, api, "online-downloader")
	_, stranger := readyMember(t, api, "online-stranger")
	source := onlineFixtureSource(t, api, admin)
	target := Playlist{ID: uuid.NewString(), Name: "Download target", TrackIDs: []string{}}
	if err := api.store.PutPlaylist(target, MutationOptions{UserID: member.ID}); err != nil {
		t.Fatal(err)
	}
	first := onlineFixtureJob(t, api, token, source.ID, target.ID)
	first = onlineAwaitFixtureJob(t, api, first.ID, "completed")
	if first.TrackID == "" || first.Downloaded != int64(len(validTestAudio(t))) {
		t.Fatal("download did not retain bytes or track identity", first)
	}
	track, ok := api.store.Track(first.TrackID)
	if !ok || track.Title != "Imported fixture" || track.UploadedBy != member.ID || track.OwnerID != member.ID {
		t.Fatalf("wrong imported track: %+v", track)
	}
	playlist, _ := api.store.Playlist(target.ID)
	if len(playlist.TrackIDs) != 1 || playlist.TrackIDs[0] != first.TrackID {
		t.Fatal("download was not added to own playlist atomically")
	}
	r := onlineFixtureRequest(t, api, stranger, "GET", "jobs", nil, 200)
	if strings.Contains(r.Body.String(), first.ID) {
		t.Fatal("other account can inspect a download task")
	}
	onlineFixtureRequest(t, api, stranger, "POST", "jobs/"+first.ID+"/retry", nil, 404)
	second := onlineFixtureJob(t, api, token, source.ID, target.ID)
	second = onlineAwaitFixtureJob(t, api, second.ID, "completed")
	if second.TrackID != first.TrackID {
		t.Fatal("duplicate audio created another track")
	}
	playlist, _ = api.store.Playlist(target.ID)
	if len(playlist.TrackIDs) != 1 {
		t.Fatal("duplicate import repeated playlist membership")
	}
}

func TestOnlineIntegrationQuotaForeignTargetAndCancellation(t *testing.T) {
	api, admin, slow := onlineIntegrationFixture(t)
	member, token := readyMember(t, api, "online-limited")
	other, _ := readyMember(t, api, "online-target-owner")
	source := onlineFixtureSource(t, api, admin)
	target := Playlist{ID: uuid.NewString(), Name: "Foreign target", TrackIDs: []string{}}
	if err := api.store.PutPlaylist(target, MutationOptions{UserID: other.ID}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"source_id": source.ID, "quality": "128k", "target_playlist_id": target.ID,
		"item": online.Item{Platform: "kw", ID: "remote-one", Title: "Imported fixture", MusicInfo: map[string]any{"songmid": "remote-one"}}}
	onlineFixtureRequest(t, api, token, "POST", "jobs", payload, 404)
	adminUser, _, _ := loginTestAccount(t, api.store, "admin")
	if _, err := api.store.SetStorageQuota(member.ID, 100, 0, MutationOptions{UserID: adminUser.ID}); err != nil {
		t.Fatal(err)
	}
	limited := onlineFixtureJob(t, api, token, source.ID, "")
	onlineAwaitFixtureJob(t, api, limited.ID, "failed")
	if _, err := api.store.SetStorageQuota(member.ID, 0, 1, MutationOptions{UserID: adminUser.ID}); err != nil {
		t.Fatal(err)
	}
	slow.Store(true)
	onlineFixtureRequest(t, api, token, "POST", "jobs/"+limited.ID+"/retry", nil, 200)
	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retried download did not reach the remote HTTP endpoint")
	}
	onlineFixtureRequest(t, api, token, "POST", "jobs/"+limited.ID+"/cancel", nil, 200)
	onlineAwaitFixtureJob(t, api, limited.ID, "cancelled")
	deadline := time.Now().Add(5 * time.Second)
	for {
		api.store.online.mu.Lock()
		_, active := api.store.online.active[limited.ID]
		api.store.online.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled job kept a running worker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	slow.Store(false)
	onlineFixtureRequest(t, api, token, "POST", fmt.Sprintf("jobs/%s/retry", limited.ID), nil, 200)
	onlineAwaitFixtureJob(t, api, limited.ID, "completed")
}
