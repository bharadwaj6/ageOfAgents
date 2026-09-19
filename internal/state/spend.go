package state

import (
	"maps"
	"slices"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Charge is one agent attempt's spend, as the event that closed the attempt
// records it.
type Charge struct {
	Tokens  int     // tokens the attempt consumed; 0 when unknown
	Model   string  // model that consumed them; "" when unknown
	CostUSD float64 // what the harness reported the attempt cost; 0 when it reports none
}

// ChargeOf returns the spend e records and the ticket it is charged to. The
// events that close an attempt carry it — a proposal, a decomposition, a
// failure, a retry — and ok is false for any other event, or one that records
// no spend. It is the one place the Event Log is read for spend: replay
// charges through it, and so does every window a budget is checked over, so
// the spend governor, the budgets and `aoa status` cannot disagree (ADR 017).
func ChargeOf(e api.Event) (ticketID string, c Charge, ok bool) {
	switch e.Type {
	case api.ProposalSubmitted, api.TicketDecomposed, api.TicketFailed, api.WorkerRestarted:
	default:
		return "", Charge{}, false
	}
	// The four payloads share these keys; one decode reads them all.
	var p struct {
		TicketID string  `json:"ticket_id"`
		Tokens   int     `json:"tokens"`
		Model    string  `json:"model"`
		CostUSD  float64 `json:"cost_usd"`
	}
	if e.DecodePayload(&p) != nil {
		return "", Charge{}, false
	}
	c = Charge{Tokens: p.Tokens, Model: p.Model, CostUSD: p.CostUSD}
	return p.TicketID, c, c.Tokens > 0 || c.CostUSD > 0
}

// Spend is what agent attempts consumed. Replay keeps one per ticket, per Goal
// and for the whole log, each charged through Add.
type Spend struct {
	TokensSpent     int            // every token charged
	TokensByModel   map[string]int // TokensSpent by model; tokens with no model count only in TokensSpent
	CostUSDReported float64        // the cost harnesses reported for their own attempts
	// TokensToPrice holds, by model, the tokens of attempts that reported no
	// cost of their own: the only spend [pricing] prices.
	TokensToPrice map[string]int
}

// Add charges one attempt's spend.
func (sp *Spend) Add(c Charge) {
	if c.CostUSD > 0 {
		sp.CostUSDReported += c.CostUSD
	}
	if c.Tokens <= 0 {
		return
	}
	sp.TokensSpent += c.Tokens
	if c.Model == "" {
		return
	}
	if sp.TokensByModel == nil {
		sp.TokensByModel = map[string]int{}
	}
	sp.TokensByModel[c.Model] += c.Tokens
	if c.CostUSD == 0 {
		if sp.TokensToPrice == nil {
			sp.TokensToPrice = map[string]int{}
		}
		sp.TokensToPrice[c.Model] += c.Tokens
	}
}

// CostUSD is the spend in dollars: the cost the harnesses reported, plus
// pricing (USD per million tokens, by model id) applied to the tokens of the
// attempts that reported none. A model pricing does not name adds nothing.
func (sp Spend) CostUSD(pricing map[string]float64) float64 {
	return sp.CostUSDReported + USD(sp.TokensToPrice, pricing)
}

// USD converts a per-model token tally into dollars using a price map of USD
// per *million* tokens. Models absent from the price map contribute 0, so an
// unpriced run reports $0 rather than a wrong number. Models are summed in
// name order: floating-point addition is not associative, and a cost that
// differed in its last digit from one call to the next would make `aoa status
// --json` differ between two reads of the same log.
func USD(tokensByModel map[string]int, pricePerMTok map[string]float64) float64 {
	var total float64
	for _, model := range slices.Sorted(maps.Keys(tokensByModel)) {
		total += float64(tokensByModel[model]) / 1e6 * pricePerMTok[model]
	}
	return total
}
