// Package invariant defines the correctness properties the orchestrator must
// preserve and checks them against an event history. It is the hermetic,
// Jepsen-style core of the test suite: define invariants, then assert they hold
// across many randomized fault/crash histories (see the orchestrator chaos
// tests). The checks are pure functions of the Event Log (the single source of
// truth, ADR 001) plus, for main-is-green, the integration repo.
//
// The invariants map to docs/design/metrics.md's litmus test:
//   - I1 merge correctness   -> MergeImpliesVerified + MainGreen
//   - I2 serial single-writer -> MergedAtMostOnceByQueue
//   - I3 replay fidelity      -> ReplayDeterministicAndTotal
//   - I4 no step repetition   -> NoDuplicateMergedKey
//   - I5 DAG acyclic          -> AcyclicGraph
//   - I6 liveness             -> Settled (asserted on completed runs)
package invariant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Violation is a single breach of an invariant.
type Violation struct {
	Invariant string
	Detail    string
}

func (v Violation) String() string { return v.Invariant + ": " + v.Detail }

// Check runs every pure (event-only) invariant over the history and returns all
// violations found. These require no git repo and are fully deterministic.
func Check(events []api.Event) []Violation {
	var vs []Violation
	vs = append(vs, MonotonicGaplessSeq(events)...)
	vs = append(vs, MergeImpliesVerified(events)...)
	vs = append(vs, MergedAtMostOnceByQueue(events)...)
	vs = append(vs, NoDuplicateMergedKey(events)...)
	vs = append(vs, AcyclicGraph(events)...)
	vs = append(vs, ReplayDeterministicAndTotal(events)...)
	vs = append(vs, ApprovalGate(events)...)
	return vs
}

// MonotonicGaplessSeq asserts the log's sequence numbers are exactly 1..N in
// order — the event log's structural integrity (no gaps, no reordering).
func MonotonicGaplessSeq(events []api.Event) []Violation {
	var vs []Violation
	for i, e := range events {
		if e.Seq != i+1 {
			vs = append(vs, Violation{"MonotonicGaplessSeq",
				fmt.Sprintf("event %d has seq %d, want %d", i, e.Seq, i+1)})
		}
	}
	return vs
}

// MergeImpliesVerified asserts no ticket is Merged unless the objective Gate
// passed for the proposal being merged (ADR 002). The queue emits
// VerificationPassed immediately before Merged, so a passing verify must exist
// after the latest proposal and before the merge.
func MergeImpliesVerified(events []api.Event) []Violation {
	var vs []Violation
	propSeq := map[string]int{}
	passSeq := map[string]int{}
	for _, e := range events {
		id := ticketID(e)
		switch e.Type {
		case api.ProposalSubmitted:
			propSeq[id] = e.Seq
		case api.VerificationPassed:
			passSeq[id] = e.Seq
		case api.Merged:
			if passSeq[id] == 0 || passSeq[id] < propSeq[id] {
				vs = append(vs, Violation{"MergeImpliesVerified",
					fmt.Sprintf("ticket %q merged (seq %d) without a passing verify for its proposal", id, e.Seq)})
			}
		}
	}
	return vs
}

// MergedAtMostOnceByQueue asserts main has a single writer (the queue) and each
// ticket merges at most once — the serialization property (ADR 002/003).
func MergedAtMostOnceByQueue(events []api.Event) []Violation {
	var vs []Violation
	merged := map[string]bool{}
	for _, e := range events {
		if e.Type != api.Merged {
			continue
		}
		id := ticketID(e)
		if merged[id] {
			vs = append(vs, Violation{"MergedAtMostOnceByQueue", fmt.Sprintf("ticket %q merged more than once", id)})
		}
		merged[id] = true
		if e.Actor != "orchestrator" {
			vs = append(vs, Violation{"MergedAtMostOnceByQueue",
				fmt.Sprintf("ticket %q merged by actor %q, want orchestrator", id, e.Actor)})
		}
	}
	return vs
}

// NoDuplicateMergedKey asserts no two distinct merged tickets share an
// idempotency key — directly checking the no-step-repetition property (ADR 001;
// MAST's largest single failure mode).
func NoDuplicateMergedKey(events []api.Event) []Violation {
	var vs []Violation
	keyOf := map[string]string{} // ticketID -> idempotency key
	for _, e := range events {
		if e.Type != api.TicketCreated {
			continue
		}
		var p api.TicketCreatedPayload
		if e.DecodePayload(&p) == nil && p.IdempotencyKey != "" {
			keyOf[p.TicketID] = p.IdempotencyKey
		}
	}
	mergedKey := map[string]string{} // key -> first merged ticket
	for _, e := range events {
		if e.Type != api.Merged {
			continue
		}
		id := ticketID(e)
		key := keyOf[id]
		if key == "" {
			continue
		}
		if prev, ok := mergedKey[key]; ok && prev != id {
			vs = append(vs, Violation{"NoDuplicateMergedKey",
				fmt.Sprintf("idempotency key %q merged for both %q and %q", key, prev, id)})
		} else if !ok {
			mergedKey[key] = id
		}
	}
	return vs
}

