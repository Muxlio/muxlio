package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guards shared documentation snippets and cross-links for install defaults.
func TestDocsSetupDefaultsConsistent(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	setupPath := filepath.Join(root, "muxlio-docs", "snippets", "setup-defaults.md")
	setup, err := os.ReadFile(setupPath)
	if err != nil {
		t.Fatalf("read setup-defaults: %v", err)
	}
	body := string(setup)
	for _, want := range []string{
		"7600",
		"7602",
		"7601",
		"antigravity",
		"`claude`",
		"`codex`",
		"`cursor`",
		"`kiro`",
		"stdio `muxlio-mcp`",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup-defaults.md missing %q", want)
		}
	}

	servicePath := filepath.Join(root, "muxlio-docs", "service.md")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read service.md: %v", err)
	}
	serviceDoc := string(service)
	for _, want := range []string{
		`--8<-- "snippets/setup-defaults.md"`,
		"## Setup defaults",
		"index.md#what-is-muxlio",
		"edit\n`capabilities`",
		"`executors` map",
	} {
		if !strings.Contains(serviceDoc, want) {
			t.Fatalf("service.md missing %q", want)
		}
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(readme), "service.md#setup-defaults") {
		t.Fatal("README.md missing link to setup-defaults")
	}

	execSecPath := filepath.Join(root, "muxlio-docs", "architecture", "execution-security.md")
	execSec, err := os.ReadFile(execSecPath)
	if err != nil {
		t.Fatalf("read execution-security: %v", err)
	}
	execDoc := string(execSec)
	for _, want := range []string{
		"## Docker Sandboxes (`sbx`) and Muxlio workers",
		"## Per-attempt isolation backends",
		"../docker-sandbox.md",
		"host.docker.internal:7600",
	} {
		if !strings.Contains(execDoc, want) {
			t.Fatalf("execution-security.md missing %q", want)
		}
	}

	dockerPath := filepath.Join(root, "muxlio-docs", "docker-sandbox.md")
	dockerDoc, err := os.ReadFile(dockerPath)
	if err != nil {
		t.Fatalf("read docker-sandbox: %v", err)
	}
	if !strings.Contains(string(dockerDoc), "execution-security.md#docker-sandboxes-sbx-and-muxlio-workers") {
		t.Fatal("docker-sandbox.md missing link to execution-security Docker section")
	}

	mkdocsPath := filepath.Join(root, "mkdocs.yml")
	mkdocsBytes, err := os.ReadFile(mkdocsPath)
	if err != nil {
		t.Fatalf("read mkdocs.yml: %v", err)
	}
	if !strings.Contains(string(mkdocsBytes), "snippets/*") {
		t.Fatal("mkdocs.yml missing exclude_docs for snippets/*")
	}

	uiPath := filepath.Join(root, "muxlio-docs", "ui-operations.md")
	uiDoc, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read ui-operations.md: %v", err)
	}
	if strings.Contains(string(uiDoc), "../service.md") {
		t.Fatal("ui-operations.md contains invalid link ../service.md; should be service.md")
	}

	errorsPath := filepath.Join(root, "muxlio-docs", "errors", "errors.md")
	errorsDoc, err := os.ReadFile(errorsPath)
	if err != nil {
		t.Fatalf("read errors.md: %v", err)
	}
	if !strings.Contains(string(errorsDoc), "../architecture/mcp-reference.md#check_plan") {
		t.Fatal("errors.md missing relative link to ../architecture/mcp-reference.md#check_plan")
	}

	for _, relRecipe := range []string{
		filepath.Join("recipes", "fix-package-bugs", "README.md"),
		filepath.Join("recipes", "research", "README.md"),
	} {
		recipePath := filepath.Join(root, relRecipe)
		recipeDoc, err := os.ReadFile(recipePath)
		if err != nil {
			t.Fatalf("read %s: %v", relRecipe, err)
		}
		if !strings.Contains(string(recipeDoc), "../../muxlio-docs/index.md#what-is-muxlio") {
			t.Fatalf("%s missing relative link ../../muxlio-docs/index.md#what-is-muxlio", relRecipe)
		}
	}
}