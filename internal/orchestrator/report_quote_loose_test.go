package orchestrator

import "testing"

func TestReportQuoteSupportedToleratesTypography(t *testing.T) {
	excerpt := "Программа “Stipendium Hungaricum” — полностью финансируемая.\nBachelor’s program: 110 slots\nMaster’s program: 90 slots\nApplications close on 15 January 2026."
	for _, quote := range []string{
		"Программа «Stipendium Hungaricum» - полностью финансируемая.",
		"Bachelor's program: 110 slots, Master's program: 90 slots",
		"Bachelor’s program: 110 slots ... Applications close on 15 January 2026",
	} {
		if !reportQuoteSupported(quote, excerpt) {
			t.Errorf("faithful quote rejected: %q", quote)
		}
	}
	for _, quote := range []string{
		"Bachelor's program: 120 slots",                          // changed number
		"Applications close on 15 January 2027",                  // changed year
		"Master’s program: 90 slots ... Bachelor’s program: 110", // segments out of order
		"полностью ... 110 slots",                                // segment too short
		"Программа финансируется правительством Венгрии полностью", // paraphrase
	} {
		if reportQuoteSupported(quote, excerpt) {
			t.Errorf("unfaithful quote accepted: %q", quote)
		}
	}
}
