package tui

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// lipglossPath is the module path style-boundary resolves against.
// E13.2: charm.land/lipgloss/v2 (compat colors live under lipglossCompatPath).
const lipglossPath = "charm.land/lipgloss/v2"
const lipglossCompatPath = "charm.land/lipgloss/v2/compat"

// Every exported Style method in Lip Gloss v2 is deliberately classified.
// Visual methods are prohibited outside theme; structural methods only control
// layout or inspect/render an existing style. The remaining methods compose a
// preexisting style or manage its stored content.
var forbiddenVisualStyleMethods = stringSet(
	"Align", "AlignHorizontal", "AlignVertical", "Background", "Blink", "Bold",
	"Border", "BorderBackground", "BorderBottom", "BorderBottomBackground", "BorderBottomForeground", "BorderForeground", "BorderForegroundBlend", "BorderForegroundBlendOffset", "BorderLeft", "BorderLeftBackground", "BorderLeftForeground", "BorderRight", "BorderRightBackground", "BorderRightForeground", "BorderStyle", "BorderTop", "BorderTopBackground", "BorderTopForeground",
	"ColorWhitespace", "Faint", "Foreground", "Hyperlink", "Inline", "Italic",
	"Margin", "MarginBackground", "MarginBottom", "MarginChar", "MarginLeft", "MarginRight", "MarginTop",
	"Padding", "PaddingBottom", "PaddingChar", "PaddingLeft", "PaddingRight", "PaddingTop",
	"Reverse", "Strikethrough", "StrikethroughSpaces", "TabWidth", "Transform", "Underline", "UnderlineColor", "UnderlineSpaces", "UnderlineStyle",
	"UnsetAlign", "UnsetAlignHorizontal", "UnsetAlignVertical", "UnsetBackground", "UnsetBlink", "UnsetBold",
	"UnsetBorderBackground", "UnsetBorderBottom", "UnsetBorderBottomBackground", "UnsetBorderBottomForeground", "UnsetBorderForeground", "UnsetBorderForegroundBlend", "UnsetBorderForegroundBlendOffset", "UnsetBorderLeft", "UnsetBorderLeftBackground", "UnsetBorderLeftForeground", "UnsetBorderRight", "UnsetBorderRightBackground", "UnsetBorderRightForeground", "UnsetBorderStyle", "UnsetBorderTop", "UnsetBorderTopBackground", "UnsetBorderTopBackgroundColor", "UnsetBorderTopForeground",
	"UnsetColorWhitespace", "UnsetFaint", "UnsetForeground", "UnsetHyperlink", "UnsetInline", "UnsetItalic",
	"UnsetMarginBackground", "UnsetMarginBottom", "UnsetMarginLeft", "UnsetMarginRight", "UnsetMarginTop", "UnsetMargins",
	"UnsetPadding", "UnsetPaddingBottom", "UnsetPaddingChar", "UnsetPaddingLeft", "UnsetPaddingRight", "UnsetPaddingTop",
	"UnsetReverse", "UnsetStrikethrough", "UnsetStrikethroughSpaces", "UnsetTabWidth", "UnsetTransform", "UnsetUnderline", "UnsetUnderlineSpaces",
)

