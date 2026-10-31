package goanalysis

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

type testFact struct{ Name string }

func (*testFact) AFact() {}

var testFactAnalyzer = &analysis.Analyzer{
	Name:      "factsequivalence",
	Doc:       "test analyzer",
	FactTypes: []analysis.Fact{new(testFact)},
}

const (
	factsFixtureLeaf = `package q

import "io"

type T struct{ X int }

func (T) M()  {}
func (T) m()  {}
func F()      {}
func g()      {}

const C = 1
const c = 2

var V int
var v int

type I interface{ Do() }

type embedsReader struct{ io.Reader }

func NewT() T { return T{} }
`

	factsFixtureLeft = `package left

import "q"

type U struct{ q.T }

func H() q.T { return q.NewT() }
`

	factsFixtureRight = `package right

import "q"

type W struct{ *q.T }

func K() {}
`

	factsFixtureTop = `package top

import (
	"left"
	"right"
)

type Both struct {
	left.U
	right.W
}
`

	factsFixtureOther = `package other

import (
	"left"
	"q"
)

var _ q.T
var _ left.U
`
)

type factsFixture struct {
	packages map[string]*types.Package
	actions  map[string]*action
	// order lists the packages dependencies first.
	order []string
}

type fixtureImporter struct {
	fallback types.Importer
	checked  map[string]*types.Package
}

func (i fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := i.checked[path]; ok {
		return pkg, nil
	}

	return i.fallback.Import(path)
}

// newFactsFixture type-checks packages q <- left, right <- top and q, left <- other, and gives each an action of one fact analyzer.
// The diamond (top -> left, right -> q) makes q a transitive-only dependency of top.
func newFactsFixture(t *testing.T) *factsFixture {
	t.Helper()

	sources := []struct {
		path    string
		source  string
		imports []string
	}{
		{"q", factsFixtureLeaf, nil},
		{"left", factsFixtureLeft, []string{"q"}},
		{"right", factsFixtureRight, []string{"q"}},
		{"top", factsFixtureTop, []string{"left", "right"}},
		{"other", factsFixtureOther, []string{"left", "q"}},
	}

	runner := newRunner("test", nil, nil, nil, LoadModeTypesInfo, nil)

	fixture := &factsFixture{packages: map[string]*types.Package{}, actions: map[string]*action{}}
	fset := token.NewFileSet()

	for _, src := range sources {
		file, err := parser.ParseFile(fset, src.path+".go", src.source, 0)
		require.NoError(t, err)

		config := types.Config{Importer: fixtureImporter{fallback: importer.Default(), checked: fixture.packages}}

		pkg, err := config.Check(src.path, fset, []*ast.File{file}, nil)
		require.NoError(t, err)

		fixture.packages[src.path] = pkg
		fixture.order = append(fixture.order, src.path)

		act := &action{
			Analyzer:     testFactAnalyzer,
			Package:      &packages.Package{PkgPath: src.path, Types: pkg, Fset: fset},
			runner:       runner,
			objectFacts:  map[objectFactKey]analysis.Fact{},
			packageFacts: map[packageFactKey]analysis.Fact{},
			factStore:    runner.factStoreFor(testFactAnalyzer),
		}

		for _, imported := range src.imports {
			act.factDeps = append(act.factDeps, fixture.actions[imported])
		}

		fixture.actions[src.path] = act
	}

	return fixture
}

// factObjects returns the objects of pkg that can carry facts: package-level objects, methods and struct fields.
func factObjects(pkg *types.Package) []types.Object {
	var objects []types.Object

	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		objects = append(objects, obj)

		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}

		if named, ok := typeName.Type().(*types.Named); ok {
			for method := range named.Methods() {
				objects = append(objects, method)
			}
		}

		if structType, ok := typeName.Type().Underlying().(*types.Struct); ok {
			for field := range structType.Fields() {
				objects = append(objects, field)
			}
		}
	}

	return objects
}

// inheritFactsReference is the copy-based inheritance the fact store replaces:
// it copies the facts of dep that exportedFrom accepts, and every package fact.
func inheritFactsReference(objects map[objectFactKey]analysis.Fact, packagesFacts map[packageFactKey]analysis.Fact, dep *action,
	depObjects map[objectFactKey]analysis.Fact, depPackages map[packageFactKey]analysis.Fact,
) {
	for key, fact := range depObjects {
		if !exportedFrom(key.obj, dep.Package.Types) {
			continue
		}

		objects[key] = fact
	}

	for key, fact := range depPackages {
		packagesFacts[key] = fact
	}
}

func factNames(objectFacts []analysis.ObjectFact, packageFacts []analysis.PackageFact) []string {
	var names []string

	for _, fact := range objectFacts {
		names = append(names, "object "+fact.Object.Pkg().Path()+"."+fact.Object.Name()+" "+fact.Fact.(*testFact).Name)
	}

	for _, fact := range packageFacts {
		names = append(names, "package "+fact.Package.Path()+" "+fact.Fact.(*testFact).Name)
	}

	sort.Strings(names)

	return names
}

