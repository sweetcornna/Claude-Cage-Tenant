package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/google/uuid"
	"github.com/klauspost/compress/gzip"
)

// Codex CLI sessions reuse the phone's shared-session records. The guest
// helper reads Codex's own rollout files and runs each phone message as a
// detached "codex exec" turn; the host keeps only public session state.
//
//go:embed codex_sessions.py
var codexSessionsHelper string

const codexProvider = "codex"

var codexSessionsEncoded = func() string {
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	_, _ = writer.Write([]byte(codexSessionsHelper))
	_ = writer.Close()
	return cliTransport85(compressed.Bytes())
}()

// Codex runs only in Linux guests, so this command is bounded by Linux's
// 128 KiB argument limit rather than the Claude channel's Windows limit. The
// helper file is versioned, so a running exec turn keeps its own copy.
func codexSessionsCommand(mode string) string {
	bootstrap := fmt.Sprintf(`import base64,gzip,os,pathlib,sys,tempfile
d=pathlib.Path.home()/".local/share/cage/codex-session-code"
d.mkdir(parents=True,exist_ok=True,mode=0o700)
p=d/"%x.py"
if not p.exists():
 with tempfile.NamedTemporaryFile(dir=d,delete=False) as f:
  f.write(gzip.decompress(base64.b85decode("%s")))
  t=f.name
 os.replace(t,p)
os.execv(sys.executable,[sys.executable,"-I",str(p),*sys.argv[1:]])
`, sha256.Sum256([]byte(codexSessionsHelper)), codexSessionsEncoded)
	return shellescape.QuoteCommand([]string{"python3", "-I", "-c", bootstrap, mode, "-"})
}

type codexConversation struct {
	Error            string       `json:"error"`
	ThreadID         string       `json:"thread_id"`
	Account          string       `json:"account"`
	Connection       string       `json:"connection"`
	Workspace        string       `json:"workspace"`
	Title            string       `json:"title"`
	UpdatedAt        float64      `json:"updated_at"`
	Turns            []mobileTurn `json:"turns"`
	Running          bool         `json:"running"`
	Active           bool         `json:"active"`
	CanSend          bool         `json:"can_send"`
	HistoryTruncated bool         `json:"history_truncated"`
	ConnectionNotice string       `json:"connection_notice"`
	AccountRefused   bool         `json:"account_refused"`
}

func (record *mobileRecord) codex() bool { return record.Provider == codexProvider }

func (record *mobileRecord) codexGuestAccount() string {
	if record.CodexAccount == "" {
		return "default"
	}
	return record.CodexAccount
}

// A new session learns its Codex thread ID from its first turn. Until then
// CLIID is only a host placeholder and Resume is false.
func (record *mobileRecord) codexThread() string {
	if record.Resume {
		return record.CLIID
	}
	return ""
}

func (s *Server) runCodexSessions(ctx context.Context, mode string, input any, output any) error {
	started := time.Now()
	epoch, ok := s.account.access()
	if !ok {
		return errMobileLocked
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stop := context.AfterFunc(epoch, cancel)
	defer func() { stop(); cancel() }()
	return s.runGuestHelper(ctx, started, "codex-sessions", mode, codexSessionsCommand(mode), input, output)
}

func (s *Server) discoverCodexConversations(ctx context.Context, paths []string) error {
	if !s.codexGuestSupported() {
		return nil
	}
	s.conversationMu.Lock()
	defer s.conversationMu.Unlock()
	s.mu.Lock()
	ready := s.ready && !s.closing
	s.mu.Unlock()
	if !ready {
		return nil
	}
	accounts := []string{"default"}
	s.codexAccounts.mu.Lock()
	list, err := s.loadCodexAccounts()
	s.codexAccounts.mu.Unlock()
	if err != nil {
		return err
	}
	for _, account := range list.Accounts {
		accounts = append(accounts, account.ID)
	}
	workspaces := make(map[string]string, len(paths))
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		workspaces[s.cliWorkspace(path)] = path
		roots = append(roots, s.cliWorkspace(path))
	}
	var found []codexConversation
	var failure struct {
		Error string `json:"error"`
	}
	var reply json.RawMessage
	if err := s.runCodexSessions(ctx, "discover", map[string]any{"accounts": accounts, "workspaces": roots}, &reply); err != nil {
		return err
	}
	if json.Unmarshal(reply, &found) != nil {
		_ = json.Unmarshal(reply, &failure)
		return errors.New(failure.Error)
	}
	m := &s.mobileCLI
	m.mu.Lock()
	defer m.mu.Unlock()
	threads := map[string]*mobileRecord{}
	connections := map[string]*mobileRecord{}
	for id := range m.catalog {
		record, err := m.record(s.profile, id)
		if err != nil {
			return err
		}
		threads[record.CLIID] = record
		if record.codex() && record.Connection != "" {
			connections[record.Connection] = record
		}
	}
	running := map[string]bool{}
	for _, item := range found {
		var workspace, matched string
		for guest, path := range workspaces {
			if cliWorkspaceWithin(item.Workspace, guest, false) && len(guest) > len(matched) {
				workspace, matched = path, guest
			}
		}
		if workspace == "" || item.ThreadID != "" && !validCLIUUID(item.ThreadID) {
			continue
		}
		record := threads[item.ThreadID]
		if record == nil && item.Connection != "" {
			record = connections[item.Connection]
		}
		if record != nil && (!record.codex() || record.Deleted || record.Archived || record.codexGuestAccount() != item.Account) {
			continue
		}
		if record == nil {
			if item.ThreadID == "" {
				continue
			}
			record = &mobileRecord{mobileSession: mobileSession{mobileSummary: mobileSummary{ID: uuid.NewString(), Provider: codexProvider, Workspace: workspace}, Turns: []mobileTurn{}}, Terminal: true}
			record.Generation = m.epochLocked()
			if item.Account != "default" {
				record.CodexAccount = item.Account
			}
		}
		before, _ := json.Marshal(record)
		if item.ThreadID != "" {
			record.CLIID, record.Resume = item.ThreadID, true
		}
		record.Workspace, record.Active = workspace, item.Active
		if item.Title != "" {
			record.Title = item.Title
		}
		if record.GuestWorkspace == "" {
			record.GuestWorkspace = item.Workspace
		}
		if item.UpdatedAt > 0 {
			record.UpdatedAt = time.UnixMilli(int64(item.UpdatedAt * 1000)).UTC()
		}
		if after, _ := json.Marshal(record); string(before) != string(after) {
			if err := writePrivate(mobileRecordPath(s.profile, record.ID), record); err != nil {
				return err
			}
			m.catalog[record.ID] = record.summary()
		}
		running[record.ID] = running[record.ID] || item.Running
		threads[record.CLIID] = record
	}
	m.codexLive = running
	return nil
}