var structuralStyleMethods = stringSet(
	"GetAlign", "GetAlignHorizontal", "GetAlignVertical", "GetBackground", "GetBlink", "GetBold", "GetBorder", "GetBorderBottom", "GetBorderBottomBackground", "GetBorderBottomForeground", "GetBorderBottomSize", "GetBorderForegroundBlend", "GetBorderForegroundBlendOffset", "GetBorderLeft", "GetBorderLeftBackground", "GetBorderLeftForeground", "GetBorderLeftSize", "GetBorderRight", "GetBorderRightBackground", "GetBorderRightForeground", "GetBorderRightSize", "GetBorderStyle", "GetBorderTop", "GetBorderTopBackground", "GetBorderTopForeground", "GetBorderTopSize", "GetBorderTopWidth", "GetColorWhitespace", "GetFaint", "GetForeground", "GetFrameSize", "GetHeight", "GetHorizontalBorderSize", "GetHorizontalFrameSize", "GetHorizontalMargins", "GetHorizontalPadding", "GetHyperlink", "GetInline", "GetItalic", "GetMargin", "GetMarginBottom", "GetMarginChar", "GetMarginLeft", "GetMarginRight", "GetMarginTop", "GetMaxHeight", "GetMaxWidth", "GetPadding", "GetPaddingBottom", "GetPaddingChar", "GetPaddingLeft", "GetPaddingRight", "GetPaddingTop", "GetReverse", "GetStrikethrough", "GetStrikethroughSpaces", "GetTabWidth", "GetTransform", "GetUnderline", "GetUnderlineColor", "GetUnderlineSpaces", "GetUnderlineStyle", "GetVerticalBorderSize", "GetVerticalFrameSize", "GetVerticalMargins", "GetVerticalPadding", "GetWidth",
	"Height", "MaxHeight", "MaxWidth", "Render", "String", "UnsetHeight", "UnsetMaxHeight", "UnsetMaxWidth", "UnsetWidth", "Value", "Width",
)

var permittedStyleMethods = stringSet(
	"Copy",        // Copies an existing style without choosing visual values.
	"Inherit",     // Composes an already-defined style, typically from theme.
	"SetString",   // Stores render content rather than visual presentation.
	"UnsetString", // Clears stored render content rather than visual presentation.
)

var borderConstructors = stringSet(
	"ASCIIBorder", "BlockBorder", "DoubleBorder", "HiddenBorder", "InnerHalfBlockBorder",
	"MarkdownBorder", "NormalBorder", "OuterHalfBlockBorder", "RoundedBorder", "ThickBorder",
)

func stringSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

type styleDiagnostic struct {
	Rule, File string
	Pos        token.Position
	Review     bool // Unknown or interprocedural values need the review checklist below.
}

// TestStyleBoundary keeps visual decisions in theme and checks the two public
// frontend layers. It resolves lipgloss declarations through go/types rather
// than trusting selector spelling, so aliases, dot imports, stored styles, and
// method expressions cannot bypass the guard.
//
// Review checklist for REVIEW diagnostics: follow values across package/function
// boundaries; accept only theme-resolved colors, spacing, icons, or styles; and
// move a visual definition into theme when its provenance cannot be established.
func TestStyleBoundary(t *testing.T) {
	root := moduleRoot(t)
	for _, scope := range []struct{ name, dir string }{
		{"root", filepath.Join(root, "internal", "tui", "app")},
		{"ui", filepath.Join(root, "internal", "tui", "ui")},
	} {
		diagnostics, err := scanStyleDir(scope.dir, scope.name)
		if err != nil {
			t.Fatalf("scan %s: %v", scope.name, err)
		}
		for _, d := range diagnostics {
			if d.Review && allowedStyleReview(d) {
				continue
			}
			if d.Review {
				t.Errorf("unexpected REVIEW %s %s:%d", d.Rule, filepath.Base(d.File), d.Pos.Line)
				continue
			}
			t.Errorf("%s %s:%d", d.Rule, filepath.Base(d.File), d.Pos.Line)
		}
	}
}

