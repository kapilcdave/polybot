package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func RenderScan(w io.Writer, state AppState) {
	result := state.LastScan
	fmt.Fprintf(w, "Weatherbot Scan  %s\n", result.CompletedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(w, "Markets: %d  Forecasts: %d  Opportunities: %d\n", result.MarketsSeen, result.LastForecasts, len(result.Opps))
	fmt.Fprintf(w, "Simulation: bankroll=$%.2f cash=$%.2f equity=$%.2f trades=%d\n",
		state.Sim.BankrollUSD, state.Sim.CashUSD, state.Sim.EquityUSD, state.Sim.Trades)
	fmt.Fprintln(w)

	if len(result.Opps) == 0 {
		fmt.Fprintln(w, "No opportunities.")
	} else {
		fmt.Fprintln(w, "Opportunities")
		fmt.Fprintln(w, "------------")
		opps := append([]Opportunity(nil), result.Opps...)
		sort.Slice(opps, func(i, j int) bool { return opps[i].Edge > opps[j].Edge })
		for _, opp := range opps {
			fmt.Fprintf(w, "%-6s %-11s %-3s %.1fF  ask=%5.1f%% fair=%5.1f%% edge=%5.1f%% stake=$%6.2f ev=$%5.2f  %s\n",
				opp.Market.Platform,
				opp.Market.City,
				strings.ToUpper(opp.Side),
				opp.Market.ThresholdF,
				opp.Ask*100,
				opp.Fair*100,
				opp.Edge*100,
				opp.StakeUSD,
				opp.ExpectedEV,
				opp.Market.EventDate.Format("2006-01-02"),
			)
		}
	}

	if len(result.Errors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Errors")
		fmt.Fprintln(w, "------")
		for _, err := range result.Errors {
			fmt.Fprintf(w, "- %s\n", err)
		}
	}
}
