package music

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"pikmelo-server/internal/activity"
	"pikmelo-server/internal/online"
)

func (o *onlineService) validateJob(reader sqlReader, job onlineJob) error {
	if err := checkStageUploadActor(reader, job.Owner, []MutationOptions{job.Actor}); err != nil {
		return err
	}
	var source onlineSource
	if err := readOnlineValue(reader, onlineSourcePrefix+job.SourceID, &source); err != nil {
		return err
	}
	if !source.Enabled {
		return errors.New("音源已停用")
	}
	capability, ok := source.Sources[job.Platform]
	if !ok || !slices.Contains(capability.Actions, "musicUrl") || !slices.Contains(capability.Qualities, job.Quality) {
		return errors.New("音源不支持该平台或音质")
	}
	return validateResumeTarget(reader, job.Owner, &job.TargetPlaylistID)
}

func (o *onlineService) createJob(auth authContext, actor MutationOptions, source string, item online.Item, quality, target string) (onlineJob, error) {
	raw, err := json.Marshal(item)
	if err != nil || len(raw) > 32<<10 || item.ID == "" || len(item.ID) > 256 || item.Title == "" || len(item.Title) > 512 || len(item.Artist) > 1024 || len(item.Album) > 1024 || item.MusicInfo == nil {
		return onlineJob{}, errors.New("曲目信息无效或过长")
	}
	if o.workerURL == "" {
		return onlineJob{}, errors.New("请配置在线音源 worker")
	}
	job := onlineJob{ID: uuid.NewString(), Owner: auth.userID, Actor: actor, SourceID: source, Item: item, Title: item.Title, Platform: item.Platform,
		Quality: quality, TargetPlaylistID: target, Status: "queued", CreatedAt: persistedTime(time.Now())}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return job, errors.New("服务器正在停止")
	}
	err = o.store.transaction(func(tx *sql.Tx) error {
		if err := o.validateJob(tx, job); err != nil {
			return err
		}
		jobs, err := onlineValues[onlineJob](tx, onlineJobPrefix)
		if err != nil {
			return err
		}
		own, global := 0, 0
		slices.SortFunc(jobs, func(a, b onlineJob) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
		retained := 0
		for _, existing := range jobs {
			if !onlineTerminal(existing.Status) {
				global++
				if existing.Owner == job.Owner {
					own++
				}
			}
			if existing.Owner == job.Owner && onlineTerminal(existing.Status) {
				retained++
				if retained >= 199 {
					if _, err := tx.Exec("DELETE FROM meta WHERE key=?", onlineJobPrefix+existing.ID); err != nil {
						return err
					}
				}
			}
			if existing.Owner == job.Owner && !onlineTerminal(existing.Status) && existing.SourceID == source && existing.Item.ID == item.ID && existing.Platform == item.Platform && existing.Quality == quality && existing.TargetPlaylistID == target {
				return errors.New("相同曲目正在导入，请查看下载任务")
			}
		}
		if own >= 16 || global >= 256 {
			return errors.New("下载任务已达上限，请先完成或取消旧任务")
		}
		return saveOnlineValue(tx, onlineJobPrefix+job.ID, job)
	})
	if err == nil {
		o.startLocked(job.ID)
	}
	return job, err
}

