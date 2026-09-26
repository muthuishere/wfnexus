package workflow

import (
	"fmt"
	"strings"
)

// Category is the kind of work a project does and a template serves — the
// same list for both, so a Finance project can be offered Finance templates
// first. It is deliberately a closed list: a free-text field drifts into
// "finance", "Finance", "fin-ops" and "accounting" within a month, and then
// nothing can be offered to anything.
type Category struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Categories, in the order a picker shows them: the work this platform is
// used for most first.
var Categories = []Category{
	{"development", "Development", "writing, reviewing, testing and shipping code"},
	{"debugging-ops", "Debugging & Ops", "incidents, CI failures, on-call, infrastructure"},
	{"security", "Security", "vulnerabilities, compliance evidence, threat intel"},
	{"data", "Data & Analytics", "analysis, reporting, data quality"},
	{"product-design", "Product & Design", "feedback, specs, research synthesis, UX"},
	{"support", "Customer Support", "tickets, escalations, knowledge base"},
	{"sales", "Sales", "leads, research, proposals, CRM hygiene"},
	{"marketing", "Marketing & Content", "campaigns, content, SEO, brand review"},
	{"finance", "Finance & Accounting", "close, reconciliation, invoices, audit, tax"},
	{"legal", "Legal & Compliance", "contracts, research, regulatory change"},
	{"hr", "HR & Recruiting", "hiring, onboarding, employee questions"},
	{"operations", "Operations & Procurement", "suppliers, purchasing, logistics, facilities"},
	{"healthcare", "Healthcare & Life Sciences", "clinical documentation, claims, trials"},
	{"research", "Research & Science", "literature, experiments, papers, grants"},
	{"education", "Education", "courses, grading, student support"},
	{"public-sector", "Public Sector", "permits, benefits, public records, grants"},
	{"general", "General", "anything that fits none of the above"},
}

// ValidCategory accepts "" (not set) or one of Categories.
func ValidCategory(id string) error {
	if id == "" {
		return nil
	}
	ids := make([]string, 0, len(Categories))
	for _, c := range Categories {
		if c.ID == id {
			return nil
		}
		ids = append(ids, c.ID)
	}
	return fmt.Errorf("unknown category %q (one of: %s)", id, strings.Join(ids, ", "))
}
