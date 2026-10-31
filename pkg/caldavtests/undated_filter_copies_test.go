// Vikunja is a to-do list application to facilitate your life.
// Copyright 2018-present Vikunja and contributors. All rights reserved.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package caldavtests

// Saved filter and Favorites collections repeat tasks that already live in a real project
// collection. With service.caldavundatedfiltercopies they are served without dates, repeats
// and alarms, so clients merging all collections show and alert each task once.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.vikunja.io/api/pkg/config"
	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/models"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 40 lives in project 36, is due 2023-03-01 15:00 and has a reminder in the fixtures.
const undatedTaskID = 40
const undatedTaskRealPath = "/dav/projects/36/uid-caldav-test.ics"

var undatedTaskDue = time.Date(2023, 3, 1, 15, 0, 0, 0, time.UTC)
var undatedTaskStart = time.Date(2023, 2, 27, 9, 0, 0, 0, time.UTC)
var undatedTaskEnd = time.Date(2023, 3, 1, 17, 0, 0, 0, time.UTC)

const undatedSyncCollectionBody = `<?xml version="1.0" encoding="utf-8"?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token/>
  <D:sync-level>1</D:sync-level>
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
</D:sync-collection>`

// dateProperties are the VTODO lines a filter copy must not carry.
var dateProperties = []string{"DUE", "DTSTART", "DTEND", "DURATION", "RRULE", "BEGIN:VALARM"}

func setupUndatedEnv(t *testing.T, enabled bool) *echo.Echo {
	t.Helper()

	e := setupTestEnv(t)
	config.ServiceCaldavUndatedFilterCopies.Set(enabled)
	t.Cleanup(func() { config.ServiceCaldavUndatedFilterCopies.Set(false) })

	s := db.NewSession()
	defer s.Close()
	_, err := s.ID(undatedTaskID).Cols("start_date", "end_date").Update(&models.Task{
		StartDate: undatedTaskStart,
		EndDate:   undatedTaskEnd,
	})
	require.NoError(t, err)
	require.NoError(t, s.Commit())

	return e
}

func setRepeat(t *testing.T, repeatAfter int64) {
	t.Helper()

	s := db.NewSession()
	defer s.Close()
	_, err := s.ID(undatedTaskID).Cols("repeat_after").Update(&models.Task{RepeatAfter: repeatAfter})
	require.NoError(t, err)
	require.NoError(t, s.Commit())
}

// createUndatedFilter returns the collection path of a new saved filter of user 15 containing task 40.
func createUndatedFilter(t *testing.T) string {
	t.Helper()

	s := db.NewSession()
	defer s.Close()
	sf := &models.SavedFilter{
		Title:   "Due soon",
		Filters: &models.TaskCollection{Filter: `due_date > '2023-01-01T00:00:00+00:00'`},
	}
	require.NoError(t, sf.Create(s, &testuser15))
	require.NoError(t, s.Commit())

	return "/dav/projects/" + strconv.FormatInt(-sf.ID-1, 10)
}

func favoriteUndatedTask(t *testing.T) string {
	t.Helper()

	s := db.NewSession()
	defer s.Close()
	_, err := s.Insert(&models.Favorite{EntityID: undatedTaskID, UserID: testuser15.ID, Kind: models.FavoriteKindTask})
	require.NoError(t, err)
	require.NoError(t, s.Commit())

	return "/dav/projects/" + strconv.FormatInt(models.FavoritesPseudoProjectID, 10)
}

func assertUndated(t *testing.T, vcalendar string) {
	t.Helper()
	require.Contains(t, vcalendar, "UID:uid-caldav-test")
	for _, line := range strings.Split(vcalendar, "\n") {
		for _, prop := range dateProperties {
			assert.False(t, strings.HasPrefix(line, prop), "filter copy carries %q", line)
		}
	}
}

func assertDated(t *testing.T, vcalendar string) {
	t.Helper()
	assert.Contains(t, vcalendar, "\nDUE:")
	assert.Contains(t, vcalendar, "\nDTSTART:")
	assert.Contains(t, vcalendar, "BEGIN:VALARM")
}

// assertStoredDatesKept checks that task 40 still has every date, reminder and repeat it started with.
func assertStoredDatesKept(t *testing.T, repeatAfter int64) {
	t.Helper()

	s := db.NewSession()
	defer s.Close()
	task, err := models.GetTaskByIDSimple(s, undatedTaskID)
	require.NoError(t, err)
	assert.True(t, task.DueDate.Equal(undatedTaskDue), "due date changed to %v", task.DueDate)
	assert.True(t, task.StartDate.Equal(undatedTaskStart), "start date changed to %v", task.StartDate)
	assert.True(t, task.EndDate.Equal(undatedTaskEnd), "end date changed to %v", task.EndDate)
	assert.Equal(t, repeatAfter, task.RepeatAfter)

	reminders, err := s.Where("task_id = ?", undatedTaskID).Count(&models.TaskReminder{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), reminders)
}

