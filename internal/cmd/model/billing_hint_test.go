package model

// White-box tests for the billing refusal hints: the code -> hint mapping,
// plus the two wired paths (upload completion and import). Download
// authorization deliberately has no billing hint.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func TestBillingHintMapsEveryCode(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"credit_balance_exhausted", "No credits available — the conversion has not started and nothing was charged. " +
			"Check `melange usage quotas` and top up on the dashboard, then re-run."},
		{"credit_debt_outstanding", "Outstanding credit debt blocks new conversions — settle it on the dashboard."},
		{"subscription_past_due", "Billing is past due — fix the payment method on the dashboard."},
		{"custom_model_too_large", "The model exceeds your plan's model-size entitlement — see `melange plan`."},
		{"credit_model_too_large", "The model exceeds the self-service size limit — contact support."},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			err := &api.Error{StatusCode: 402, Type: "billing_error", Code: tt.code, Message: tt.code}
			assert.Equal(t, tt.want, billingHint("melange", err))
		})
	}
}

func TestBillingHintInspectsWrappedErrors(t *testing.T) {
	apiErr := &api.Error{StatusCode: 402, Type: "billing_error", Code: "credit_balance_exhausted"}
	wrapped := fmt.Errorf("completing upload: %w", apiErr)
	assert.Contains(t, billingHint("melange-qcom", wrapped), "`melange-qcom usage quotas`",
		"the hint embeds the edition's program name")
}

func TestBillingHintIgnoresUnrecognizedErrors(t *testing.T) {
	assert.Empty(t, billingHint("melange", &api.Error{StatusCode: 402, Type: "billing_error", Code: "novel_code"}))
	assert.Empty(t, billingHint("melange", &api.Error{StatusCode: 403, Type: "permission_error"}))
	assert.Empty(t, billingHint("melange", errors.New("plain")))
}

const billing402Body = `{"type":"error","error":{"type":"billing_error","code":"credit_balance_exhausted",` +
	`"message":"credit_balance_exhausted"},"request_id":"req_402"}`

func TestUploadComplete402AppendsHintAfterResumeGuidance(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(402, billing402Body))

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input)

	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	msg := err.Error()
	assert.Contains(t, msg, "billing_error/credit_balance_exhausted")
	resume := "The session is preserved; resume with: melange model upload --resume up_1 -R zetic/whisper"
	hint := "No credits available — the conversion has not started and nothing was charged."
	assert.Contains(t, msg, resume, "the existing resume guidance must survive")
	assert.Contains(t, msg, hint)
	assert.Less(t, strings.Index(msg, resume), strings.Index(msg, hint),
		"the hint APPENDS to the resume guidance, never replaces it")
	_, loadErr := upload.LoadState("up_1")
	require.NoError(t, loadErr, "a 402-parked session must stay resumable (replay complete after topping up)")
}

func TestImport402AppendsBillingHint(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/import"),
		jsonStub(402, billing402Body))

	err := run(t, e, "import", "meta-llama/Llama-3.2-1B", "-R", repoArg)

	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "billing_error/credit_balance_exhausted")
	assert.Contains(t, err.Error(),
		"No credits available — the conversion has not started and nothing was charged. "+
			"Check `melange usage quotas` and top up on the dashboard, then re-run.")
}
