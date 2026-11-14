package online

// Platform request/metadata compatibility follows LX Music Desktop at
// 323e4bac505da428adae8db6b58039fb2b36b86d, src/renderer/utils/musicSdk.
// LX Music Desktop is distributed under Apache-2.0.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Item struct {
	Platform  string         `json:"platform"`
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Artist    string         `json:"artist"`
	Album     string         `json:"album"`
	Duration  float64        `json:"duration"`
	CoverURL  string         `json:"cover_url"`
	Qualities []string       `json:"qualities"`
	MusicInfo map[string]any `json:"music_info"`
}

type PlatformResult struct {
	Platform string `json:"platform"`
	Error    string `json:"error,omitempty"`
	HasMore  bool   `json:"has_more"`
}

type SearchResult struct {
	Items     []Item           `json:"items"`
	Platforms []PlatformResult `json:"platforms"`
	HasMore   bool             `json:"has_more"`
}

// Search aggregates only explicitly selected platforms. A partial upstream
// failure is represented in Platforms and never turns into a successful empty page.
func Search(ctx context.Context, client *http.Client, platforms []string, query string, page, limit int) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if client == nil || query == "" || utf8.RuneCountInString(query) > 200 || page < 1 || page > 1000 || limit < 1 || limit > 50 || len(platforms) == 0 || len(platforms) > 5 {
		return SearchResult{}, errors.New("invalid online search parameters")
	}
	seen := map[string]bool{}
	for _, platform := range platforms {
		if seen[platform] || !strings.Contains("|kw|kg|tx|wy|mg|", "|"+platform+"|") {
			return SearchResult{}, errors.New("unsupported or repeated online platform")
		}
		seen[platform] = true
	}
	type pageResult struct {
		items []Item
		more  bool
		err   error
	}
	pages := make([]pageResult, len(platforms))
	var group sync.WaitGroup
	for index, platform := range platforms {
		group.Add(1)
		go func() {
			defer group.Done()
			requestContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			pages[index].items, pages[index].more, pages[index].err = searchPlatform(requestContext, client, platform, query, page, limit)
		}()
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Items: []Item{}, Platforms: []PlatformResult{}}
	for index, platform := range platforms {
		status := PlatformResult{Platform: platform, HasMore: pages[index].more}
		if pages[index].err != nil {
			status.Error = "平台搜索暂时不可用"
		} else {
			result.Items = append(result.Items, pages[index].items...)
		}
		result.Platforms = append(result.Platforms, status)
		result.HasMore = result.HasMore || status.HasMore
	}
	needle := strings.ToLower(query)
	score := func(item Item) int {
		if strings.ToLower(item.Title) == needle {
			return 2
		}
		if strings.Contains(strings.ToLower(item.Title+" "+item.Artist), needle) {
			return 1
		}
		return 0
	}
	sort.SliceStable(result.Items, func(i, j int) bool { return score(result.Items[i]) > score(result.Items[j]) })
	return result, nil
}

