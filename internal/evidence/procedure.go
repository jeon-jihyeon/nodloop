package evidence

// One procedure document split into citable paragraphs
type Procedure struct {
	// File name without the extension
	// The first part of every paragraph id of the file
	Slug string
	File string
	// Empty means every event
	Scope      Scope
	Paragraphs []Paragraph
}

// Procedures in file name order
type Procedures []Procedure

// The procedures whose scope fits the event in their order
// The metrics are the observed ones so a procedure that reads a data gap stays with the event that has one
func (ps Procedures) Applicable(changeContext Context, observed []string) Procedures {
	out := Procedures{}
	for _, p := range ps {
		if p.Scope.Matches(changeContext, observed) {
			out = append(out, p)
		}
	}
	return out
}

// Every paragraph in procedure order
func (ps Procedures) Paragraphs() []Paragraph {
	var out []Paragraph
	for _, p := range ps {
		out = append(out, p.Paragraphs...)
	}
	return out
}

func (ps Procedures) Slugs() []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}
