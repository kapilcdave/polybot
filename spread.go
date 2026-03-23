package main

import (
	"strings"
	"time"
)

const (
	KalshiFeePct = 0.07
	PolyFeeFlat  = 0.001
	ArbThreshold = 0.93
)

var activeArbThreshold = ArbThreshold
var activeKalshiFeePct = KalshiFeePct
var activePolyFeeFlat = PolyFeeFlat

type pricedLeg struct {
	Platform string
	Side     string
	Price    float64
	Team     string
}

func kalshiFee(p float64) float64 {
	return activeKalshiFeePct * p * (1 - p)
}

func Check(game MatchedGame) []ArbOpportunity {
	now := time.Now().UTC()
	opps := make([]ArbOpportunity, 0, 4)

	if game.GameTime().IsZero() || now.After(game.GameTime()) {
		return opps
	}
	if now.Sub(game.Kalshi.UpdatedAt) > 60*time.Second || now.Sub(game.Poly.UpdatedAt) > 60*time.Second {
		return opps
	}

	teamA := normalizeTeamNameWithLeague(game.HomeTeam, game.League)
	teamB := normalizeTeamNameWithLeague(game.AwayTeam, game.League)
	if teamA == "" || teamB == "" || teamA == teamB {
		return opps
	}

	kalshiLegs := executableLegs(game.Kalshi)
	polyLegs := executableLegs(game.Poly)
	for _, kLeg := range kalshiLegs {
		for _, pLeg := range polyLegs {
			kTeam := normalizeTeamNameWithLeague(kLeg.Team, game.League)
			pTeam := normalizeTeamNameWithLeague(pLeg.Team, game.League)
			if !coversBinaryMatchup(kTeam, pTeam, teamA, teamB) {
				continue
			}
			combined := kLeg.Price + pLeg.Price
			if combined >= activeArbThreshold {
				continue
			}
			kFee := kalshiFee(kLeg.Price)
			pFee := pLeg.Price * activePolyFeeFlat
			gross := 1.0 - combined
			net := gross - kFee - pFee
			if net <= 0 {
				continue
			}
			opps = append(opps, ArbOpportunity{
				Game:         game,
				Direction:    kLeg.Side + "+" + pLeg.Side,
				Leg1Platform: kLeg.Platform,
				Leg1Side:     kLeg.Side,
				Leg1Price:    kLeg.Price,
				Leg1Team:     kLeg.Team,
				Leg2Platform: pLeg.Platform,
				Leg2Side:     pLeg.Side,
				Leg2Price:    pLeg.Price,
				Leg2Team:     pLeg.Team,
				Combined:     combined,
				GrossProfit:  gross,
				KalshiFee:    kFee,
				PolyFee:      pFee,
				NetProfit:    net,
				SeenAt:       now,
				ExpiresAt:    game.GameTime(),
			})
		}
	}
	return opps
}

func executableLegs(market SportsMarket) []pricedLeg {
	yesTeam := strings.TrimSpace(market.YesTeam)
	noTeam := opposingTeam(market, yesTeam)
	legs := make([]pricedLeg, 0, 2)
	if market.YesAsk > 0 && yesTeam != "" {
		legs = append(legs, pricedLeg{
			Platform: market.Platform,
			Side:     "yes",
			Price:    market.YesAsk,
			Team:     yesTeam,
		})
	}
	if market.NoAsk > 0 && noTeam != "" {
		legs = append(legs, pricedLeg{
			Platform: market.Platform,
			Side:     "no",
			Price:    market.NoAsk,
			Team:     noTeam,
		})
	}
	return legs
}

func opposingTeam(market SportsMarket, team string) string {
	switch {
	case sameNormalizedTeam(team, market.HomeTeam, market.League):
		return market.AwayTeam
	case sameNormalizedTeam(team, market.AwayTeam, market.League):
		return market.HomeTeam
	default:
		return ""
	}
}

func coversBinaryMatchup(leftTeam, rightTeam, teamA, teamB string) bool {
	if leftTeam == "" || rightTeam == "" || leftTeam == rightTeam {
		return false
	}
	return (leftTeam == teamA && rightTeam == teamB) || (leftTeam == teamB && rightTeam == teamA)
}
