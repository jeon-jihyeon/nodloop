package extract

// How a lesson stands to the approved items the run reaches
type Relation string

const (
	RelationAdd       Relation = "add"       // no item says it
	RelationUpdate    Relation = "update"    // an item says part of it
	RelationDuplicate Relation = "duplicate" // an item already says it
	RelationConflict  Relation = "conflict"  // an item says the opposite
)

func (r Relation) Valid() bool {
	switch r {
	case RelationAdd, RelationUpdate, RelationDuplicate, RelationConflict:
		return true
	}
	return false
}

// Only add and update become a candidate
// A duplicate and a conflict leave the decision on the named item to a person
func (r Relation) proposes() bool {
	return r == RelationAdd || r == RelationUpdate
}
