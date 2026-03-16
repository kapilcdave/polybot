package main

import "time"

const (
	KalshiFeePct = 0.07
	PolyFeeFlat  = 0.001
	ArbThreshold = 0.95
)

var activeArbThreshold = ArbThreshold

func kalshiFee(p float64) float64 {
	return KalshiFeePct * p * (1 - p)
}

func Check(game MatchedGame) []ArbOpportunity {
	now := time.Now().UTC()
	opps := make([]ArbOpportunity, 0, 2)

	checkDirection := func(direction, yesPlatform, noPlatform string, yesPrice, noPrice, kalshiPrice, polyPrice float64) {
		combined := yesPrice + noPrice
		if combined >= activeArbThreshold {
			return
		}
		kFee := kalshiFee(kalshiPrice)
		pFee := polyPrice * PolyFeeFlat
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
			ExpiresAt:   game.Kalshi.GameTime,
		})
	}

	checkDirection("K_YES+P_NO", "KALSHI", "POLY", game.Kalshi.YesBid, game.Poly.NoBid, game.Kalshi.YesBid, game.Poly.NoBid)
	checkDirection("K_NO+P_YES", "POLY", "KALSHI", game.Poly.YesBid, game.Kalshi.NoBid, game.Kalshi.NoBid, game.Poly.YesBid)

	return opps
}