func TestStyleBoundaryFixtures(t *testing.T) {
	root := filepath.Join(moduleRoot(t), "internal", "tui", "app", "testdata", "style_boundary")
	want := map[string]struct {
		scope string
		rules []string
	}{
		"bad_alias":                 {"root", []string{"SB001"}},
		"bad_dot":                   {"root", []string{"SB001"}},
		"bad_hex":                   {"ui", []string{"SB002", "SB003"}},
		"bad_ansi":                  {"ui", []string{"SB002", "SB003"}},
		"bad_constants":             {"ui", []string{"SB002", "SB002"}},
		"bad_border":                {"ui", []string{"SB002", "SB004"}},
		"bad_border_constructors":   {"ui", []string{"SB004", "SB004", "SB004", "SB004", "SB004", "SB004", "SB004", "SB004", "SB004", "SB004"}},
		"bad_spacing":               {"ui", []string{"SB002", "SB002"}},
		"bad_bold":                  {"ui", []string{"SB002"}},
		"bad_stored_style":          {"root", []string{"SB001"}},
		"bad_method_expression":     {"root", []string{"SB001"}},
		"bad_method_value":          {"root", []string{"SB007"}},
		"bad_inline_transform":      {"root", []string{"SB001", "SB001", "SB007", "SB007"}},
		"bad_method_value_argument": {"root", []string{"SB007"}},
		"bad_tab_width":             {"ui", []string{"SB002", "SB007"}},
		// v2: Color() args are SB003; Border*Foreground/Background with Color()
		// still flag the Color constructor (11×) plus structural visual methods.
		"bad_missing_modifiers": {"ui", []string{"SB002", "SB002", "SB002", "SB002", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB004"}},
		// Adaptive/Complete live in compat; each composite + Color() arg is SB003.
		"bad_color_composites": {"ui", []string{"SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB003", "SB004"}},
		"bad_embedded_dot":     {"ui", []string{"SB006"}},
		"bad_glyph_constant":   {"ui", []string{"SB006", "SB006", "SB006", "SB006"}},
		"bad_unresolved":       {"ui", []string{"SB002", "SB002"}},
		"bad_visual_unsets":    {"ui", []string{"SB002", "SB002", "SB007", "SB007"}},
		"good":                 {"ui", nil},
	}
	for name, tc := range want {
		t.Run(name, func(t *testing.T) {
			diagnostics, err := scanStyleDir(filepath.Join(root, name), tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range diagnostics {
				if d.Review {
					got = append(got, d.Rule)
				} else {
					got = append(got, d.Rule)
				}
			}
			sort.Strings(got)
			sort.Strings(tc.rules)
			if strings.Join(got, ",") != strings.Join(tc.rules, ",") {
				t.Fatalf("rules = %v, want %v", got, tc.rules)
			}
		})
	}
}

func TestLipglossStyleMethodClassification(t *testing.T) {
	pkg := lipglossTypesPackage(t)
	style, ok := pkg.Scope().Lookup("Style").Type().(*types.Named)
	if !ok {
		t.Fatal("lipgloss.Style is not a named type")
	}
	actual := map[string]bool{}
	methodSet := types.NewMethodSet(style)
	for i := 0; i < methodSet.Len(); i++ {
		selection := methodSet.At(i)
		if selection.Obj().Exported() {
			actual[selection.Obj().Name()] = true
		}
	}
	classified := unionStyleMethods(forbiddenVisualStyleMethods, structuralStyleMethods, permittedStyleMethods)
	if diff := setDifference(actual, classified); len(diff) > 0 {
		t.Errorf("unclassified exported lipgloss.Style methods: %s", strings.Join(diff, ", "))
	}
	if diff := setDifference(classified, actual); len(diff) > 0 {
		t.Errorf("classified methods absent from exported lipgloss.Style: %s", strings.Join(diff, ", "))
	}
}

func TestLipglossBorderConstructorClassification(t *testing.T) {
	pkg := lipglossTypesPackage(t)
	actual := map[string]bool{}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.Func)
		if !ok || !obj.Exported() {
			continue
		}
		sig := obj.Type().(*types.Signature)
		if sig.Params().Len() == 0 && sig.Results().Len() == 1 && namedTypePath(sig.Results().At(0).Type(), "Border") == lipglossPath {
			actual[name] = true
		}
	}
	if diff := setDifference(actual, borderConstructors); len(diff) > 0 {
		t.Errorf("unclassified exported zero-argument lipgloss.Border constructors: %s", strings.Join(diff, ", "))
	}
	if diff := setDifference(borderConstructors, actual); len(diff) > 0 {
		t.Errorf("classified constructors absent from exported zero-argument lipgloss.Border constructors: %s", strings.Join(diff, ", "))
	}
}

func lipglossTypesPackage(t *testing.T) *types.Package {
	t.Helper()
	pkg, err := exportDataImporter(moduleRoot(t), token.NewFileSet()).Import(lipglossPath)
	if err != nil {
		t.Fatalf("import lipgloss export data: %v", err)
	}
	return pkg
}

func unionStyleMethods(sets ...map[string]bool) map[string]bool {
	result := map[string]bool{}
	for _, set := range sets {
		for name := range set {
			result[name] = true
		}
	}
	return result
}

func setDifference(left, right map[string]bool) []string {
	var result []string
	for name := range left {
		if !right[name] {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

// allowedStyleReview is deliberately narrow: panel's helper accepts styles and
// colors assembled by its public callers, so its parameter provenance cannot
// be established within this package-level scan. All other unknown visual
// values must be resolved before use.
func allowedStyleReview(d styleDiagnostic) bool {
	return filepath.Base(d.File) == "panel.go" && d.Rule == "SB005"
}

func scanStyleDir(dir, scope string) ([]styleDiagnostic, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.AllErrors)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	info := &types.Info{
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: exportDataImporter(dir, fset), Error: func(error) {}}
	_, _ = conf.Check("styleboundary", fset, files, info)
	provenance := collectStyleProvenance(files, info)
	var out []styleDiagnostic
	add := func(rule string, n ast.Node, review bool) {
		out = append(out, styleDiagnostic{rule, fset.Position(n.Pos()).Filename, fset.Position(n.Pos()), review})
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, visual, constructor := lipglossCall(call, info)
			if !visual && !constructor {
				return true
			}
			if constructor {
				if borderConstructors[name] {
					add("SB004", call, false)
				} else {
					add("SB003", call, false)
				}
				return true
			}
			if name == "Border" { // Borders are visual even if the argument is not literal.
				if scope == "root" {
					add("SB001", call, false)
				} else if unsafeVisualArg(call.Args, info, provenance) {
					add("SB002", call, false)
				} else {
					add("SB005", call, true)
				}
				return true
			}
			if scope == "root" {
				add("SB001", call, false)
				return true
			}
			if len(call.Args) == 0 || unsafeVisualArg(call.Args, info, provenance) {
				add("SB002", call, false)
			} else if unknownVisualArg(call.Args, info, provenance) {
				add("SB005", call, true)
			}
			return true
		})
		// A composite literal is construction too; types distinguishes unrelated Border types.
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if namedTypePath(info.TypeOf(lit.Type), "Border") == lipglossPath || isLipglossBorder(lit.Type, info) {
				add("SB004", lit, false)
			} else if isLipglossColorComposite(lit, info) {
				add("SB003", lit, false)
			}
			return true
		})
		calledMethods := map[*ast.SelectorExpr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && visualMethodValue(selector, info) != "" {
					calledMethods[selector] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if !calledMethods[x] && visualMethodValue(x, info) != "" {
					add("SB007", x, false)
				}
			case *ast.BasicLit:
				if visualGlyphConstant(x, info) {
					add("SB006", x, false)
				}
			}
			return true
		})
	}
	return out, nil
}

