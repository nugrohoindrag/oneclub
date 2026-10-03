package app

import (
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/httpapi"
)

// SpecInfo describes the published OpenAPI document.
func SpecInfo() route.Info {
	return route.Info{
		Title:   "OneClub API",
		Version: Version,
		Description: "OneClub P0 Platform Foundation API. Conventions: /api/v1/<module>/<resource>, camelCase JSON, " +
			"cursor pagination (?cursor=&limit=), filters (?filter[field]=), RFC 9457 Problem Details, Idempotency-Key, " +
			"X-Property-Id for property-scoped resources.",
	}
}

func httpapiSpec(reg *route.Registry) ([]byte, error) {
	return httpapi.MarshalSpec(reg, SpecInfo())
}
