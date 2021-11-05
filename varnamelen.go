package varnamelen

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// varNameLen is an analyzer that checks that the length of a variable's name matches its usage scope.
// It will create a report for a variable's assignment if that variable has a short name, but its
// usage scope is not considered "small."
type varNameLen struct {
	// maxDistance is the longest distance, in source lines, that is being considered a "small scope."
	maxDistance int

	// minNameLength is the minimum length of a variable's name that is considered "long."
	minNameLength int

	// ignoreNames is an optional list of variable names that should be ignored completely.
	ignoreNames stringsValue

	// checkReceiver determines whether a method receiver's name should be checked.
	checkReceiver bool

	// checkReturn determines whether named return values should be checked.
	checkReturn bool

	// ignoreTypeAssertOk determines whether "ok" variables that hold the bool return value of a type assertion should be ignored.
	ignoreTypeAssertOk bool

	// ignoreMapIndexOk determines whether "ok" variables that hold the bool return value of a map index should be ignored.
	ignoreMapIndexOk bool

	// ignoreChannelReceiveOk determines whether "ok" variables that hold the bool return value of a channel receive should be ignored.
	ignoreChannelReceiveOk bool

	// ignoreDeclarations is an optional list of variable declarations that should be ignored completely.
	ignoreDeclarations declarationsValue
}

// stringsValue is the value of a list-of-strings flag.
type stringsValue struct {
	Values []string
}

// declarationsValue is the value of a list-of-declarations flag.
type declarationsValue struct {
	Values []declaration
}

// variable represents a declared variable.
type variable struct {
	// name is the name of the variable.
	name string

	// assign is the assign statement that declares the variable.
	assign *ast.AssignStmt
}

// parameter represents a declared function or method parameter.
type parameter struct {
	// name is the name of the parameter.
	name string

	// field is the declaration of the parameter.
	field *ast.Field
}

// declaration is a variable declaration.
type declaration struct {
	// name is the name of the variable.
	name string

	// pointer determines whether the variable is a pointer.
	pointer bool

	// typ is the type of the variable.
	typ string
}

const (
	// defaultMaxDistance is the default value for the maximum distance between the declaration of a variable and its usage
	// that is considered a "small scope."
	defaultMaxDistance = 5

	// defaultMinNameLength is the default value for the minimum length of a variable's name that is considered "long."
	defaultMinNameLength = 3
)

// conventionalDecls is a list of conventional variable declarations.
var conventionalDecls = []declaration{
	mustParseDeclaration("t *testing.T"),
	mustParseDeclaration("b *testing.B"),
	mustParseDeclaration("tb testing.TB"),
	mustParseDeclaration("pb *testing.PB"),
	mustParseDeclaration("m *testing.M"),
	mustParseDeclaration("ctx context.Context"),
}

// errParseDeclaration is returned when parsing of a variable declaration fails.
var errParseDeclaration = errors.New("parse declaration")

// NewAnalyzer returns a new analyzer that checks variable name length.
func NewAnalyzer() *analysis.Analyzer {
	vnl := varNameLen{
		maxDistance:        defaultMaxDistance,
		minNameLength:      defaultMinNameLength,
		ignoreNames:        stringsValue{},
		ignoreDeclarations: declarationsValue{},
	}

	analyzer := analysis.Analyzer{
		Name: "varnamelen",
		Doc: "checks that the length of a variable's name matches its scope\n\n" +
			"A variable with a short name can be hard to use if the variable is used\n" +
			"over a longer span of lines of code. A longer variable name may be easier\n" +
			"to comprehend.",

		Run: func(pass *analysis.Pass) (interface{}, error) {
			vnl.run(pass)
			return nil, nil
		},

		Requires: []*analysis.Analyzer{
			inspect.Analyzer,
		},
	}

	analyzer.Flags.IntVar(&vnl.maxDistance, "maxDistance", defaultMaxDistance, "maximum number of lines of variable usage scope considered 'short'")
	analyzer.Flags.IntVar(&vnl.minNameLength, "minNameLength", defaultMinNameLength, "minimum length of variable name considered 'long'")
	analyzer.Flags.Var(&vnl.ignoreNames, "ignoreNames", "comma-separated list of ignored variable names")
	analyzer.Flags.BoolVar(&vnl.checkReceiver, "checkReceiver", false, "check method receiver names")
	analyzer.Flags.BoolVar(&vnl.checkReturn, "checkReturn", false, "check named return values")
	analyzer.Flags.BoolVar(&vnl.ignoreTypeAssertOk, "ignoreTypeAssertOk", false, "ignore 'ok' variables that hold the bool return value of a type assertion")
	analyzer.Flags.BoolVar(&vnl.ignoreMapIndexOk, "ignoreMapIndexOk", false, "ignore 'ok' variables that hold the bool return value of a map index")
	analyzer.Flags.BoolVar(&vnl.ignoreChannelReceiveOk, "ignoreChanRecvOk", false, "ignore 'ok' variables that hold the bool return value of a channel receive")
	analyzer.Flags.Var(&vnl.ignoreDeclarations, "ignoreDecls", "comma-separated list of ignored variable declarations")

	return &analyzer
}

