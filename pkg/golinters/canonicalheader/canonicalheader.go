package canonicalheader

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/golangci/canonicalheader"
	"golang.org/x/tools/go/analysis"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis"
)

func New(settings *config.CanonicalHeaderSettings) *goanalysis.Linter {
	var cfg map[string]any

	if settings != nil {
		cfg = map[string]any{
			"useDefaultExclusion": settings.UseDefaultExclusions,
		}

		if len(settings.Exclusions) > 0 {
			cfg["exclusions"] = strings.Join(settings.Exclusions, ",")
		}
	}

	return goanalysis.
		NewLinterFromAnalyzer(newAnalyzer()).
		WithConfig(cfg).
		WithLoadMode(goanalysis.LoadModeTypesInfo)
}

func newAnalyzer() *analysis.Analyzer {
	analyzer := canonicalheader.New()

	run := analyzer.Run

	analyzer.Run = func(pass *analysis.Pass) (any, error) {
		return run(withoutHeaderMethodUses(pass))
	}

	return analyzer
}

// withoutHeaderMethodUses hides the uses of http.ResponseWriter's Header method.
// The analyzer takes the first object named Header that it finds in a map of uses as the http.Header type:
// when that is the method it reports nothing, so the result changed from run to run.
func withoutHeaderMethodUses(pass *analysis.Pass) *analysis.Pass {
	hasMethodUse := false

	for _, object := range pass.TypesInfo.Uses {
		if isHeaderMethod(object) {
			hasMethodUse = true

			break
		}
	}

	if !hasMethodUse {
		return pass
	}

	uses := make(map[*ast.Ident]types.Object, len(pass.TypesInfo.Uses))

	for ident, object := range pass.TypesInfo.Uses {
		if !isHeaderMethod(object) {
			uses[ident] = object
		}
	}

	info := *pass.TypesInfo
	info.Uses = uses

	filtered := *pass
	filtered.TypesInfo = &info

	return &filtered
}

func isHeaderMethod(object types.Object) bool {
	method, ok := object.(*types.Func)

	return ok && method.Name() == "Header" && method.Pkg() != nil && method.Pkg().Path() == "net/http"
}
