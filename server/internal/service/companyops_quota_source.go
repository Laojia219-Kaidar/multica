package service

import (
	"context"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// companyOpsQuotaObservationSource adapts the read-only HiveCosm Authority
// observation client to the continuous-dispatch shadow's quota seam. It does
// not cache, infer, or write quota facts; a client error remains quota_unknown
// in the shadow and the Authority client itself reports source_gap.
type companyOpsQuotaObservationSource struct {
	client *companyopsapi.HiveCosmQuotaObservationClient
}

// NewCompanyOpsQuotaObservationSource returns nil for an unconfigured
// Authority client. Keeping the nil result explicit preserves the existing
// fail-closed shadow behavior and avoids creating a second quota authority.
func NewCompanyOpsQuotaObservationSource(client *companyopsapi.HiveCosmQuotaObservationClient) ContinuousDispatchQuotaSource {
	if client == nil {
		return nil
	}
	return companyOpsQuotaObservationSource{client: client}
}

func (s companyOpsQuotaObservationSource) Lookup(
	ctx context.Context,
	agent db.Agent,
	runtime db.AgentRuntime,
) (ShadowQuotaSnapshot, error) {
	observation, err := s.client.Lookup(ctx, agent, runtime)
	if err != nil {
		return ShadowQuotaSnapshot{}, err
	}
	return ShadowQuotaSnapshot{
		State:      observation.State,
		CheckedAt:  observation.CheckedAt,
		AccountRef: observation.AccountRef,
	}, nil
}

var _ ContinuousDispatchQuotaSource = companyOpsQuotaObservationSource{}
