package main

import (
	"context"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const localOperatorSessionDefaultJWTSecret = "multica-dev-secret-change-in-production"

// localOperatorSessionConfigFromEnv validates every boot-time condition before
// the route is registered. A missing, malformed, or non-existent user keeps
// the route absent instead of creating an account or accepting an email.
func localOperatorSessionConfigFromEnv(ctx context.Context, queries *db.Queries) (bool, pgtype.UUID) {
	if os.Getenv("HIVECREW_LOCAL_OPERATOR_SESSION_ENABLED") != "true" ||
		!localOperatorSessionEnvironmentFromEnv() {
		return false, pgtype.UUID{}
	}
	if secret := strings.TrimSpace(os.Getenv("JWT_SECRET")); secret == "" || secret == localOperatorSessionDefaultJWTSecret {
		return false, pgtype.UUID{}
	}
	parsed, err := uuid.Parse(strings.TrimSpace(os.Getenv("HIVECREW_LOCAL_OPERATOR_USER_ID")))
	if err != nil {
		return false, pgtype.UUID{}
	}
	userID := pgtype.UUID{Bytes: parsed, Valid: true}
	if _, err := queries.GetUser(ctx, userID); err != nil {
		return false, pgtype.UUID{}
	}
	return true, userID
}

func localOperatorSessionEnvironmentFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))) {
	case "local", "development", "test":
		return true
	default:
		return false
	}
}
