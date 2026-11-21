package tenant

import "testing"

// The tenant gateway forwards phone control to the workspace's workbench,
// which authenticates it with the workspace session like /api/mobile/cli.
func TestPhoneControlRoutesReachWorkspace(t *testing.T) {
	for _, path := range []string{"/api/mobile/environment", "/api/mobile/environment/restart", "/api/mobile/environment/proxy/select",
		"/api/mobile/terminals", "/api/mobile/terminals/0123456789abcdef/output", "/api/mobile/files/list", "/api/mobile/files/upload/chunk"} {
		if internalPath(path) {
			t.Fatal("phone route withheld from the workspace", path)
		}
	}
}
