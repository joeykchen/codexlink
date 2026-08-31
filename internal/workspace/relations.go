package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// RepositoryRelation is a best-effort dependency hint between repositories in
// one workspace group. It is descriptive only; it never expands authorization.
type RepositoryRelation struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Type      string `json:"type"`
	Reference string `json:"reference,omitempty"`
}

type repositoryMetadata struct {
	repository   Repository
	nodeName     string
	nodeDeps     []string
	goModule     string
	goRequires   []string
	goLocalLinks []goLocalLink
}

type goLocalLink struct {
	module string
	path   string
}

func detectRepositoryRelations(workspaceRoot string, repositories []Repository) []RepositoryRelation {
	metadata := make([]repositoryMetadata, 0, len(repositories))
	for _, repository := range repositories {
		item := repositoryMetadata{repository: repository}
		item.nodeName, item.nodeDeps = readNodeMetadata(repository.Root)
		item.goModule, item.goRequires, item.goLocalLinks = readGoMetadata(repository.Root)
		metadata = append(metadata, item)
	}

	nodeOwners := uniqueOwners(metadata, func(item repositoryMetadata) string { return item.nodeName })
	goOwners := uniqueOwners(metadata, func(item repositoryMetadata) string { return item.goModule })
	repositoryRoots := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		repositoryRoots[normalizeIdentityPath(repository.Root)] = repository
	}

	seen := map[string]struct{}{}
	relations := make([]RepositoryRelation, 0)
	add := func(relation RepositoryRelation) {
		if relation.From == relation.To || relation.From == "" || relation.To == "" {
			return
		}
		key := relation.From + "\x00" + relation.To + "\x00" + relation.Type + "\x00" + relation.Reference
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		relations = append(relations, relation)
	}

	for _, item := range metadata {
		for _, dependency := range item.nodeDeps {
			if target, ok := nodeOwners[dependency]; ok {
				add(RepositoryRelation{From: item.repository.Path, To: target.Path, Type: "node-package", Reference: dependency})
			}
		}
		for _, required := range item.goRequires {
			if target, ok := goOwners[required]; ok {
				add(RepositoryRelation{From: item.repository.Path, To: target.Path, Type: "go-module", Reference: required})
			}
		}
		for _, link := range item.goLocalLinks {
			targetRoot := link.path
			if !filepath.IsAbs(targetRoot) {
				targetRoot = filepath.Join(item.repository.Root, targetRoot)
			}
			targetRoot = filepath.Clean(targetRoot)
			if real, err := filepath.EvalSymlinks(targetRoot); err == nil {
				targetRoot = real
			}
			if !pathInside(workspaceRoot, targetRoot) {
				continue
			}
			if target, ok := repositoryRoots[normalizeIdentityPath(targetRoot)]; ok {
				add(RepositoryRelation{From: item.repository.Path, To: target.Path, Type: "go-replace", Reference: link.module})
				continue
			}
			// A replace target may point at a package below a repository root.
			best := Repository{}
			bestLength := -1
			for _, candidate := range repositories {
				if pathInside(candidate.Root, targetRoot) && len(candidate.Root) > bestLength {
					best, bestLength = candidate, len(candidate.Root)
				}
			}
			if bestLength >= 0 {
				add(RepositoryRelation{From: item.repository.Path, To: best.Path, Type: "go-replace", Reference: link.module})
			}
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		left := relations[i].From + "\x00" + relations[i].To + "\x00" + relations[i].Type + "\x00" + relations[i].Reference
		right := relations[j].From + "\x00" + relations[j].To + "\x00" + relations[j].Type + "\x00" + relations[j].Reference
		return left < right
	})
	return relations
}

func uniqueOwners(metadata []repositoryMetadata, value func(repositoryMetadata) string) map[string]Repository {
	result := map[string]Repository{}
	duplicates := map[string]bool{}
	for _, item := range metadata {
		key := strings.TrimSpace(value(item))
		if key == "" {
			continue
		}
		if _, exists := result[key]; exists {
			duplicates[key] = true
			continue
		}
		result[key] = item.repository
	}
	for key := range duplicates {
		delete(result, key)
	}
	return result
}

func readNodeMetadata(root string) (string, []string) {
	data, err := readSmallFile(filepath.Join(root, "package.json"), 1024*1024)
	if err != nil {
		return "", nil
	}
	var document struct {
		Name                 string            `json:"name"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal(data, &document) != nil {
		return "", nil
	}
	seen := map[string]struct{}{}
	for _, group := range []map[string]string{document.Dependencies, document.DevDependencies, document.PeerDependencies, document.OptionalDependencies} {
		for name := range group {
			seen[name] = struct{}{}
		}
	}
	dependencies := make([]string, 0, len(seen))
	for name := range seen {
		dependencies = append(dependencies, name)
	}
	sort.Strings(dependencies)
	return strings.TrimSpace(document.Name), dependencies
}

func readGoMetadata(root string) (module string, requires []string, localLinks []goLocalLink) {
	data, err := readSmallFile(filepath.Join(root, "go.mod"), 1024*1024)
	if err != nil {
		return "", nil, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	section := ""
	requireSet := map[string]struct{}{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if index := strings.Index(line, "//"); index >= 0 {
			line = strings.TrimSpace(line[:index])
		}
		if line == "" {
			continue
		}
		if line == ")" {
			section = ""
			continue
		}
		if strings.HasSuffix(line, "(") {
			fields := strings.Fields(strings.TrimSuffix(line, "("))
			if len(fields) == 1 && (fields[0] == "require" || fields[0] == "replace") {
				section = fields[0]
			}
			continue
		}
		if strings.HasPrefix(line, "module ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				module = fields[1]
			}
			continue
		}
		kind := section
		value := line
		if strings.HasPrefix(line, "require ") {
			kind, value = "require", strings.TrimSpace(strings.TrimPrefix(line, "require "))
		} else if strings.HasPrefix(line, "replace ") {
			kind, value = "replace", strings.TrimSpace(strings.TrimPrefix(line, "replace "))
		}
		switch kind {
		case "require":
			fields := strings.Fields(value)
			if len(fields) >= 1 {
				requireSet[fields[0]] = struct{}{}
			}
		case "replace":
			left, right, ok := strings.Cut(value, "=>")
			if !ok {
				continue
			}
			leftFields, rightFields := strings.Fields(strings.TrimSpace(left)), strings.Fields(strings.TrimSpace(right))
			if len(leftFields) == 0 || len(rightFields) == 0 {
				continue
			}
			target := rightFields[0]
			if filepath.IsAbs(target) || strings.HasPrefix(target, ".") {
				localLinks = append(localLinks, goLocalLink{module: leftFields[0], path: target})
			}
		}
	}
	for required := range requireSet {
		requires = append(requires, required)
	}
	sort.Strings(requires)
	sort.Slice(localLinks, func(i, j int) bool {
		return localLinks[i].module+"\x00"+localLinks[i].path < localLinks[j].module+"\x00"+localLinks[j].path
	})
	return strings.TrimSpace(module), requires, localLinks
}

func readSmallFile(path string, limit int64) ([]byte, error) {
	return readBoundedRegularFile(path, limit)
}

func pathInside(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
