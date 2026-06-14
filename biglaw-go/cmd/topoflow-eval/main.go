// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// topoflow-eval runs the TopoFlow evaluation harness with REAL components:
// the Anthropic provider transport, the project embeddings client, a real
// subprocess code runner, and real datasets. Requires ANTHROPIC_API_KEY (and an
// embeddings key/Ollama for dytopo arms) for a live run.
//
//	go run ./cmd/topoflow-eval -dataset humaneval -epochs 1 -out report.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/topoflow"
)

func main() {
	_ = godotenv.Load()

	dataset := flag.String("dataset", "humaneval", "humaneval | math | mixed | <path-to.jsonl>")
	epochs := flag.Int("epochs", 1, "epochs for learning arms")
	out := flag.String("out", "", "write the JSON report to this path")
	offline := flag.Bool("offline", false, "use the offline mock transport/embedder (no network)")
	noSkip := flag.Bool("no-skip", false, "AgensFlow no-skip ablation: force skip:X off across all arms (§6.2)")
	flag.Parse()

	cfg := config.Load()
	tfcfg := topoflow.DefaultConfig()
	tfcfg.SkipEnabled = !*noSkip

	tasks, err := loadTasks(*dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dataset: %v\n", err)
		os.Exit(1)
	}

	opts := topoflow.SuiteOptions{
		Tasks:      tasks,
		Epochs:     *epochs,
		OutPath:    *out,
		CodeRunner: topoflow.NewSubprocessCodeRunner(),
	}
	if !*offline {
		provReg := providers.NewRegistry(cfg)
		lightID := routing.Light(cfg)
		opts.Transport = topoflow.NewAnthropicTransport(provReg.MustGet(lightID), tfcfg.DytopoMaxTok)
		opts.Embedder = topoflow.NewEmbeddingsAdapter(embeddings.NewClient(cfg))
	}

	report, err := topoflow.RunSuite(tfcfg, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run suite: %v\n", err)
		os.Exit(1)
	}

	// Headline: H1 selection table + per-arm means.
	fmt.Printf("TopoFlow eval — %d tasks, %d epochs\n\n", report.NTasks, report.Epochs)
	fmt.Println("Per-arm (meanQuality / auditQuality / meanTokens):")
	for _, name := range []string{
		"1_fixed_linear", "2_pure_dytopo", "3_pure_agensflow", "4_topoflow_linear",
		"5_topoflow_dytopo", "6_topoflow_free_cold", "7_topoflow_free_warm", "8_no_skip_ablation",
	} {
		a := report.Arms[name]
		fmt.Printf("  %-24s  Q=%.3f  audit=%.3f  tok=%.0f\n", name, a.MeanQuality, a.AuditMeanQuality, a.MeanTokens)
	}
	fmt.Println("\nH1 selection (generator distribution by scenario class):")
	if h1, err := json.MarshalIndent(report.Metrics["H1_selection"], "  ", "  "); err == nil {
		fmt.Printf("  %s\n", h1)
	}
	fmt.Printf("\nH1 separates by regime: %v\n", report.Metrics["H1_separates"])
	fmt.Println("\nH6 no-skip ablation (arm 3 skip-on vs arm 8 skip-off):")
	if h6, err := json.MarshalIndent(report.Metrics["H6_skip_ablation"], "  ", "  "); err == nil {
		fmt.Printf("  %s\n", h6)
	}
	if *out != "" {
		fmt.Printf("Full report written to %s\n", *out)
	}
}

func loadTasks(dataset string) ([]topoflow.TaskContext, error) {
	switch dataset {
	case "humaneval":
		return topoflow.RealHumanEvalSample(), nil
	case "math":
		return topoflow.RealMathSample(), nil
	case "mixed":
		return append(topoflow.RealHumanEvalSample(), topoflow.RealMathSample()...), nil
	default:
		return topoflow.LoadJSONL(dataset)
	}
}
