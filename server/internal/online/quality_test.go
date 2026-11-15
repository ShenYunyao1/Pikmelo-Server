package online

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCandidateQualityMetadata(t *testing.T) {
	fixtures := []struct {
		platform, payload string
		expected          QualityOption
	}{
		{"kw", `{"MUSICRID":"MUSIC_1","SONGNAME":"Song","N_MINFO":"level:h,bitrate:128,format:mp3,size:2.85Mb;level:ff,bitrate:2000,format:flac,size:18.51Mb"}`, QualityOption{Quality: "flac", Format: "FLAC", SizeText: "18.51Mb"}},
		{"kg", `{"Audioid":"1","OriSongName":"Song","FileSize":1048576,"ExtName":"mp3","Bitrate":128}`, QualityOption{Quality: "128k", Format: "MP3", BitrateKbps: 128, SizeBytes: 1048576}},
		{"tx", `{"mid":"one","title":"Song","file":{"media_mid":"media","size_hires":8388608,"hires_sample":96000,"hires_bitdepth":24,"size_96aac":1048576}}`, QualityOption{Quality: "flac24bit", Format: "FLAC", SizeBytes: 8388608, SampleRate: 96000, BitDepth: 24}},
		{"wy", `{"id":"1","name":"Song","privilege":{"maxbr":999000},"sq":{"size":5257385,"br":398059,"sr":44100}}`, QualityOption{Quality: "flac", BitrateKbps: 398.059, SizeBytes: 5257385, SampleRate: 44100}},
		{"mg", `{"songId":"1","copyrightId":"two","name":"Song","audioFormats":[{"formatType":"SQ","asize":"26674330","aformat":"011002"}]}`, QualityOption{Quality: "flac", SizeBytes: 26674330, FormatCode: "011002"}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.platform, func(t *testing.T) {
			var row map[string]any
			decoder := json.NewDecoder(strings.NewReader(fixture.payload))
			decoder.UseNumber()
			if err := decoder.Decode(&row); err != nil {
				t.Fatal(err)
			}
			item := normalizeItem(fixture.platform, row)
			var found *QualityOption
			for i := range item.QualityOptions {
				if item.QualityOptions[i].Quality == fixture.expected.Quality {
					found = &item.QualityOptions[i]
					break
				}
			}
			if found == nil || *found != fixture.expected {
				t.Fatalf("quality metadata got %+v, want %+v", found, fixture.expected)
			}
			if len(item.Qualities) == 0 || item.MusicInfo["types"] == nil || item.MusicInfo["_types"] == nil {
				t.Fatal("lost LX source metadata")
			}
			if fixture.platform == "tx" {
				var aac *QualityOption
				for i := range item.QualityOptions {
					if item.QualityOptions[i].Quality == "96aac" {
						aac = &item.QualityOptions[i]
					}
				}
				if aac == nil || aac.Codec != "AAC" || aac.BitrateKbps != 96 || aac.Format != "" {
					t.Fatalf("additional AAC candidate %+v", aac)
				}
			}
		})
	}
}

func TestCandidateQualityMissingAndInvalidFields(t *testing.T) {
	var row map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"id":"1","name":"Song","privilege":{"maxbr":320000},"l":{"size":-1,"br":0,"sr":-1},"h":{"size":"bad","br":"NaN","sr":"Infinity"}}`))
	decoder.UseNumber()
	if err := decoder.Decode(&row); err != nil {
		t.Fatal(err)
	}
	item := normalizeItem("wy", row)
	if len(item.QualityOptions) != 2 {
		t.Fatalf("missing candidates %+v", item.QualityOptions)
	}
	for _, option := range item.QualityOptions {
		if option.Format != "" || option.BitrateKbps != 0 || option.SizeBytes != 0 || option.SampleRate != 0 {
			t.Fatalf("invented unavailable attributes %+v", option)
		}
	}
}

func TestCandidateQualityVariantIdentityAndMiguFallback(t *testing.T) {
	decode := func(payload string) map[string]any {
		var row map[string]any
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	kuwo := normalizeItem("kw", decode(`{"MUSICRID":"MUSIC_1","SONGNAME":"Song","N_MINFO":"level:p,bitrate:128,format:ogg,size:1Mb;level:h,bitrate:128,format:mp3,size:2Mb;level:dtsx,bitrate:25000,format:mmp4,size:0Mb"}`))
	if len(kuwo.QualityOptions) != 3 || kuwo.QualityOptions[0].Format != "MP3" || kuwo.QualityOptions[1].Format != "OGG" || kuwo.QualityOptions[1].Quality == "128k" {
		t.Fatalf("lost distinct encoding variant %+v", kuwo.QualityOptions)
	}
	if option := kuwo.QualityOptions[2]; option.BitrateKbps != 0 || option.SizeText != "" {
		t.Fatalf("platform grade or zero size became an audio attribute %+v", option)
	}
	migu := normalizeItem("mg", decode(`{"songId":"1","copyrightId":"2","name":"Song","audioFormats":[{"formatType":"SQ","asize":"0","isize":"12345","aformat":"unused","iformat":"011003"}]}`))
	if len(migu.QualityOptions) != 1 || migu.QualityOptions[0].SizeBytes != 12345 || migu.QualityOptions[0].FormatCode != "011003" {
		t.Fatalf("lost fallback size and corresponding format code %+v", migu.QualityOptions)
	}
	wy := normalizeItem("wy", decode(`{"id":"1","name":"Song","privilege":{"maxbr":320000},"m":{"br":192000,"size":2537265,"sr":44100}}`))
	if len(wy.Qualities) != 2 || len(wy.QualityOptions) != 3 || wy.QualityOptions[2].Quality != "192k" || wy.QualityOptions[2].BitrateKbps != 192 {
		t.Fatalf("lost reported intermediate tier or changed LX request capabilities %+v", wy)
	}
}