// Run applies v to a package, according to pass.
func (v *varNameLen) run(pass *analysis.Pass) {
	varToDist, paramToDist, returnToDist := v.distances(pass)

	v.checkVariables(pass, varToDist)
	v.checkParams(pass, paramToDist)
	v.checkReturns(pass, returnToDist)
}

// checkVariables applies v to variables in varToDist.
func (v *varNameLen) checkVariables(pass *analysis.Pass, varToDist map[variable]int) {
	for variable, dist := range varToDist {
		if v.ignoreNames.contains(variable.name) {
			continue
		}

		if v.ignoreDeclarations.matchVariable(variable) {
			continue
		}

		if v.checkNameAndDistance(variable.name, dist) {
			continue
		}

		if v.checkTypeAssertOk(variable) {
			continue
		}

		if v.checkMapIndexOk(variable) {
			continue
		}

		if v.checkChannelReceiveOk(variable) {
			continue
		}

		pass.Reportf(variable.assign.Pos(), "variable name '%s' is too short for the scope of its usage", variable.name)
	}
}

// checkParams applies v to parameters in paramToDist.
func (v *varNameLen) checkParams(pass *analysis.Pass, paramToDist map[parameter]int) {
	for param, dist := range paramToDist {
		if v.ignoreNames.contains(param.name) {
			continue
		}

		if v.ignoreDeclarations.matchParameter(param) {
			continue
		}

		if v.checkNameAndDistance(param.name, dist) {
			continue
		}

		if param.isConventional() {
			continue
		}

		pass.Reportf(param.field.Pos(), "parameter name '%s' is too short for the scope of its usage", param.name)
	}
}

// checkReturns applies v to named return values in returnToDist.
func (v *varNameLen) checkReturns(pass *analysis.Pass, returnToDist map[parameter]int) {
	for returnValue, dist := range returnToDist {
		if v.ignoreNames.contains(returnValue.name) {
			continue
		}

		if v.ignoreDeclarations.matchParameter(returnValue) {
			continue
		}

		if v.checkNameAndDistance(returnValue.name, dist) {
			continue
		}

		pass.Reportf(returnValue.field.Pos(), "return value name '%s' is too short for the scope of its usage", returnValue.name)
	}
}

// checkNameAndDistance returns true if name or dist are considered "short".
func (v *varNameLen) checkNameAndDistance(name string, dist int) bool {
	if len(name) >= v.minNameLength {
		return true
	}

	if dist <= v.maxDistance {
		return true
	}

	return false
}

// checkTypeAssertOk returns true if "ok" variables that hold the bool return value of a type assertion
// should be ignored, and if vari is such a variable.
func (v *varNameLen) checkTypeAssertOk(vari variable) bool {
	return v.ignoreTypeAssertOk && vari.isTypeAssertOk()
}

// checkMapIndexOk returns true if "ok" variables that hold the bool return value of a map index
// should be ignored, and if vari is such a variable.
func (v *varNameLen) checkMapIndexOk(vari variable) bool {
	return v.ignoreMapIndexOk && vari.isMapIndexOk()
}

// checkChannelReceiveOk returns true if "ok" variables that hold the bool return value of a channel receive
// should be ignored, and if vari is such a variable.
func (v *varNameLen) checkChannelReceiveOk(vari variable) bool {
	return v.ignoreChannelReceiveOk && vari.isChannelReceiveOk()
}

