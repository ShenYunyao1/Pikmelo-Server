package online

// Lyric endpoints and request formats follow LX Desktop 323e4bac, musicSdk/*/lyric.js.
// Encrypted QRC/KRC and MRC formats require their own decoders and are not treated as LRC.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxLyricBytes = 2 * 1024 * 1024

var errLyricsUnavailable = errors.New("平台歌词暂时不可用")

// Lyrics obtains bounded plain LRC with translations where available. The caller
// supplies the same public-network-only client used for search and downloads.
func Lyrics(ctx context.Context, client *http.Client, item Item) (string, error) {
	if client == nil || item.ID == "" || len(item.ID) > 200 {
		return "", errLyricsUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var original, translation string
	switch item.Platform {
	case "kw":
		address := "http://mlyric.kuwo.cn/mobi.s?" + url.Values{"f": {"web"}, "type": {"lyric"}, "lrcx": {"0"}, "rid": {item.ID}, "encode": {"utf8"}}.Encode()
		data, err := lyricFetch(ctx, client, http.MethodGet, address, "", nil)
		if err != nil {
			return "", err
		}
		separator := bytes.Index(data, []byte("\r\n\r\n"))
		if len(data) < 10 || !strings.EqualFold(string(data[:10]), "tp=content") || separator < 0 {
			return "", errLyricsUnavailable
		}
		reader, err := zlib.NewReader(bytes.NewReader(data[separator+4:]))
		if err != nil {
			return "", errLyricsUnavailable
		}
		defer reader.Close()
		decoded, err := io.ReadAll(io.LimitReader(reader, maxLyricBytes+1))
		if err != nil || len(decoded) > maxLyricBytes {
			return "", errLyricsUnavailable
		}
		original = string(decoded)
	case "wy":
		data, err := json.Marshal(map[string]any{"id": item.ID, "cp": false, "tv": 0, "lv": 0, "rv": 0, "kv": 0, "yv": 0, "ytv": 0, "yrv": 0})
		if err != nil {
			return "", errLyricsUnavailable
		}
		path := "/api/song/lyric/v1"
		digest := md5.Sum([]byte("nobody" + path + "use" + string(data) + "md5forencrypt"))
		plaintext := []byte(path + "-36cd479b6b5-" + string(data) + "-36cd479b6b5-" + hex.EncodeToString(digest[:]))
		padding := aes.BlockSize - len(plaintext)%aes.BlockSize
		plaintext = append(plaintext, bytes.Repeat([]byte{byte(padding)}, padding)...)
		block, err := aes.NewCipher([]byte("e82ckenh8dichen8"))
		if err != nil {
			return "", errLyricsUnavailable
		}
		ciphertext := make([]byte, len(plaintext))
		for offset := 0; offset < len(plaintext); offset += aes.BlockSize {
			block.Encrypt(ciphertext[offset:offset+aes.BlockSize], plaintext[offset:offset+aes.BlockSize])
		}
		body := url.Values{"params": {strings.ToUpper(hex.EncodeToString(ciphertext))}}.Encode()
		response, err := lyricFetch(ctx, client, http.MethodPost, "https://interface3.music.163.com/eapi/song/lyric/v1", body, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://music.163.com"})
		if err != nil {
			return "", err
		}
		payload, err := lyricJSON(response)
		if err != nil || number(payload["code"]) != 200 {
			return "", errLyricsUnavailable
		}
		original = text(object(payload["lrc"])["lyric"])
		translation = text(object(payload["tlyric"])["lyric"])
	case "mg":
		address := text(item.MusicInfo["lrcUrl"])
		if address == "" {
			return "", errors.New("该曲目未提供普通 LRC 歌词")
		}
		headers := map[string]string{"Referer": "https://app.c.nf.migu.cn/", "channel": "0146921"}
		data, err := lyricFetch(ctx, client, http.MethodGet, address, "", headers)
		if err != nil {
			return "", err
		}
		original = string(data)
		if address := text(item.MusicInfo["trcUrl"]); address != "" {
			data, err = lyricFetch(ctx, client, http.MethodGet, address, "", headers)
			if err == nil {
				translation = string(data)
			}
		}
	case "kg":
		hash := text(item.MusicInfo["hash"])
		if hash == "" {
			return "", errLyricsUnavailable
		}
		headers := map[string]string{"KG-RC": "1", "KG-THash": "expand_search_manager.cpp:852736169:451", "User-Agent": "KuGou2012-9020-ExpandSearchManager"}
		address := "http://lyrics.kugou.com/search?" + url.Values{"ver": {"1"}, "man": {"yes"}, "client": {"pc"}, "keyword": {item.Title}, "hash": {hash}, "timelength": {strconv.FormatFloat(item.Duration, 'f', 0, 64)}, "lrctxt": {"1"}}.Encode()
		data, err := lyricFetch(ctx, client, http.MethodGet, address, "", headers)
		if err != nil {
			return "", err
		}
		payload, err := lyricJSON(data)
		if err != nil {
			return "", err
		}
		candidates := array(payload["candidates"])
		if len(candidates) == 0 {
			return "", errLyricsUnavailable
		}
		candidate := object(candidates[0])
		if number(candidate["krctype"]) == 1 && number(candidate["contenttype"]) != 1 {
			return "", errors.New("该曲目提供的 KRC 歌词格式暂未适配")
		}
		address = "http://lyrics.kugou.com/download?" + url.Values{"ver": {"1"}, "client": {"pc"}, "id": {text(candidate["id"])}, "accesskey": {text(candidate["accesskey"])}, "fmt": {"lrc"}, "charset": {"utf8"}}.Encode()
		data, err = lyricFetch(ctx, client, http.MethodGet, address, "", headers)
		if err != nil {
			return "", err
		}
		payload, err = lyricJSON(data)
		if err != nil || text(payload["fmt"]) != "lrc" {
			return "", errLyricsUnavailable
		}
		decoded, err := base64.StdEncoding.DecodeString(text(payload["content"]))
		if err != nil {
			return "", errLyricsUnavailable
		}
		original = string(decoded)
	case "tx":
		return "", errors.New("QQ 音乐 QRC 歌词解码暂未适配")
	default:
		return "", errors.New("该平台暂未适配歌词")
	}
	if !utf8.ValidString(original) || !utf8.ValidString(translation) || len(original)+len(translation) > maxLyricBytes {
		return "", errLyricsUnavailable
	}
	result := MergeLyrics(original, translation)
	if result == "" || len(result) > maxLyricBytes {
		return "", errLyricsUnavailable
	}
	return result, nil
}

func lyricFetch(ctx context.Context, client *http.Client, method, address, body string, headers map[string]string) ([]byte, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, errLyricsUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, method, address, strings.NewReader(body))
	if err != nil {
		return nil, errLyricsUnavailable
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errLyricsUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errLyricsUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLyricBytes+1))
	if err != nil || len(data) > maxLyricBytes {
		return nil, errLyricsUnavailable
	}
	return data, nil
}

