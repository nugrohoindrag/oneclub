package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"oneclub/internal/platform/maintenance"
	"oneclub/internal/platform/rules"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mustUUID(s string) uuid.UUID { return uuid.MustParse(s) }

// multipartBody builds a multipart/form-data body with fields and one file.
func multipartBody(t testing.TB, fields map[string]string, fileField, filename, content string) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile(fileField, filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// maintenanceCheck runs the job health check and alert for the last hour.
func maintenanceCheck(in *Instance) ([]string, error) {
	ctx := context.Background()
	h, err := maintenance.Check(ctx, in.DB, time.Hour)
	if err != nil {
		return nil, err
	}
	_, err = maintenance.Alert(ctx, &maintenance.Deps{DB: in.DB, Notify: in.App.Notification, Approvals: in.App.Approvals, Cfg: in.App.Cfg}, h)
	return h.Failed, err
}

func rulesResolve(in *Instance, kind, code string) (json.RawMessage, int, bool, error) {
	return rules.Resolve(context.Background(), in.DB.Primary, kind, code, nil, time.Now())
}
