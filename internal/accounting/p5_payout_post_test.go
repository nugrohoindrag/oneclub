package accounting

// PRD P5 payouts: the e2e tests asserting the ledger effect of the HRIS
// payout events (FR-REL-P5-04).
func init() {
	postingCoverage[EventServiceChargeDistributed] = []string{"TestP5PayoutsServiceCharge"}
	postingCoverage[EventPayoutPosted] = []string{"TestP5PayoutsCaddyRun", "TestP5PayoutsInstructorRun"}
	postingCoverage[EventPayoutPaid] = []string{"TestP5PayoutsCaddyRun"}
}
