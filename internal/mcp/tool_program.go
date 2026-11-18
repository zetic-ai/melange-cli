package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/programsheet"
)

// registerProgram registers the partner program read tools.
func registerProgram(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_program_benchmarks",
		Title: "Get program benchmarks",
		Description: "Read a partner program's benchmark sheets: results per SoC for each " +
			"of the program's models, measured and published by Zetic, with Zetic's builds " +
			"by inference mode like a repository model page. A build's 'modes' lists the " +
			"modes it serves (speed, accuracy; at most one build per mode, per variant); " +
			"Auto follows Speed, and a build with no modes (such as a float reference) is " +
			"context, not a mode. Builds carry 'speed' and 'accuracy' metrics keyed by " +
			"name; 'metrics' gives, per task (llm, asr, tts, image_classification), each " +
			"metric's label, unit, which direction is better, its category (speed, " +
			"memory, energy, size, accuracy) for grouping, and keyed_by when a value is " +
			"an object keyed by prompt length or test set. Targets with has_results " +
			"false are SoCs still to be measured. " +
			"'model' (a slug) and 'soc' narrow the answer. Only program members and Zetic " +
			"staff can read a program; anyone else gets not found, the same as a program " +
			"that does not exist. whoami lists the caller's programs in access_programs.",
		OutputSchema: outputSchema("get_program_benchmarks"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, getProgramBenchmarksHandler(d))
}

// getProgramBenchmarksArgs are the arguments of get_program_benchmarks.
type getProgramBenchmarksArgs struct {
	Program string `json:"program" jsonschema:"Program name, as listed in whoami's access_programs."`
	Model   string `json:"model,omitempty" jsonschema:"Keep one model, by its slug."`
	SoC     string `json:"soc,omitempty" jsonschema:"Keep one SoC of the program's catalog (a SoC code such as SM8750); models without a result on it are left out."`
}

// getProgramBenchmarksHandler wraps GET /v1/programs/{program}/benchmarks.
// Unfiltered answers pass through byte-exact; filters narrow the raw body
// without re-encoding any value.
func getProgramBenchmarksHandler(d Deps) mcp.ToolHandlerFor[getProgramBenchmarksArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getProgramBenchmarksArgs) (*mcp.CallToolResult, any, error) {
		program := strings.TrimSpace(in.Program)
		if program == "" {
			return d.toolError(errors.New(
				"program is required: pass a program name from whoami's access_programs")), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		resp, err := g.GetProgramBenchmarksWithResponse(ctx, program)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		body, err := programsheet.Apply(resp.Body, programsheet.Filter{Model: in.Model, SoC: in.SoC})
		if err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(body)
	}
}