// distances maps of variables or parameters and their longest usage distances.
func (v *varNameLen) distances(pass *analysis.Pass) (map[variable]int, map[parameter]int, map[parameter]int) {
	assignIdents, paramIdents, returnIdents := v.idents(pass)

	varToDist := map[variable]int{}

	for _, ident := range assignIdents {
		assign := ident.Obj.Decl.(*ast.AssignStmt) //nolint:forcetypeassert // check is done in idents()
		variable := variable{
			name:   ident.Name,
			assign: assign,
		}

		useLine := pass.Fset.Position(ident.NamePos).Line
		declLine := pass.Fset.Position(assign.Pos()).Line
		varToDist[variable] = useLine - declLine
	}

	paramToDist := map[parameter]int{}

	for _, ident := range paramIdents {
		field := ident.Obj.Decl.(*ast.Field) //nolint:forcetypeassert // check is done in idents()
		param := parameter{
			name:  ident.Name,
			field: field,
		}

		useLine := pass.Fset.Position(ident.NamePos).Line
		declLine := pass.Fset.Position(field.Pos()).Line
		paramToDist[param] = useLine - declLine
	}

	returnToDist := map[parameter]int{}

	for _, ident := range returnIdents {
		field := ident.Obj.Decl.(*ast.Field) //nolint:forcetypeassert // check is done in idents()
		param := parameter{
			name:  ident.Name,
			field: field,
		}

		useLine := pass.Fset.Position(ident.NamePos).Line
		declLine := pass.Fset.Position(field.Pos()).Line
		returnToDist[param] = useLine - declLine
	}

	return varToDist, paramToDist, returnToDist
}

// idents returns Idents referencing assign statements, parameters, and return values, respectively.
func (v *varNameLen) idents(pass *analysis.Pass) ([]*ast.Ident, []*ast.Ident, []*ast.Ident) { //nolint:gocognit // this is complex stuff
	inspector := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector) //nolint:forcetypeassert // inspect.Analyzer always returns *inspector.Inspector

	filter := []ast.Node{
		(*ast.FuncDecl)(nil),
		(*ast.Ident)(nil),
	}

	funcs := []*ast.FuncDecl{}
	methods := []*ast.FuncDecl{}
	assignIdents := []*ast.Ident{}
	paramIdents := []*ast.Ident{}
	returnIdents := []*ast.Ident{}

	inspector.Preorder(filter, func(node ast.Node) {
		if f, ok := node.(*ast.FuncDecl); ok {
			funcs = append(funcs, f)
			if f.Recv != nil {
				methods = append(methods, f)
			}
			return
		}

		ident := node.(*ast.Ident) //nolint:forcetypeassert // see filter
		if ident.Obj == nil {
			return
		}

		if _, ok := ident.Obj.Decl.(*ast.AssignStmt); ok {
			assignIdents = append(assignIdents, ident)
			return
		}

		if field, ok := ident.Obj.Decl.(*ast.Field); ok {
			if isReceiver(field, methods) && !v.checkReceiver {
				return
			}

			if isReturn(field, funcs) {
				if !v.checkReturn {
					return
				}
				returnIdents = append(returnIdents, ident)
				return
			}

			paramIdents = append(paramIdents, ident)
		}
	})

	return assignIdents, paramIdents, returnIdents
}

