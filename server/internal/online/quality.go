package online

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// QualityOption describes platform-reported candidates. Audio probing after
// download remains the authority for the imported file's actual properties.
type QualityOption struct {
	Quality     string  `json:"quality"`
	Label       string  `json:"label,omitempty"`
	Format      string  `json:"format,omitempty"`
	Codec       string  `json:"codec,omitempty"`
	BitrateKbps float64 `json:"bitrate_kbps,omitempty"`
	SizeBytes   int64   `json:"size_bytes,omitempty"`
	SizeText    string  `json:"size_text,omitempty"`
	SampleRate  int     `json:"sample_rate,omitempty"`
	BitDepth    int     `json:"bit_depth,omitempty"`
	Channels    int     `json:"channels,omitempty"`
	FormatCode  string  `json:"format_code,omitempty"`
}

func qualityNumber(value any) float64 {
	number, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
		return 0
	}
	return number
}

func qualityInteger(value any) int64 {
	number := qualityNumber(value)
	if number >= math.MaxInt64 || math.Trunc(number) != number {
		return 0
	}
	return int64(number)
}

func qualityAudioFields(data map[string]any) QualityOption {
	return QualityOption{
		Format:     strings.ToUpper(text(first(data["format"], data["ext"]))),
		Codec:      strings.ToUpper(text(data["codec"])),
		SampleRate: int(qualityInteger(first(data["sr"], data["sampleRate"]))),
		BitDepth:   int(qualityInteger(first(data["bitDepth"], data["bitsPerSample"], data["bit"]))),
		Channels:   int(qualityInteger(data["channels"])),
	}
}

var platformSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(?:[kKmMgGtT](?:[iI]?[bB])?|[bB])?$`)

func kuwoQualityOptions(info string) []QualityOption {
	options := []QualityOption{}
	for _, entry := range strings.Split(info, ";") {
		fields := map[string]any{}
		for _, field := range strings.Split(entry, ",") {
			pair := strings.SplitN(field, ":", 2)
			if len(pair) == 2 {
				fields[strings.TrimSpace(pair[0])] = strings.TrimSpace(pair[1])
			}
		}
		option := qualityAudioFields(fields)
		if option.Format == "" {
			continue
		}
		rate := text(fields["bitrate"])
		option.Quality = map[string]string{"128:MP3": "128k", "320:MP3": "320k", "2000:FLAC": "flac", "4000:FLAC": "flac24bit"}[rate+":"+option.Format]
		if option.Quality == "" {
			option.Quality = "kw-" + text(fields["level"]) + "-" + strings.ToLower(option.Format) + "-" + rate
			option.Label = option.Format
		}
		if option.Format == "MP3" || option.Format == "OGG" || option.Format == "AAC" {
			option.BitrateKbps = qualityNumber(fields["bitrate"])
		}
		size := strings.TrimSpace(text(fields["size"]))
		if match := platformSizePattern.FindStringSubmatch(size); len(match) > 0 && qualityNumber(match[1]) > 0 {
			option.SizeText = size
		}
		options = append(options, option)
	}
	return options
}
