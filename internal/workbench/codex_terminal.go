package workbench

import (
	"context"
	"errors"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// Caller holds the environment operation lock through PTY publication.
func (s *Server) createCodexTerminalLocked(input terminalRequest) (*terminalSession, error) {
	if !s.codexGuestSupported() {
		return nil, errors.New("Codex 终端目前只支持 Linux 环境")
	}
	if input.ClaudeAccountID != "" || input.sharedID != "" {
		return nil, errors.New("Codex 终端不能指定 Claude 账号或会话")
	}
	s.mu.Lock()
	closing, componentUpdate := s.closing, s.guestUpdateRequired
	command := codexTerminalCommand(s.preferences)
	s.mu.Unlock()
	if closing {
		return nil, errTerminalsClosed
	}
	if !s.vmRunning() || s.busy() {
		return nil, errTerminalNotReady
	}
	login := input.Kind == "codex-login"
	// Login runs from the guest home and needs no project share.
	workspace := s.workspace()
	if !login {
		if err := s.terminals.canStart(); err != nil {
			return nil, err
		}
		var err error
		if workspace, err = s.connectProject(input.Workspace, false); err != nil {
			return nil, err
		}
		directories, err := s.projectAdditionalDirectories(workspace)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			command += " --add-dir " + shellescape.Quote(directory)
		}
	}
	selection, err := s.selectCodexAccount(input.CodexAccountID, login, workspace)
	if err != nil {
		return nil, err
	}
	var finished func(*terminalSession)
	if selection.ID != "" {
		if componentUpdate {
			return nil, errGuestUpdateRequired
		}
		if !validClaudeID(selection.ID) || selection.GuestID != selection.ID {
			return nil, errors.New("Codex 账号 ID 无效")
		}
		if existing, conflict := s.terminals.existingCodexLogin(selection.ID, login); existing != nil || conflict != nil {
			return existing, conflict
		}
		if !login && selection.Subject == "" {
			return nil, errors.New("Codex 账号状态已变化，请重试")
		}
		finished = s.codexTerminalFinished(selection.ID, login)
	}
	script := codexTerminalScript(s.cliWorkspace(workspace), command, selection.GuestID, selection.Subject, login)
	cmd := s.sshCommand(s.lifetimeContext(), "-tt", s.sshTarget(), "--", script)
	if input.requestContext != nil {
		s.account.mu.Lock()
		if !s.mobileAuthorized(input.requestContext) {
			s.account.mu.Unlock()
			return nil, errMobileLocked
		}
	}
	session, err := s.terminals.startCodexSession(cmd, workspace, input.Kind, selection, finished)
	if input.requestContext != nil {
		s.account.mu.Unlock()
	}
	if err == nil && !login && selection.ID != "" {
		s.noteCodexSelection(selection.ID, workspace)
	}
	return session, err
}

// codexQuickExit bounds how soon after start a failed exit is taken as the
// guest helper refusing the account rather than Codex ending normally.
const codexQuickExit = 20 * time.Second

func (s *Server) codexTerminalFinished(id string, login bool) func(*terminalSession) {
	if login {
		return func(*terminalSession) {
			ctx, cancel := context.WithTimeout(s.lifetimeContext(), 25*time.Second)
			defer cancel()
			_ = s.confirmCodexLogin(ctx, id)
		}
	}
	started := time.Now()
	return func(session *terminalSession) {
		// The helper ends the terminal when the account is no longer signed in
		// as selected. Re-check it in the background so allocation stops
		// choosing it; the terminal's teardown never waits for the probe.
		if session.exitStatus() > 0 && time.Since(started) < codexQuickExit {
			s.recheckCodexAccount(id)
		}
	}
}

// The saved command is interpreted only by the guest's interactive shell and
// leaves a shell when Codex exits. With a managed account, the shell keeps
// CODEX_HOME and the shim, so later codex invocations stay on that account.
// When the helper refuses the account (status 78), the terminal ends instead,
// so the host learns that the account needs to be checked.
func codexTerminalScript(workspace, command, accountID, subject string, login bool) string {
	if login {
		return "cd \"$HOME\" && exec /usr/local/bin/cage-codex-account login " + shellescape.Quote(accountID)
	}
	body := `eval "$CAGE_TERMINAL_COMMAND"; unset CAGE_TERMINAL_COMMAND; exec bash -il`
	if accountID != "" {
		body = `export CAGE_CODEX_ACCOUNT_ID=` + shellescape.Quote(accountID) + ` CAGE_CODEX_SUBJECT=` + shellescape.Quote(subject) + "; " +
			`export CODEX_HOME="$HOME/.local/share/cage/codex-accounts/$CAGE_CODEX_ACCOUNT_ID/home"; (umask 077; mkdir -p "$CODEX_HOME"); ` +
			`export PATH="/usr/local/lib/cage-codex-bin:$PATH"; eval "$CAGE_TERMINAL_COMMAND"; cage_status=$?; unset CAGE_TERMINAL_COMMAND; ` +
			`if [ "$cage_status" = 78 ]; then exit 78; fi; unset cage_status; exec bash -i`
	}
	return "cd " + shellescape.Quote(workspace) +
		" && exec env TERM=xterm-256color COLORTERM=truecolor CAGE_WORKBENCH=1 CAGE_TERMINAL_COMMAND=" +
		shellescape.Quote(command) + " bash -lic " + shellescape.Quote(body)
}
