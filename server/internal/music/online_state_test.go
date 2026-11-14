package music

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOnlineSourcePublicViewAndDisabledJobs(t *testing.T) {
	source := onlineSource{ID: "source", URL: "https://example.com/source?token=private", Script: "private script", Enabled: true}
	view := publicOnlineSource(source, false)
	raw, _ := json.Marshal(view)
	if string(raw) == "" || view.URL != "" || view.Script != "" {
		t.Fatal("source credentials exposed")
	}
	if publicOnlineSource(source, true).URL != source.URL || publicOnlineSource(source, true).Script != "" {
		t.Fatal("administrator view invalid")
	}
}

func TestOnlineRecoveryRetainsSourceAndRequiresExplicitRetry(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "library.sqlite")
	store, err := NewStore(data, root)
	if err != nil {
		t.Fatal(err)
	}
	source := onlineSource{ID: "retained-source", Name: "Retained", Script: "private-script", URL: "https://example.com/source.js", Enabled: true}
	job := onlineJob{ID: "interrupted", Owner: "fixture-owner", Status: "downloading", Downloaded: 123, ResumeID: "checkpoint"}
	if err := store.transaction(func(tx *sql.Tx) error {
		if err := saveOnlineValue(tx, onlineSourcePrefix+source.ID, source); err != nil {
			return err
		}
		return saveOnlineValue(tx, onlineJobPrefix+job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(data, root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var retained onlineSource
	if err := readOnlineValue(reopened.db, onlineSourcePrefix+source.ID, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Script != source.Script || retained.URL != source.URL {
		t.Fatal("source configuration lost")
	}
	if err := readOnlineValue(reopened.db, onlineJobPrefix+job.ID, &job); err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.ResumeID != "checkpoint" || job.Downloaded != 123 || len(reopened.online.active) != 0 {
		t.Fatal("unsafe or destructive recovery", job)
	}
}

func TestOnlineCompletionIsAtomicWithPlaylistAndReceipt(t *testing.T) {
	a, _ := testAPI(t)
	users, err := a.store.Users()
	if err != nil {
		t.Fatal(err)
	}
	owner := users[0]
	p := Playlist{ID: "online-target", Name: "Online target", TrackIDs: []string{}}
	if err := a.store.PutPlaylist(p, MutationOptions{UserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	tracks := a.store.Tracks()
	if len(tracks) == 0 {
		t.Fatal("missing fixture")
	}
	job := onlineJob{ID: "job", Owner: owner.ID, Status: "validating", StageID: "stage", TargetPlaylistID: p.ID}
	if err := a.store.transaction(func(tx *sql.Tx) error { return saveOnlineValue(tx, onlineJobPrefix+job.ID, job) }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec("INSERT INTO meta VALUES(?,?)", onlineStagePrefix+job.StageID, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.store.transaction(func(tx *sql.Tx) error { return recordUploadReceipt(tx, job.StageID, owner.ID, tracks[0].ID, false) }); err != nil {
		t.Fatal(err)
	}
	if err := readOnlineValue(a.store.db, onlineJobPrefix+job.ID, &job); err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || job.TrackID != tracks[0].ID {
		t.Fatal("job not completed", job.Status)
	}
	got, _ := a.store.Playlist(p.ID)
	if len(got.TrackIDs) != 1 || got.TrackIDs[0] != tracks[0].ID {
		t.Fatal("target not atomically updated")
	}
	job.ID = "cancelled-job"
	job.StageID = "cancelled-stage"
	job.Status = "cancelled"
	if err := a.store.transaction(func(tx *sql.Tx) error { return saveOnlineValue(tx, onlineJobPrefix+job.ID, job) }); err != nil {
		t.Fatal(err)
	}
	a.store.db.Exec("INSERT INTO meta VALUES(?,?)", onlineStagePrefix+job.StageID, job.ID)
	if err := a.store.transaction(func(tx *sql.Tx) error { return recordUploadReceipt(tx, job.StageID, owner.ID, tracks[0].ID, false) }); err == nil {
		t.Fatal("cancelled job committed")
	}
	var receipt bool
	a.store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)", uploadReceiptPrefix+job.StageID).Scan(&receipt)
	if receipt {
		t.Fatal("failed job retained receipt")
	}
}