func lyricJSON(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result map[string]any
	if decoder.Decode(&result) != nil {
		return nil, errLyricsUnavailable
	}
	return result, nil
}

var lyricTimestamp = regexp.MustCompile(`^\[(\d{1,6}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
var lyricOffset = regexp.MustCompile(`(?im)^\[offset:\s*([+-]?\d{1,9})\s*\]`)

type lyricLine struct {
	milliseconds int64
	words        string
}

func lyricLines(value string) []lyricLine {
	var offset int64
	if matches := lyricOffset.FindAllStringSubmatch(value, -1); len(matches) > 0 {
		offset, _ = strconv.ParseInt(matches[len(matches)-1][1], 10, 64)
		if offset < -86400000 || offset > 86400000 {
			offset = 0
		}
	}
	result := []lyricLine{}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		timestamps := []int64{}
		for {
			match := lyricTimestamp.FindStringSubmatch(line)
			if match == nil {
				break
			}
			minutes, _ := strconv.ParseInt(match[1], 10, 64)
			seconds, _ := strconv.ParseInt(match[2], 10, 64)
			fraction, _ := strconv.ParseInt((match[3] + "000")[:3], 10, 64)
			if seconds < 60 {
				timestamps = append(timestamps, max(int64(0), minutes*60000+seconds*1000+fraction-offset))
			}
			line = line[len(match[0]):]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, timestamp := range timestamps {
			result = append(result, lyricLine{timestamp, line})
		}
	}
	return result
}

// MergeLyrics groups original and translated lines by playback time. Provider
// offsets are applied once, and translated timestamps within 100 ms align with
// an existing original line so the player renders one bilingual group.
func MergeLyrics(original, translation string) string {
	if len(original)+len(translation) > maxLyricBytes {
		return ""
	}
	lines := lyricLines(original)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].milliseconds < lines[j].milliseconds })
	translations := lyricLines(translation)
	for translationIndex, translated := range translations {
		index := sort.Search(len(lines), func(index int) bool { return lines[index].milliseconds >= translated.milliseconds })
		nearest := translated.milliseconds
		distance := int64(101)
		for _, candidate := range []int{index - 1, index} {
			if candidate >= 0 && candidate < len(lines) {
				delta := lines[candidate].milliseconds - translated.milliseconds
				if delta < 0 {
					delta = -delta
				}
				if delta < distance {
					nearest = lines[candidate].milliseconds
					distance = delta
				}
			}
		}
		if distance <= 100 {
			translated.milliseconds = nearest
		}
		translations[translationIndex] = translated
	}
	lines = append(lines, translations...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].milliseconds < lines[j].milliseconds })
	var output strings.Builder
	seen := map[lyricLine]bool{}
	for _, line := range lines {
		if seen[line] {
			continue
		}
		seen[line] = true
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "[%02d:%02d.%03d]%s", line.milliseconds/60000, line.milliseconds/1000%60, line.milliseconds%1000, line.words)
	}
	return output.String()
}