// isTypeAssertOk returns true if v is an "ok" variable that holds the bool return value of a type assertion.
func (v variable) isTypeAssertOk() bool {
	if v.name != "ok" {
		return false
	}

	if len(v.assign.Lhs) != 2 {
		return false
	}

	ident, ok := v.assign.Lhs[1].(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name != "ok" {
		return false
	}

	if len(v.assign.Rhs) != 1 {
		return false
	}

	if _, ok := v.assign.Rhs[0].(*ast.TypeAssertExpr); !ok {
		return false
	}

	return true
}

// isMapIndexOk returns true if v is an "ok" variable that holds the bool return value of a map index.
func (v variable) isMapIndexOk() bool {
	if v.name != "ok" {
		return false
	}

	if len(v.assign.Lhs) != 2 {
		return false
	}

	ident, ok := v.assign.Lhs[1].(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name != "ok" {
		return false
	}

	if len(v.assign.Rhs) != 1 {
		return false
	}

	if _, ok := v.assign.Rhs[0].(*ast.IndexExpr); !ok {
		return false
	}

	return true
}

// isChannelReceiveOk returns true if v is an "ok" variable that holds the bool return value of a channel receive.
func (v variable) isChannelReceiveOk() bool {
	if v.name != "ok" {
		return false
	}

	if len(v.assign.Lhs) != 2 {
		return false
	}

	ident, ok := v.assign.Lhs[1].(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name != "ok" {
		return false
	}

	if len(v.assign.Rhs) != 1 {
		return false
	}

	unary, ok := v.assign.Rhs[0].(*ast.UnaryExpr)
	if !ok {
		return false
	}

	if unary.Op != token.ARROW {
		return false
	}

	return true
}

func (v variable) match(decl declaration) bool {
	return false
}

// isReceiver returns true if field is a receiver parameter of any of the given methods.
func isReceiver(field *ast.Field, methods []*ast.FuncDecl) bool {
	for _, m := range methods {
		for _, recv := range m.Recv.List {
			if recv == field {
				return true
			}
		}
	}

	return false
}

// isReturn returns true if field is a return value of any of the given funcs.
func isReturn(field *ast.Field, funcs []*ast.FuncDecl) bool {
	for _, f := range funcs {
		if f.Type.Results == nil {
			continue
		}

		for _, r := range f.Type.Results.List {
			if r == field {
				return true
			}
		}
	}

	return false
}

// Set implements Value.
func (sv *stringsValue) Set(s string) error {
	parts := strings.Split(s, ",")

	sv.Values = make([]string, len(parts))
	for i, part := range parts {
		sv.Values[i] = strings.TrimSpace(part)
	}

	return nil
}

// String implements Value.
func (sv *stringsValue) String() string {
	return strings.Join(sv.Values, ",")
}

// contains returns true if sv contains s.
func (sv *stringsValue) contains(s string) bool {
	for _, v := range sv.Values {
		if v == s {
			return true
		}
	}

	return false
}

// Set implements Value.
func (dv *declarationsValue) Set(s string) error {
	parts := strings.Split(s, ",")

	dv.Values = make([]declaration, len(parts))
	for i, part := range parts {
		decl, ok := parseDeclaration(part)
		if !ok {
			return fmt.Errorf("%s: %w", part, errParseDeclaration)
		}

		dv.Values[i] = decl
	}

	return nil
}

// String implements Value.
func (dv *declarationsValue) String() string {
	parts := make([]string, len(dv.Values))

	for i, v := range dv.Values {
		part := v.name + " "

		if v.pointer {
			part += "*"
		}

		part += v.typ

		parts[i] = part
	}

	return strings.Join(parts, ",")
}

func (dv *declarationsValue) matchVariable(vari variable) bool {
	for _, decl := range dv.Values {
		if vari.match(decl) {
			return true
		}
	}

	return false
}

// matchParameter returns true if param matches any of the declarations in dv.
func (dv *declarationsValue) matchParameter(param parameter) bool {
	for _, decl := range dv.Values {
		if param.match(decl) {
			return true
		}
	}

	return false
}

// isConventional returns true if p is a conventional Go parameter, such as "ctx context.Context" or
// "t *testing.T".
func (p parameter) isConventional() bool {
	for _, decl := range conventionalDecls {
		if p.match(decl) {
			return true
		}
	}

	return false
}

// match returns whether p matches decl.
func (p parameter) match(decl declaration) bool {
	if p.name != decl.name {
		return false
	}

	var sel *ast.SelectorExpr

	if decl.pointer {
		star, ok := p.field.Type.(*ast.StarExpr)
		if !ok {
			return false
		}

		sel, ok = star.X.(*ast.SelectorExpr)
		if !ok {
			return false
		}
	} else {
		var ok bool
		sel, ok = p.field.Type.(*ast.SelectorExpr)
		if !ok {
			return false
		}
	}

	selIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	if selIdent.Name+"."+sel.Sel.Name != decl.typ {
		return false
	}

	return true
}

func mustParseDeclaration(decl string) declaration {
	dcl, ok := parseDeclaration(decl)
	if !ok {
		panic("parse declaration: " + decl)
	}

	return dcl
}

func parseDeclaration(decl string) (declaration, bool) {
	expr, err := parser.ParseExpr("func(" + decl + ") {}")
	if err != nil {
		return declaration{}, false
	}

	fDecl, ok := expr.(*ast.FuncLit)
	if !ok {
		return declaration{}, false
	}

	params := fDecl.Type.Params.List
	if len(params) != 1 {
		return declaration{}, false
	}

	if len(params[0].Names) != 1 {
		return declaration{}, false
	}

	var sel *ast.SelectorExpr

	pointer := false

	switch typeExpr := params[0].Type.(type) {
	case *ast.StarExpr:
		var ok bool
		sel, ok = typeExpr.X.(*ast.SelectorExpr)

		if !ok {
			return declaration{}, false
		}

		pointer = true

	case *ast.SelectorExpr:
		sel = typeExpr

	default:
		return declaration{}, false
	}

	selIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return declaration{}, false
	}

	return declaration{
		name:    params[0].Names[0].Name,
		pointer: pointer,
		typ:     selIdent.Name + "." + sel.Sel.Name,
	}, true
}