func (o *onlineService) changeJob(auth authContext, actor MutationOptions, id, action string) (onlineJob, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var job onlineJob
	if o.closed {
		return job, errors.New("服务器正在停止")
	}
	err := o.store.transaction(func(tx *sql.Tx) error {
		if err := validateAccountSession(tx, auth); err != nil {
			return errResourceHidden
		}
		if err := readOnlineValue(tx, onlineJobPrefix+id, &job); err != nil {
			return err
		}
		if job.Owner != auth.userID {
			return errResourceHidden
		}
		if job.Status == "completed" {
			return errors.New("任务已完成")
		}
		if action == "cancel" {
			job.Actor = actor
			job.Status = "cancelled"
			job.Error = ""
		} else {
			if _, active := o.active[id]; active || !onlineTerminal(job.Status) {
				return errors.New("任务仍在处理中")
			}
			job.Actor = actor
			if err := o.validateJob(tx, job); err != nil {
				return err
			}
			job.Status = "queued"
			job.Error = ""
			job.ActualFormat = ""
			job.ActualBitrate = 0
			job.Warning = ""
		}
		return saveOnlineValue(tx, onlineJobPrefix+id, job)
	})
	if err != nil {
		return job, err
	}
	if action == "retry" {
		o.startLocked(id)
	} else if cancel := o.active[id]; cancel != nil {
		cancel()
	} else {
		o.active[job.ID] = func() {}
		o.workers.Add(1)
		go func() {
			defer o.workers.Done()
			defer func() { o.mu.Lock(); delete(o.active, job.ID); o.mu.Unlock() }()
			o.cleanupCancelled(job)
		}()
	}
	return job, nil
}

// Caller holds mu, which also serializes shutdown with WaitGroup admission.
func (o *onlineService) startLocked(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	o.active[id] = cancel
	o.workers.Add(1)
	go func() {
		defer o.workers.Done()
		defer func() { cancel(); o.mu.Lock(); delete(o.active, id); o.mu.Unlock() }()
		defer func() {
			var job onlineJob
			if readOnlineValue(o.store.db, onlineJobPrefix+id, &job) == nil && (job.Status == "completed" || job.Status == "cancelled") {
				o.cleanupCancelled(job)
			}
		}()
		select {
		case o.slots <- struct{}{}:
			defer func() { <-o.slots }()
		case <-ctx.Done():
			o.failJob(id, ctx.Err())
			return
		}
		err := o.runJob(ctx, id)
		if err != nil {
			o.failJob(id, err)
		}
	}()
}

func (o *onlineService) updateJob(id string, update func(*onlineJob) error) error {
	return o.store.transaction(func(tx *sql.Tx) error {
		var job onlineJob
		if err := readOnlineValue(tx, onlineJobPrefix+id, &job); err != nil {
			return err
		}
		if job.Status == "cancelled" || job.Status == "completed" {
			return context.Canceled
		}
		if err := update(&job); err != nil {
			return err
		}
		return saveOnlineValue(tx, onlineJobPrefix+id, job)
	})
}

func (o *onlineService) failJob(id string, cause error) {
	var failed onlineJob
	_ = readOnlineValue(o.store.db, onlineJobPrefix+id, &failed)
	message := "下载或校验失败，可重试；详情请查看系统日志"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		message = "任务已中断，可重试继续"
	}
	if errors.Is(cause, errResourceHidden) || errors.Is(cause, errUploadSession) {
		message = "账户或歌单权限已变更，请重新登录后重试"
	}
	if errors.Is(cause, errUploadTemporaryLimit) || errors.Is(cause, errResumeLimit) {
		message = "临时容量或任务额度不足，请完成或取消其他任务"
	}
	_ = o.updateJob(id, func(job *onlineJob) error { job.Status = "failed"; job.Error = message; return nil })
	_ = o.store.activity.Emit("online.download.failed", activity.Fields{Reason: "ONLINE_" + strings.ToUpper(failed.Status), UserID: failed.Owner, RequestID: id})
}

func (o *onlineService) jobIdentity(job onlineJob) (authContext, error) {
	user, err := readUser(o.store.db, job.Owner)
	if err != nil {
		return authContext{}, errResourceHidden
	}
	auth := authContext{session: true, userID: user.ID, keyID: job.Actor.CredentialID, authVersion: user.AuthVersion, role: user.Role, username: user.Username}
	return auth, validateAccountSession(o.store.db, auth)
}

func (o *onlineService) jobAuth(job onlineJob) (authContext, error) {
	auth, err := o.jobIdentity(job)
	if err != nil {
		return auth, err
	}
	return auth, checkResumeUploadActor(o.store.db, auth)
}