// Caller holds conversationMu.
func (s *Server) codexConversationView(ctx context.Context, record *mobileRecord) (mobileSession, error) {
	id := record.ID
	root := s.cliWorkspace(record.Workspace)
	var current codexConversation
	input := map[string]any{"thread": record.codexThread(), "account": record.codexGuestAccount(), "connection": record.Connection, "workspace": root}
	if err := s.runCodexSessions(ctx, "view", input, &current); err != nil {
		return mobileSession{}, err
	}
	if current.Error != "" {
		return mobileSession{}, errors.New(current.Error)
	}
	if current.ThreadID == "" && len(current.Turns) == 0 {
		return record.snapshot(), nil
	}
	if current.ThreadID != "" && !validCLIUUID(current.ThreadID) || current.Workspace != "" && !cliWorkspaceWithin(current.Workspace, root, false) {
		return mobileSession{}, errors.New("Codex 会话的项目已变化，请重新读取会话列表")
	}
	if current.AccountRefused && record.CodexAccount != "" {
		s.recheckCodexAccount(record.CodexAccount)
	}
	m := &s.mobileCLI
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reload after SSH so metadata written meanwhile is retained.
	record, err := m.record(s.profile, id)
	if err != nil {
		return mobileSession{}, err
	}
	if record.Deleted {
		return mobileSession{}, os.ErrNotExist
	}
	before, _ := json.Marshal(record)
	record.Turns = current.Turns
	if record.Turns == nil {
		record.Turns = []mobileTurn{}
	}
	if current.ThreadID != "" {
		record.CLIID, record.Resume = current.ThreadID, true
	}
	if current.Title != "" {
		record.Title = current.Title
	}
	if current.Workspace != "" {
		record.GuestWorkspace = current.Workspace
	}
	record.ConnectionNotice = current.ConnectionNotice
	record.CanSend, record.HistoryTruncated = &current.CanSend, current.HistoryTruncated
	record.TerminalRunning, record.Active = current.Running, current.Active
	if after, _ := json.Marshal(record); string(before) != string(after) {
		record.Revision++
		if current.UpdatedAt > 0 {
			record.UpdatedAt = time.UnixMilli(int64(current.UpdatedAt * 1000)).UTC()
		}
		if err := writePrivate(mobileRecordPath(s.profile, id), record); err != nil {
			return mobileSession{}, err
		}
		if m.catalog != nil {
			m.catalog[id] = record.summary()
		}
	}
	if m.codexLive != nil {
		m.codexLive[id] = current.Running
	}
	return record.snapshot(), nil
}

