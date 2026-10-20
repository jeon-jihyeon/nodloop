package extract

// How a lesson stands to the approved items and the waiting candidates the run reaches
type Relation string

const (
	RelationAdd       Relation = "add"       // no item or candidate says it
	RelationUpdate    Relation = "update"    // an item or a candidate says part of it
	RelationDuplicate Relation = "duplicate" // an item or a candidate already says it
	RelationConflict  Relation = "conflict"  // an item or a candidate says the opposite
)

func (r Relation) Valid() bool {
	switch r {
	case RelationAdd, RelationUpdate, RelationDuplicate, RelationConflict:
		return true
	}
	return false
}

// Only add and update become a new candidate version
// A duplicate and a conflict leave the decision on what they name to a person
func (r Relation) proposes() bool {
	return r == RelationAdd || r == RelationUpdate
}
