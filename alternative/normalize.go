package feed

import (
	"regexp"
	"strings"
	"time"
)

// teamAliases maps any known name variant to a canonical 3-6 char code.
// Add entries here whenever you see a "no match" in the logs.
var teamAliases = map[string]string{
	// NBA
	"lakers": "LAL", "los angeles lakers": "LAL", "la lakers": "LAL",
	"celtics": "BOS", "boston celtics": "BOS",
	"warriors": "GSW", "golden state": "GSW", "golden state warriors": "GSW",
	"heat": "MIA", "miami heat": "MIA",
	"nets": "BKN", "brooklyn nets": "BKN",
	"knicks": "NYK", "new york knicks": "NYK",
	"bucks": "MIL", "milwaukee bucks": "MIL",
	"76ers": "PHI", "sixers": "PHI", "philadelphia 76ers": "PHI",
	"suns": "PHX", "phoenix suns": "PHX",
	"nuggets": "DEN", "denver nuggets": "DEN",
	"clippers": "LAC", "la clippers": "LAC", "los angeles clippers": "LAC",
	"bulls": "CHI", "chicago bulls": "CHI",
	"raptors": "TOR", "toronto raptors": "TOR",
	"hawks": "ATL", "atlanta hawks": "ATL",
	"cavaliers": "CLE", "cavs": "CLE", "cleveland cavaliers": "CLE",
	"timberwolves": "MIN", "wolves": "MIN", "minnesota timberwolves": "MIN",
	"thunder": "OKC", "oklahoma city thunder": "OKC",
	"blazers": "POR", "trail blazers": "POR", "portland trail blazers": "POR",
	"mavericks": "DAL", "mavs": "DAL", "dallas mavericks": "DAL",
	"spurs": "SAS", "san antonio spurs": "SAS",
	"grizzlies": "MEM", "memphis grizzlies": "MEM",
	"pelicans": "NOP", "new orleans pelicans": "NOP",
	"rockets": "HOU", "houston rockets": "HOU",
	"jazz": "UTA", "utah jazz": "UTA",
	"kings": "SAC", "sacramento kings": "SAC",
	"magic": "ORL", "orlando magic": "ORL",
	"pacers": "IND", "indiana pacers": "IND",
	"pistons": "DET", "detroit pistons": "DET",
	"hornets": "CHA", "charlotte hornets": "CHA",
	"wizards": "WAS", "washington wizards": "WAS",
	// NFL
	"patriots": "NE", "new england patriots": "NE",
	"chiefs": "KC", "kansas city chiefs": "KC",
	"bills": "BUF", "buffalo bills": "BUF",
	"49ers": "SF", "san francisco 49ers": "SF", "niners": "SF",
	"eagles": "PHI_NF", "philadelphia eagles": "PHI_NF",
	"cowboys": "DAL_NF", "dallas cowboys": "DAL_NF",
	"ravens": "BAL", "baltimore ravens": "BAL",
	"bengals": "CIN", "cincinnati bengals": "CIN",
	"rams": "LAR", "los angeles rams": "LAR",
	"lions": "DET_NF", "detroit lions": "DET_NF",
	"packers": "GB", "green bay packers": "GB",
	"seahawks": "SEA", "seattle seahawks": "SEA",
	"steelers": "PIT", "pittsburgh steelers": "PIT",
	"broncos": "DEN_NF", "denver broncos": "DEN_NF",
	"chargers": "LAC_NF", "los angeles chargers": "LAC_NF",
	"raiders": "LV", "las vegas raiders": "LV",
	"colts": "IND_NF", "indianapolis colts": "IND_NF",
	"titans": "TEN", "tennessee titans": "TEN",
	"jaguars": "JAX", "jacksonville jaguars": "JAX",
	"texans": "HOU_NF", "houston texans": "HOU_NF",
	"browns": "CLE_NF", "cleveland browns": "CLE_NF",
	"bears": "CHI_NF", "chicago bears": "CHI_NF",
	"vikings": "MIN_NF", "minnesota vikings": "MIN_NF",
	"saints": "NO", "new orleans saints": "NO",
	"falcons": "ATL_NF", "atlanta falcons": "ATL_NF",
	"panthers": "CAR", "carolina panthers": "CAR",
	"buccaneers": "TB", "tampa bay buccaneers": "TB",
	"cardinals": "ARI", "arizona cardinals": "ARI",
	"seahawks2": "SEA",
	"giants_nfl": "NYG", "new york giants": "NYG",
	"jets": "NYJ", "new york jets": "NYJ",
	"commanders": "WAS_NF", "washington commanders": "WAS_NF",
	// MLB
	"yankees": "NYY", "new york yankees": "NYY",
	"red sox": "BOS_ML", "boston red sox": "BOS_ML",
	"dodgers": "LAD", "los angeles dodgers": "LAD",
	"astros": "HOU_ML", "houston astros": "HOU_ML",
	"mets": "NYM", "new york mets": "NYM",
	"cubs": "CHC", "chicago cubs": "CHC",
	"white sox": "CWS", "chicago white sox": "CWS",
	"cardinals_mlb": "STL", "st louis cardinals": "STL", "st. louis cardinals": "STL",
	"giants_mlb": "SFG", "san francisco giants": "SFG",
	"braves": "ATL_ML", "atlanta braves": "ATL_ML",
	"phillies": "PHI_ML", "philadelphia phillies": "PHI_ML",
	"blue jays": "TOR_ML", "toronto blue jays": "TOR_ML",
	"rays": "TB_ML", "tampa bay rays": "TB_ML",
	"orioles": "BAL_ML", "baltimore orioles": "BAL_ML",
	"tigers": "DET_ML", "detroit tigers": "DET_ML",
	"twins": "MIN_ML", "minnesota twins": "MIN_ML",
	"royals": "KC_ML", "kansas city royals": "KC_ML",
	"guardians": "CLE_ML", "cleveland guardians": "CLE_ML",
	"mariners": "SEA_ML", "seattle mariners": "SEA_ML",
	"angels": "LAA", "los angeles angels": "LAA",
	"athletics": "OAK", "oakland athletics": "OAK",
	"padres": "SD", "san diego padres": "SD",
	"rockies": "COL", "colorado rockies": "COL",
	"diamondbacks": "ARI_ML", "arizona diamondbacks": "ARI_ML",
	"brewers": "MIL_ML", "milwaukee brewers": "MIL_ML",
	"reds": "CIN_ML", "cincinnati reds": "CIN_ML",
	"pirates": "PIT_ML", "pittsburgh pirates": "PIT_ML",
	"nationals": "WAS_ML", "washington nationals": "WAS_ML",
	"marlins": "MIA_ML", "miami marlins": "MIA_ML",
	// NHL
	"bruins": "BOS_NH", "boston bruins": "BOS_NH",
	"maple leafs": "TOR_NH", "toronto maple leafs": "TOR_NH",
	"rangers": "NYR", "new york rangers": "NYR",
	"penguins": "PIT_NH", "pittsburgh penguins": "PIT_NH",
	"capitals": "WAS_NH", "washington capitals": "WAS_NH",
	"blackhawks": "CHI_NH", "chicago blackhawks": "CHI_NH",
	"oilers": "EDM", "edmonton oilers": "EDM",
	"flames": "CGY", "calgary flames": "CGY",
	"canucks": "VAN", "vancouver canucks": "VAN",
	"canadiens": "MTL", "montreal canadiens": "MTL",
	"avalanche": "COL_NH", "colorado avalanche": "COL_NH",
	"lightning": "TB_NH", "tampa bay lightning": "TB_NH",
	"panthers_nhl": "FLA", "florida panthers": "FLA",
	"golden knights": "VGK", "vegas golden knights": "VGK",
	"kraken": "SEA_NH", "seattle kraken": "SEA_NH",
}