// ApprovalGate asserts the human-in-the-loop gate is honored: any ticket for
// which approval was requested must have been granted before it merges (ADR
// 008). This holds regardless of config — if a proposal was ever parked for
// approval, it cannot reach main without an explicit ApprovalGranted.
func ApprovalGate(events []api.Event) []Violation {
	var vs []Violation
	requested := map[string]bool{}
	granted := map[string]bool{}
	for _, e := range events {
		id := ticketID(e)
		switch e.Type {
		case api.ApprovalRequested:
			requested[id] = true
		case api.ApprovalGranted:
			granted[id] = true
		case api.Merged:
			if requested[id] && !granted[id] {
				vs = append(vs, Violation{"ApprovalGate",
					fmt.Sprintf("ticket %q merged (seq %d) after approval was requested but never granted", id, e.Seq)})
			}
		}
	}
	return vs
}

// AcyclicGraph asserts the task graph is a DAG at the end of the history — a
// cycle would deadlock dependency readiness (the emergent-graph guard, ADR 006).
func AcyclicGraph(events []api.Event) []Violation {
	s, err := state.Fold(events)
	if err != nil {
		return []Violation{{"AcyclicGraph", "fold failed: " + err.Error()}}
	}
	edges := map[string][]string{}
	for id, t := range s.Tickets {
		edges[id] = t.DependsOn
	}
	if state.HasCycle(edges) {
		return []Violation{{"AcyclicGraph", "dependency graph contains a cycle"}}
	}
	return nil
}

// ReplayDeterministicAndTotal asserts folding the log is deterministic (same
// input, same state) and total (every prefix folds without error) — the
// event-replay fidelity property (ADR 001).
func ReplayDeterministicAndTotal(events []api.Event) []Violation {
	var vs []Violation
	for k := 0; k <= len(events); k++ {
		if _, err := state.Fold(events[:k]); err != nil {
			vs = append(vs, Violation{"ReplayDeterministicAndTotal",
				fmt.Sprintf("prefix of length %d failed to fold: %v", k, err)})
			return vs
		}
	}
	a, errA := state.Fold(events)
	b, errB := state.Fold(events)
	if errA != nil || errB != nil {
		return append(vs, Violation{"ReplayDeterministicAndTotal", "full fold errored"})
	}
	if summarize(a) != summarize(b) {
		vs = append(vs, Violation{"ReplayDeterministicAndTotal", "two folds of the same log differ"})
	}
	return vs
}

// Settled asserts the run reached a terminal state: every goal has at least one
// ticket and every ticket is terminal (merged/failed/decomposed). This is the
// liveness property and is asserted on histories from completed runs.
func Settled(events []api.Event) []Violation {
	s, err := state.Fold(events)
	if err != nil {
		return []Violation{{"Settled", "fold failed: " + err.Error()}}
	}
	var vs []Violation
	for id := range s.Goals {
		has := false
		for _, t := range s.Tickets {
			if t.GoalID == id {
				has = true
				break
			}
		}
		if !has {
			vs = append(vs, Violation{"Settled", fmt.Sprintf("goal %q has no tickets", id)})
		}
	}
	for _, t := range s.Tickets {
		if !t.Status.IsTerminal() {
			vs = append(vs, Violation{"Settled", fmt.Sprintf("ticket %q is %s, not terminal", t.ID, t.Status)})
		}
	}
	return vs
}

// MainGreen asserts the integration repo passes the Gate now — the actual
// "main is never red" property, checked against git rather than the log.
func MainGreen(ctx context.Context, v verify.Verifier, dir string) []Violation {
	if res := v.Run(ctx, dir); !res.Passed {
		return []Violation{{"MainGreen", "gate failed on main: " + res.Failed}}
	}
	return nil
}

// summarize renders a canonical, comparable snapshot of derived state.
func summarize(s *state.State) string {
	lines := make([]string, 0, len(s.Tickets)+len(s.Goals))
	for id, g := range s.Goals {
		lines = append(lines, "goal "+id+" "+g.Text)
	}
	for id, t := range s.Tickets {
		lines = append(lines, fmt.Sprintf("ticket %s %s attempts=%d depth=%d deps=%v children=%v",
			id, t.Status, t.Attempts, t.Depth, t.DependsOn, t.Children))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + fmt.Sprintf("\nlastSeq=%d", s.LastSeq)
}

// ticketID extracts a ticket_id from any payload that carries one.
func ticketID(e api.Event) string {
	if len(e.Payload) == 0 {
		return ""
	}
	var m struct {
		TicketID string `json:"ticket_id"`
	}
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		return ""
	}
	return m.TicketID
}
