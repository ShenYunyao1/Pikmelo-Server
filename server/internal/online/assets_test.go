package online

import (
	"bytes"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLyricsKuwoPlainCompressed(t *testing.T) {
	var compressed bytes.Buffer
	compressed.WriteString("tp=content\r\n\r\n")
	writer := zlib.NewWriter(&compressed)
	writer.Write([]byte("[00:01.00]Original\n[00:02.00]Next"))
	writer.Close()
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "mlyric.kuwo.cn" || r.URL.Query().Get("lrcx") != "0" || r.URL.Query().Get("rid") != "42" {
			t.Fatal("invalid lyric query")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(compressed.Bytes())), Header: make(http.Header)}, nil
	})}
	lyric, err := Lyrics(context.Background(), client, Item{Platform: "kw", ID: "42"})
	if err != nil || !strings.Contains(lyric, "[00:01.000]Original") {
		t.Fatalf("%q %v", lyric, err)
	}
}

func TestLyricsNeteaseTranslation(t *testing.T) {
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "interface3.music.163.com" || r.Method != "POST" {
			t.Fatal("invalid NetEase request")
		}
		return searchResponse(`{"code":200,"lrc":{"lyric":"[00:01.00]Original\n[00:02.00]Next"},"tlyric":{"lyric":"[00:01.00]翻译\n[00:02.05]下一行"}}`), nil
	})}
	lyric, err := Lyrics(context.Background(), client, Item{Platform: "wy", ID: "42"})
	if err != nil || lyric != "[00:01.000]Original\n[00:01.000]翻译\n[00:02.000]Next\n[00:02.000]下一行" {
		t.Fatalf("%q %v", lyric, err)
	}
}

func TestLyricsMiguAndErrors(t *testing.T) {
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) { return searchResponse("[00:01.00]Song"), nil })}
	lyric, err := Lyrics(context.Background(), client, Item{Platform: "mg", ID: "42", MusicInfo: map[string]any{"lrcUrl": "https://example.com/song.lrc"}})
	if err != nil || lyric != "[00:01.000]Song" {
		t.Fatalf("%q %v", lyric, err)
	}
	for _, platform := range []string{"tx", "unknown"} {
		if _, err := Lyrics(context.Background(), client, Item{Platform: platform, ID: "42"}); err == nil {
			t.Fatal("unsupported lyrics accepted")
		}
	}
	client.Transport = searchTransport(func(r *http.Request) (*http.Response, error) {
		return searchResponse(strings.Repeat("x", 2*1024*1024+1)), nil
	})
	if _, err := Lyrics(context.Background(), client, Item{Platform: "mg", ID: "42", MusicInfo: map[string]any{"lrcUrl": "https://example.com/song.lrc"}}); err == nil {
		t.Fatal("oversized lyric accepted")
	}
}

func TestMergeLyricsOffsetsAndDuplicateTimestamps(t *testing.T) {
	got := MergeLyrics("[offset:100]\n[00:01.00][00:02.00]Line", "[00:00.90]翻译\n[00:01.90]Line")
	if got != "[00:00.900]Line\n[00:00.900]翻译\n[00:01.900]Line" {
		t.Fatalf("%q", got)
	}
}