var (
	reVS          = regexp.MustCompile(`(?i)^(.+?)\s+(?:vs\.?|@|at)\s+(.+?)(?:\s*[-–—].*)?$`)
	reWillBeat    = regexp.MustCompile(`(?i)will\s+(?:the\s+)?(.+?)\s+(?:beat|defeat|win\s+(?:vs?\.?\s+)?(?:against\s+)?)(?:the\s+)?(.+?)(?:\?|$|\s+-\s+)`)
	reWillWin     = regexp.MustCompile(`(?i)will\s+(?:the\s+)?(.+?)\s+win\b`)
	reMLSuffix    = regexp.MustCompile(`(?i)\s+[-–]\s+.*(moneyline|ml|to win|win)\s*$`)
	reSpread      = regexp.MustCompile(`(?i)(\b[+-]\d+\.?\d*\b|\bspread\b|\bcover\b|\bATS\b)`)
	reTotal       = regexp.MustCompile(`(?i)(\bover\b|\bunder\b|\btotal\b|\bo\/u\b|\bover\/under\b)`)
	reSeries      = regexp.MustCompile(`(?i)(\bseries\b|\badvance\b|\bplayoffs?\b|\bchampionship\b|\btitle\b|\bwinner\b|\bwins the\b|\bcup\b|\bsuper bowl\b|\bnba finals\b|\bworld series\b)`)
	reDate        = regexp.MustCompile(`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\w*\s+\d{1,2}`)
	reYear        = regexp.MustCompile(`\b(20\d{2})\b`)
	monthMap      = map[string]string{
		"jan": "01", "feb": "02", "mar": "03", "apr": "04",
		"may": "05", "jun": "06", "jul": "07", "aug": "08",
		"sep": "09", "oct": "10", "nov": "11", "dec": "12",
	}
)

