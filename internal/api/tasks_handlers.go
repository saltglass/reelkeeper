package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/jobs"
)

// recentJobs is how many runs System → Tasks lists.
const recentJobs = 50

// GetTasks lists background tasks and recent runs.
func (h *Handlers) GetTasks(w http.ResponseWriter, r *http.Request, params gen.GetTasksParams) {
	infos, err := h.Jobs.Tasks(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	recent, hidden, err := h.Jobs.Recent(r.Context(), recentJobs, params.Routine != nil && *params.Routine)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.TasksOverview{Tasks: make([]gen.Task, 0, len(infos)), Recent: make([]gen.Job, 0, len(recent)), HiddenRoutine: hidden}
	for _, info := range infos {
		t := gen.Task{
			Kind:         info.Kind,
			Name:         info.Name,
			Description:  info.Description,
			EverySeconds: int64(info.Every.Seconds()),
		}
		if !info.NextRun.IsZero() {
			next := info.NextRun
			t.NextRunAt = &next
		}
		if info.Last != nil {
			j := ToJob(h.Jobs, *info.Last)
			t.Last = &j
		}
		if info.Active != nil {
			j := ToJob(h.Jobs, *info.Active)
			t.Active = &j
		}
		out.Tasks = append(out.Tasks, t)
	}
	for _, j := range recent {
		out.Recent = append(out.Recent, ToJob(h.Jobs, j))
	}
	writeJSON(w, http.StatusOK, out)
}

// RunTask queues a task now.
func (h *Handlers) RunTask(w http.ResponseWriter, r *http.Request, kind string) {
	job, created, err := h.Jobs.RunNow(r.Context(), kind)
	if errors.Is(err, jobs.ErrUnknownTask) {
		writeError(w, http.StatusNotFound, "not_found", "There is no task called "+kind+".", nil)
		return
	}
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": ToJob(h.Jobs, *job), "alreadyActive": !created})
}

// CancelJob stops a queued or running task.
func (h *Handlers) CancelJob(w http.ResponseWriter, r *http.Request, id int64) {
	err := h.Jobs.Cancel(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "There is no such task run.", nil)
	case errors.Is(err, jobs.ErrNotCancellable):
		writeError(w, http.StatusConflict, "finished", "That task has already finished.", nil)
	default:
		h.writeServiceError(w, r, err)
	}
}

// ToJob converts a job for the API and for task.updated events.
func ToJob(m *jobs.Manager, j jobs.Job) gen.Job {
	return gen.Job{
		Id:         j.ID,
		Kind:       j.Kind,
		Name:       m.Name(j.Kind),
		Status:     gen.JobStatus(j.Status),
		Trigger:    gen.JobTrigger(j.Trigger),
		Progress:   j.Progress,
		Message:    j.Message,
		Error:      j.Error,
		Attempts:   j.Attempts,
		Quiet:      j.Quiet,
		CreatedAt:  j.CreatedAt,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
}
