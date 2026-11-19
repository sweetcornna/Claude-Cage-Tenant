package tenant

import (
	"cmp"
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ticketTTL       = 60 * time.Second
	maxTickets      = 4096
	maxLoginTickets = 16
	readinessTTL    = 30 * time.Second
)

func validVMName(name string) bool {
	length := utf8.RuneCountInString(name)
	return length >= 1 && length <= 64 && strings.IndexFunc(name, unicode.IsControl) == -1
}

func (s *Server) listVMs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session := s.expectedSessionLocked(w, r)
	if session == nil {
		s.mu.Unlock()
		return
	}
	list := make([]vm, 0)
	for _, record := range s.store.VMs {
		if record.Owner == session.user.ID {
			list = append(list, s.visibleVMLocked(record))
		}
	}
	s.mu.Unlock()
	slices.SortFunc(list, func(left, right vm) int {
		if order := left.CreatedAt.Compare(right.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(left.ID, right.ID)
	})
	jsonReply(w, http.StatusOK, map[string]any{"vms": list, "limit": s.cfg.AccountVMLimit})
}

func (s *Server) visibleVMLocked(record vmRecord) vm {
	visible := record.vm
	workspace := s.workspaces[record.ID]
	switch {
	case s.starting[record.ID]:
		visible.State = starting
	case workspace != nil && !workspace.checkedAt.IsZero():
		visible.State = ready
	case workspace != nil || s.startFailed[record.ID]:
		visible.State, visible.Message = failed, startupMessage
	}
	return visible
}

