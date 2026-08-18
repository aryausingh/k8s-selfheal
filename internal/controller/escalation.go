package controller

// ShouldEscalate implements the Task 6 policy: a remediation action may run
// automatically only when the classifier (Subhashini's package) returned a
// valid proposal with SafeForAutomation == true. Everything else escalates
// instead of acting — never the other way around.
//
// Confirmed with Subhashini on 2026-08-18:
//   - Her response type is a plain `SafeForAutomation bool` (not *bool), so
//     "missing" isn't representable once a Proposal value exists — her
//     validator rejects malformed/incomplete responses before they ever
//     become a Proposal, rather than handing back a zero-value bool for us
//     to misread as an explicit "false".
//   - A *valid* false is a real classification result ("don't automate
//     this one") and escalates.
//   - A malformed/invalid response never reaches here as a Proposal at all
//     — it surfaces as classifyErr instead, and also escalates.
//
// classifyErr is whatever the eventual call into her classifier returns
// when it fails to produce a valid Proposal (transport error, or her
// validator rejecting the response). This function takes the two
// already-decided inputs rather than her concrete Proposal/Target structs
// so it doesn't duplicate a type definition that isn't in this repo yet —
// once her package lands, wiring is `ShouldEscalate(proposal.SafeForAutomation, err)`
// at the call site, no change needed here.
func ShouldEscalate(safeForAutomation bool, classifyErr error) bool {
	if classifyErr != nil {
		return true
	}
	return !safeForAutomation
}
