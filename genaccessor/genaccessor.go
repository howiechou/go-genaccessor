/*
genaccessor is accsessor generator for Go.

```go
    type Foo struct {
        key string `getter:"[alias,]..." setter:"[alias,]..."`
    }
```

with `go generate` command

```go
    //go:generate go-genaccessor
```
*/
package genaccessor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
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
	fileFilter    func(finfo os.FileInfo) bool
	generatorName string
}

func WithFileFilter(fileFilter func(finfo os.FileInfo) bool) Option {
	return func(o *option) {
		o.fileFilter = fileFilter
	}
}

func WithGeneratorName(generatorName string) Option {
	return func(o *option) {
		o.generatorName = generatorName
	}
}

func Run(targetDir string, newWriter func(pkg *ast.Package) io.Writer, opts ...Option) error {
	option := option{
		generatorName: "go-genaccessor",
	}
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

	for _, pkg := range pkgMap {
		body := new(bytes.Buffer)
		importPackages := make([]*ast.ImportSpec, 0, 10)

		// sort filelist by name
		sortedFileNameList := make([]string, 0, len(pkg.Files))
		for name := range pkg.Files {
			sortedFileNameList = append(sortedFileNameList, name)
		}
		sort.Strings(sortedFileNameList)
		sortedFileList := make([]*ast.File, len(pkg.Files))
		for i, name := range sortedFileNameList {
			sortedFileList[i] = pkg.Files[name]
		}

		for _, file := range sortedFileList {
			for _, decl := range file.Decls {
				decl, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				if decl.Tok != token.TYPE {
					continue
				}
				for _, spec := range decl.Specs {
					spec := spec.(*ast.TypeSpec)
					structType, ok := spec.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structType.Fields.List {
						if field.Tag == nil {
							continue
						}
						tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))

						b := new(bytes.Buffer)
						err := printer.Fprint(b, fset, field.Type)
						if err != nil {
							return err
						}
						fieldTypeText := b.String()

						for _, genMethod := range genMethods {
							methodNamesText, hasTag := tag.Lookup(genMethod.tagKey)
							if !hasTag {
								continue
							}

							methodNames := []string{genMethod.defaultMethodName(field.Names[0].Name)}
							if len(methodNamesText) != 0 {
								methodNames = strings.Split(methodNamesText, ",")
							}

							for _, s := range strings.FieldsFunc(fieldTypeText, func(c rune) bool {
								return !unicode.IsLetter(c) && c != '.'
							}) {
								ss := strings.SplitN(s, ".", 2)
								if len(ss) == 2 {
									for i := range file.Imports {
										if file.Imports[i].Name == nil {
											if path.Base(strings.Trim(file.Imports[i].Path.Value, `"`)) != ss[0] {
												continue
											}
											importPackages = append(importPackages, file.Imports[i])
											break
										}
										if file.Imports[i].Name.Name != ss[0] {
											continue
										}
										importPackages = append(importPackages, file.Imports[i])
										break
									}
								}
							}

							for _, methodName := range methodNames {
								if err := genMethod.tmpl.Execute(body, tmplParam{
									StructName: spec.Name.Name,
									MethodName: methodName,
									FieldType:  fieldTypeText,
									FieldName:  field.Names[0].Name,
								}); err != nil {
									panic(err)
								}
							}
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
			// Code generated by {{ .GeneratorName }}; DO NOT EDIT.
		
			package {{ .PackageName }}
		
			{{ .ImportPackages }}
		
			{{ .Body }}
		`)).Execute(out, map[string]string{
			"GeneratorName":  option.generatorName,
			"PackageName":    pkg.Name,
			"ImportPackages": fmtImports(importPackages, fset),
			"Body":           body.String(),
		})
		if err != nil {
			return err
		}

		str, err := format.Source(out.Bytes())
		if err != nil {
			return err
		}
		writer := newWriter(pkg)
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

func fmtImports(pkgs []*ast.ImportSpec, fset *token.FileSet) string {
	if len(pkgs) == 0 {
		return ""
	}

	groups := make([][]*ast.ImportSpec, 2)

	for _, pkg := range pkgs {
		if len(strings.Split(pkg.Path.Value, "/")) < 3 && !strings.Contains(pkg.Path.Value, ".") {
			groups[0] = append(groups[0], pkg)
			continue
		}
		groups[1] = append(groups[1], pkg)
	}

	b := new(bytes.Buffer)
	for _, group := range groups {
		group := group
		sort.Slice(group, func(i, j int) bool {
			return group[i].Path.Value < group[j].Path.Value
		})
		for _, pkg := range group {
			err := printer.Fprint(b, fset, pkg)
			if err != nil {
				panic(err)
			}
			_, err = b.WriteRune('\n')
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