func (s *Server) createVM(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		SystemID string `json:"system_id"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.expectedSessionLocked(w, r)
	if session == nil {
		return
	}
	if !validVMName(name) {
		invalidInput(w)
		return
	}
	system, configured := s.systems[input.SystemID]
	if !configured {
		invalidInput(w)
		return
	}
	count := 0
	for _, record := range s.store.VMs {
		if record.Owner == session.user.ID {
			count++
		}
	}
	if count >= s.cfg.AccountVMLimit || len(s.store.VMs) >= s.cfg.TotalVMLimit {
		problem(w, http.StatusConflict, "vm_limit", "The workspace quota has been reached.")
		return
	}
	id := secret(16)
	for s.store.VMs[id].ID != "" {
		id = secret(16)
	}
	record := vmRecord{
		vm:    vm{ID: id, Name: name, SystemID: system.ID, SystemLabel: system.Label, State: stopped, CreatedAt: s.now().UTC()},
		Owner: session.user.ID, Image: system.Image,
	}
	if s.saveVM(record) != nil {
		unavailable(w)
		return
	}
	jsonReply(w, http.StatusCreated, map[string]vm{"vm": record.vm})
}

func (s *Server) saveVM(record vmRecord) error {
	updated := s.store.database
	updated.VMs = maps.Clone(updated.VMs)
	updated.VMs[record.ID] = record
	return s.store.save(updated)
}

func (s *Server) openVM(w http.ResponseWriter, r *http.Request, id string) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.expectedSessionLocked(w, r)
	if session == nil {
		return
	}
	record, exists := s.store.VMs[id]
	if !exists || record.Owner != session.user.ID {
		vmNotFound(w)
		return
	}
	if s.store.blocked {
		unavailable(w)
		return
	}
	now := s.now()
	if !s.allowRate("open", session.user.ID, 120, now) {
		rateLimited(w)
		return
	}
	current := s.workspaces[id]
	visible := s.visibleVMLocked(record)
	if visible.State == ready && now.Sub(current.checkedAt) < readinessTTL {
		if session.ticketCount >= maxLoginTickets || len(s.tickets) >= maxTickets {
			s.prune(now)
			if session.ticketCount >= maxLoginTickets || len(s.tickets) >= maxTickets {
				rateLimited(w)
				return
			}
		}
		token := secret(32)
		s.tickets[token] = entryTicket{grant: vmGrant{login: session, vmID: id}, expires: now.Add(ticketTTL)}
		session.ticketCount++
		jsonReply(w, http.StatusOK, map[string]any{"vm": visible, "workspace_url": s.site.vmOrigin(id) + "/_tenant/enter#" + token})
		return
	}
	if !s.starting[id] && (current == nil || !now.Before(current.nextProbe)) {
		s.starting[id] = true
		delete(s.startFailed, id)
		var faults uint64
		if current != nil {
			faults = current.faults
		}
		spec := LaunchSpec{VMID: id, SystemID: record.SystemID, Image: record.Image}
		s.workers.Go(func() { s.prepare(spec, current, faults) })
	}
	jsonReply(w, http.StatusAccepted, map[string]vm{"vm": s.visibleVMLocked(record)})
}

func (s *Server) invalidateWorkspace(id string, workspace *Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaces[id] != workspace {
		return
	}
	s.markWorkspaceUnready(id, workspace)
}

// Caller holds s.mu.
func (s *Server) markWorkspaceUnready(id string, workspace *Workspace) {
	workspace.faults++
	workspace.checkedAt = time.Time{}
	s.deleteWorkspaceTickets(id)
}

func (s *Server) confirmWorkspaceFault(id string, workspace *Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.context.Err() != nil || s.workspaces[id] != workspace || workspace.checkedAt.IsZero() || now.Before(workspace.nextFaultProbe) || !workspace.checkingSession.CompareAndSwap(false, true) {
		return
	}
	workspace.nextFaultProbe = now.Add(3 * time.Second)
	faults := workspace.faults
	s.workers.Go(func() {
		defer workspace.checkingSession.Store(false)
		ctx, cancel := context.WithTimeout(workspace.ctx, 3*time.Second)
		defer cancel()
		stop := context.AfterFunc(s.context, cancel)
		defer stop()
		if err := workspace.check(ctx); err == nil || workspace.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.context.Err() == nil && s.workspaces[id] == workspace && workspace.faults == faults {
			s.markWorkspaceUnready(id, workspace)
		}
	})
}

func (s *Server) deleteWorkspaceTickets(id string) {
	for token, ticket := range s.tickets {
		if ticket.grant.vmID == id {
			s.deleteTicket(token, ticket)
		}
	}
}

func (s *Server) prepare(spec LaunchSpec, previous *Workspace, faults uint64) {
	id := spec.VMID
	select {
	case s.startSlots <- struct{}{}:
		defer func() { <-s.startSlots }()
	case <-s.context.Done():
		return
	}
	ctx, cancel := context.WithTimeout(s.context, 5*time.Minute)
	defer cancel()
	workspace := previous
	if previous != nil {
		probe, done := context.WithTimeout(ctx, 5*time.Second)
		err := previous.recheck(probe)
		probeErr := probe.Err()
		done()
		if err != nil {
			s.mu.Lock()
			if probeErr != nil || errors.Is(err, errIncusQuery) || isTimeout(err) {
				// A slow or unavailable probe cannot prove that active streams are broken.
				s.markWorkspaceUnready(id, previous)
				previous.nextProbe = s.now().Add(2 * time.Second)
				delete(s.starting, id)
				s.mu.Unlock()
				return
			}
			workspace = nil
			delete(s.workspaces, id)
			s.deleteWorkspaceTickets(id)
			s.mu.Unlock()
			previous.close()
		}
	}
	if workspace == nil {
		var err error
		workspace, err = s.cfg.Runtime.Prepare(ctx, spec)
		if err != nil {
			if s.context.Err() == nil {
				log.Printf("cage-tenant: workspace %s preparation failed: %v", id, err)
			}
			if workspace != nil {
				workspace.close()
				workspace = nil
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.starting, id)
	if s.context.Err() != nil {
		if workspace != nil && workspace != previous {
			workspace.close()
		}
		return
	}
	if workspace == nil {
		s.startFailed[id] = true
		return
	}
	if workspace != previous || workspace.faults == faults {
		workspace.checkedAt = s.now()
	}
	s.workspaces[id] = workspace
}