func searchPlatform(ctx context.Context, client *http.Client, platform, query string, page, limit int) ([]Item, bool, error) {
	request, err := platformRequest(ctx, platform, query, page, limit)
	if err != nil {
		return nil, false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("platform HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > 4*1024*1024 {
		return nil, false, errors.New("platform search response too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, false, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false, errors.New("invalid trailing platform response")
	}
	return parsePlatform(platform, payload, page, limit)
}

func platformRequest(ctx context.Context, platform, query string, page, limit int) (*http.Request, error) {
	parameters := url.Values{}
	method, address, body := http.MethodGet, "", ""
	switch platform {
	case "kw":
		address = "http://search.kuwo.cn/r.s"
		parameters = url.Values{"client": {"kt"}, "all": {query}, "pn": {strconv.Itoa(page - 1)}, "rn": {strconv.Itoa(limit)}, "uid": {"794762570"}, "ver": {"kwplayer_ar_9.2.2.1"}, "vipver": {"1"}, "show_copyright_off": {"1"}, "newver": {"1"}, "ft": {"music"}, "cluster": {"0"}, "strategy": {"2012"}, "encoding": {"utf8"}, "rformat": {"json"}, "vermerge": {"1"}, "mobi": {"1"}, "issubtitle": {"1"}}
	case "kg":
		address = "http://songsearch.kugou.com/song_search_v2"
		parameters = url.Values{"platform": {"AndroidFilter"}, "iscorrection": {"1"}, "keyword": {query}, "hifiquality": {"0"}, "pagesize": {strconv.Itoa(limit)}, "PrivilegeFilter": {"0"}, "page": {strconv.Itoa(page)}}
	case "tx":
		address = "https://u.y.qq.com/cgi-bin/musics.fcg"
		method = http.MethodPost
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		data := map[string]any{"comm": map[string]any{"_channelid": "0", "_os_version": "6.2.9200-2", "ct": "19", "cv": "2151", "guid": "1F70E520B2EAA7D25E11760783C53CA9", "patch": "118", "psrf_access_token_expiresAt": 0, "psrf_qqaccess_token": "", "psrf_qqopenid": "", "psrf_qqunionid": "", "tmeAppID": "qqmusic", "tmeLoginType": 0, "uin": "0", "wid": "7223299733393904640"}, "music.search.SearchCgiService": map[string]any{"module": "music.search.SearchCgiService", "method": "DoSearchForQQMusicDesktop", "param": map[string]any{"grp": 1, "num_per_page": limit, "page_num": page, "query": query, "remoteplace": "txt.newclient.top", "search_type": 0, "searchid": strings.ToUpper(hex.EncodeToString(random)) + "00000"}}}
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		body = string(encoded)
		parameters.Set("sign", qqSignature(encoded))
	case "wy":
		address = "http://interface.music.163.com/eapi/batch"
		method = http.MethodPost
		encoded, err := json.Marshal(map[string]any{"keyword": query, "needCorrect": "1", "channel": "typing", "offset": limit * (page - 1), "scene": "normal", "total": page == 1, "limit": limit})
		if err != nil {
			return nil, err
		}
		path := "/api/search/song/list/page"
		digest := md5.Sum([]byte("nobody" + path + "use" + string(encoded) + "md5forencrypt"))
		plaintext := []byte(path + "-36cd479b6b5-" + string(encoded) + "-36cd479b6b5-" + hex.EncodeToString(digest[:]))
		padding := aes.BlockSize - len(plaintext)%aes.BlockSize
		plaintext = append(plaintext, bytes.Repeat([]byte{byte(padding)}, padding)...)
		block, err := aes.NewCipher([]byte("e82ckenh8dichen8"))
		if err != nil {
			return nil, err
		}
		ciphertext := make([]byte, len(plaintext))
		for offset := 0; offset < len(plaintext); offset += aes.BlockSize {
			block.Encrypt(ciphertext[offset:offset+aes.BlockSize], plaintext[offset:offset+aes.BlockSize])
		}
		body = url.Values{"params": {strings.ToUpper(hex.EncodeToString(ciphertext))}}.Encode()
	case "mg":
		address = "https://jadeite.migu.cn/music_search/v3/search/searchAll"
		parameters = url.Values{"isCorrect": {"0"}, "isCopyright": {"1"}, "searchSwitch": {`{"song":1,"album":0,"singer":0,"tagSong":1,"mvSong":0,"bestShow":1,"songlist":0,"lyricSong":0}`}, "pageSize": {strconv.Itoa(limit)}, "text": {query}, "pageNo": {strconv.Itoa(page)}, "sort": {"0"}, "sid": {"USS"}}
	}
	if len(parameters) > 0 {
		address += "?" + parameters.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, address, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	if platform == "tx" {
		request.Header.Set("User-Agent", "QQMusic 14090508(android 12)")
		request.Header.Set("Content-Type", "application/json")
	}
	if platform == "wy" {
		request.Header.Set("Origin", "https://music.163.com")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if platform == "mg" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		device := "963B7AA0D21511ED807EE5846EC87D20"
		digest := md5.Sum([]byte(query + "6cdc72a439cef99a3418d2a78aa28c73yyapp2d16148780a1dcc7408e06336b98cfd50" + device + timestamp))
		request.Header.Set("uiVersion", "A_music_3.6.1")
		request.Header.Set("deviceId", device)
		request.Header.Set("timestamp", timestamp)
		request.Header.Set("sign", hex.EncodeToString(digest[:]))
		request.Header.Set("channel", "0146921")
	}
	return request, nil
}

func qqSignature(data []byte) string {
	digest := sha1.Sum(data)
	hash := hex.EncodeToString(digest[:])
	pick := func(indices []int) string {
		var result strings.Builder
		for _, index := range indices {
			if index < len(hash) {
				result.WriteByte(hash[index])
			}
		}
		return result.String()
	}
	scramble := []byte{89, 39, 179, 150, 218, 82, 58, 252, 177, 52, 186, 123, 120, 64, 242, 133, 143, 161, 121, 179}
	for index := range scramble {
		scramble[index] ^= digest[index]
	}
	encoded := strings.NewReplacer("/", "", "+", "", "=", "", "\\", "").Replace(base64.StdEncoding.EncodeToString(scramble))
	return strings.ToLower("zzc" + pick([]int{23, 14, 6, 36, 16, 40, 7, 19}) + encoded + pick([]int{16, 1, 32, 12, 19, 27, 8, 5}))
}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any           { result, _ := value.([]any); return result }
func text(value any) string {
	switch value := value.(type) {
	case string:
		return html.UnescapeString(value)
	case json.Number:
		return value.String()
	}
	return ""
}
func number(value any) float64 { result, _ := strconv.ParseFloat(text(value), 64); return result }
func singers(value any) string {
	names := []string{}
	for _, value := range array(value) {
		name := text(object(value)["name"])
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "、")
}
func first(values ...any) any {
	for _, value := range values {
		if value != nil && value != "" {
			return value
		}
	}
	return nil
}
func interval(seconds float64) string {
	return fmt.Sprintf("%02d:%02d", int(seconds)/60, int(seconds)%60)
}

func parsePlatform(platform string, payload map[string]any, page, limit int) ([]Item, bool, error) {
	var rows []any
	var total float64
	switch platform {
	case "kw":
		if payload["TOTAL"] == nil || (text(payload["TOTAL"]) != "0" && text(payload["SHOW"]) == "0") {
			return nil, false, errors.New("invalid Kuwo response")
		}
		rows = array(payload["abslist"])
		total = number(payload["TOTAL"])
	case "kg":
		if payload["error_code"] == nil || number(payload["error_code"]) != 0 {
			return nil, false, errors.New("invalid Kugou response")
		}
		data := object(payload["data"])
		rows = array(data["lists"])
		total = number(data["total"])
	case "tx":
		data := object(first(payload["music.search.SearchCgiService"], payload["req"]))
		if payload["code"] == nil || number(payload["code"]) != 0 || data["code"] == nil || number(data["code"]) != 0 {
			return nil, false, errors.New("invalid QQ response")
		}
		data = object(data["data"])
		rows = array(object(object(data["body"])["song"])["list"])
		total = number(object(data["meta"])["sum"])
	case "wy":
		if number(payload["code"]) != 200 {
			return nil, false, errors.New("invalid NetEase response")
		}
		data := object(payload["data"])
		rows = array(data["resources"])
		total = number(data["totalCount"])
	case "mg":
		if text(payload["code"]) != "000000" {
			return nil, false, errors.New("invalid Migu response")
		}
		data := object(payload["songResultData"])
		for _, group := range array(data["resultList"]) {
			rows = append(rows, array(group)...)
		}
		total = number(data["totalCount"])
	}
	items := []Item{}
	seen := map[string]bool{}
	for _, value := range rows {
		row := object(value)
		if row == nil {
			continue
		}
		if platform == "wy" {
			row = object(object(row["baseInfo"])["simpleSongData"])
		}
		item := normalizeItem(platform, row)
		if item.ID == "" || item.Title == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	return items, total > float64(page*limit), nil
}

func normalizeItem(platform string, row map[string]any) Item {
	item := Item{Platform: platform, Qualities: []string{}, MusicInfo: map[string]any{}}
	info := item.MusicInfo
	types := []any{}
	typeMap := map[string]any{}
	addQuality := func(quality string, size any, hash string) {
		entry := map[string]any{"type": quality, "size": text(size)}
		details := map[string]any{"size": text(size)}
		if hash != "" {
			entry["hash"] = hash
			details["hash"] = hash
		}
		types = append(types, entry)
		typeMap[quality] = details
		item.Qualities = append(item.Qualities, quality)
	}
	switch platform {
	case "kw":
		item.ID = strings.TrimPrefix(text(row["MUSICRID"]), "MUSIC_")
		item.Title = text(row["SONGNAME"])
		item.Artist = text(row["ARTIST"])
		item.Album = text(row["ALBUM"])
		item.Duration = number(row["DURATION"])
		info["albumId"] = text(row["ALBUMID"])
		for _, quality := range []struct{ rate, name string }{{"128", "128k"}, {"320", "320k"}, {"2000", "flac"}, {"4000", "flac24bit"}} {
			if strings.Contains(text(row["N_MINFO"]), "bitrate:"+quality.rate+",") {
				addQuality(quality.name, nil, "")
			}
		}
	case "kg":
		item.ID = text(row["Audioid"])
		if item.ID == "" {
			item.ID = text(row["FileHash"])
		}
		item.Title = strings.TrimSpace(text(row["OriSongName"]) + " " + text(row["Suffix"]))
		item.Artist = singers(row["Singers"])
		item.Album = text(row["AlbumName"])
		item.Duration = number(row["Duration"])
		info["albumId"] = text(row["AlbumID"])
		info["albumAudioId"] = text(row["MixSongID"])
		info["hash"] = text(row["FileHash"])
		info["_interval"] = item.Duration
		for _, quality := range []struct{ prefix, name string }{{"", "128k"}, {"HQ", "320k"}, {"SQ", "flac"}, {"Res", "flac24bit"}} {
			if number(row[quality.prefix+"FileSize"]) > 0 {
				addQuality(quality.name, row[quality.prefix+"FileSize"], text(row[quality.prefix+"FileHash"]))
			}
		}
	case "tx":
		file, album := object(row["file"]), object(row["album"])
		if text(file["media_mid"]) == "" {
			return item
		}
		item.ID = text(row["mid"])
		item.Title = text(row["title"])
		item.Artist = singers(row["singer"])
		item.Album = text(album["name"])
		item.Duration = number(row["interval"])
		albumMid := text(album["mid"])
		info["songId"] = text(row["id"])
		info["albumId"] = albumMid
		info["albumMid"] = albumMid
		info["strMediaMid"] = text(file["media_mid"])
		if albumMid != "" {
			item.CoverURL = "https://y.gtimg.cn/music/photo_new/T002R500x500M000" + url.PathEscape(albumMid) + ".jpg"
		}
		for _, quality := range []struct{ field, name string }{{"size_128mp3", "128k"}, {"size_320mp3", "320k"}, {"size_flac", "flac"}, {"size_hires", "flac24bit"}} {
			if number(file[quality.field]) > 0 {
				addQuality(quality.name, file[quality.field], "")
			}
		}
	case "wy":
		album := object(row["al"])
		item.ID = text(row["id"])
		item.Title = text(row["name"])
		item.Artist = singers(row["ar"])
		item.Album = text(album["name"])
		item.Duration = number(row["dt"]) / 1000
		item.CoverURL = text(album["picUrl"])
		info["albumId"] = text(album["id"])
		privilege := object(row["privilege"])
		maxbr := number(privilege["maxbr"])
		for _, quality := range []struct {
			field, name string
			min         float64
		}{{"l", "128k", 128000}, {"h", "320k", 320000}, {"sq", "flac", 999000}} {
			if maxbr >= quality.min {
				addQuality(quality.name, object(row[quality.field])["size"], "")
			}
		}
		if text(privilege["maxBrLevel"]) == "hires" {
			addQuality("flac24bit", object(row["hr"])["size"], "")
		}
	case "mg":
		if text(row["copyrightId"]) == "" {
			return item
		}
		item.ID = text(row["songId"])
		item.Title = text(row["name"])
		item.Artist = singers(row["singerList"])
		item.Album = text(row["album"])
		item.Duration = number(row["duration"])
		item.CoverURL = text(first(row["img3"], row["img2"], row["img1"]))
		if item.CoverURL != "" && !strings.HasPrefix(item.CoverURL, "http") {
			item.CoverURL = "http://d.musicapp.migu.cn" + item.CoverURL
		}
		info["copyrightId"] = text(row["copyrightId"])
		info["albumId"] = text(row["albumId"])
		for _, field := range []string{"lrcUrl", "mrcurl", "trcUrl"} {
			info[field] = text(row[field])
		}
		for _, value := range array(row["audioFormats"]) {
			format := object(value)
			quality := map[string]string{"PQ": "128k", "HQ": "320k", "SQ": "flac", "ZQ24": "flac24bit"}[text(format["formatType"])]
			if quality != "" {
				addQuality(quality, first(format["asize"], format["isize"]), "")
			}
		}
	}
	info["source"] = platform
	info["songmid"] = item.ID
	info["name"] = item.Title
	info["singer"] = item.Artist
	info["albumName"] = item.Album
	info["interval"] = interval(item.Duration)
	info["img"] = item.CoverURL
	info["types"] = types
	info["_types"] = typeMap
	info["typeUrl"] = map[string]any{}
	return item
}
