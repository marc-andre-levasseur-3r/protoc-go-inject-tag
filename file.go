package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
)

var (
	subOneofStructRx = regexp.MustCompile(`^//\s\*(\w+)$`)
	oneOfCommentRx   = regexp.MustCompile(`^//\s*@inject_tag_oneof:\s([\w]+):\s*(.*)$`)
	rComment         = regexp.MustCompile(`^//\s*@inject_tag:\s*(.*)$`)
	rInject          = regexp.MustCompile("`.+`$")
	rTags            = regexp.MustCompile(`[\w_]+:"[^"]+"`)
)

type textArea struct {
	Start      int
	End        int
	CurrentTag string
	InjectTag  string
}

type aeraContainer []textArea

func (a aeraContainer) Len() int {
	return len(a)
}

func  (a aeraContainer) Less(i, j int) bool {
	return a[i].Start < a[j].Start
}

func (a aeraContainer) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

type oneofTagInfo struct {
	varName string
	tag     string
}

func parseFile(inputPath string, xxxSkip []string) (areas aeraContainer, err error) {
	oneofTags := make(map[string]oneofTagInfo)
	log.Printf("parsing file %q for inject tag comments", inputPath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, inputPath, nil, parser.ParseComments)
	if err != nil {
		return
	}

	for _, decl := range f.Decls {
		// check if is generic declaration
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		var typeSpec *ast.TypeSpec
		for _, spec := range genDecl.Specs {
			if ts, tsOK := spec.(*ast.TypeSpec); tsOK {
				typeSpec = ts
				break
			}
		}

		// skip if can't get type spec
		if typeSpec == nil {
			continue
		}

		// not a struct, skip
		structDecl, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			continue
		}

		builder := strings.Builder{}
		if len(xxxSkip) > 0 {
			for i, skip := range xxxSkip {
				builder.WriteString(fmt.Sprintf("%s:\"-\"", skip))
				if i > 0 {
					builder.WriteString(",")
				}
			}
		}

		for _, field := range structDecl.Fields.List {
			// skip if field has no doc
			if len(field.Names) > 0 {
				name := field.Names[0].Name
				if len(xxxSkip) > 0 && strings.HasPrefix(name, "XXX") {
					currentTag := field.Tag.Value
					area := textArea{
						Start:      int(field.Pos()),
						End:        int(field.End()),
						CurrentTag: currentTag[1 : len(currentTag)-1],
						InjectTag:  builder.String(),
					}
					areas = append(areas, area)
				}
			}
			if field.Doc == nil {
				continue
			}

			// If field is oneof, it has generated structs for each alternative. We get the names here
			oneofStructs := make([]string, 0)
			for _, comment := range field.Doc.List {
				match := subOneofStructRx.FindStringSubmatch(comment.Text)
				if len(match) == 2 {
					oneofStructs = append(oneofStructs, match[1])
				}
			}

			//
			for _, comment := range field.Doc.List {
				// Store data for oneof tags in a separate collection first
				varName, oneofTag := tagOneofFromComment(comment.Text)
				if varName != "" {
					varName = strings.Title(varName)
					// The generated struct name has mangleName as part of it (but not necessary equal
					mangleName := fmt.Sprintf("%s_%s", typeSpec.Name.String(), varName)
					for _, structName := range oneofStructs {
						if strings.Contains(structName, mangleName) {
							oneofTags[structName] = oneofTagInfo{varName: varName, tag: oneofTag}
							continue
						}
					}
				}

				tag := tagFromComment(comment.Text)
				if tag == "" {
					continue
				}
				currentTag := field.Tag.Value
				area := textArea{
					Start:      int(field.Pos()),
					End:        int(field.End()),
					CurrentTag: currentTag[1 : len(currentTag)-1],
					InjectTag:  tag,
				}
				areas = append(areas, area)
			}
		}
	}
	// Redo a second pass to retrieve all the generated structs where to inject oneof tags
	for _, decl := range f.Decls {
		// check if is generic declaration
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		var typeSpec *ast.TypeSpec
		for _, spec := range genDecl.Specs {
			if ts, tsOK := spec.(*ast.TypeSpec); tsOK {
				typeSpec = ts
				break
			}
		}

		// skip if can't get type spec
		if typeSpec == nil {
			continue
		}
		oneofData, ok := oneofTags[typeSpec.Name.String()]

		// Not in the list, skip
		if !ok {
			continue
		}

		// not a struct, skip
		structDecl, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			continue
		}
		field := structDecl.Fields.List[0]
		currentTag := field.Tag.Value
		area := textArea{
			Start:      int(field.Pos()),
			End:        int(field.End()),
			CurrentTag: currentTag[1 : len(currentTag)-1],
			InjectTag:  oneofData.tag,
		}
		areas = append(areas, area)
	}
	// Sort Text areas by position to make sure that the intervals increase correctly when we rewrite file
	sort.Sort(areas)
	log.Printf("parsed file %q, number of fields to inject custom tags: %d", inputPath, len(areas))
	return
}

func writeFile(inputPath string, areas aeraContainer) (err error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return
	}

	contents, err := ioutil.ReadAll(f)
	if err != nil {
		return
	}

	if err = f.Close(); err != nil {
		return
	}

	// inject custom tags from tail of file first to preserve order
	for i := range areas {
		area := areas[len(areas)-i-1]
		log.Printf("inject custom tag %q to expression %q", area.InjectTag, string(contents[area.Start-1:area.End-1]))
		contents = injectTag(contents, area)
	}
	if err = ioutil.WriteFile(inputPath, contents, 0644); err != nil {
		return
	}

	if len(areas) > 0 {
		log.Printf("file %q is injected with custom tags", inputPath)
	}
	return
}
