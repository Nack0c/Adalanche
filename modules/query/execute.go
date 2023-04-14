package query

import (
	"sort"

	"github.com/lkarlslund/adalanche/modules/engine"
)

type IndexSelectorInfo struct {
	a          engine.Attribute
	match      string
	results    engine.ObjectSlice
	queryIndex int
}

func Execute(q NodeFilter, ao *engine.Objects) *engine.Objects {
	var potentialindexes []IndexSelectorInfo
	switch t := q.(type) {
	case AndQuery:
		// Iterate over all subitems
		for _, st := range t.Subitems {
			if qo, ok := st.(FilterOneAttribute); ok {
				if sm, ok := qo.q.(hasStringMatch); ok {
					// This might be in an index
					potentialindexes = append(potentialindexes, IndexSelectorInfo{
						a:     qo.a,
						match: sm.m,
					})
				}
			}
		}
	case FilterOneAttribute:
		qo := t
		if sm, ok := qo.q.(hasStringMatch); ok {
			// This might be in an index
			potentialindexes = append(potentialindexes, IndexSelectorInfo{
				a:          qo.a,
				match:      sm.m,
				queryIndex: -1,
			})
		}
	}

	// No optimization possible
	if len(potentialindexes) == 0 {
		return ao.Filter(q.Evaluate)
	}

	for i, potentialIndex := range potentialindexes {
		index := ao.GetIndex(potentialIndex.a)
		foundObjects, found := index.Lookup(engine.AttributeValueString(potentialIndex.match))
		if found {
			potentialindexes[i].results = foundObjects
		}
	}

	sort.Slice(potentialindexes, func(i, j int) bool {
		return potentialindexes[i].results.Len() < potentialindexes[j].results.Len()
	})

	for _, foundindex := range potentialindexes {
		if foundindex.results.Len() != 0 {
			filteredobjects := engine.NewObjects()

			// best working index is first
			if foundindex.queryIndex == -1 {
				// not an AND query with subitems

				foundindex.results.Iterate(func(o *engine.Object) bool {
					filteredobjects.Add(o)
					return true
				})
			} else {
				// can be optimized by patching out the index matched query filter (remove queryIndex item from filter)
				foundindex.results.Iterate(func(o *engine.Object) bool {
					if q.Evaluate(o) {
						filteredobjects.Add(o)
					}
					return true
				})
			}

			return filteredobjects
		}
	}

	// Return unoptimized filter
	return ao.Filter(q.Evaluate)
}
