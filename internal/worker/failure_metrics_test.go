package worker

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFailedJobCompletionPersistsOwnMetrics(t *testing.T) {
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(rw, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		time.Sleep(15 * time.Millisecond)
	})
	j := enqueue(t, st, ctx, m, "artifact", "cn_html")
	if worked, err := w.WorkOne(ctx, "artifact"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	current, err := st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "failed" || current.DurationMS < 10 || current.FirstTextMS == nil || current.HTTPStatus != 200 || current.TextBytes != 7 || current.ReceivedBytes < 7 {
		t.Fatalf("failed task dropped measured result: %+v", current)
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 0 {
		t.Fatal("partial failure published artwork", err)
	}
	if !strings.Contains(current.ErrorCode, "incomplete") {
		t.Fatal(current.ErrorCode)
	}
}
