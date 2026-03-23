package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reThreshold = regexp.MustCompile(`(?i)(\d{2,3}(?:\.\d+)?)\s*(?:°|degrees|f\b)`)
	reDateMDY   = regexp.MustCompile(`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+(\d{1,2})(?:,\s*(20\d{2}))?`)
)

var monthNumber = map[string]time.Month{
	"jan": time.January,
	"feb": time.February,
	"mar": time.March,
	"apr": time.April,
	"may": time.May,
	"jun": time.June,
	"jul": time.July,
	"aug": time.August,
	"sep": time.September,
	"oct": time.October,
	"nov": time.November,
	"dec": time.December,
}

func parseThresholdF(text string) (float64, bool) {
	m := reThreshold.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseEventDate(text string, fallback time.Time, tz string) (time.Time, bool) {
	if !fallback.IsZero() {
		return fallback.In(loadLocation(tz)), true
	}
	m := reDateMDY.FindStringSubmatch(text)
	if len(m) < 3 {
		return time.Time{}, false
	}
	month := monthNumber[strings.ToLower(m[1])[:3]]
	day, err := strconv.Atoi(m[2])
	if err != nil {
		return time.Time{}, false
	}
	year := time.Now().In(loadLocation(tz)).Year()
	if len(m) >= 4 && m[3] != "" {
		year, err = strconv.Atoi(m[3])
		if err != nil {
			return time.Time{}, false
		}
	}
	loc := loadLocation(tz)
	return time.Date(year, month, day, 12, 0, 0, 0, loc), true
}

func normalizeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer("?", " ", ",", " ", ".", " ", "(", " ", ")", " ", "-", " ")
	return strings.Join(strings.Fields(replacer.Replace(s)), " ")
}

func marketKey(m Market, side string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%.1f", m.Platform, m.ID, m.City, m.EventDate.Format("2006-01-02"), side, m.ThresholdF)
}

func loadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}
