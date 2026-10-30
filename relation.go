package pod

// This file holds the generic form of the relation that almost every pod-backed
// application needs: a directed edge between two record ids, with a kind and an
// origin. Applications model their own richer link records; this is the shape
// the *views* need, and the index answers the two questions a detail card asks:
//
//	"what does this record point at?"   Outbound(id)
//	"what points at this record?"       Inbound(id)
//
// A relation has no direction for rendering purposes: an incoming edge and an
// outgoing edge are both "links to this record". Both returns them together.

// Relation is one directed edge between two records.
type Relation struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation,omitempty"`
	Origin   string `json:"origin,omitempty"` // auto | human
}

// RelationIndex groups relations by both ends. It is read-only after New.
type RelationIndex struct {
	outbound map[string][]Relation
	inbound  map[string][]Relation
	all      []Relation
}

// NewRelationIndex indexes rels. Empty ids are ignored so a partially written
// link cannot create a phantom entry.
func NewRelationIndex(rels []Relation) *RelationIndex {
	ix := &RelationIndex{
		outbound: map[string][]Relation{},
		inbound:  map[string][]Relation{},
	}
	for _, r := range rels {
		if r.From == "" || r.To == "" {
			continue
		}
		ix.all = append(ix.all, r)
		ix.outbound[r.From] = append(ix.outbound[r.From], r)
		ix.inbound[r.To] = append(ix.inbound[r.To], r)
	}
	return ix
}

// Outbound returns the relations whose From is id.
func (ix *RelationIndex) Outbound(id string) []Relation {
	if ix == nil {
		return nil
	}
	return ix.outbound[id]
}

// Inbound returns the relations whose To is id.
func (ix *RelationIndex) Inbound(id string) []Relation {
	if ix == nil {
		return nil
	}
	return ix.inbound[id]
}

// Both returns every relation that names id, incoming first then outgoing.
// A self-relation appears once.
func (ix *RelationIndex) Both(id string) []Relation {
	if ix == nil {
		return nil
	}
	out := make([]Relation, 0, len(ix.inbound[id])+len(ix.outbound[id]))
	seen := map[Relation]bool{}
	for _, r := range ix.inbound[id] {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, r := range ix.outbound[id] {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Other returns the id at the far end of r from id. It is the id a view should
// link to when it renders a relation on id's card.
func (ix *RelationIndex) Other(r Relation, id string) string {
	if r.From == id {
		return r.To
	}
	return r.From
}

// All returns every indexed relation in the order given.
func (ix *RelationIndex) All() []Relation {
	if ix == nil {
		return nil
	}
	return ix.all
}