// Caller holds the operation lock. The guest admits a message durably before
// Codex reads it, so a retried request ID never starts a second turn.
func (s *Server) codexConversationAction(ctx context.Context, record *mobileRecord, input map[string]any) error {
	action, _ := input["action"].(string)
	payload := map[string]any{"request_id": input["request_id"], "connection": record.Connection}
	switch action {
	case "send":
		if err := s.codexConnection(record); err != nil {
			return err
		}
		subject := ""
		if record.CodexAccount != "" {
			s.mu.Lock()
			update := s.guestUpdateRequired
			s.mu.Unlock()
			if update {
				return errGuestUpdateRequired
			}
			s.codexAccounts.mu.Lock()
			list, err := s.loadCodexAccounts()
			s.codexAccounts.mu.Unlock()
			if err != nil {
				return err
			}
			a := list.find(record.CodexAccount)
			if a == nil {
				return errors.New("这段会话所用的 Codex 账号已移除，无法继续")
			}
			if a.Email == "" {
				return errors.New("这段会话所用的 Codex 账号尚未登录，请在 Codex 账号管理中检查")
			}
			subject = a.subject()
		}
		directories, err := s.projectAdditionalDirectories(record.Workspace)
		if err != nil {
			return err
		}
		root := s.cliWorkspace(record.Workspace)
		directory := record.GuestWorkspace
		if directory == "" {
			directory = root
		}
		if !cliWorkspaceWithin(directory, root, false) {
			return errors.New("会话目录不在已授权的项目内，请重新读取会话列表")
		}
		if directories == nil {
			directories = []string{}
		}
		payload["connection"] = record.Connection
		payload["text"], payload["thread"], payload["cwd"] = input["text"], record.codexThread(), directory
		payload["account"], payload["subject"], payload["additional_directories"] = record.codexGuestAccount(), subject, directories
	case "stop":
		if record.Connection == "" {
			return errors.New("这次运行不是从手机发起的，请在电脑终端停止")
		}
	case "approve":
		return errors.New("Codex 会话不使用审批，请重新读取会话")
	default:
		return errors.New("Codex 会话不支持此操作")
	}
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := s.runCodexSessions(ctx, action, payload, &response); err != nil {
		return err
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "电脑未能处理 Codex 会话请求，请稍后重试。"
		}
		return errors.New(response.Error)
	}
	return nil
}

// Discovered sessions get their guest state key on the first phone message.
func (s *Server) codexConnection(record *mobileRecord) error {
	if record.Connection != "" {
		return nil
	}
	m := &s.mobileCLI
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.record(s.profile, record.ID)
	if err != nil {
		return err
	}
	if current.Connection == "" {
		current.Connection = uuid.NewString()
		if err := writePrivate(mobileRecordPath(s.profile, current.ID), current); err != nil {
			return err
		}
	}
	record.Connection = current.Connection
	return nil
}

// A new Codex session receives an account like a new Codex terminal and runs
// nothing until its first message.
func (s *Server) createCodexConversation(ctx context.Context, id, workspace string) (mobileSession, error) {
	if !s.codexGuestSupported() {
		return mobileSession{}, errors.New("Codex 会话目前只支持 Linux 环境")
	}
	s.mobileCLI.mu.Lock()
	_, err := s.mobileCLI.record(s.profile, id)
	s.mobileCLI.mu.Unlock()
	var selection codexSelection
	if errors.Is(err, os.ErrNotExist) {
		if selection, err = s.selectCodexAccount("", false, workspace); err != nil {
			return mobileSession{}, err
		}
	} else if err != nil {
		return mobileSession{}, err
	}
	created := false
	sendable := true
	s.account.mu.Lock()
	if !s.mobileAuthorized(ctx) {
		s.account.mu.Unlock()
		return mobileSession{}, errMobileLocked
	}
	view, err := s.mobileCLI.createProvider(s.profile, workspace, id, codexProvider, func(record *mobileRecord) {
		created = true
		record.Terminal, record.CodexAccount = true, selection.ID
		record.Connection, record.GuestWorkspace = uuid.NewString(), s.cliWorkspace(workspace)
		record.CanSend = &sendable
	})
	s.account.mu.Unlock()
	if err != nil {
		return mobileSession{}, err
	}
	if created && selection.ID != "" {
		s.noteCodexSelection(selection.ID, workspace)
	}
	return view, nil
}

func (m *mobileCLIStore) hasCodexSession(folders []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, summary := range m.catalog {
		if summary.Provider == codexProvider && !summary.Deleted && slices.Contains(folders, summary.Workspace) {
			return true
		}
	}
	return false
}

// Caller holds the operation and conversation locks.
func (s *Server) purgeCodexConversation(ctx context.Context, record *mobileRecord) error {
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	input := map[string]any{"thread": record.codexThread(), "account": record.codexGuestAccount(), "connection": record.Connection, "workspace": s.cliWorkspace(record.Workspace)}
	if err := s.runCodexSessions(ctx, "purge", input, &result); err != nil {
		return err
	}
	if !result.OK {
		return errors.New(result.Error)
	}
	return nil
}