func isLipglossBorder(e ast.Expr, info *types.Info) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	o, ok := info.Uses[s.Sel].(*types.TypeName)
	return ok && o.Name() == "Border" && o.Pkg() != nil && o.Pkg().Path() == lipglossPath
}

func isLipglossColorComposite(lit *ast.CompositeLit, info *types.Info) bool {
	path := namedTypePath(info.TypeOf(lit), "Color")
	if path == lipglossPath {
		return true
	}
	for _, name := range []string{"ANSIColor", "NoColor"} {
		if namedTypePath(info.TypeOf(lit), name) == lipglossPath {
			return true
		}
	}
	// Adaptive/Complete colors moved to lipgloss/v2/compat in E13.2.
	for _, name := range []string{"AdaptiveColor", "CompleteColor", "CompleteAdaptiveColor"} {
		if p := namedTypePath(info.TypeOf(lit), name); p == lipglossPath || p == lipglossCompatPath {
			return true
		}
	}
	return false
}

// exportDataImporter asks the active Go module for each package's compiled
// export data. Unlike importer.Default, this also resolves this repository's
// internal/theme package while type-checking fixtures under testdata.
func exportDataImporter(dir string, fset *token.FileSet) types.Importer {
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return importer.Default()
		}
		root = parent
	}
	lookup := func(path string) (io.ReadCloser, error) {
		cmd := exec.Command("go", "list", "-export", "-f={{.Export}}", path)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		return os.Open(strings.TrimSpace(string(out)))
	}
	return importer.ForCompiler(fset, "gc", lookup)
}