// Test_action_factsMatchInheritance compares what every action reads through the fact store with the facts
// the former copy-based inheritance gave it, for every kind of object and every position in the import graph.
func Test_action_factsMatchInheritance(t *testing.T) {
	fixture := newFactsFixture(t)

	// Every package exports a fact about each of its objects and about itself.
	for _, path := range fixture.order {
		act := fixture.actions[path]

		for _, obj := range factObjects(fixture.packages[path]) {
			act.setObjectFact(objectFactKey{obj, reflect.TypeFor[*testFact]()}, &testFact{Name: obj.Name()})
		}

		act.setPackageFact(packageFactKey{fixture.packages[path], reflect.TypeFor[*testFact]()}, &testFact{Name: path})
	}

	// Reference: the maps the copy-based inheritance would have built, dependencies first.
	referenceObjects := map[string]map[objectFactKey]analysis.Fact{}
	referencePackages := map[string]map[packageFactKey]analysis.Fact{}

	for _, path := range fixture.order {
		act := fixture.actions[path]

		objects := map[objectFactKey]analysis.Fact{}
		packagesFacts := map[packageFactKey]analysis.Fact{}

		for key, fact := range act.objectFacts {
			objects[key] = fact
		}

		for key, fact := range act.packageFacts {
			packagesFacts[key] = fact
		}

		for _, dep := range act.factDeps {
			inheritFactsReference(objects, packagesFacts, dep, referenceObjects[dep.Package.PkgPath], referencePackages[dep.Package.PkgPath])
		}

		referenceObjects[path] = objects
		referencePackages[path] = packagesFacts
	}

	for _, path := range fixture.order {
		act := fixture.actions[path]

		t.Run(path, func(t *testing.T) {
			var expectedObjects []analysis.ObjectFact
			for key, fact := range referenceObjects[path] {
				expectedObjects = append(expectedObjects, analysis.ObjectFact{Object: key.obj, Fact: fact})
			}

			var expectedPackages []analysis.PackageFact
			for key, fact := range referencePackages[path] {
				expectedPackages = append(expectedPackages, analysis.PackageFact{Package: key.pkg, Fact: fact})
			}

			expected := factNames(expectedObjects, expectedPackages)

			assert.Equal(t, expected, factNames(act.AllObjectFacts(), act.AllPackageFacts()), "AllObjectFacts and AllPackageFacts")

			// Point lookups, for the objects and packages of the dependencies this action can name.
			closure := map[string]bool{path: true}
			for _, otherPath := range fixture.order {
				if isTransitiveDependency(fixture, path, otherPath) {
					closure[otherPath] = true
				}
			}

			for otherPath := range closure {
				for _, obj := range factObjects(fixture.packages[otherPath]) {
					key := objectFactKey{obj, reflect.TypeFor[*testFact]()}

					_, expectedFound := referenceObjects[path][key]

					actual := new(testFact)
					assert.Equal(t, expectedFound, act.ObjectFact(obj, actual), "ObjectFact(%s.%s)", otherPath, obj.Name())
				}

				actual := new(testFact)
				assert.True(t, act.PackageFact(fixture.packages[otherPath], actual), "PackageFact(%s)", otherPath)
				assert.Equal(t, otherPath, actual.Name)
			}
		})
	}
}

func isTransitiveDependency(fixture *factsFixture, from, target string) bool {
	var visit func(act *action) bool
	visit = func(act *action) bool {
		for _, dep := range act.factDeps {
			if dep.Package.PkgPath == target || visit(dep) {
				return true
			}
		}

		return false
	}

	return visit(fixture.actions[from])
}

// Test_action_factsAreSeenThroughExport checks the facts of the objects the analysis exports, not only ones set directly.
func Test_action_factsAreSeenThroughExport(t *testing.T) {
	fixture := newFactsFixture(t)

	leaf := fixture.actions["q"]
	leaf.pass = &analysis.Pass{ExportObjectFact: leaf.exportObjectFact, ExportPackageFact: leaf.exportPackageFact, Pkg: fixture.packages["q"], Files: []*ast.File{{}}}

	exported := fixture.packages["q"].Scope().Lookup("T")
	leaf.pass.ExportObjectFact(exported, &testFact{Name: "T"})
	leaf.pass.ExportPackageFact(&testFact{Name: "q"})

	top := fixture.actions["top"]

	actual := new(testFact)
	require.True(t, top.ObjectFact(exported, actual))
	assert.Equal(t, "T", actual.Name)

	actual = new(testFact)
	require.True(t, top.PackageFact(fixture.packages["q"], actual))
	assert.Equal(t, "q", actual.Name)
}

func Test_action_releaseFacts(t *testing.T) {
	fixture := newFactsFixture(t)

	leaf := fixture.actions["q"]
	function := fixture.packages["q"].Scope().Lookup("F")
	key := objectFactKey{function, reflect.TypeFor[*testFact]()}

	leaf.setObjectFact(key, &testFact{Name: "F"})

	left := fixture.actions["left"]
	require.True(t, left.ObjectFact(function, new(testFact)))

	leaf.releaseFacts()

	assert.False(t, left.ObjectFact(function, new(testFact)))
	assert.Empty(t, leaf.objectFacts)
	assert.Nil(t, leaf.factDeps)
}

func Test_action_resetFacts(t *testing.T) {
	fixture := newFactsFixture(t)

	leaf := fixture.actions["q"]
	function := fixture.packages["q"].Scope().Lookup("F")
	key := objectFactKey{function, reflect.TypeFor[*testFact]()}

	leaf.setObjectFact(key, &testFact{Name: "F"})

	leaf.resetFacts()

	assert.False(t, fixture.actions["left"].ObjectFact(function, new(testFact)))
	assert.NotNil(t, leaf.objectFacts, "an action analyzed after a reset exports into fresh maps")

	left := fixture.actions["left"]
	left.resetFacts()
	assert.Len(t, left.factDeps, 1, "a reset keeps the dependencies")
}
