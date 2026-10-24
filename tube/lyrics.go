package tube

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	LyricsFromFile   = "file"
	LyricsFromLRCLIB = "lrclib"
)

type Lyrics struct {
	Plain        string    `json:",omitempty"`
	Synced       string    `json:",omitempty"`
	Instrumental bool      `json:",omitempty"`
	Source       string    `json:",omitempty"`
	Checked      time.Time `json:",omitempty"`
}

type LyricLine struct {
	Start int64
	Text  string
}

var (
	lrcStamp = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	lrcTag   = regexp.MustCompile(`^\[[a-zA-Z]+:[^\]]*\]$`)
)

func ParseLyrics(text string) Lyrics {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\r\n", "\n"))
	if !lrcStamp.MatchString(text) {
		return Lyrics{Plain: text}
	}
	l := Lyrics{Synced: text}
	var plain []string
	for _, line := range l.Lines() {
		plain = append(plain, line.Text)
	}
	l.Plain = strings.Join(plain, "\n")
	return l
}

func (l Lyrics) Empty() bool {
	return l.Plain == "" && l.Synced == ""
}

func (l Lyrics) Lines() []LyricLine {
	if l.Synced == "" {
		var lines []LyricLine
		for _, text := range strings.Split(l.Plain, "\n") {
			lines = append(lines, LyricLine{Text: strings.TrimSpace(text)})
		}
		return lines
	}
	var lines []LyricLine
	for _, raw := range strings.Split(l.Synced, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" || (lrcTag.MatchString(raw) && !lrcStamp.MatchString(raw)) {
			continue
		}
		stamps := lrcStamp.FindAllStringSubmatchIndex(raw, -1)
		if len(stamps) == 0 {
			continue
		}
		text := strings.TrimSpace(raw[stamps[len(stamps)-1][1]:])
		for _, m := range stamps {
			lines = append(lines, LyricLine{Start: lrcMillis(raw, m), Text: text})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Start < lines[j].Start })
	return lines
}

func lrcMillis(raw string, m []int) int64 {
	min, _ := strconv.ParseInt(raw[m[2]:m[3]], 10, 64)
	sec, _ := strconv.ParseInt(raw[m[4]:m[5]], 10, 64)
	var frac int64
	if m[6] >= 0 {
		digits := raw[m[6]:m[7]]
		frac, _ = strconv.ParseInt(digits, 10, 64)
		for i := len(digits); i < 3; i++ {
			frac *= 10
		}
	}
	return (min*60+sec)*1000 + frac
}
