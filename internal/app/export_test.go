package app

import "skillhub/internal/manage/engine"

// EnginePlanForTest is the engine plan behind a proposal, for tests that
// play the engine's part directly (a crash after Begin).
func (a *App) EnginePlanForTest(p Proposal) (engine.Plan, error) { return p.plan, nil }

// RefusalForTest is refusal, for tests of the mapping of a planner error
// that the app's own composition cannot produce.
func RefusalForTest(err error) error { return refusal(err) }
