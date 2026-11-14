package music

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"pikmelo-server/internal/online"
)

func onlineDecode(r *http.Request, value any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return errors.New("请求内容无效")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("请求只允许一个 JSON 对象")
	}
	return nil
}

func (o *onlineService) workerAvailable(ctx context.Context) bool {
	if o.workerURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(o.workerURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	client := http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect prohibited") }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == 200
}

func (a *API) onlineHTTP(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "/api/v2/online/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	if !a.requireAccount(w, r) {
		return true
	}
	o := a.store.online
	if o == nil {
		writeError(w, 503, "ONLINE_UNAVAILABLE", "在线模块不可用")
		return true
	}
	auth := authFromRequest(r)
	if err := validateAccountSession(a.store.db, auth); err != nil {
		writeError(w, 401, "AUTH_INVALID", "请重新登录")
		return true
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case path == "sources":
		a.onlineSources(w, r, o)
	case strings.HasPrefix(path, "sources/"):
		a.onlineSourceMutation(w, r, o, strings.TrimPrefix(path, "sources/"))
	case path == "search":
		if r.Method != "GET" {
			methodNotAllowed(w, r)
			return true
		}
		if o.workerURL == "" {
			writeError(w, 503, "ONLINE_WORKER_REQUIRED", "请配置在线音源 worker")
			return true
		}
		sources, err := onlineValues[onlineSource](a.store.db, onlineSourcePrefix)
		if err != nil {
			writeError(w, 503, "ONLINE_STORAGE", "读取音源失败")
			return true
		}
		platforms := []string{}
		requested := r.URL.Query().Get("platform")
		for _, platform := range []string{"kw", "kg", "tx", "wy", "mg"} {
			if requested != "" && requested != "all" && requested != platform {
				continue
			}
			for _, source := range sources {
				if _, ok := source.Sources[platform]; source.Enabled && ok {
					platforms = append(platforms, platform)
					break
				}
			}
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit == 0 {
			limit = 20
		}
		select {
		case o.searchSlots <- struct{}{}:
			defer func() { <-o.searchSlots }()
		default:
			writeError(w, 429, "ONLINE_BUSY", "在线搜索繁忙，请稍后重试")
			return true
		}
		result, err := online.Search(r.Context(), o.client, platforms, r.URL.Query().Get("q"), page, limit)
		if err != nil {
			writeError(w, 400, "ONLINE_SEARCH_INVALID", "请检查关键词、页码及可用平台")
			return true
		}
		if validateAccountSession(a.store.db, auth) != nil {
			writeError(w, 401, "AUTH_INVALID", "请重新登录")
			return true
		}
		writeJSON(w, 200, result)
	case path == "jobs":
		if r.Method == "GET" {
			jobs, err := onlineValues[onlineJob](a.store.db, onlineJobPrefix)
			if err != nil {
				writeError(w, 503, "ONLINE_STORAGE", "读取下载任务失败")
				return true
			}
			views := []map[string]any{}
			slices.SortFunc(jobs, func(a, b onlineJob) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
			for _, job := range jobs {
				if job.Owner == auth.userID && len(views) < 200 {
					views = append(views, publicOnlineJob(job))
				}
			}
			writeJSON(w, 200, map[string]any{"jobs": views})
		} else if r.Method == "POST" {
			var input struct {
				SourceID string      `json:"source_id"`
				Item     online.Item `json:"item"`
				Quality  string      `json:"quality"`
				Target   string      `json:"target_playlist_id"`
			}
			if err := onlineDecode(r, &input); err != nil {
				writeError(w, 400, "ONLINE_INPUT", err.Error())
				return true
			}
			job, err := o.createJob(auth, mutationOptions(r), input.SourceID, input.Item, input.Quality, input.Target)
			if err != nil {
				writeOnlineError(w, err)
				return true
			}
			writeJSON(w, 202, publicOnlineJob(job))
		} else {
			methodNotAllowed(w, r)
		}
	case strings.HasPrefix(path, "jobs/"):
		parts := strings.Split(strings.TrimPrefix(path, "jobs/"), "/")
		if len(parts) != 2 || !tagUUID(parts[0]) || (parts[1] != "cancel" && parts[1] != "retry") {
			writeError(w, 404, "NOT_FOUND", "任务不存在")
			return true
		}
		if r.Method != "POST" {
			methodNotAllowed(w, r)
			return true
		}
		job, err := o.changeJob(auth, mutationOptions(r), parts[0], parts[1])
		if err != nil {
			writeOnlineError(w, err)
			return true
		}
		writeJSON(w, 200, publicOnlineJob(job))
	default:
		writeError(w, 404, "NOT_FOUND", "在线接口不存在")
	}
	return true
}

func writeOnlineError(w http.ResponseWriter, err error) {
	if writeStorageError(w, err) {
		return
	}
	if errors.Is(err, errResourceHidden) || errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "NOT_FOUND", "资源不存在或无权操作")
		return
	}
	writeError(w, 409, "ONLINE_OPERATION", err.Error())
}

