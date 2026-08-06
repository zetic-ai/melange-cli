package mcp

// FixtureTool maps each shared contract fixture — openapi/fixtures/<stem>.json,
// stems are the spec's operationIds — to the registered MCP tool whose handler
// consumes that operation's response. It is the single source of the
// fixture→tool association: the schema-conformance table and the
// fixture→tool round-trip table in this package's tests both key on it, and
// the schema generator (tools/mcpschemas) cross-checks its catalog against it
// so a renamed tool cannot leave the mapping pointing at nothing.
//
// Every fixture must appear in exactly one of FixtureTool and FixtureSkipped,
// so a new backend fixture forces a deliberate decision (enforced in
// schemas_test.go).
var FixtureTool = map[string]string{
	"get_me":                        "whoami",
	"get_usage":                     "get_account_info",
	"get_usage_quotas":              "get_account_info",
	"get_billing_plan":              "get_account_info",
	"list_repos":                    "list_repos",
	"create_repo":                   "create_repo",
	"get_repo":                      "get_repo",
	"get_model":                     "get_model",
	"list_model_targets":            "get_model",
	"get_model_status":              "get_conversion_status",
	"set_default_model":             "set_default_model",
	"import_model":                  "import_model",
	"get_deployment_options":        "get_deployment_info",
	"get_deployment_guide":          "get_deployment_info",
	"get_general_report":            "get_model_report",
	"get_llm_report":                "get_model_report",
	"get_package_report":            "get_model_report",
	"list_library_models":           "search_library",
	"list_library_providers":        "search_library",
	"get_library_model":             "get_library_model",
	"create_download_authorization": "request_model_download",
	"create_model_upload":           "upload_model",
	"get_model_upload":              "upload_model",
	"complete_model_upload":         "upload_model",
}

// FixtureSkipped documents each fixture that flows through no MCP tool, with
// the reason it is excluded.
var FixtureSkipped = map[string]string{
	"create_model_upload_conflict": "409 error exchange: upload_model surfaces the conflict as IsError resume guidance, not structuredContent",
	"cancel_model_upload":          "upload_model never cancels a session; cancellation stays CLI-only (melange model upload --cancel)",
	"error_401":                    "error envelope: failures surface as IsError text, not structuredContent",
	"error_422":                    "error envelope: failures surface as IsError text, not structuredContent",
	"error_422_enum":               "error envelope: failures surface as IsError text, not structuredContent",
}
