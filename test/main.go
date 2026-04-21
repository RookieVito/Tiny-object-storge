package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

type testFunc func()

type testCase struct {
	name string
	fn   testFunc
}

var tests []testCase

// registerTest adds a test phase. Call from init().
func registerTest(name string, fn testFunc) {
	tests = append(tests, testCase{name: name, fn: fn})
}

func main() {
	// Parse optional filter: go run ./test [phase]
	filter := ""
	if len(os.Args) > 1 {
		filter = strings.ToLower(os.Args[1])
	}

	// Sort by name for deterministic order.
	sort.Slice(tests, func(i, j int) bool { return tests[i].name < tests[j].name })

	total := 0
	for _, t := range tests {
		if filter != "" && !strings.Contains(strings.ToLower(strings.ReplaceAll(t.name, " ", "")), filter) {
			continue
		}
		fmt.Printf("\n=== %s ===\n", t.name)
		t.fn()
		total++
	}

	if total == 0 {
		fmt.Fprintf(os.Stderr, "no tests matched filter %q\n", filter)
		os.Exit(1)
	}

	fmt.Println()
	CheckFailed()
	fmt.Println("=== ALL TESTS PASSED ===")
}