func (o *onlineService) cleanupCancelled(job onlineJob) {
	if job.ResumeID == "" {
		return
	}
	auth, err := o.jobIdentity(job)
	if err != nil {
		return
	}
	_ = o.cancelCheckpoint(auth, job.ResumeID)
}

func (o *onlineService) readCheckpoint(auth authContext, id string) (resumableUpload, error) {
	lock := resumableLock(id)
	lock.Lock()
	defer lock.Unlock()
	return o.store.resumeOwned(id, auth)
}

func (o *onlineService) cancelCheckpoint(auth authContext, id string) error {
	lock := resumableLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err := readResume(o.store.db, id)
	if err != nil {
		return err
	}
	if task.OwnerID != auth.userID {
		return errResourceHidden
	}
	if err := o.store.cancelResume(auth, task); err != nil {
		return err
	}
	stageID := task.StageID
	if stageID == "" {
		stageID = task.ID
	}
	_, err = o.store.db.Exec("DELETE FROM meta WHERE key=?", onlineStagePrefix+stageID)
	return err
}

func (o *onlineService) runJob(ctx context.Context, id string) error {
	var job onlineJob
	if err := readOnlineValue(o.store.db, onlineJobPrefix+id, &job); err != nil {
		return err
	}
	if err := o.validateJob(o.store.db, job); err != nil {
		return err
	}
	_ = o.store.activity.Emit("online.download.started", activity.Fields{UserID: job.Owner, RequestID: id})
	auth, err := o.jobAuth(job)
	if err != nil {
		return err
	}
	if job.ResumeID != "" {
		task, e := o.readCheckpoint(auth, job.ResumeID)
		if e == nil && task.State == "ready" && task.Preview != nil && time.Now().Before(task.Preview.ExpiresAt) {
			return o.finishJob(ctx, job, auth, task)
		}
		if e != nil || task.State != "uploading" {
			if err := o.cancelCheckpoint(auth, job.ResumeID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := o.updateJob(id, func(current *onlineJob) error {
				current.ResumeID = ""
				current.StageID = ""
				current.ETag = ""
				current.Downloaded = 0
				return nil
			}); err != nil {
				return err
			}
			job.ResumeID = ""
			job.StageID = ""
			job.ETag = ""
		}
	}
	if err := o.updateJob(id, func(job *onlineJob) error { job.Status = "resolving"; return nil }); err != nil {
		return err
	}
	source, err := o.readSource(job.SourceID)
	if err != nil {
		return err
	}
	raw, err := o.broker.Execute(ctx, o.workerURL, source.Script, "request", map[string]any{"source": job.Platform, "action": "musicUrl", "info": map[string]any{"type": job.Quality, "musicInfo": job.Item.MusicInfo}})
	if err != nil {
		return err
	}
	var resolved struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(raw, &resolved) != nil || resolved.Result == "" || len(resolved.Result) > 8192 {
		return errors.New("invalid music URL")
	}
	task, err := o.receiveJob(ctx, &job, auth, resolved.Result)
	if err != nil {
		return err
	}
	return o.finishJob(ctx, job, auth, task)
}

func onlineAudioName(quality string, response *http.Response) string {
	ext := strings.ToLower(filepath.Ext(response.Request.URL.Path))
	if !audioExtensions[ext] {
		types := map[string]string{"audio/flac": ".flac", "audio/x-flac": ".flac", "audio/wav": ".wav", "audio/x-wav": ".wav", "audio/mp4": ".m4a", "audio/aac": ".aac", "audio/ogg": ".ogg", "audio/opus": ".opus", "audio/mpeg": ".mp3"}
		ext = types[strings.Split(response.Header.Get("Content-Type"), ";")[0]]
		if ext == "" {
			ext = ".mp3"
			if strings.HasPrefix(quality, "flac") {
				ext = ".flac"
			}
		}
	}
	return "online" + ext
}

func (o *onlineService) receiveJob(ctx context.Context, job *onlineJob, auth authContext, address string) (resumableUpload, error) {
	var task resumableUpload
	if job.ResumeID != "" {
		task, _ = o.readCheckpoint(auth, job.ResumeID)
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return task, errors.New("invalid music URL")
	}
	request.Header.Set("Accept-Encoding", "identity")
	if task.ID != "" && task.Offset > 0 && job.ETag != "" && !strings.HasPrefix(job.ETag, "W/") {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", task.Offset))
		request.Header.Set("If-Range", job.ETag)
	}
	response, err := o.client.Do(request)
	if err != nil {
		return task, errors.New("audio endpoint connection failed")
	}
	defer func() { response.Body.Close() }()
	continued := task.ID != "" && task.Offset > 0 && response.StatusCode == 206 && job.ETag != "" && response.Header.Get("ETag") == job.ETag && response.Header.Get("Content-Range") == fmt.Sprintf("bytes %d-%d/%d", task.Offset, task.Size-1, task.Size)
	if task.ID != "" && !continued {
		if err := o.cancelCheckpoint(auth, task.ID); err != nil {
			return task, err
		}
		task = resumableUpload{}
		if response.StatusCode == 206 {
			response.Body.Close()
			request.Header.Del("Range")
			request.Header.Del("If-Range")
			response, err = o.client.Do(request)
			if err != nil {
				return task, errors.New("audio endpoint connection failed")
			}
		}
	}
	if response.StatusCode != 200 && !continued {
		return task, errors.New("audio endpoint unavailable")
	}
	max := o.store.Settings().MaxUploadBytes
	if max <= 0 {
		return task, errors.New("invalid upload limit")
	}
	unknown := response.ContentLength < 0
	if task.ID == "" {
		size := response.ContentLength
		if unknown {
			size = min(max, int64(64<<20))
		}
		if size <= 0 || size > max {
			return task, errors.New("audio exceeds upload limit or is empty")
		}
		fields := map[string]string{"title": job.Item.Title, "artist": job.Item.Artist, "album": job.Item.Album}
		task, err = o.store.createResume(auth, onlineAudioName(job.Quality, response), size, fields, nil, &job.TargetPlaylistID)
		if err != nil {
			return task, err
		}
		job.ResumeID = task.ID
		job.ETag = response.Header.Get("ETag")
		if len(job.ETag) > 512 {
			job.ETag = ""
		}
		if err := o.updateJob(job.ID, func(current *onlineJob) error { current.ResumeID = task.ID; current.ETag = job.ETag; return nil }); err != nil {
			_ = o.cancelCheckpoint(auth, task.ID)
			return task, err
		}
	}
	if err := o.updateJob(job.ID, func(current *onlineJob) error {
		current.Status = "downloading"
		current.Downloaded = task.Offset
		current.Total = task.Size
		if unknown {
			current.Total = 0
		}
		return nil
	}); err != nil {
		return task, err
	}
	buffer := make([]byte, 256<<10)
	for task.Offset < task.Size {
		if err := ctx.Err(); err != nil {
			return task, err
		}
		if err := o.validateJob(o.store.db, *job); err != nil {
			return task, err
		}
		count, readErr := io.ReadFull(response.Body, buffer[:min(int64(len(buffer)), task.Size-task.Offset)])
		if count > 0 {
			digest := sha256.Sum256(buffer[:count])
			lock := resumableLock(task.ID)
			lock.Lock()
			current, readErr := o.store.resumeOwned(task.ID, auth)
			if readErr != nil {
				err = readErr
			} else if current.Offset != task.Offset || current.State != "uploading" {
				err = errResumeConflict
			} else {
				task, err = o.store.patchResume(ctx, auth, current, current.Offset, hex.EncodeToString(digest[:]), bytes.NewReader(buffer[:count]))
			}
			lock.Unlock()
			if err != nil {
				return task, err
			}
			if err := o.updateJob(job.ID, func(current *onlineJob) error { current.Downloaded = task.Offset; return nil }); err != nil {
				return task, err
			}
		}
		if readErr != nil {
			if unknown && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
				break
			}
			return task, readErr
		}
	}
	var extra [1]byte
	if count, err := response.Body.Read(extra[:]); count != 0 || err != io.EOF {
		return task, errors.New("audio response length does not match")
	}
	if task.Offset == 0 {
		return task, errors.New("empty audio response")
	}
	if unknown {
		task.Size = task.Offset
		lock := resumableLock(task.ID)
		lock.Lock()
		err := o.store.transaction(func(tx *sql.Tx) error {
			if err := checkResumeUploadActor(tx, auth); err != nil {
				return err
			}
			current, err := readResume(tx, task.ID)
			if err != nil {
				return err
			}
			if current.State != "uploading" || current.Offset != task.Offset {
				return errResumeConflict
			}
			current.Size = task.Offset
			task = current
			return saveResume(tx, current, o.store.temporaryLimits())
		})
		lock.Unlock()
		if err != nil {
			return task, err
		}
	}
	return task, nil
}

