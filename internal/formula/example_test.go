package formula_test

import (
	"fmt"
	"log"

	"github.com/jonbaldie/gastown/internal/formula"
)

func ExampleParse_workflow() {
	toml := `
formula = "release"
description = "Standard release process"
type = "workflow"

[[steps]]
id = "test"
title = "Run Tests"

[[steps]]
id = "build"
title = "Build"
needs = ["test"]

[[steps]]
id = "publish"
title = "Publish"
needs = ["build"]
`
	f, err := formula.Parse([]byte(toml))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Formula: %s\n", f.Name)
	fmt.Printf("Type: %s\n", f.Type)
	fmt.Printf("Steps: %d\n", len(f.Steps))

	// Output:
	// Formula: release
	// Type: workflow
	// Steps: 3
}

func ExampleFormula_TopologicalSort() {
	toml := `
formula = "build-pipeline"
type = "workflow"

[[steps]]
id = "lint"
title = "Lint"

[[steps]]
id = "test"
title = "Test"
needs = ["lint"]

[[steps]]
id = "build"
title = "Build"
needs = ["lint"]

[[steps]]
id = "deploy"
title = "Deploy"
needs = ["test", "build"]
`
	f, _ := formula.Parse([]byte(toml))
	order, _ := f.TopologicalSort()

	fmt.Println("Execution order:")
	for i, id := range order {
		fmt.Printf("  %d. %s\n", i+1, id)
	}

	// Output:
	// Execution order:
	//   1. lint
	//   2. test
	//   3. build
	//   4. deploy
}

func ExampleFormula_ReadySteps() {
	toml := `
formula = "pipeline"
type = "workflow"

[[steps]]
id = "a"
title = "Step A"

[[steps]]
id = "b"
title = "Step B"
needs = ["a"]

[[steps]]
id = "c"
title = "Step C"
needs = ["a"]

[[steps]]
id = "d"
title = "Step D"
needs = ["b", "c"]
`
	f, _ := formula.Parse([]byte(toml))

	// Initially, only "a" is ready (no dependencies)
	completed := map[string]bool{}
	ready := f.ReadySteps(completed)
	fmt.Printf("Initially ready: %v\n", ready)

	// After completing "a", both "b" and "c" become ready
	completed["a"] = true
	ready = f.ReadySteps(completed)
	fmt.Printf("After 'a': %v\n", ready)

	// After completing "b" and "c", "d" becomes ready
	completed["b"] = true
	completed["c"] = true
	ready = f.ReadySteps(completed)
	fmt.Printf("After 'b' and 'c': %v\n", ready)

	// Output:
	// Initially ready: [a]
	// After 'a': [b c]
	// After 'b' and 'c': [d]
}

func ExampleParse_convoy() {
	toml := `
formula = "security-audit"
type = "convoy"

[[legs]]
id = "sast"
title = "Static Analysis"
focus = "Code vulnerabilities"

[[legs]]
id = "deps"
title = "Dependency Check"
focus = "Vulnerable packages"

[synthesis]
title = "Combine Findings"
depends_on = ["sast", "deps"]
`
	f, _ := formula.Parse([]byte(toml))

	fmt.Printf("Formula: %s\n", f.Name)
	fmt.Printf("Legs: %d\n", len(f.Legs))

	// All legs are ready immediately (parallel execution)
	ready := f.ReadySteps(map[string]bool{})
	fmt.Printf("Ready for parallel execution: %v\n", ready)

	// Output:
	// Formula: security-audit
	// Legs: 2
	// Ready for parallel execution: [sast deps]
}

func ExampleParse_typeInference() {
	// Type can be inferred from content
	toml := `
formula = "auto-typed"

[[steps]]
id = "first"
title = "First Step"

[[steps]]
id = "second"
title = "Second Step"
needs = ["first"]
`
	f, _ := formula.Parse([]byte(toml))

	// Type was inferred as "workflow" because [[steps]] were present
	fmt.Printf("Inferred type: %s\n", f.Type)

	// Output:
	// Inferred type: workflow
}

func ExampleFormula_Validate_cycleDetection() {
	// This formula has a cycle: a -> b -> c -> a
	toml := `
formula = "cyclic"
type = "workflow"

[[steps]]
id = "a"
title = "Step A"
needs = ["c"]

[[steps]]
id = "b"
title = "Step B"
needs = ["a"]

[[steps]]
id = "c"
title = "Step C"
needs = ["b"]
`
	_, err := formula.Parse([]byte(toml))
	if err != nil {
		fmt.Printf("Validation error: %v\n", err)
	}

	// Output:
	// Validation error: cycle detected involving: a
}

func ExampleFormula_GetStep() {
	toml := `
formula = "lookup-demo"
type = "workflow"

[[steps]]
id = "build"
title = "Build Application"
description = "Compile source code"
`
	f, _ := formula.Parse([]byte(toml))

	step := f.GetStep("build")
	if step != nil {
		fmt.Printf("Found: %s\n", step.Title)
		fmt.Printf("Description: %s\n", step.Description)
	}

	missing := f.GetStep("nonexistent")
	fmt.Printf("Missing step is nil: %v\n", missing == nil)

	// Output:
	// Found: Build Application
	// Description: Compile source code
	// Missing step is nil: true
}
