package middleware

import (
	"github.com/atharva-ng/crunch/internal/authz"
)

// rolesCache is the boot-injected role catalog the admin gate resolves
// permissions against. Configured once at startup via SetRolesCache — mirrors
// SetPlansCache. Consumed by WithAdminAuthorization. A nil cache (or an
// unseeded catalog) resolves no permissions, so every admin route answers 403
// until rolesmigrate -seed-roles has run — fail closed.
var rolesCache *authz.RolesCache

// SetRolesCache injects the roles cache. Call once at startup, before serving.
func SetRolesCache(c *authz.RolesCache) {
	rolesCache = c
}