func lipglossCall(call *ast.CallExpr, info *types.Info) (name string, visual, constructor bool) {
	var obj types.Object
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		obj = info.Uses[f.Sel]
		if selection := info.Selections[f]; selection != nil {
			obj = selection.Obj()
		}
	case *ast.Ident: // dot import
		obj = info.Uses[f]
	}
	if typ, ok := obj.(*types.TypeName); ok && typ.Pkg() != nil {
		switch typ.Pkg().Path() {
		case lipglossPath:
			switch typ.Name() {
			case "Color", "ANSIColor", "NoColor":
				return typ.Name(), false, true
			}
		case lipglossCompatPath:
			switch typ.Name() {
			case "AdaptiveColor", "CompleteColor", "CompleteAdaptiveColor":
				return typ.Name(), false, true
			}
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return "", false, false
	}
	name = fn.Name()
	if fn.Pkg().Path() != lipglossPath {
		return "", false, false
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && namedTypePath(sig.Recv().Type(), "Style") == lipglossPath && forbiddenVisualStyleMethods[name] {
		return name, true, false
	}
	switch name {
	case "Color", "ANSIColor", "NoColor":
		return name, false, true
	}
	if borderConstructors[name] {
		return name, false, true
	}
	return name, false, false
}

func namedTypePath(t types.Type, want string) string {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Name() != want || n.Obj().Pkg() == nil {
		return ""
	}
	return n.Obj().Pkg().Path()
}

func unsafeVisualArg(args []ast.Expr, info *types.Info, provenance map[types.Object]styleProvenance) bool {
	for _, arg := range args {
		if literalOrLocalConst(arg, info, map[types.Object]bool{}) || unresolvedThemeField(arg, info, provenance) {
			return true
		}
	}
	return false
}
func unknownVisualArg(args []ast.Expr, info *types.Info, provenance map[types.Object]styleProvenance) bool {
	for _, arg := range args {
		if !themeDerived(arg, info, provenance) && !literalOrLocalConst(arg, info, map[types.Object]bool{}) && !lipglossColorConstructor(arg, info) {
			return true
		}
	}
	return false
}

func lipglossColorConstructor(e ast.Expr, info *types.Info) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	_, _, constructor := lipglossCall(call, info)
	return constructor
}
func literalOrLocalConst(e ast.Expr, info *types.Info, seen map[types.Object]bool) bool {
	if value := info.Types[e].Value; value != nil && value.Kind() != constant.Unknown {
		return true
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		o := info.Uses[e]
		if o == nil || seen[o] {
			return false
		}
		seen[o] = true
		if _, ok := o.(*types.Const); ok {
			return true
		}
		return false
	case *ast.CompositeLit:
		return true
	}
	return false
}

func visualMethodValue(e ast.Expr, info *types.Info) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	selection := info.Selections[sel]
	if selection == nil {
		return ""
	}
	fn, ok := selection.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != lipglossPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && namedTypePath(sig.Recv().Type(), "Style") == lipglossPath && forbiddenVisualStyleMethods[fn.Name()] {
		return fn.Name()
	}
	return ""
}

