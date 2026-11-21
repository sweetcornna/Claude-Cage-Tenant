package workbench

import (
	"errors"
	"net/http"
)

func (s *Server) launch(w http.ResponseWriter, kind string, run func() error) {
	s.launchJob(w, kind, run, func(w http.ResponseWriter, job Job) { jsonReply(w, job) })
}

// launchJob shares admission errors between the desktop and phone replies.
func (s *Server) launchJob(w http.ResponseWriter, kind string, run func() error, reply func(http.ResponseWriter, Job)) {
	job, err := s.submitJob(kind, run)
	switch {
	case errors.Is(err, errJobsClosed):
		http.Error(w, "后台正在更新，请重新打开 Super Cage。", http.StatusServiceUnavailable)
	case errors.Is(err, errJobBusy):
		http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
	case err != nil:
		requestError(w, err)
	default:
		reply(w, job)
	}
}
