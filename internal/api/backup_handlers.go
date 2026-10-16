package api

import (
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/backup"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// maxUploadBytes caps an uploaded backup. A backup is a few MB; this leaves
// room for a very large library.
const maxUploadBytes = 1 << 30

// largeUploads are the endpoints allowed a body larger than maxBodyBytes:
// both take a backup file and set their own limit (maxUploadBytes).
var largeUploads = map[string]bool{"/backups/upload": true, "/auth/setup/restore": true}

// --- Settings ---------------------------------------------------------------

// GetBackupSettings returns System → Backups' settings.
func (h *Handlers) GetBackupSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.toBackupSettings(h.Settings.Backups()))
}

// PutBackupSettings saves System → Backups' settings.
func (h *Handlers) PutBackupSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutBackupSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveBackups(r.Context(), settings.Backups{
		Folder: body.Folder, Scheduled: body.Scheduled, Time: body.Time, Keep: body.Keep,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toBackupSettings(saved))
}

func (h *Handlers) toBackupSettings(b settings.Backups) gen.BackupSettings {
	// Folder() reads the saved settings, which are the ones just returned.
	effective := h.Backups.Folder()
	return gen.BackupSettings{Folder: b.Folder, Scheduled: b.Scheduled, Time: b.Time, Keep: b.Keep, EffectiveFolder: &effective}
}

// --- Backups ----------------------------------------------------------------

// ListBackups lists the backups in the folder, and how the last restore went.
func (h *Handlers) ListBackups(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Backups.List()
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.BackupList{Folder: h.Backups.Folder(), Backups: make([]gen.BackupEntry, 0, len(entries))}
	for _, e := range entries {
		out.Backups = append(out.Backups, toBackupEntry(e))
	}
	if res := h.Backups.LastResult(); res != nil {
		out.LastRestore = &gen.RestoreResult{At: res.At, Backup: res.Backup, Ok: res.OK, Message: res.Message}
		if res.Safety != "" {
			safety := res.Safety
			out.LastRestore.Safety = &safety
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func toBackupEntry(e backup.Entry) gen.BackupEntry {
	out := gen.BackupEntry{Name: e.Name, Size: e.Size, KeyMatches: e.KeyMatches}
	if e.Problem != "" {
		p := e.Problem
		out.Problem = &p
	}
	if hd := e.Header; hd != nil {
		created, kind, app, engine, schema := hd.CreatedAt, hd.Kind, hd.App, hd.Engine, hd.SchemaVersion
		out.CreatedAt, out.Kind, out.App, out.Engine, out.SchemaVersion = &created, &kind, &app, &engine, &schema
	}
	return out
}

// CreateBackup makes a manual backup now.
func (h *Handlers) CreateBackup(w http.ResponseWriter, r *http.Request) {
	e, err := h.Backups.Create(r.Context(), backup.KindManual)
	if err != nil {
		h.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackupEntry(*e))
}

// UploadBackup saves a backup sent as the request body.
func (h *Handlers) UploadBackup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	e, err := h.Backups.Upload(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That file is larger than 1 GiB, which is more than a backup should be.", nil)
			return
		}
		h.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackupEntry(*e))
}

// DownloadBackup sends a backup file.
func (h *Handlers) DownloadBackup(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.Backups.Open(name)
	if err != nil {
		h.writeBackupError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// DeleteBackup deletes a backup file.
func (h *Handlers) DeleteBackup(w http.ResponseWriter, r *http.Request, name string) {
	if err := h.Backups.Delete(name); err != nil {
		h.writeBackupError(w, r, err)
		return
	}
	h.Logger.Info("backup deleted", "backup", name, "by", requestUser(r))
	w.WriteHeader(http.StatusNoContent)
}

// CheckBackup reads a backup in full and says whether it can be restored.
func (h *Handlers) CheckBackup(w http.ResponseWriter, r *http.Request, name string) {
	sum, err := h.Backups.Check(r.Context(), name)
	if err != nil {
		h.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupCheck(sum))
}

func toBackupCheck(s *backup.Summary) gen.BackupCheck {
	rows := make(map[string]int, len(s.Rows))
	for k, v := range s.Rows {
		rows[k] = v
	}
	return gen.BackupCheck{
		CreatedAt: s.Header.CreatedAt, App: s.Header.App, Engine: s.Header.Engine,
		SchemaVersion: s.Header.SchemaVersion, Kind: s.Header.Kind, Rows: rows, Total: s.Total,
	}
}

// RestoreBackup checks a backup, records it for restoring and restarts
// Reelkeeper, which restores it before anything else runs.
func (h *Handlers) RestoreBackup(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := h.Backups.StageRestore(r.Context(), name); err != nil {
		h.writeBackupError(w, r, err)
		return
	}
	h.Logger.Warn("restore of a backup requested from System → Backups", "backup", name, "by", requestUser(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "Reelkeeper is restarting to restore " + name + "."})
}

// requestUser names who made a request, for the log.
func requestUser(r *http.Request) string {
	if id := auth.Info(r.Context()).Identity; id != nil && id.User != nil {
		return id.User.Username
	}
	return "an API key"
}

// writeBackupError maps the backup service's errors: no such backup is a
// 404, a file that can't be used is a 422 with the reason, and anything
// else a 500 that keeps its plain message (it is often "the folder can't be
// written", which the user can fix).
func (h *Handlers) writeBackupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, backup.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "There is no such backup.", nil)
	case backup.IsUserError(err):
		writeError(w, http.StatusUnprocessableEntity, "backup", capitalize(err.Error()), nil)
	default:
		h.Logger.Error("backup request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", capitalize(err.Error()), nil)
	}
}
