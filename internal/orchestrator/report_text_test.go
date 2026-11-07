package orchestrator

import "testing"

// Run 2d18e5e5 rendered "bachelor&\\#39;s" and "&\\#34;partial&\\#34;": the
// text was HTML-escaped and then Markdown-escaped twice.
func TestReportTextDecodesEntitiesBeforeEscaping(t *testing.T) {
	for input, want := range map[string]string{
		"70% (bachelor's)":                  "70% (bachelor's)",
		"Bachelor&#39;sMaster&#39;s":        "Bachelor'sMaster's",
		"&#34;partial&#34; scholarships":    `"partial" scholarships`,
		"Tom &amp;amp; Jerry":               "Tom &amp; Jerry",
		"Fully Funded | Truescho [2026] #1": `Fully Funded \| Truescho \[2026\] \#1`,
		"<script>alert(1)</script>":         "&lt;script&gt;alert(1)&lt;/script&gt;",
		"a_b *c*  \n d":                     `a\_b \*c\* d`,
	} {
		if got := reportText(input); got != want {
			t.Fatalf("reportText(%q) = %q, want %q", input, got, want)
		}
	}
}
