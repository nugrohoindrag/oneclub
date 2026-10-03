package iam

import (
	"net/http"

	"oneclub/internal/kernel/route"
)

// Register adds the IAM routes (PRD §10 Auth + IAM).
func (s *Service) Register(reg *route.Registry) {
	const auth = "Auth"
	const iam = "Identity & Access"
	add := func(rt route.Route) {
		rt.Module = "platform"
		reg.Add(rt)
	}

	// Auth
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/login", Tag: auth, Summary: "Log in with e-mail and password",
		Auth: route.AuthPublic, Request: LoginRequest{}, Response: LoginResponse{}, Status: http.StatusOK, Handler: s.handleLogin})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/device-login", Tag: auth, Summary: "Log in on a registered device with a staff PIN",
		Auth: route.AuthPublic, Request: DeviceLoginRequest{}, Response: LoginResponse{}, Status: http.StatusOK, Handler: s.handleDeviceLogin})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/logout", Tag: auth, Summary: "Log out (revoke the current session)",
		Auth: route.AuthMFAPending, Handler: s.handleLogout})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/auth/me", Tag: auth, Summary: "Current user, properties, permissions and shells",
		Auth: route.AuthMFAPending, Response: MeResponse{}, Handler: s.handleMe})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/auth/me/preferences", Tag: auth, Summary: "Update language and theme",
		Request: PreferencesRequest{}, Handler: s.handlePreferences})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/auth/me/pin", Tag: auth, Summary: "Set my staff PIN",
		Request: SetPINRequest{}, Handler: s.setPIN(true)})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/mfa/setup", Tag: auth, Summary: "Start TOTP enrolment",
		Auth: route.AuthMFAPending, Response: MFASetupResponse{}, Status: http.StatusOK, Handler: s.handleMFASetup})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/mfa/verify", Tag: auth, Summary: "Verify a TOTP code (completes enrolment or login)",
		Auth: route.AuthMFAPending, Request: MFAVerifyRequest{}, Handler: s.handleMFAVerify})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/password/reset", Tag: auth, Summary: "Request a password reset e-mail",
		Auth: route.AuthPublic, Request: PasswordResetRequest{}, Response: map[string]string{}, Status: http.StatusAccepted, Handler: s.handlePasswordReset})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/password/reset/confirm", Tag: auth, Summary: "Set a new password with a reset token",
		Auth: route.AuthPublic, Request: PasswordResetConfirm{}, Handler: s.handlePasswordResetConfirm})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/auth/password/change", Tag: auth, Summary: "Change my password",
		Auth: route.AuthMFAPending, Request: PasswordChangeRequest{}, Handler: s.handlePasswordChange})

	// Users
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/users", Tag: iam, Summary: "List users", Permission: "platform.user.view",
		Response: User{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[roleCode]"}, {Name: "filter[propertyId]"}},
		Handler: s.listUsers})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/users", Tag: iam, Summary: "Add user", Permission: "platform.user.create",
		Request: CreateUserRequest{}, Response: User{}, Handler: s.createUser})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/users/{id}", Tag: iam, Summary: "View user", Permission: "platform.user.view",
		Response: User{}, Handler: s.getUser})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/users/{id}", Tag: iam, Summary: "Edit user", Permission: "platform.user.update",
		Request: UpdateUserRequest{}, Response: User{}, Handler: s.updateUser})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/users/{id}:deactivate", Tag: iam, Summary: "Deactivate user",
		Permission: "platform.user.deactivate", Request: ReasonRequest{}, Response: User{}, Status: http.StatusOK, Handler: s.setUserStatus("inactive")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/users/{id}:activate", Tag: iam, Summary: "Activate user",
		Permission: "platform.user.deactivate", Request: ReasonRequest{}, Response: User{}, Status: http.StatusOK, Handler: s.setUserStatus("active")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/users/{id}:reset-mfa", Tag: iam, Summary: "Reset user MFA",
		Permission: "platform.user.reset_mfa", Request: ReasonRequest{}, Handler: s.resetMFA})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/platform/users/{id}/pin", Tag: iam, Summary: "Set a user's staff PIN",
		Permission: "platform.user.update", Request: SetPINRequest{}, Handler: s.setPIN(false)})

	// Roles & permissions
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/roles", Tag: iam, Summary: "List roles", Permission: "platform.role.view",
		Response: Role{}, List: true, Query: []route.Param{{Name: "filter[category]"}}, Handler: s.listRoles})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/roles", Tag: iam, Summary: "Add custom role", Permission: "platform.role.create",
		Request: RoleRequest{}, Response: Role{}, Handler: s.createRole})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/roles/{id}", Tag: iam, Summary: "View role", Permission: "platform.role.view",
		Response: Role{}, Handler: s.getRole})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/roles/{id}", Tag: iam, Summary: "Edit role", Permission: "platform.role.update",
		Request: RoleUpdateRequest{}, Response: Role{}, Handler: s.updateRole})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/platform/roles/{id}", Tag: iam, Summary: "Delete custom role", Permission: "platform.role.delete",
		Handler: s.deleteRole})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/permissions", Tag: iam, Summary: "Permission catalogue", Permission: "platform.permission.view",
		Response: PermissionInfo{}, List: true, Handler: s.listPermissions})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/role-assignments", Tag: iam, Summary: "List role assignments",
		Permission: "platform.role_assignment.view", Response: Assignment{}, List: true,
		Query: []route.Param{{Name: "filter[userId]"}, {Name: "filter[propertyId]"}, {Name: "filter[roleId]"}}, Handler: s.listAssignments})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/role-assignments", Tag: iam, Summary: "Assign role",
		Permission: "platform.role_assignment.manage", Request: CreateAssignmentRequest{}, Response: Assignment{}, Handler: s.createAssignment})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/platform/role-assignments/{id}", Tag: iam, Summary: "Remove role assignment",
		Permission: "platform.role_assignment.manage", Handler: s.deleteAssignment})

	// Sessions
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/sessions", Tag: iam, Summary: "List active sessions (mine, or a user's with session.view_all)",
		Response: Session{}, List: true, Query: []route.Param{{Name: "userId"}}, Handler: s.listSessions})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/platform/sessions/{id}", Tag: iam, Summary: "Revoke session", Handler: s.revokeSession})

	// Devices
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/devices", Tag: iam, Summary: "List devices", Permission: "platform.device.view",
		Scope: route.ScopeProperty, Response: Device{}, List: true, Handler: s.listDevices})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/devices", Tag: iam, Summary: "Register device", Permission: "platform.device.manage",
		Scope: route.ScopeProperty, Request: DeviceRequest{}, Response: Device{}, Handler: s.createDevice})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/devices/{id}", Tag: iam, Summary: "Edit device", Permission: "platform.device.manage",
		Scope: route.ScopeProperty, Request: DeviceUpdateRequest{}, Response: Device{}, Handler: s.updateDevice})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/devices/{id}:rotate-token", Tag: iam, Summary: "Rotate device token",
		Permission: "platform.device.manage", Scope: route.ScopeProperty, Response: Device{}, Status: http.StatusOK, Handler: s.rotateDeviceToken})

	// API keys
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/api-keys", Tag: iam, Summary: "List API keys", Permission: "platform.api_key.view",
		Response: APIKey{}, List: true, Handler: s.listAPIKeys})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/api-keys", Tag: iam, Summary: "Create API key", Permission: "platform.api_key.manage",
		Request: APIKeyRequest{}, Response: APIKey{}, Handler: s.createAPIKey})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/api-keys/{id}:rotate", Tag: iam, Summary: "Rotate API key",
		Permission: "platform.api_key.manage", Response: APIKey{}, Status: http.StatusOK, Handler: s.apiKeyAction("rotate")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/api-keys/{id}:revoke", Tag: iam, Summary: "Revoke API key",
		Permission: "platform.api_key.manage", Response: APIKey{}, Status: http.StatusOK, Handler: s.apiKeyAction("revoke")})
}
