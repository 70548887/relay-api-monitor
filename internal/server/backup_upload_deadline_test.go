package server

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBackupReadDeadlineExtendsOnlyAdmittedBody(t *testing.T) {
	for _, extend := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "backup"}[extend], func(t *testing.T) {
			seen := make(chan error, 1)
			router := gin.New()
			router.POST("/", func(c *gin.Context) {
				w, r := c.Writer, c.Request
				if extend {
					if e := extendBackupReadDeadline(w, time.Second); e != nil {
						seen <- e
						return
					}
				}
				_, e := io.Copy(io.Discard, r.Body)
				seen <- e
				w.WriteHeader(200)
			})
			ts := httptest.NewUnstartedServer(router)
			ts.Config.ReadTimeout = 60 * time.Millisecond
			ts.Start()
			defer ts.Close()
			conn, e := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\nConnection: close\r\n\r\na")
			time.Sleep(150 * time.Millisecond)
			_, _ = io.WriteString(conn, "b")
			select {
			case e = <-seen:
				if extend && e != nil {
					t.Fatal("admitted upload hit ordinary deadline", e)
				}
				if !extend && e == nil {
					t.Fatal("ordinary body lost short timeout")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("handler stuck")
			}
		})
	}
}
func TestBackupImportFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{context.DeadlineExceeded, 408, "import_timeout"}, {&http.MaxBytesError{Limit: backupImportLimit}, 413, "import_too_large"}, {errors.New("read failed"), 400, "import_read_failed"}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", nil)
		backupImportReadError(c, tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
