package online

import (
	"context"
	"crypto/aes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func searchResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestSearchPlatformRequestsAndLXMetadata(t *testing.T) {
	fixtures := []struct {
		platform, host, payload, id string
		check                       func(*testing.T, *http.Request)
	}{
		{"kg", "songsearch.kugou.com", `{"error_code":0,"data":{"total":1,"lists":[{"Audioid":9007199254740993,"MixSongID":42,"FileHash":"abc","FileSize":10000,"OriSongName":"Song","Singers":[{"name":"Singer"}],"Duration":60,"AlbumID":88,"AlbumName":"Album"}]}}`, "9007199254740993", func(t *testing.T, r *http.Request) {
			if r.URL.Query().Get("keyword") != "Song" || r.URL.Query().Get("page") != "1" {
				t.Fatal("invalid Kugou query")
			}
		}},
		{"tx", "u.y.qq.com", `{"code":0,"music.search.SearchCgiService":{"code":0,"data":{"meta":{"sum":1},"body":{"song":{"list":[{"id":9007199254740993,"mid":"qqmid","title":"Song","singer":[{"name":"Singer"}],"album":{"mid":"alb","name":"Album"},"interval":100,"file":{"media_mid":"mediamid","size_128mp3":10000}}]}}}}}`, "qqmid", func(t *testing.T, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.URL.Query().Get("sign") != qqSignature(body) {
				t.Fatal("invalid QQ signature")
			}
			var request map[string]any
			if json.Unmarshal(body, &request) != nil {
				t.Fatal("invalid QQ body")
			}
			param := object(object(request["music.search.SearchCgiService"])["param"])
			if param["query"] != "Song" {
				t.Fatal("missing QQ query")
			}
		}},
		{"wy", "interface.music.163.com", `{"code":200,"data":{"totalCount":1,"resources":[{"baseInfo":{"simpleSongData":{"id":9007199254740993,"name":"Song","ar":[{"name":"Singer"}],"al":{"id":3,"name":"Album","picUrl":"https://example.com/a.jpg"},"dt":100000,"privilege":{"maxbr":320000},"l":{"size":10000},"h":{"size":30000}}}}]}}`, "9007199254740993", func(t *testing.T, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			encrypted, err := hex.DecodeString(form.Get("params"))
			if err != nil {
				t.Fatal(err)
			}
			block, _ := aes.NewCipher([]byte("e82ckenh8dichen8"))
			if len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
				t.Fatal("invalid eapi ciphertext")
			}
			plaintext := make([]byte, len(encrypted))
			for offset := 0; offset < len(encrypted); offset += aes.BlockSize {
				block.Decrypt(plaintext[offset:offset+aes.BlockSize], encrypted[offset:offset+aes.BlockSize])
			}
			if !strings.HasPrefix(string(plaintext), `/api/search/song/list/page-36cd479b6b5-`) || !strings.Contains(string(plaintext), `"keyword":"Song"`) {
				t.Fatal("invalid NetEase eapi request")
			}
		}},
		{"mg", "jadeite.migu.cn", `{"code":"000000","songResultData":{"totalCount":1,"resultList":[[{"songId":"mgid","copyrightId":"copyright","name":"Song","singerList":[{"name":"Singer"}],"album":"Album","albumId":"mg-album","duration":100,"audioFormats":[{"formatType":"PQ","asize":10000}]}]]}}`, "mgid", func(t *testing.T, r *http.Request) {
			if r.URL.Query().Get("text") != "Song" || len(r.Header.Get("sign")) != 32 || r.Header.Get("timestamp") == "" {
				t.Fatal("invalid Migu signature")
			}
		}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.platform, func(t *testing.T) {
			client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != fixture.host {
					t.Fatal("wrong platform host")
				}
				fixture.check(t, r)
				return searchResponse(fixture.payload), nil
			})}
			result, err := Search(context.Background(), client, []string{fixture.platform}, "Song", 1, 20)
			if err != nil || len(result.Items) != 1 {
				t.Fatalf("result %+v err %v", result, err)
			}
			item := result.Items[0]
			if item.ID != fixture.id || item.MusicInfo["songmid"] != fixture.id || item.MusicInfo["name"] != "Song" || item.Artist != "Singer" || len(item.Qualities) == 0 || result.HasMore {
				t.Fatalf("bad LX metadata %+v", item)
			}
			if fixture.platform == "tx" && (item.MusicInfo["songId"] != "9007199254740993" || item.MusicInfo["strMediaMid"] != "mediamid") {
				t.Fatal("lost QQ secondary IDs")
			}
			if fixture.platform == "kg" && item.MusicInfo["hash"] != "abc" {
				t.Fatal("lost Kugou hash")
			}
			if fixture.platform == "mg" && item.MusicInfo["copyrightId"] != "copyright" {
				t.Fatal("lost Migu copyright ID")
			}
		})
	}
}

func TestSearchCancelledContextAndInvalidPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	if _, err := Search(ctx, client, []string{"kw"}, "song", 1, 20); err == nil {
		t.Fatal("cancelled search accepted")
	}
	for _, input := range []struct {
		query       string
		page, limit int
	}{{" ", 1, 20}, {strings.Repeat("歌", 201), 1, 20}, {"song", 0, 20}, {"song", 1, 51}, {"song", 1001, 20}} {
		if _, err := Search(context.Background(), client, []string{"kw"}, input.query, input.page, input.limit); err == nil {
			t.Fatal("invalid parameters accepted")
		}
	}
}

func TestSearchKuwoAndPartialFailure(t *testing.T) {
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "search.kuwo.cn" {
			if r.URL.Query().Get("pn") != "1" || r.URL.Query().Get("all") != "Test & song" {
				t.Error("invalid Kuwo request")
			}
			return searchResponse(`{"TOTAL":"99","SHOW":"1","abslist":[{"MUSICRID":"MUSIC_9007199254740993","SONGNAME":"Test &amp; song","ARTIST":"Artist","ALBUM":"Album","ALBUMID":"123","DURATION":"125","N_MINFO":"level:standard,bitrate:128,format:mp3,size:3.0M;level:lossless,bitrate:2000,format:flac,size:30M"}]}`), nil
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("failure")), Header: make(http.Header)}, nil
	})}
	result, err := Search(context.Background(), client, []string{"kw", "kg"}, "Test & song", 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "9007199254740993" || result.Items[0].Title != "Test & song" || !result.HasMore {
		t.Fatalf("unexpected result %+v", result)
	}
	if result.Platforms[1].Error == "" || result.Items[0].MusicInfo["songmid"] != "9007199254740993" {
		t.Fatal("lost platform error or exact ID")
	}
}

func TestSearchInvalidInputAndOversizedResponse(t *testing.T) {
	for _, platforms := range [][]string{nil, {"unknown"}, {"kw", "kw"}} {
		if _, err := Search(context.Background(), http.DefaultClient, platforms, "song", 1, 20); err == nil {
			t.Fatal("invalid platforms accepted")
		}
	}
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		return searchResponse(strings.Repeat("x", 4*1024*1024+1)), nil
	})}
	result, err := Search(context.Background(), client, []string{"kw"}, "song", 1, 20)
	if err != nil || len(result.Items) != 0 || result.Platforms[0].Error == "" {
		t.Fatalf("oversized response not isolated %+v %v", result, err)
	}
}