// NormalizeTeam converts any known alias to a canonical code.
func NormalizeTeam(raw string) string {
	cleaned := strings.ToLower(raw)
	cleaned = strings.TrimSpace(cleaned)
	// strip common suffixes
	for _, suffix := range []string{" fc", " sc", " cf", " afc", " utd", " united"} {
		cleaned = strings.TrimSuffix(cleaned, suffix)
	}
	cleaned = strings.TrimSpace(cleaned)
	if code, ok := teamAliases[cleaned]; ok {
		return code
	}
	// partial scan — pick longest alias match
	best := ""
	bestCode := ""
	for alias, code := range teamAliases {
		if strings.Contains(cleaned, alias) && len(alias) > len(best) {
			best = alias
			bestCode = code
		}
	}
	if bestCode != "" {
		return bestCode
	}
	// fallback: uppercase first 6 chars
	upper := strings.ToUpper(cleaned)
	upper = regexp.MustCompile(`[^A-Z0-9]`).ReplaceAllString(upper, "")
	if len(upper) > 6 {
		upper = upper[:6]
	}
	return upper
}

// ExtractTeams tries to pull two team names from a market title.
func ExtractTeams(title string) (teamA, teamB string, ok bool) {
	// Try "Team A vs Team B" pattern first
	if m := reVS.FindStringSubmatch(title); m != nil {
		a := NormalizeTeam(strings.TrimSpace(m[1]))
		b := NormalizeTeam(strings.TrimSpace(m[2]))
		if a != "" && b != "" && a != b {
			return a, b, true
		}
	}
	// Try "Will Team A beat Team B"
	if m := reWillBeat.FindStringSubmatch(title); m != nil {
		a := NormalizeTeam(strings.TrimSpace(m[1]))
		b := NormalizeTeam(strings.TrimSpace(m[2]))
		if a != "" && b != "" && a != b {
			return a, b, true
		}
	}
	// Try "Will Team A win" — only one team extractable, but still useful for series
	if m := reWillWin.FindStringSubmatch(title); m != nil {
		a := NormalizeTeam(strings.TrimSpace(m[1]))
		if a != "" {
			return a, "", false
		}
	}
	return "", "", false
}

// ExtractMarketType detects spread, total, series, or moneyline.
func ExtractMarketType(title string) MarketType {
	if reSeries.MatchString(title) {
		return Series
	}
	if reSpread.MatchString(title) {
		return Spread
	}
	if reTotal.MatchString(title) {
		return Total
	}
	return Moneyline
}

// ExtractDate attempts to pull a YYYY-MM-DD date from a title or ISO string.
// Falls back to parsing the closeTime if titleDate is "".
func ExtractDate(titleHint string, closeTime time.Time) string {
	// Try explicit date in title like "March 18" or "Mar 18"
	if m := reDate.FindStringSubmatch(titleHint); m != nil {
		parts := strings.Fields(m[0])
		if len(parts) == 2 {
			monthKey := strings.ToLower(parts[0])[:3]
			if mo, ok := monthMap[monthKey]; ok {
				day := parts[1]
				if len(day) == 1 {
					day = "0" + day
				}
				year := "2026"
				if yr := reYear.FindString(titleHint); yr != "" {
					year = yr
				} else if !closeTime.IsZero() {
					year = closeTime.Format("2006")
				}
				return year + "-" + mo + "-" + day
			}
		}
	}
	// Fall back to closeTime
	if !closeTime.IsZero() {
		return closeTime.Format("2006-01-02")
	}
	return ""
}

// EnrichMarket fills in normalized fields on a Market in-place.
func EnrichMarket(m *Market) {
	mtype := ExtractMarketType(m.Title)
	m.Type = mtype

	teamA, teamB, ok := ExtractTeams(m.Title)
	if ok {
		m.TeamA = teamA
		m.TeamB = teamB
	}

	m.GameDate = ExtractDate(m.Title, m.CloseTime)
	m.Key = CanonicalKey(m.TeamA, m.TeamB, m.GameDate, m.Type)
}
