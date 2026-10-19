package adminapi

import (
	"net/http"
	"strings"

	"github.com/bropines/rdpanel/internal/aclpolicy"
	"github.com/bropines/rdpanel/internal/httpx"
)

type aclPolicyExportDTO struct {
	Policy string `json:"policy"`
}

type aclPolicyInputDTO struct {
	Policy string `json:"policy"`
}

func (a *resources) handleGetACLPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc := aclpolicy.NewService(a.d.Store)
	policyStr, err := svc.Export(ctx)
	if err != nil {
		a.fail500(w, r, "export acl policy", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, aclPolicyExportDTO{Policy: policyStr})
}

func (a *resources) handleTestACLPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in aclPolicyInputDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	svc := aclpolicy.NewService(a.d.Store)
	_, valResult, err := svc.ValidateAndTest(ctx, []byte(in.Policy))
	if err != nil {
		validationFailed(w, "invalid acl policy format", map[string]string{"policy": err.Error()})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, valResult)
}

func (a *resources) handleUpdateACLPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in aclPolicyInputDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	svc := aclpolicy.NewService(a.d.Store)
	valResult, err := svc.Apply(ctx, []byte(in.Policy))
	if err != nil {
		if valResult != nil && len(valResult.Errors) > 0 {
			validationFailed(w, "acl policy validation failed", map[string]string{"policy": strings.Join(valResult.Errors, "; ")})
			return
		}
		validationFailed(w, "acl policy error", map[string]string{"policy": err.Error()})
		return
	}
	a.audit(ctx, r, "acl.policy_apply", "acl_policy", "current", map[string]any{
		"roles_count": valResult.RolesCount,
		"rules_count": valResult.RulesCount,
		"tests_count": valResult.TestsCount,
	})
	httpx.WriteJSON(w, http.StatusOK, valResult)
}
