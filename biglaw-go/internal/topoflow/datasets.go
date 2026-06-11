// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"bufio"
	"encoding/json"
	"os"
)

// RealHumanEvalSample returns genuine HumanEval problems (stub + canonical test
// harness) so the verifiable code pipeline can run real execution without a
// download. GroundTruth carries {entry_point, test} for the SubprocessCodeRunner.
func RealHumanEvalSample() []TaskContext {
	return []TaskContext{
		{
			TaskID:        "HumanEval/0",
			ScenarioClass: "C1",
			Domain:        "code",
			Prompt: "from typing import List\n\n\n" +
				"def has_close_elements(numbers: List[float], threshold: float) -> bool:\n" +
				"    \"\"\" Check if in given list of numbers, are any two numbers closer to each other than\n" +
				"    given threshold.\n" +
				"    >>> has_close_elements([1.0, 2.0, 3.0], 0.5)\n    False\n" +
				"    >>> has_close_elements([1.0, 2.8, 3.0, 4.0, 5.0, 2.0], 0.3)\n    True\n    \"\"\"\n",
			GroundTruth: map[string]any{
				"entry_point": "has_close_elements",
				"test": "def check(candidate):\n" +
					"    assert candidate([1.0, 2.0, 3.9, 4.0, 5.0, 2.2], 0.3) == True\n" +
					"    assert candidate([1.0, 2.0, 3.9, 4.0, 5.0, 2.2], 0.05) == False\n" +
					"    assert candidate([1.0, 2.0, 5.9, 4.0, 5.0], 0.95) == True\n" +
					"    assert candidate([1.0, 2.0, 5.9, 4.0, 5.0], 0.8) == False\n" +
					"    assert candidate([1.0, 2.0, 3.0, 4.0, 5.0, 2.0], 0.1) == True\n" +
					"    assert candidate([1.1, 2.2, 3.1, 4.1, 5.1], 1.0) == True\n" +
					"    assert candidate([1.1, 2.2, 3.1, 4.1, 5.1], 0.5) == False\n",
			},
		},
		{
			TaskID:        "HumanEval/53",
			ScenarioClass: "C1",
			Domain:        "code",
			Prompt: "def add(x: int, y: int):\n" +
				"    \"\"\"Add two numbers x and y\n" +
				"    >>> add(2, 3)\n    5\n    >>> add(5, 7)\n    12\n    \"\"\"\n",
			GroundTruth: map[string]any{
				"entry_point": "add",
				"test": "def check(candidate):\n" +
					"    import random\n" +
					"    assert candidate(0, 1) == 1\n" +
					"    assert candidate(2, 3) == 5\n" +
					"    assert candidate(5, 7) == 12\n" +
					"    for _ in range(100):\n" +
					"        x, y = random.randint(0, 1000), random.randint(0, 1000)\n" +
					"        assert candidate(x, y) == x + y\n",
			},
		},
	}
}

// RealMathSample returns a few exact-answer math tasks.
func RealMathSample() []TaskContext {
	return []TaskContext{
		{TaskID: "math/1", ScenarioClass: "C1", Domain: "math", Prompt: "What is 2+2?", GroundTruth: "4"},
		{TaskID: "math/2", ScenarioClass: "C6", Domain: "math", Prompt: "Compute 12 / 3 + 1.", GroundTruth: "5"},
	}
}

type rawTask struct {
	TaskID        string `json:"task_id"`
	Prompt        string `json:"prompt"`
	ScenarioClass string `json:"scenario_class"`
	Domain        string `json:"domain"`
	EntryPoint    string `json:"entry_point"`
	Test          string `json:"test"`
	Answer        any    `json:"answer"`
}

// LoadJSONL loads a real dataset from a JSONL file. Per-line schema:
//
//	code: {task_id, prompt, entry_point, test}      -> domain "code"
//	math: {task_id, prompt, answer}                 -> domain "math"
//	af:   {task_id, prompt, scenario_class, domain} -> judged
func LoadJSONL(path string) ([]TaskContext, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []TaskContext
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r rawTask
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		ctx := TaskContext{TaskID: r.TaskID, Prompt: r.Prompt, ScenarioClass: r.ScenarioClass, Domain: r.Domain}
		switch {
		case r.EntryPoint != "" && r.Test != "":
			ctx.Domain = "code"
			ctx.GroundTruth = map[string]any{"entry_point": r.EntryPoint, "test": r.Test}
		case r.Answer != nil:
			ctx.Domain = "math"
			ctx.GroundTruth = r.Answer
		}
		out = append(out, ctx)
	}
	return out, sc.Err()
}