func (o *onlineService) optionalAssets(ctx context.Context, item online.Item) resumableFinish {
	input := resumableFinish{CoverSource: "external"}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if lyric, err := online.Lyrics(ctx, o.client, item); err == nil {
		input.Lyrics = []byte(lyric)
	}
	if item.CoverURL != "" {
		request, err := http.NewRequestWithContext(ctx, "GET", item.CoverURL, nil)
		if err == nil {
			response, err := o.client.Do(request)
			if err == nil {
				defer response.Body.Close()
				body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
				if err == nil && response.StatusCode == 200 && len(body) <= 8<<20 {
					if _, _, ok := validateCover(body, ""); ok {
						input.Cover = body
					}
				}
			}
		}
	}
	return input
}

func (o *onlineService) finishJob(ctx context.Context, job onlineJob, auth authContext, task resumableUpload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.updateJob(job.ID, func(current *onlineJob) error { current.Status = "validating"; return nil }); err != nil {
		return err
	}
	input := o.optionalAssets(ctx, job.Item)
	if len(input.Lyrics) == 0 || (job.Item.CoverURL != "" && len(input.Cover) == 0) {
		_ = o.store.activity.Emit("online.assets.partial", activity.Fields{UserID: job.Owner, RequestID: job.ID, Reason: "OPTIONAL_ASSETS_UNAVAILABLE"})
	}
	lock := resumableLock(task.ID)
	lock.Lock()
	defer lock.Unlock()
	current, err := o.store.resumeOwned(task.ID, auth)
	if err != nil {
		return err
	}
	task = current
	preview, err := o.store.finishResume(ctx, auth, task, input, job.Actor)
	if err != nil {
		return err
	}
	if err := o.store.transaction(func(tx *sql.Tx) error {
		var current onlineJob
		if err := readOnlineValue(tx, onlineJobPrefix+job.ID, &current); err != nil {
			return err
		}
		if current.Status != "validating" {
			return context.Canceled
		}
		if err := o.validateJob(tx, current); err != nil {
			return err
		}
		current.StageID = preview.ID
		current.ActualFormat = preview.Track.Format
		current.ActualBitrate = preview.Track.BitrateKbps
		current.Warning = ""
		if strings.HasPrefix(current.Quality, "flac") && preview.Track.Format != "flac" {
			current.Warning = "音源返回的实际格式为 " + strings.ToUpper(preview.Track.Format) + "，与请求的 FLAC 不一致。"
		} else if current.Quality == "flac24bit" && preview.Track.BitsPerSample < 24 {
			current.Warning = "音源返回的音频未确认为 24 位，请以曲目实际信息为准。"
		}
		if err := saveOnlineValue(tx, onlineJobPrefix+job.ID, current); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", onlineStagePrefix+preview.ID, job.ID)
		return err
	}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, err = o.store.CommitUploadStage(preview.ID, job.Actor)
	if err == nil {
		_ = o.store.activity.Emit("online.download.completed", activity.Fields{UserID: job.Owner})
	}
	return err
}
