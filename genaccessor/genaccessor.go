package genaccessor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

type Option func(o *option)

type option struct {
	fileFilter func(finfo os.FileInfo) bool
}

func WithFileFilter(fileFilter func(finfo os.FileInfo) bool) Option {
	return func(o *option) {
		o.fileFilter = fileFilter
	}
}

func Run(targetDir string, newWriter func(pkg *ast.Package) io.Writer, opts ...Option) error {
	option := option{}
	for _, opt := range opts {
		opt(&option)
	}

	fset := token.NewFileSet()
	pkgMap, err := parser.ParseDir(
		fset,
		filepath.FromSlash(targetDir),
		option.fileFilter,
		0,
	)
	if err != nil {
		return err
	}

	for _, astPkg := range pkgMap {
		body := new(bytes.Buffer)
		importPackages := make([]*types.Package, 0, 10)

		typesConf := types.Config{Importer: importer.Default()}

		files := make([]*ast.File, 0, len(astPkg.Files))
		for _, f := range astPkg.Files {
			files = append(files, f)
		}

		pkg, err := typesConf.Check("", fset, files, nil)
		if err != nil {
			return err
		}

		for _, defName := range pkg.Scope().Names() {
			namedDef, isNamed := pkg.Scope().Lookup(defName).Type().(*types.Named)
			if !isNamed {
				continue
			}
			structDef, isStruct := namedDef.Underlying().(*types.Struct)
			if !isStruct {
				continue
			}
			for i := 0; i < structDef.NumFields(); i++ {
				tag := reflect.StructTag(structDef.Tag(i))
				field := structDef.Field(i)

				for _, genMethod := range genMethods {
					methodNamesText, hasTag := tag.Lookup(genMethod.tagKey)
					if !hasTag {
						continue
					}

					fieldTypeText := field.Type().String()
					if namedDef, isNamed := field.Type().(*types.Named); isNamed &&
						namedDef.Obj().Pkg() != nil &&
						namedDef.Obj().Pkg().Path() != "" {
						fieldTypeText = namedDef.Obj().Pkg().Name() + "." + namedDef.Obj().Name()
						importPackages = append(importPackages, namedDef.Obj().Pkg())
					}

					methodNames := []string{genMethod.defaultMethodName(field.Name())}
					if len(methodNamesText) != 0 {
						methodNames = strings.Split(methodNamesText, ",")
					}

					for _, methodName := range methodNames {
						if err := genMethod.tmpl.Execute(body, tmplParam{
							StructName: namedDef.String(),
							MethodName: methodName,
							FieldType:  fieldTypeText,
							FieldName:  field.Name(),
						}); err != nil {
							panic(err)
						}
					}
				}
			}
		}
		if body.Len() == 0 {
			continue
		}

		out := new(bytes.Buffer)

		err = template.Must(template.New("out").Parse(`
			// Automatically generated by go generate; DO NOT EDIT.

			package {{ .PackageName }}

			{{ .ImportPackages }}

			{{ .Body }}
		`)).Execute(out, map[string]string{
			"PackageName":    pkg.Name(),
			"ImportPackages": fmtImports(importPackages),
			"Body":           body.String(),
		})
		if err != nil {
			return err
		}

		str, err := format.Source(out.Bytes())
		if err != nil {
			return err
		}
		writer := newWriter(astPkg)
		if closer, ok := writer.(io.Closer); ok {
			defer closer.Close()
		}
		if _, err := writer.Write(str); err != nil {
			return err
		}
	}

	return nil
}

type tmplParam struct {
	StructName string
	MethodName string
	FieldType  string
	FieldName  string
}

var genMethods = []struct {
	tagKey            string
	tmpl              *template.Template
	defaultMethodName func(filedName string) string
}{
	{
		tagKey: "getter",
		tmpl: template.Must(template.New("getter").Parse(`
func (m {{ .StructName }}) {{ .MethodName }}() {{ .FieldType }} {
				return m.{{ .FieldName }}
			}
		`)),
		defaultMethodName: toUpperCamel,
	},
	{
		tagKey: "setter",
		tmpl: template.Must(template.New("getter").Parse(`
func (m *{{ .StructName }}) {{ .MethodName }}(s {{ .FieldType }}) {
				m.{{ .FieldName }} = s
			}
		`)),
		defaultMethodName: func(fieldName string) string {
			return "Set" + toUpperCamel(fieldName)
		},
	},
}

func toUpperCamel(s string) string {
	if s == "" {
		return s
	}
	firstNotLowerIndex := strings.IndexFunc(s, func(c rune) bool {
		return !unicode.IsLower(c)
	})
	if firstNotLowerIndex == -1 {
		firstNotLowerIndex = len(s)
	}
	if commonInitialisms[s[:firstNotLowerIndex]] {
		return strings.ToUpper(s[:firstNotLowerIndex]) + s[firstNotLowerIndex:]
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// from https://github.com/golang/lint
var commonInitialisms = map[string]bool{
	"acl":   true,
	"api":   true,
	"ascii": true,
	"cpu":   true,
	"css":   true,
	"dns":   true,
	"eof":   true,
	"guid":  true,
	"html":  true,
	"http":  true,
	"https": true,
	"id":    true,
	"ip":    true,
	"json":  true,
	"lhs":   true,
	"qps":   true,
	"ram":   true,
	"rhs":   true,
	"rpc":   true,
	"sla":   true,
	"smtp":  true,
	"sql":   true,
	"ssh":   true,
	"tcp":   true,
	"tls":   true,
	"ttl":   true,
	"udp":   true,
	"ui":    true,
	"uid":   true,
	"uuid":  true,
	"uri":   true,
	"url":   true,
	"utf8":  true,
	"vm":    true,
	"xml":   true,
	"xmpp":  true,
	"xsrf":  true,
	"xss":   true,
}

func fmtImports(pkgs []*types.Package) string {
	if len(pkgs) == 0 {
		return ""
	}

	groups := make([][]*types.Package, 2)

	for _, pkg := range pkgs {
		if len(strings.Split(pkg.Path(), "/")) < 3 && !strings.Contains(pkg.Path(), ".") {
			groups[0] = append(groups[0], pkg)
			continue
		}
		groups[1] = append(groups[1], pkg)
	}

	b := new(bytes.Buffer)
	for _, group := range groups {
		group := group
		sort.Slice(group, func(i, j int) bool {
			return group[i].Path() < group[j].Path()
		})
		for _, pkg := range group {
			var err error
			if path.Base(pkg.Path()) == pkg.Name() {
				_, err = b.WriteString(`"` + pkg.Path() + `"` + "\n")
			} else {
				_, err = b.WriteString(pkg.Name() + ` "` + pkg.Path() + `"` + "\n")
			}
			if err != nil {
				panic(err)
			}
		}
		_, err := b.WriteRune('\n')
		if err != nil {
			panic(err)
		}
	}

	return fmt.Sprintf(`import (
%s
		)`,
		b.String(),
	)
}