package main

import "time"

const (
	KalshiFeePct = 0.07
	PolyFeeFlat  = 0.001
	ArbThreshold = 0.93
)

var activeArbThreshold = ArbThreshold

func kalshiFee(p float64) float64 {
	return KalshiFeePct * p * (1 - p)
}

func Check(game MatchedGame) []ArbOpportunity {
	now := time.Now().UTC()
	opps := make([]ArbOpportunity, 0, 2)

	if game.GameTime().IsZero() || now.After(game.GameTime()) {
		return opps
	}

	checkDirection := func(direction, yesPlatform string, yesPrice float64, noPlatform string, noPrice float64, kPrice float64, pPrice float64) {
		if yesPrice <= 0 || noPrice <= 0 {
			return
		}
		combined := yesPrice + noPrice
		if combined >= activeArbThreshold {
			return
		}
		kFee := kalshiFee(kPrice)
		pFee := pPrice * PolyFeeFlat
		gross := 1.0 - combined
		net := gross - kFee - pFee
		if net <= 0 {
			return
		}
		opps = append(opps, ArbOpportunity{
			Game:        game,
			Direction:   direction,
			BuyYesAt:    yesPlatform,
			YesPrice:    yesPrice,
			BuyNoAt:     noPlatform,
			NoPrice:     noPrice,
			Combined:    combined,
			GrossProfit: gross,
			KalshiFee:   kFee,
			PolyFee:     pFee,
			NetProfit:   net,
			SeenAt:      now,
			ExpiresAt:   game.GameTime(),
		})
	}

	checkDirection("K_YES+P_NO", "KALSHI", game.Kalshi.YesBid, "POLY", game.Poly.NoBid, game.Kalshi.YesBid, game.Poly.NoBid)
	checkDirection("K_NO+P_YES", "POLY", game.Poly.YesBid, "KALSHI", game.Kalshi.NoBid, game.Kalshi.NoBid, game.Poly.YesBid)
	return opps
}
