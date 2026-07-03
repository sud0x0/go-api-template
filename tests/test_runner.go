//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	colourReset  = "\033[0m"
	colourRed    = "\033[31m"
	colourGreen  = "\033[32m"
	colourYellow = "\033[33m"
	colourBold   = "\033[1m"
)

type TestEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

type TestResult struct {
	Package  string  `json:"package"`
	Test     string  `json:"test"`
	Status   string  `json:"status"`
	Duration float64 `json:"duration"`
}

type TestReport struct {
	Timestamp string        `json:"timestamp"`
	Summary   TestSummary   `json:"summary"`
	Results   []*TestResult `json:"results"`
}

type TestSummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

func main() {
	fmt.Println()
	fmt.Printf("%sRunning tests...%s\n\n", colourBold, colourReset)

	// -race so the pretty wrapper exercises the SAME suite as `make test-unit`
	// (go test -race ./...) — pretty and plain must test the same thing.
	cmd := exec.Command("go", "test", "-race", "-json", "./...")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating pipe: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting tests: %v\n", err)
		os.Exit(1)
	}

	results := make(map[string]*TestResult)
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		var event TestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Test == "" {
			continue
		}
		key := event.Package + "/" + event.Test
		switch event.Action {
		case "run":
			results[key] = &TestResult{Package: event.Package, Test: event.Test, Status: "RUNNING"}
		case "pass":
			if r, ok := results[key]; ok {
				r.Status = "PASS"
				r.Duration = event.Elapsed
			}
		case "fail":
			if r, ok := results[key]; ok {
				r.Status = "FAIL"
				r.Duration = event.Elapsed
			}
		case "skip":
			if r, ok := results[key]; ok {
				r.Status = "SKIP"
				r.Duration = event.Elapsed
			}
		}
	}

	cmd.Wait()

	var sortedResults []*TestResult
	for _, r := range results {
		sortedResults = append(sortedResults, r)
	}
	sort.Slice(sortedResults, func(i, j int) bool {
		if sortedResults[i].Package == sortedResults[j].Package {
			return sortedResults[i].Test < sortedResults[j].Test
		}
		return sortedResults[i].Package < sortedResults[j].Package
	})

	maxPkg, maxTest := len("Package"), len("Test")
	for _, r := range sortedResults {
		pkg := shortPackage(r.Package)
		if len(pkg) > maxPkg {
			maxPkg = len(pkg)
		}
		if len(r.Test) > maxTest {
			maxTest = len(r.Test)
		}
	}
	if maxPkg > 30 {
		maxPkg = 30
	}
	if maxTest > 40 {
		maxTest = 40
	}

	statusWidth := 6
	durationWidth := 10

	printTableLine(maxPkg, maxTest, statusWidth, durationWidth, "top")
	printTableHeader(maxPkg, maxTest, statusWidth, durationWidth)
	printTableLine(maxPkg, maxTest, statusWidth, durationWidth, "middle")

	passed, failed, skipped := 0, 0, 0
	for _, r := range sortedResults {
		printTableRow(r, maxPkg, maxTest, statusWidth, durationWidth)
		switch r.Status {
		case "PASS":
			passed++
		case "FAIL":
			failed++
		case "SKIP":
			skipped++
		}
	}

	printTableLine(maxPkg, maxTest, statusWidth, durationWidth, "bottom")

	fmt.Println()
	fmt.Printf("Results: %s%d passed%s, ", colourGreen, passed, colourReset)
	if failed > 0 {
		fmt.Printf("%s%d failed%s, ", colourRed, failed, colourReset)
	} else {
		fmt.Printf("%d failed, ", failed)
	}
	if skipped > 0 {
		fmt.Printf("%s%d skipped%s\n", colourYellow, skipped, colourReset)
	} else {
		fmt.Printf("%d skipped\n", skipped)
	}
	fmt.Println()

	if err := saveResultsToJSON(sortedResults, passed, failed, skipped); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to save JSON results: %v\n", err)
	}

	if failed > 0 {
		os.Exit(1)
	}
}

func saveResultsToJSON(results []*TestResult, passed, failed, skipped int) error {
	tmpDir := "_test_results_"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	now := time.Now()
	filename := fmt.Sprintf("%s/test_results_%s.json", tmpDir, now.Format("20060102_150405"))
	report := TestReport{
		Timestamp: now.Format(time.RFC3339),
		Summary:   TestSummary{Total: passed + failed + skipped, Passed: passed, Failed: failed, Skipped: skipped},
		Results:   results,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	fmt.Printf("Results saved to: %s\n\n", filename)
	return nil
}

func shortPackage(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return pkg
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func printTableLine(pkgW, testW, statusW, durW int, position string) {
	var left, mid, right, horiz string
	switch position {
	case "top":
		left, mid, right, horiz = "\u250c", "\u252c", "\u2510", "\u2500"
	case "middle":
		left, mid, right, horiz = "\u251c", "\u253c", "\u2524", "\u2500"
	case "bottom":
		left, mid, right, horiz = "\u2514", "\u2534", "\u2518", "\u2500"
	}
	fmt.Print(left)
	fmt.Print(strings.Repeat(horiz, pkgW+2))
	fmt.Print(mid)
	fmt.Print(strings.Repeat(horiz, testW+2))
	fmt.Print(mid)
	fmt.Print(strings.Repeat(horiz, statusW+2))
	fmt.Print(mid)
	fmt.Print(strings.Repeat(horiz, durW+2))
	fmt.Println(right)
}

func printTableHeader(pkgW, testW, statusW, durW int) {
	fmt.Printf("\u2502 %-*s \u2502 %-*s \u2502 %-*s \u2502 %-*s \u2502\n",
		pkgW, "Package", testW, "Test", statusW, "Status", durW, "Duration")
}

func printTableRow(r *TestResult, pkgW, testW, statusW, durW int) {
	pkg := truncate(shortPackage(r.Package), pkgW)
	test := truncate(r.Test, testW)
	duration := fmt.Sprintf("%.3fs", r.Duration)
	var statusColour string
	switch r.Status {
	case "PASS":
		statusColour = colourGreen
	case "FAIL":
		statusColour = colourRed
	case "SKIP":
		statusColour = colourYellow
	default:
		statusColour = colourReset
	}
	fmt.Printf("\u2502 %-*s \u2502 %-*s \u2502 %s%-*s%s \u2502 %*s \u2502\n",
		pkgW, pkg, testW, test, statusColour, statusW, r.Status, colourReset, durW, duration)
}
