package goanalysis

import (
	"go/types"
	"sync"

	"golang.org/x/tools/go/analysis"
)

// factStore indexes the facts of every action of one analyzer by key,
// so an action reads the facts of its dependencies without copying them.
// Copying every transitive fact into every dependant grows quadratically with the import graph.
// An action removes its own entries when its package is released (see releaseFacts).
type factStore struct {
	objects  sync.Map // objectFactKey -> analysis.Fact
	packages sync.Map // packageFactKey -> analysis.Fact
}

func (r *runner) factStoreFor(analyzer *analysis.Analyzer) *factStore {
	store, ok := r.factStores[analyzer]
	if !ok {
		store = &factStore{}
		r.factStores[analyzer] = store
	}

	return store
}

func (act *action) setObjectFact(key objectFactKey, fact analysis.Fact) {
	act.objectFacts[key] = fact
	act.factStore.objects.Store(key, fact)
}

func (act *action) setPackageFact(key packageFactKey, fact analysis.Fact) {
	act.packageFacts[key] = fact
	act.factStore.packages.Store(key, fact)
}

// resetFacts drops the facts of this action and gives it empty fact maps.
func (act *action) resetFacts() {
	if act.factStore == nil {
		return
	}

	act.removeOwnFactsFromStore()

	act.objectFacts = make(map[objectFactKey]analysis.Fact)
	act.packageFacts = make(map[packageFactKey]analysis.Fact)
}

// releaseFacts drops the facts of this action, and the references to the facts of its dependencies.
func (act *action) releaseFacts() {
	act.removeOwnFactsFromStore()

	act.objectFacts = nil
	act.packageFacts = nil
	act.factDeps = nil
}

func (act *action) removeOwnFactsFromStore() {
	if act.factStore == nil {
		return
	}

	for key := range act.objectFacts {
		act.factStore.objects.Delete(key)
	}

	for key := range act.packageFacts {
		act.factStore.packages.Delete(key)
	}
}

// lookupObjectFact returns the fact of this package, or a fact of a dependency that the dependency exposes to this action.
//
// The facts a dependency exposes are the ones the former copy-based inheritance propagated:
// a package passes on the facts it owns that exportedFrom accepts for it, and every package
// along the import chain passes on the facts that exportedFrom accepts for no package (types, constants, fields, methods).
// An action only gets objects of packages it imports, directly or not, so the dependency is not looked up.
func (act *action) lookupObjectFact(key objectFactKey) (analysis.Fact, bool) {
	if fact, ok := act.objectFacts[key]; ok {
		return fact, true
	}

	if act.factStore == nil || !act.exposesObjectFact(key.obj) {
		return nil, false
	}

	fact, ok := act.factStore.objects.Load(key)
	if !ok {
		return nil, false
	}

	return fact.(analysis.Fact), true //nolint:forcetypeassert // The store only holds facts.
}

func (act *action) lookupPackageFact(key packageFactKey) (analysis.Fact, bool) {
	if fact, ok := act.packageFacts[key]; ok {
		return fact, true
	}

	if act.factStore == nil {
		return nil, false
	}

	fact, ok := act.factStore.packages.Load(key)
	if !ok {
		return nil, false
	}

	return fact.(analysis.Fact), true //nolint:forcetypeassert // The store only holds facts.
}

// exposesObjectFact reports whether a fact about obj, owned by another package, reaches this action.
func (act *action) exposesObjectFact(obj types.Object) bool {
	owner := obj.Pkg()
	if owner == nil || !exportedFrom(obj, owner) {
		return false
	}

	return exportedFrom(obj, nil) || act.importsDirectly(owner)
}

func (act *action) importsDirectly(pkg *types.Package) bool {
	for _, dep := range act.factDeps {
		if dep.Package.Types == pkg {
			return true
		}
	}

	return false
}

// visitInheritedObjectFacts calls visit for every fact about an object of a dependency that is exposed to this action.
func (act *action) visitInheritedObjectFacts(visit func(objectFactKey, analysis.Fact)) {
	act.visitFactDeps(func(dep *action, isDirect bool) {
		for key, fact := range dep.objectFacts {
			if exportedFrom(key.obj, dep.Package.Types) && (isDirect || exportedFrom(key.obj, nil)) {
				visit(key, fact)
			}
		}
	})
}

// visitInheritedPackageFacts calls visit for every package fact of the dependencies.
func (act *action) visitInheritedPackageFacts(visit func(packageFactKey, analysis.Fact)) {
	act.visitFactDeps(func(dep *action, _ bool) {
		for key, fact := range dep.packageFacts {
			visit(key, fact)
		}
	})
}

// visitFactDeps calls visit once for each action this action transitively depends on through the same analyzer.
func (act *action) visitFactDeps(visit func(dep *action, isDirect bool)) {
	visited := make(map[*action]bool, len(act.factDeps))
	queue := make([]*action, 0, len(act.factDeps))

	for _, dep := range act.factDeps {
		if !visited[dep] {
			visited[dep] = true
			queue = append(queue, dep)
			visit(dep, true)
		}
	}

	for next := 0; next < len(queue); next++ {
		for _, dep := range queue[next].factDeps {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
				visit(dep, false)
			}
		}
	}
}
