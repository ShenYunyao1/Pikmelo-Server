package music

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"pikmelo-server/internal/online"
)

const onlineSourcePrefix = "online/source/"
const onlineJobPrefix = "online/job/"
const onlineStagePrefix = "online/stage/"

type onlineCapability struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Actions   []string `json:"actions"`
	Qualities []string `json:"qualitys"`
}

type onlineSource struct {
	ID      string                      `json:"id"`
	Name    string                      `json:"name"`
	Version string                      `json:"version"`
	URL     string                      `json:"url,omitempty"`
	Script  string                      `json:"script,omitempty"`
	Digest  string                      `json:"digest"`
	Enabled bool                        `json:"enabled"`
	Sources map[string]onlineCapability `json:"sources"`
}

func publicOnlineSource(source onlineSource, admin bool) onlineSource {
	source.Script = ""
	if !admin {
		source.URL = ""
	}
	return source
}

type onlineJob struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	Platform         string          `json:"platform"`
	Quality          string          `json:"quality"`
	Status           string          `json:"status"`
	Downloaded       int64           `json:"downloaded"`
	Total            int64           `json:"total"`
	Error            string          `json:"error"`
	TrackID          string          `json:"track_id"`
	CreatedAt        string          `json:"created_at"`
	SourceID         string          `json:"source_id"`
	TargetPlaylistID string          `json:"target_playlist_id"`
	Owner            string          `json:"owner"`
	Actor            MutationOptions `json:"actor"`
	Item             online.Item     `json:"item"`
	ResumeID         string          `json:"resume_id"`
	StageID          string          `json:"stage_id"`
	ETag             string          `json:"etag"`
}

func publicOnlineJob(job onlineJob) map[string]any {
	return map[string]any{"id": job.ID, "title": job.Title, "platform": job.Platform, "quality": job.Quality, "status": job.Status,
		"downloaded": job.Downloaded, "total": job.Total, "error": job.Error, "track_id": job.TrackID, "created_at": job.CreatedAt}
}

func readOnlineValue(reader sqlReader, key string, target any) error {
	var raw string
	if err := reader.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&raw); err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), target)
}

func saveOnlineValue(tx *sql.Tx, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(raw))
	return err
}

func onlineValues[T any](reader sqlReader, prefix string) ([]T, error) {
	return queryJSON[T](reader, "SELECT value FROM meta WHERE key LIKE ? ORDER BY key", prefix+"%")
}

type onlineService struct {
	store       *Store
	client      *http.Client
	broker      *online.Broker
	workerURL   string
	mu          sync.Mutex
	active      map[string]context.CancelFunc
	closed      bool
	workers     sync.WaitGroup
	slots       chan struct{}
	searchSlots chan struct{}
}

func (s *Store) initializeOnline() error {
	client := online.NewHTTPClient()
	brokerURL := os.Getenv("PIKMELO_ONLINE_BROKER_URL")
	if brokerURL == "" {
		brokerURL = "http://pikmelo:8080/api/v2/online/broker"
	}
	service := &onlineService{store: s, client: client, broker: online.NewBroker(client, brokerURL), workerURL: os.Getenv("PIKMELO_ONLINE_WORKER_URL"),
		active: map[string]context.CancelFunc{}, slots: make(chan struct{}, 2), searchSlots: make(chan struct{}, 4)}
	jobs, err := onlineValues[onlineJob](s.db, onlineJobPrefix)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if !onlineTerminal(job.Status) {
			job.Status = "failed"
			job.Error = "服务已重启，可重试以继续下载"
			if err := s.transaction(func(tx *sql.Tx) error { return saveOnlineValue(tx, onlineJobPrefix+job.ID, job) }); err != nil {
				return err
			}
		}
	}
	s.online = service
	return nil
}

func onlineTerminal(status string) bool {
	return slices.Contains([]string{"completed", "failed", "cancelled"}, status)
}

func (o *onlineService) close() {
	o.mu.Lock()
	o.closed = true
	for _, cancel := range o.active {
		cancel()
	}
	o.mu.Unlock()
	o.workers.Wait()
}

func (o *onlineService) readSource(id string) (onlineSource, error) {
	var source onlineSource
	err := readOnlineValue(o.store.db, onlineSourcePrefix+id, &source)
	if err != nil || !source.Enabled {
		return source, errors.New("音源已停用或删除")
	}
	return source, nil
}

// An online import completes in the same transaction as the ordinary upload
// receipt. Cancellation, playlist ownership and the job state are checked here.
func finalizeOnlineUpload(tx *sql.Tx, stage, owner, trackID string) error {
	var id string
	err := tx.QueryRow("SELECT value FROM meta WHERE key=?", onlineStagePrefix+stage).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var job onlineJob
	if err := readOnlineValue(tx, onlineJobPrefix+id, &job); err != nil {
		return err
	}
	if job.Owner != owner || job.StageID != stage || job.Status != "validating" {
		return errors.New("在线任务已取消或状态改变")
	}
	if job.Actor.UserID != "" {
		if err := checkStageUploadActor(tx, owner, []MutationOptions{job.Actor}); err != nil {
			return err
		}
		var source onlineSource
		if err := readOnlineValue(tx, onlineSourcePrefix+job.SourceID, &source); err != nil {
			return err
		}
		if !source.Enabled {
			return errors.New("音源已停用")
		}
	}
	if job.TargetPlaylistID != "" {
		p, err := readPlaylistFrom(tx, job.TargetPlaylistID)
		if err != nil {
			return err
		}
		if p.OwnerID != owner || p.System {
			return errResourceHidden
		}
		if !slices.Contains(p.TrackIDs, trackID) {
			p.TrackIDs = append(p.TrackIDs, trackID)
			p.Version++
			p.UpdatedAt = time.Now().UTC()
			if err := writePlaylist(tx, p); err != nil {
				return err
			}
			if err := recordChange(tx, "playlist", p.ID, false); err != nil {
				return err
			}
		}
	}
	job.TrackID = trackID
	job.Status = "completed"
	job.Error = ""
	if err := saveOnlineValue(tx, onlineJobPrefix+id, job); err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM meta WHERE key=?", onlineStagePrefix+stage)
	return err
}
