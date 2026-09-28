package collectors

import "testing"

// deriveCategoryQuery is the category probe's entire judgment surface — the
// SERP call itself is a passthrough. Bad derivations search garbage, so the
// skip paths matter as much as the extractions.
func TestDeriveCategoryQuery(t *testing.T) {
	cases := []struct {
		name  string
		title string
		label string
		want  string
	}{
		{
			name:  "brand token with vanity prefix stripped, filler lead-in trimmed",
			title: "Meet Elip, your job agent in WhatsApp",
			label: "tryelip",
			want:  "job agent in WhatsApp",
		},
		{
			name:  "pipe separator keeps the category segment",
			title: "Acme | Invoice automation for freelancers",
			label: "acme",
			want:  "Invoice automation for freelancers",
		},
		{
			name:  "spaced hyphen is a separator",
			title: "Acme - Invoice automation for freelancers",
			label: "acme",
			want:  "Invoice automation for freelancers",
		},
		{
			name:  "in-word hyphen is not a separator",
			title: "Self-serve billing platform for startups",
			label: "acme",
			want:  "Self-serve billing platform for startups",
		},
		{
			name:  "too short after stripping — skip",
			title: "Acme",
			label: "acme",
			want:  "",
		},
		{
			name:  "empty title — skip",
			title: "",
			label: "acme",
			want:  "",
		},
		{
			name:  "single word left — skip",
			title: "Acme Dashboard",
			label: "acme",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveCategoryQuery(tc.title, tc.label); got != tc.want {
				t.Errorf("deriveCategoryQuery(%q, %q) = %q, want %q", tc.title, tc.label, got, tc.want)
			}
		})
	}
}

func TestBuyerIntentQuery(t *testing.T) {
	cases := []struct {
		phrase string
		want   string
		ok     bool
	}{
		{"AI-Native Audit Workspace", "best ai audit software", true},
		{"AI-Powered Accounting Platform", "best ai accounting software", true},
		{"Smart Invoicing Tool", "best invoicing software", true},
		{"All-in-One CRM", "", false}, // no qualifier left before the noun
		{"Cooking Recipes and Tips", "", false},
		{"Audit Software", "best audit software", true},
		{"Marketing Blog", "", false},
	}
	for _, c := range cases {
		got, ok := buyerIntentQuery(c.phrase)
		if ok != c.ok || got != c.want {
			t.Errorf("buyerIntentQuery(%q) = (%q, %v), want (%q, %v)", c.phrase, got, ok, c.want, c.ok)
		}
	}
}
