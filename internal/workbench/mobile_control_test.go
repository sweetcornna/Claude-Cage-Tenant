package workbench

import "testing"

func TestPhoneControlRoutesReachEveryGuest(t *testing.T) {
	for _, path := range []string{"/api/mobile/environment", "/api/mobile/environment/restart", "/api/mobile/environment/proxy/select",
		"/api/mobile/terminals", "/api/mobile/terminals/0123456789abcdef/output", "/api/mobile/files/list", "/api/mobile/files/upload/chunk"} {
		if !phoneRoomRoute(path) || !macOSWorkbenchRoute(path) || !windowsWorkbenchRoute(path) {
			t.Fatal("phone route not reachable", path)
		}
	}
	for _, path := range []string{"/api/mobile/environmentx", "/api/mobile/terminalsx", "/api/mobile/files", "/api/mobile-pair", "/api/terminals"} {
		if phoneRoomRoute(path) {
			t.Fatal("route granted to the phone room", path)
		}
	}
}
