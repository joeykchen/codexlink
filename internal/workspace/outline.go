package workspace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	FileOutlineDefaultLimit = 200
	FileOutlineMaxLimit     = 500
	FileOutlineMaxBytes     = 1024 * 1024
)

type OutlineItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	Detail  string `json:"detail,omitempty"`
}

type FileOutlineResult struct {
	Path          string        `json:"path"`
	SizeBytes     int64         `json:"sizeBytes"`
	Language      string        `json:"language"`
	Package       string        `json:"package,omitempty"`
	Imports       []string      `json:"imports,omitempty"`
	Items         []OutlineItem `json:"items"`
	TotalItems    int           `json:"totalItems"`
	ReturnedItems int           `json:"returnedItems"`
	Truncated     bool          `json:"truncated"`
	Complete      bool          `json:"complete"`
	ParseError    string        `json:"parseError,omitempty"`
}

func (w *Workspace) OutlineFile(requested string, limit int) (FileOutlineResult, error) {
	if strings.TrimSpace(requested) == "" {
		return FileOutlineResult{}, NewError(ErrInvalidArgument, "path is required")
	}
	if limit == 0 {
		limit = FileOutlineDefaultLimit
	}
	if limit < 1 || limit > FileOutlineMaxLimit {
		return FileOutlineResult{}, NewError(ErrInvalidArgument, "limit must be between 1 and %d", FileOutlineMaxLimit)
	}
	abs, rel, err := w.Resolve(requested, false)
	if err != nil {
		return FileOutlineResult{}, err
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return FileOutlineResult{}, NewError(ErrFileNotFound, "file not found: %s", rel)
	}
	if err != nil {
		return FileOutlineResult{}, err
	}
	if !info.Mode().IsRegular() {
		return FileOutlineResult{}, NewError(ErrNotFile, "not a regular file: %s", rel)
	}
	if info.Size() > FileOutlineMaxBytes {
		return FileOutlineResult{}, NewError(ErrFileTooLarge, "file exceeds %d bytes: %s", FileOutlineMaxBytes, rel)
	}
	binary, err := isBinary(abs)
	if err != nil {
		return FileOutlineResult{}, err
	}
	if binary {
		return FileOutlineResult{}, NewError(ErrBinaryFile, "binary file: %s", rel)
	}
	data, err := readBoundedRegularFile(abs, FileOutlineMaxBytes)
	if err != nil {
		return FileOutlineResult{}, err
	}
	result := FileOutlineResult{Path: rel, SizeBytes: info.Size(), Items: []OutlineItem{}, Complete: true}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		result.Language = "go"
		outlineGo(data, limit, &result)
	case ".md", ".markdown":
		result.Language = "markdown"
		outlineMarkdown(data, limit, &result)
	default:
		return FileOutlineResult{}, NewError(ErrUnsupportedType, "file outline supports Go and Markdown files")
	}
	result.ReturnedItems = len(result.Items)
	result.Truncated = result.ReturnedItems < result.TotalItems
	return result, nil
}

func outlineGo(data []byte, limit int, result *FileOutlineResult) {
	files := token.NewFileSet()
	parsed, parseErr := parser.ParseFile(files, result.Path, data, parser.AllErrors)
	if parsed == nil {
		result.Complete = false
		result.ParseError = "source contains parse errors; outline is incomplete"
		return
	}
	result.Package = parsed.Name.Name
	for _, spec := range parsed.Imports {
		if value, err := strconv.Unquote(spec.Path.Value); err == nil {
			result.Imports = append(result.Imports, value)
		}
	}
	for _, declaration := range parsed.Decls {
		switch decl := declaration.(type) {
		case *ast.FuncDecl:
			kind := "function"
			name := decl.Name.Name
			prefix := "func "
			if decl.Recv != nil {
				kind = "method"
				prefix += renderNode(files, decl.Recv) + " "
			}
			detail := prefix + name + strings.TrimPrefix(renderSafeSignature(files, decl.Type), "func")
			appendOutline(result, limit, OutlineItem{Kind: kind, Name: name, Line: files.Position(decl.Pos()).Line, EndLine: files.Position(decl.End()).Line, Detail: boundedSingleLine(detail, 500)})
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					appendOutline(result, limit, OutlineItem{Kind: "type", Name: item.Name.Name, Line: files.Position(item.Pos()).Line, EndLine: files.Position(item.End()).Line, Detail: "type " + item.Name.Name})
				case *ast.ValueSpec:
					names := make([]string, 0, len(item.Names))
					for _, name := range item.Names {
						names = append(names, name.Name)
					}
					kind := strings.ToLower(decl.Tok.String())
					appendOutline(result, limit, OutlineItem{Kind: kind, Name: strings.Join(names, ", "), Line: files.Position(item.Pos()).Line, EndLine: files.Position(item.End()).Line, Detail: kind + " " + strings.Join(names, ", ")})
				}
			}
		}
	}
	if parseErr != nil {
		result.Complete = false
		result.ParseError = "source contains parse errors; outline may be incomplete"
	}
}

func outlineMarkdown(data []byte, limit int, result *FileOutlineResult) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), FileOutlineMaxBytes+64*1024)
	line := 0
	fence := byte(0)
	fenceLength := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if marker, length, ok := markdownFence(text); ok {
			if fence == 0 {
				fence, fenceLength = marker, length
			} else if marker == fence && length >= fenceLength {
				fence, fenceLength = 0, 0
			}
			continue
		}
		if fence != 0 {
			continue
		}
		level := 0
		for level < len(text) && level < 6 && text[level] == '#' {
			level++
		}
		if level == 0 || len(text) <= level || text[level] != ' ' {
			continue
		}
		name := boundedSingleLine(strings.TrimSpace(strings.TrimRight(text[level+1:], "#")), 500)
		if name != "" {
			appendOutline(result, limit, OutlineItem{Kind: "heading", Name: name, Line: line, EndLine: line, Detail: fmt.Sprintf("h%d", level)})
		}
	}
	if err := scanner.Err(); err != nil {
		result.Complete = false
		result.ParseError = boundedSingleLine(err.Error(), 300)
	}
}

func markdownFence(text string) (byte, int, bool) {
	if len(text) < 3 || (text[0] != '`' && text[0] != '~') {
		return 0, 0, false
	}
	marker := text[0]
	length := 0
	for length < len(text) && text[length] == marker {
		length++
	}
	return marker, length, length >= 3
}

func appendOutline(result *FileOutlineResult, limit int, item OutlineItem) {
	result.TotalItems++
	if len(result.Items) < limit {
		result.Items = append(result.Items, item)
	}
}

func renderNode(files *token.FileSet, node any) string {
	var output bytes.Buffer
	_ = printer.Fprint(&output, files, node)
	return output.String()
}

func renderSafeSignature(files *token.FileSet, node ast.Node) string {
	ast.Inspect(node, func(current ast.Node) bool {
		switch value := current.(type) {
		case *ast.Field:
			value.Tag = nil
		case *ast.BasicLit:
			switch value.Kind {
			case token.STRING:
				value.Value = `""`
			case token.CHAR:
				value.Value = `'_'`
			case token.FLOAT:
				value.Value = "0.0"
			case token.IMAG:
				value.Value = "0i"
			default:
				value.Value = "0"
			}
		}
		return true
	})
	return renderNode(files, node)
}
