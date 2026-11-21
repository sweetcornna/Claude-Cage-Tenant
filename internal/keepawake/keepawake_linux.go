package keepawake

import "context"

func hold(ctx context.Context, ready func()) error {
	return holdProcess(ctx, ready, "/usr/bin/systemd-inhibit", "--what=sleep", "--mode=block", "--who=Super Cage", "--why=Super Cage remote access", "--no-ask-password", "/bin/sh", "-c", waitForHost)
}