func visualGlyphConstant(e ast.Expr, info *types.Info) bool {
	value := info.Types[e].Value
	if value == nil || value.Kind() != constant.String {
		return false
	}
	s := constant.StringVal(value)
	if strings.ContainsRune(s, '·') {
		return true
	}
	// These are the other concrete visual tokens exposed by theme.Icons and
	// theme.BorderStyle. Exact matching avoids treating ordinary UI prose,
	// OAuth URLs, and keyboard hints as visual declarations.
	switch strings.TrimSpace(s) {
	case "…", "│", "─", "╭", "╮", "╰", "╯", "┏", "┓", "┗", "┛", "━", "┃", "❯", "●", "⚙", "✓", "✗", "◦", "◆", "⚡", "▸", "▏", "—", "▁", "▔", "▂", "▃", "▄", "▅", "▆", "▇", "█", "░", "▾":
		return true
	}
	return false
}

type styleProvenance uint8

const (
	provenanceUnknown styleProvenance = iota
	provenanceUnresolvedTheme
	provenanceResolvedTheme
	provenanceResolvedValue
)

func collectStyleProvenance(files []*ast.File, info *types.Info) map[types.Object]styleProvenance {
	p := map[types.Object]styleProvenance{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil {
				return true
			}
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					if o := info.Defs[name]; o != nil && namedTypePath(o.Type(), "Theme") == modulePath+"/internal/tui/theme" {
						p[o] = provenanceUnresolvedTheme
					}
				}
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				assignProvenance := func(names []*ast.Ident, values []ast.Expr) {
					for i, name := range names {
						if i >= len(values) {
							break
						}
						o := info.Defs[name]
						if o == nil {
							o = info.Uses[name]
						}
						if o == nil {
							continue
						}
						v := expressionProvenance(values[i], info, p)
						if v != provenanceUnknown && p[o] != v {
							p[o], changed = v, true
						}
					}
				}
				switch x := n.(type) {
				case *ast.ValueSpec:
					assignProvenance(x.Names, x.Values)
				case *ast.AssignStmt:
					names := make([]*ast.Ident, 0, len(x.Lhs))
					for _, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							names = append(names, id)
						}
					}
					assignProvenance(names, x.Rhs)
				}
				return true
			})
		}
	}
	return p
}

func expressionProvenance(e ast.Expr, info *types.Info, p map[types.Object]styleProvenance) styleProvenance {
	switch x := e.(type) {
	case *ast.Ident:
		return p[info.Uses[x]]
	case *ast.CallExpr:
		if s, ok := x.Fun.(*ast.SelectorExpr); ok && (s.Sel.Name == "Resolve" || s.Sel.Name == "S") && expressionProvenance(s.X, info, p) == provenanceUnresolvedTheme {
			return provenanceResolvedTheme
		}
		if s, ok := x.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Default" {
			if o := info.Uses[s.Sel]; o != nil && o.Pkg() != nil && o.Pkg().Path() == modulePath+"/internal/tui/theme" {
				return provenanceResolvedTheme
			}
		}
	case *ast.SelectorExpr:
		if expressionProvenance(x.X, info, p) == provenanceResolvedTheme || expressionProvenance(x.X, info, p) == provenanceResolvedValue {
			return provenanceResolvedValue
		}
	}
	return provenanceUnknown
}

func unresolvedThemeField(e ast.Expr, info *types.Info, p map[types.Object]styleProvenance) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if expressionProvenance(s.X, info, p) == provenanceUnresolvedTheme {
		return true
	}
	return unresolvedThemeField(s.X, info, p)
}

func themeDerived(e ast.Expr, info *types.Info, p map[types.Object]styleProvenance) bool {
	return expressionProvenance(e, info, p) == provenanceResolvedValue
}
