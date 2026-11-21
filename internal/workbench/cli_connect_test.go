package workbench

import "testing"

func TestCLIStartupDialogs(t *testing.T) {
	const own = "46157ee6-e255-40a0-b51a-0b6d652443b7"
	for _, tc := range []struct{ text, want string }{
		{"\x1b[31mWARNING: Loading development channels\x1b[0m\r\nChannels: server:cage\r\nI am using this for local development", "channel"},
		{"Quick safety check: is this a project you created or one you trust?\r\n/work/project\r\nYes, I trust this folder", "project"},
		{"Quick safety check: is this a project you created or one you trust? /other/project Yes, I trust this folder", ""},
		{"WARNING: Loading development channels Channels: server:other I am using this for local development", ""},
		{"WARNING: Loading development channels Channels: server:cage-extra 1. I am using this for local development", ""},
		{"WARNING: Loading development channels Channels: server:cage,server:other 1. I am using this for local development", ""},
		{"WARNING: Loading development channels Channels: server:cage ❯ 1. I am using this for local development", "channel"},
		// Phone sessions name the channel after their own connection.
		{"WARNING: Loading development channels\r\n  Channels: server:" + own + "\r\n  ❯ 1. I am using this for local development\r\n    2. Exit", "channel"},
		{"WARNING: Loading development channels Channels: server:46157ee6-e255-40a0-\r\n  b51a-0b6d652443b7 ❯ 1. I am using this for local development", "channel"},
		{"WARNING: Loading development channels Channels: server:0a2f8c1e-9b7d-4e4a-8f00-3c5d1e2b7a90 ❯ 1. I am using this for local development", ""},
		{"WARNING: Loading development channels Channels: server:" + own + ",server:other ❯ 1. I am using this for local development", ""},
		{"WARNING: Loading development channels Channels: server:" + own + "-extra ❯ 1. I am using this for local development", ""},
		{"Do you want to proceed? 1. Yes 2. No", ""},
		{"New MCP server found in this project: other Use this MCP server", ""},
		{"Do you want to use this API key?", ""},
	} {
		if got := cliStartupDialog(cliStartupText([]byte(tc.text)), "/work/project", own); got != tc.want {
			t.Errorf("dialog %q: %q, want %q", tc.text, got, tc.want)
		}
	}
	if got := cliStartupDialog(cliStartupText([]byte("WARNING: Loading development channels Channels: server:"+own+" ❯ 1. I am using this for local development")), "/work/project", ""); got != "" {
		t.Errorf("a connection channel without a known connection was accepted: %q", got)
	}
}
