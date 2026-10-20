package extract

// How a lesson stands to the approved items, the waiting candidates and the retired versions the run reaches
type Relation string

const (
	RelationAdd       Relation = "add"       // no item, candidate or retired version says it
	RelationUpdate    Relation = "update"    // an item or a candidate says part of it
	RelationDuplicate Relation = "duplicate" // an item, a candidate or a retired version already says it
	RelationConflict  Relation = "conflict"  // an item or a candidate says the opposite
)

func (r Relation) Valid() bool {
	switch r {
	case RelationAdd, RelationUpdate, RelationDuplicate, RelationConflict:
		return true
	}
	return false
}
