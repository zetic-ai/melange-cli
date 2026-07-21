package cmdutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// Exporter renders a command's documented result in one of the structured
// output modes selected by --json, --jq, or --template. A nil *Exporter means
// the command should use its human/tab output instead.
type Exporter struct {
	jq   *gojq.Query
	tmpl *template.Template
}

// AddJSONFlags registers the shared structured-output flags on cmd and, after
// flag parsing, points *exporter at a configured Exporter when any of them
// was used:
//
//	--json               emit the complete documented result as JSON
//	--jq <expr>          filter the JSON through a jq expression (implies --json)
//	--template <tpl>     format the JSON with a Go template (implies --json)
//
// --jq and --template are mutually exclusive; expression syntax errors are
// usage errors (FlagError, exit 2).
func AddJSONFlags(cmd *cobra.Command, exporter **Exporter) {
	fl := cmd.Flags()
	fl.Bool("json", false, "Output the full result as JSON")
	fl.String("jq", "", "Filter JSON output using a jq `expression` (implies --json)")
	fl.String("template", "", "Format JSON output using a Go template (implies --json)")

	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(c, args); err != nil {
				return err
			}
		}
		jsonOn, _ := c.Flags().GetBool("json")
		jqExpr, _ := c.Flags().GetString("jq")
		tmplStr, _ := c.Flags().GetString("template")

		if jqExpr != "" && tmplStr != "" {
			return FlagError{Err: errors.New("cannot use --jq and --template together")}
		}
		if !jsonOn && jqExpr == "" && tmplStr == "" {
			return nil
		}

		e := &Exporter{}
		if jqExpr != "" {
			q, err := gojq.Parse(jqExpr)
			if err != nil {
				return FlagError{Err: fmt.Errorf("invalid --jq expression: %w", err)}
			}
			e.jq = q
		}
		if tmplStr != "" {
			tmpl, err := template.New("template").Funcs(templateFuncs()).Parse(tmplStr)
			if err != nil {
				return FlagError{Err: fmt.Errorf("invalid --template: %w", err)}
			}
			e.tmpl = tmpl
		}
		*exporter = e
		return nil
	}
}

// Write renders data to ios.Out in the selected mode. data is either a
// json.RawMessage carrying the exact bytes the API returned (preferred: field
// names and order survive untouched) or any JSON-marshalable value.
func (e *Exporter) Write(ios *iostreams.IOStreams, data any) error {
	raw, err := rawJSON(data)
	if err != nil {
		return err
	}
	switch {
	case e.tmpl != nil:
		return e.writeTemplate(ios, raw)
	case e.jq != nil:
		return e.writeJQ(ios, raw)
	}
	_, err = fmt.Fprintf(ios.Out, "%s\n", raw)
	return err
}

// writeJQ runs the jq query and prints each result value as JSON per line;
// bare strings print raw without quotes (like gh).
func (e *Exporter) writeJQ(ios *iostreams.IOStreams, raw json.RawMessage) error {
	value, err := decodeAny(raw)
	if err != nil {
		return err
	}
	iter := e.jq.Run(value)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if jqErr, isErr := v.(error); isErr {
			return fmt.Errorf("jq: %w", jqErr)
		}
		if s, isStr := v.(string); isStr {
			if _, err := fmt.Fprintln(ios.Out, s); err != nil {
				return err
			}
			continue
		}
		out, err := gojq.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(ios.Out, "%s\n", out); err != nil {
			return err
		}
	}
}

// writeTemplate executes the template against the decoded value.
func (e *Exporter) writeTemplate(ios *iostreams.IOStreams, raw json.RawMessage) error {
	value, err := decodeAny(raw)
	if err != nil {
		return err
	}
	return e.tmpl.Execute(ios.Out, value)
}

// templateFuncs is the minimal function set available in --template:
// tablerow (tab-joined line), timeago (compact relative time), json (marshal).
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"tablerow": func(fields ...any) string {
			cells := make([]string, len(fields))
			for i, f := range fields {
				cells[i] = fmt.Sprint(f)
			}
			return strings.Join(cells, "\t") + "\n"
		},
		"timeago": func(v any) (string, error) {
			switch t := v.(type) {
			case time.Time:
				return text.RelativeTime(t, time.Now()), nil
			case string:
				parsed, err := time.Parse(time.RFC3339, t)
				if err != nil {
					return "", fmt.Errorf("timeago: %w", err)
				}
				return text.RelativeTime(parsed, time.Now()), nil
			}
			return "", fmt.Errorf("timeago: unsupported type %T", v)
		},
		"json": func(v any) (string, error) {
			out, err := json.Marshal(v)
			return string(out), err
		},
	}
}

// rawJSON returns data's JSON bytes, passing json.RawMessage through untouched.
func rawJSON(data any) (json.RawMessage, error) {
	if raw, ok := data.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(data)
}

// decodeAny decodes JSON into the generic value shapes gojq and text/template
// operate on (map[string]any, []any, float64, ...).
func decodeAny(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decoding response for structured output: %w", err)
	}
	return value, nil
}