func TestUndatedFilterCopies_Serving(t *testing.T) {
	t.Run("a saved filter serves the task without dates, repeats or alarms", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		setRepeat(t, 86400)
		filterPath := createUndatedFilter(t)

		rec := caldavGET(t, e, filterPath+"/uid-caldav-test.ics")
		assertResponseStatus(t, rec, http.StatusOK)
		assertUndated(t, rec.Body.String())
	})

	t.Run("the real project still serves the dated task", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		setRepeat(t, 86400)
		createUndatedFilter(t)

		rec := caldavGET(t, e, undatedTaskRealPath)
		assertResponseStatus(t, rec, http.StatusOK)
		assertDated(t, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "\nRRULE:")
	})

	t.Run("the two copies have different, stable etags", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)

		filterETag := caldavGET(t, e, filterPath+"/uid-caldav-test.ics").Header().Get("ETag")
		realETag := caldavGET(t, e, undatedTaskRealPath).Header().Get("ETag")
		assert.NotEmpty(t, filterETag)
		assert.NotEqual(t, realETag, filterETag)
		assert.Equal(t, filterETag, caldavGET(t, e, filterPath+"/uid-caldav-test.ics").Header().Get("ETag"))
	})

	t.Run("the collection GET serves undated copies", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)

		rec := caldavGET(t, e, filterPath)
		assertResponseStatus(t, rec, http.StatusOK)
		assertUndated(t, rec.Body.String())
	})

	t.Run("calendar-multiget serves undated copies with the filter etag", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)
		filterETag := caldavGET(t, e, filterPath+"/uid-caldav-test.ics").Header().Get("ETag")

		rec := caldavREPORT(t, e, filterPath, ReportCalendarMultiget(filterPath+"/uid-caldav-test.ics"))
		assertResponseStatus(t, rec, 207)
		ms := parseMultistatus(t, rec)
		require.Len(t, ms.Responses, 1)
		prop := getSuccessfulProp(t, ms.Responses[0])
		assertUndated(t, prop.CalendarData)
		assert.Equal(t, filterETag, prop.GetETag)
	})

	t.Run("sync-collection serves undated copies with the filter etag", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)
		filterETag := caldavGET(t, e, filterPath+"/uid-caldav-test.ics").Header().Get("ETag")

		rec := caldavREPORT(t, e, filterPath, undatedSyncCollectionBody)
		assertResponseStatus(t, rec, 207)
		ms := parseMultistatus(t, rec)
		var found bool
		for _, r := range ms.Responses {
			if !strings.HasSuffix(r.Href, "/uid-caldav-test.ics") {
				continue
			}
			found = true
			prop := getSuccessfulProp(t, r)
			assertUndated(t, prop.CalendarData)
			assert.Equal(t, filterETag, prop.GetETag)
		}
		assert.True(t, found, "sync-collection did not report task 40")
	})

	t.Run("enabling the setting changes the collection etag", func(t *testing.T) {
		e := setupUndatedEnv(t, false)
		filterPath := createUndatedFilter(t)

		collectionProps := func() Prop {
			rec := caldavPROPFIND(t, e, filterPath, "0", PropfindCalendarCollectionProperties)
			assertResponseStatus(t, rec, 207)
			ms := parseMultistatus(t, rec)
			require.NotEmpty(t, ms.Responses)
			return getSuccessfulProp(t, ms.Responses[0])
		}

		before := collectionProps()
		config.ServiceCaldavUndatedFilterCopies.Set(true)
		after := collectionProps()
		assert.NotEmpty(t, before.GetETag)
		assert.NotEqual(t, before.GetETag, after.GetETag)
		assert.NotEmpty(t, before.GetCTag)
		assert.NotEqual(t, before.GetCTag, after.GetCTag)
	})

	t.Run("favorites serve undated copies", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		favoritesPath := favoriteUndatedTask(t)

		rec := caldavGET(t, e, favoritesPath+"/uid-caldav-test.ics")
		assertResponseStatus(t, rec, http.StatusOK)
		assertUndated(t, rec.Body.String())
	})

	t.Run("off by default: a saved filter serves the dated task", func(t *testing.T) {
		e := setupUndatedEnv(t, false)
		filterPath := createUndatedFilter(t)

		rec := caldavGET(t, e, filterPath+"/uid-caldav-test.ics")
		assertResponseStatus(t, rec, http.StatusOK)
		assertDated(t, rec.Body.String())
		assert.Equal(t, caldavGET(t, e, undatedTaskRealPath).Header().Get("ETag"), rec.Header().Get("ETag"))
	})
}