func (a *API) onlineSources(w http.ResponseWriter, r *http.Request, o *onlineService) {
	if r.Method == "GET" {
		sources, err := onlineValues[onlineSource](a.store.db, onlineSourcePrefix)
		if err != nil {
			writeError(w, 503, "ONLINE_STORAGE", "读取音源失败")
			return
		}
		views := []onlineSource{}
		for _, source := range sources {
			if authFromRequest(r).role == "admin" || source.Enabled {
				views = append(views, publicOnlineSource(source, authFromRequest(r).role == "admin"))
			}
		}
		writeJSON(w, 200, map[string]any{"sources": views, "worker_available": o.workerAvailable(r.Context())})
		return
	}
	if r.Method != "POST" {
		methodNotAllowed(w, r)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	if o.workerURL == "" {
		writeError(w, 503, "ONLINE_WORKER_REQUIRED", "请配置在线音源 worker")
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if onlineDecode(r, &input) != nil || len(input.URL) > 4096 {
		writeError(w, 400, "ONLINE_INPUT", "请输入有效的音源链接")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
	if err != nil {
		writeError(w, 400, "ONLINE_INPUT", "音源链接无效")
		return
	}
	response, err := o.client.Do(request)
	if err != nil {
		writeError(w, 502, "ONLINE_SOURCE_FETCH", "音源链接访问失败，仅支持公网 HTTP(S) 地址")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || response.StatusCode != 200 || len(body) > 512<<10 || len(body) == 0 || !utf8.Valid(body) {
		writeError(w, 400, "ONLINE_SOURCE_INVALID", "音源须为有效 UTF-8 脚本，最大 512 KiB")
		return
	}
	result, err := o.broker.Execute(ctx, o.workerURL, string(body), "init", nil)
	if err != nil {
		writeError(w, 502, "ONLINE_SOURCE_INIT", "音源初始化失败，请检查脚本兼容性和 worker 状态")
		return
	}
	var initialized struct {
		Sources map[string]onlineCapability `json:"sources"`
	}
	if json.Unmarshal(result, &initialized) != nil || len(initialized.Sources) == 0 || len(initialized.Sources) > 6 {
		writeError(w, 400, "ONLINE_SOURCE_INVALID", "音源未声明有效平台")
		return
	}
	for id, capability := range initialized.Sources {
		if !slices.Contains([]string{"kw", "kg", "tx", "wy", "mg", "local"}, id) || capability.Type != "music" || !slices.Contains(capability.Actions, "musicUrl") || len(capability.Qualities) > 4 {
			writeError(w, 400, "ONLINE_SOURCE_INVALID", "平台能力声明无效")
			return
		}
		for _, quality := range capability.Qualities {
			if !slices.Contains([]string{"128k", "320k", "flac", "flac24bit"}, quality) {
				writeError(w, 400, "ONLINE_SOURCE_INVALID", "音质声明无效")
				return
			}
		}
	}
	digest := sha256.Sum256(body)
	metadata := func(name string) string {
		match := regexp.MustCompile(`(?m)^\s*\*?\s*@` + name + `\s+([^\r\n]+)`).FindSubmatch(body)
		if len(match) == 2 {
			return strings.TrimSpace(string(match[1]))
		}
		return ""
	}
	source := onlineSource{ID: uuid.NewString(), Name: metadata("name"), Version: metadata("version"), URL: input.URL, Script: string(body), Digest: hex.EncodeToString(digest[:]), Enabled: true, Sources: initialized.Sources}
	if source.Name == "" || len(source.Name) > 256 || len(source.Version) > 128 {
		writeError(w, 400, "ONLINE_SOURCE_INVALID", "脚本缺少有效 @name 或元数据过长")
		return
	}
	err = a.store.transaction(func(tx *sql.Tx) error {
		if err := requireAdminActor(tx, []MutationOptions{mutationOptions(r)}); err != nil {
			return err
		}
		sources, err := onlineValues[onlineSource](tx, onlineSourcePrefix)
		if err != nil {
			return err
		}
		for _, existing := range sources {
			if existing.Digest == source.Digest {
				source = existing
				return nil
			}
		}
		if len(sources) >= 8 {
			return errors.New("最多保存 8 个音源，请先删除不用的音源")
		}
		if err := saveOnlineValue(tx, onlineSourcePrefix+source.ID, source); err != nil {
			return err
		}
		return auditMutation(tx, "online.source.import", source.ID, nil, map[string]string{"name": source.Name, "digest": source.Digest}, []MutationOptions{mutationOptions(r)})
	})
	if err != nil {
		writeOnlineError(w, err)
		return
	}
	writeJSON(w, 201, publicOnlineSource(source, true))
}

func (a *API) onlineSourceMutation(w http.ResponseWriter, r *http.Request, o *onlineService, id string) {
	if !tagUUID(id) {
		writeError(w, 404, "NOT_FOUND", "音源不存在")
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != "PATCH" && r.Method != "DELETE" {
		methodNotAllowed(w, r)
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if r.Method == "PATCH" && onlineDecode(r, &input) != nil {
		writeError(w, 400, "ONLINE_INPUT", "启停参数无效")
		return
	}
	err := a.store.transaction(func(tx *sql.Tx) error {
		if err := requireAdminActor(tx, []MutationOptions{mutationOptions(r)}); err != nil {
			return err
		}
		var source onlineSource
		if err := readOnlineValue(tx, onlineSourcePrefix+id, &source); err != nil {
			return err
		}
		if r.Method == "DELETE" {
			jobs, err := onlineValues[onlineJob](tx, onlineJobPrefix)
			if err != nil {
				return err
			}
			for _, job := range jobs {
				if job.SourceID == id && !onlineTerminal(job.Status) {
					return errors.New("请先取消使用该音源的下载任务")
				}
			}
			if _, err := tx.Exec("DELETE FROM meta WHERE key=?", onlineSourcePrefix+id); err != nil {
				return err
			}
		} else {
			source.Enabled = input.Enabled
			if err := saveOnlineValue(tx, onlineSourcePrefix+id, source); err != nil {
				return err
			}
		}
		return auditMutation(tx, "online.source.change", id, nil, map[string]any{"enabled": input.Enabled, "deleted": r.Method == "DELETE"}, []MutationOptions{mutationOptions(r)})
	})
	if err != nil {
		writeOnlineError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
