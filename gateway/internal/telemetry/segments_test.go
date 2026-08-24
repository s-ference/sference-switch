package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDiscoverSegmentsRecognizesExactRegularFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"requests-2026-08-002.jsonl",
		"requests-2026-07-999.jsonl",
		"requests-2026-08-001.jsonl",
		"requests-2026-13-001.jsonl",
		"requests-2026-08-000.jsonl",
		"requests-2026-08-01.jsonl",
		"requests-2026-08-001.json",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "requests-2026-09-001.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(
			filepath.Join(dir, "requests-2026-08-001.jsonl"),
			filepath.Join(dir, "requests-2026-10-001.jsonl"),
		); err != nil {
			t.Fatal(err)
		}
	}

	segments, err := DiscoverSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(segments))
	for index, segment := range segments {
		got[index] = segment.Name
	}
	want := []string{
		"requests-2026-07-999.jsonl",
		"requests-2026-08-001.jsonl",
		"requests-2026-08-002.jsonl",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("DiscoverSegments() = %v, want %v", got, want)
	}
}

func TestDiscoverSegmentsMissingDirectoryIsEmpty(t *testing.T) {
	segments, err := DiscoverSegments(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 0 {
		t.Fatalf("DiscoverSegments() returned %d segments, want 0", len(segments))
	}
}

func TestNextSegmentNameUsesHighestSequenceForMonth(t *testing.T) {
	segments := []Segment{
		{Year: 2026, Month: time.July, Sequence: 1},
		{Year: 2026, Month: time.July, Sequence: 7},
		{Year: 2026, Month: time.August, Sequence: 20},
	}
	name, err := nextSegmentName(segments, time.Date(2026, time.July, 1, 0, 0, 0, 0, time.FixedZone("west", -7*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	if name != "requests-2026-07-008.jsonl" {
		t.Fatalf("nextSegmentName() = %q", name)
	}
}

func TestDeleteExpiredSegmentsUsesMonthBoundaryWithoutReadingRows(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"requests-2026-01-001.jsonl",
		"requests-2026-01-002.jsonl",
		"requests-2026-03-001.jsonl",
		"requests-2026-04-001.jsonl",
		"requests-2026-07-001.jsonl",
	} {
		if err := os.WriteFile(
			filepath.Join(dir, name),
			[]byte("{contents-are-not-read}\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	err := DeleteExpiredSegments(
		dir,
		"requests-2026-01-002.jsonl",
		time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertFileExistence(t, filepath.Join(dir, "requests-2026-01-001.jsonl"), false)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-01-002.jsonl"), true)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-03-001.jsonl"), false)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-04-001.jsonl"), true)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-07-001.jsonl"), true)
}

// writeSizedSegment writes a segment of exactly size bytes.
func writeSizedSegment(t *testing.T, dir, name string, size int) {
	t.Helper()
	contents := make([]byte, size)
	for i := range contents {
		contents[i] = 'x'
	}
	if size > 0 {
		contents[size-1] = '\n'
	}
	if err := os.WriteFile(
		filepath.Join(dir, name), contents, 0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSegmentsOverBudgetRemovesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	// 4 x 1000 bytes = 4000 total, budget 2500 -> the two oldest go.
	for _, name := range []string{
		"requests-2026-01-001.jsonl",
		"requests-2026-01-002.jsonl",
		"requests-2026-02-001.jsonl",
		"requests-2026-02-002.jsonl",
	} {
		writeSizedSegment(t, dir, name, 1000)
	}

	if err := DeleteSegmentsOverBudget(
		dir, "requests-2026-02-002.jsonl", 2500,
	); err != nil {
		t.Fatal(err)
	}
	assertFileExistence(t, filepath.Join(dir, "requests-2026-01-001.jsonl"), false)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-01-002.jsonl"), false)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-02-001.jsonl"), true)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-02-002.jsonl"), true)
}

func TestDeleteSegmentsOverBudgetKeepsStoreWithinBudget(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"requests-2026-01-001.jsonl",
		"requests-2026-02-001.jsonl",
		"requests-2026-03-001.jsonl",
	} {
		writeSizedSegment(t, dir, name, 1000)
	}

	if err := DeleteSegmentsOverBudget(
		dir, "requests-2026-03-001.jsonl", 1500,
	); err != nil {
		t.Fatal(err)
	}
	segments, err := DiscoverSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, segment := range segments {
		total += segment.Size
	}
	if total > 1500 {
		t.Fatalf("retained %d bytes, want <= 1500", total)
	}
}

// The active segment is never deleted, even when it alone exceeds the
// budget: deleting the file the writer holds open would strand writes.
func TestDeleteSegmentsOverBudgetNeverDeletesActiveSegment(t *testing.T) {
	dir := t.TempDir()
	writeSizedSegment(t, dir, "requests-2026-01-001.jsonl", 1000)
	writeSizedSegment(t, dir, "requests-2026-02-001.jsonl", 5000)

	if err := DeleteSegmentsOverBudget(
		dir, "requests-2026-02-001.jsonl", 100,
	); err != nil {
		t.Fatal(err)
	}
	assertFileExistence(t, filepath.Join(dir, "requests-2026-01-001.jsonl"), false)
	assertFileExistence(t, filepath.Join(dir, "requests-2026-02-001.jsonl"), true)
}

func TestDeleteSegmentsOverBudgetDisabledAndUnderBudget(t *testing.T) {
	for _, test := range []struct {
		name   string
		budget int64
	}{
		// Both non-positive values disable deletion in this function.
		// NewWriter maps a zero option to DefaultMaxTotalBytes before it
		// ever reaches here; only a negative value opts out end to end.
		{name: "negative disables", budget: -1},
		{name: "zero disables", budget: 0},
		{name: "under budget", budget: 10_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSizedSegment(t, dir, "requests-2026-01-001.jsonl", 1000)
			writeSizedSegment(t, dir, "requests-2026-02-001.jsonl", 1000)

			if err := DeleteSegmentsOverBudget(
				dir, "requests-2026-02-001.jsonl", test.budget,
			); err != nil {
				t.Fatal(err)
			}
			assertFileExistence(
				t, filepath.Join(dir, "requests-2026-01-001.jsonl"), true)
			assertFileExistence(
				t, filepath.Join(dir, "requests-2026-02-001.jsonl"), true)
		})
	}
}

func TestSegmentMonthEndsByUTCBoundary(t *testing.T) {
	segment := Segment{Year: 2026, Month: time.March}
	for _, test := range []struct {
		name   string
		cutoff time.Time
		want   bool
	}{
		{
			name:   "before boundary",
			cutoff: time.Date(2026, time.March, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:   "at boundary",
			cutoff: time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			want:   true,
		},
		{
			name: "after boundary in non UTC zone",
			cutoff: time.Date(2026, time.March, 31, 17, 0, 0, 0,
				time.FixedZone("west", -7*60*60)),
			want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := segmentMonthEndsBy(segment, test.cutoff); got != test.want {
				t.Fatalf("segmentMonthEndsBy() = %t, want %t", got, test.want)
			}
		})
	}
}

func writeMinimalSegment(t *testing.T, dir, name string, completedAt time.Time) {
	t.Helper()
	line := fmt.Sprintf(
		"{\"schema_version\":1,\"event\":\"request\",\"completed_at\":%q}\n",
		completedAt.Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileExistence(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("stat %s error = %v, want not exist", path, err)
	}
}