func TestUndatedFilterCopies_WriteBack(t *testing.T) {
	// putBack sends the served copy back the way a client does after editing it.
	putBack := func(t *testing.T, e *echo.Echo, path string, edit func(string) string) *httptest.ResponseRecorder {
		t.Helper()

		get := caldavGET(t, e, path)
		assertResponseStatus(t, get, http.StatusOK)
		body := edit(get.Body.String())

		return caldavRequest(t, e, http.MethodPut, path, body, map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
			"If-Match":     get.Header().Get("ETag"),
		})
	}
	complete := func(vcalendar string) string {
		return strings.Replace(vcalendar, "STATUS:NEEDS-ACTION", "STATUS:COMPLETED\nCOMPLETED:20230301T120000Z", 1)
	}

	t.Run("completing through a filter keeps dates and reminders", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)

		rec := putBack(t, e, filterPath+"/uid-caldav-test.ics", complete)
		require.True(t, rec.Code >= 200 && rec.Code < 300, "PUT returned %d", rec.Code)

		db.AssertExists(t, "tasks", map[string]interface{}{"id": undatedTaskID, "done": true, "project_id": 36}, false)
		assertStoredDatesKept(t, 0)
	})

	t.Run("renaming through a filter keeps dates and reminders", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)

		rec := putBack(t, e, filterPath+"/uid-caldav-test.ics", func(v string) string {
			return strings.Replace(v, "SUMMARY:", "SUMMARY:Renamed ", 1)
		})
		require.True(t, rec.Code >= 200 && rec.Code < 300, "PUT returned %d", rec.Code)

		assertStoredDatesKept(t, 0)
	})

	t.Run("completing a repeating task through a filter reschedules from the stored due date", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		setRepeat(t, 86400)
		filterPath := createUndatedFilter(t)

		rec := putBack(t, e, filterPath+"/uid-caldav-test.ics", complete)
		require.True(t, rec.Code >= 200 && rec.Code < 300, "PUT returned %d", rec.Code)

		s := db.NewSession()
		defer s.Close()
		task, err := models.GetTaskByIDSimple(s, undatedTaskID)
		require.NoError(t, err)
		assert.False(t, task.Done, "a repeating task is reopened when marked done")
		// Repeating advances by whole intervals until the date is in the future.
		assert.True(t, task.DueDate.After(time.Now()), "due date is %v", task.DueDate)
		assert.Zero(t, task.DueDate.Sub(undatedTaskDue)%(24*time.Hour), "due date %v is not whole days after the stored one", task.DueDate)
		assert.Equal(t, int64(86400), task.RepeatAfter)
	})

	t.Run("a second edit through a filter can use the etag of the first", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)
		path := filterPath + "/uid-caldav-test.ics"

		first := putBack(t, e, path, func(v string) string {
			return strings.Replace(v, "SUMMARY:", "SUMMARY:First ", 1)
		})
		require.True(t, first.Code >= 200 && first.Code < 300, "PUT returned %d", first.Code)
		etag := first.Header().Get("ETag")
		assert.Equal(t, caldavGET(t, e, path).Header().Get("ETag"), etag)

		second := caldavRequest(t, e, http.MethodPut, path, complete(caldavGET(t, e, path).Body.String()), map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
			"If-Match":     etag,
		})
		require.True(t, second.Code >= 200 && second.Code < 300, "PUT returned %d", second.Code)
		assertStoredDatesKept(t, 0)
	})

	t.Run("a stale copy is rejected instead of overwriting newer changes", func(t *testing.T) {
		e := setupUndatedEnv(t, true)
		filterPath := createUndatedFilter(t)
		path := filterPath + "/uid-caldav-test.ics"

		stale := caldavGET(t, e, path)
		// Etags have second granularity.
		time.Sleep(time.Second)
		rec := putBack(t, e, undatedTaskRealPath, func(v string) string {
			return strings.Replace(v, "SUMMARY:", "SUMMARY:Newer ", 1)
		})
		require.True(t, rec.Code >= 200 && rec.Code < 300, "PUT returned %d", rec.Code)

		rec = caldavRequest(t, e, http.MethodPut, path, complete(stale.Body.String()), map[string]string{
			"Content-Type": "text/calendar; charset=utf-8",
			"If-Match":     stale.Header().Get("ETag"),
		})
		assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
		db.AssertExists(t, "tasks", map[string]interface{}{"id": undatedTaskID, "done": false}, false)
	})
}
