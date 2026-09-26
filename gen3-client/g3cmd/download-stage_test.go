package g3cmd

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uc-cdis/gen3-client/gen3-client/commonUtils"
	pb "gopkg.in/cheggaaa/pb.v1"
)

type interruptedReader struct {
	sent bool
}

func (r *interruptedReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "abc"), nil
	}
	return 0, errors.New("connection dropped")
}

func (r *interruptedReader) Close() error { return nil }

func testDownloadResponse(status int, body io.ReadCloser, contentLength int64, contentRange string) *http.Response {
	response := &http.Response{StatusCode: status, Body: body, ContentLength: contentLength, Header: make(http.Header)}
	if contentRange != "" {
		response.Header.Set("Content-Range", contentRange)
	}
	return response
}

func testMD5(content string) string {
	sum := md5.Sum([]byte(content))
	return fmt.Sprintf("%x", sum)
}

func TestInterruptedDownloadLeavesOnlyPartAndResumes(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 6,
		ExpectedMD5:  testMD5("abcdef"),
		Response:     testDownloadResponse(http.StatusOK, &interruptedReader{}, -1, ""),
	}
	if err := downloadResponseToStage(object, pb.New64(6)); err == nil {
		t.Fatal("expected an interrupted transfer to fail")
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final filename appeared before completion: %v", err)
	}
	part, err := os.ReadFile(stagingPath(finalPath))
	if err != nil || string(part) != "abc" {
		t.Fatalf("expected partial bytes to remain for resumption, got %q, %v", part, err)
	}

	resume := validateLocalFileStat(dir, "archive.zip", 6, testMD5("abcdef"), true)
	if resume.Range != 3 {
		t.Fatalf("expected resume offset 3, got %d", resume.Range)
	}
	resume.GUID = "test-guid"
	resume.Response = testDownloadResponse(http.StatusPartialContent, io.NopCloser(strings.NewReader("def")), 3, "bytes 3-5/6")
	if err := downloadResponseToStage(resume, pb.New64(6)); err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(finalPath)
	if err != nil || string(final) != "abcdef" {
		t.Fatalf("expected verified final bytes, got %q, %v", final, err)
	}
	if _, err := os.Stat(stagingPath(finalPath)); !os.IsNotExist(err) {
		t.Fatalf("part file remained after publication: %v", err)
	}
}

func TestFinalFilenameIsAbsentWhileDownloadIsRunning(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	reader, writer := io.Pipe()
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 6,
		Response:     testDownloadResponse(http.StatusOK, reader, 6, ""),
	}
	result := make(chan error, 1)
	go func() { result <- downloadResponseToStage(object, pb.New64(6)) }()
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if fi, err := os.Stat(stagingPath(finalPath)); err == nil && fi.Size() == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first bytes never appeared in staging file")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final filename appeared during transfer: %v", err)
	}
	if _, err := writer.Write([]byte("def")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(finalPath)
	if err != nil || string(final) != "abcdef" {
		t.Fatalf("final bytes incorrect: %q, %v", final, err)
	}
}

func TestShortResponseNeverPublishesFinalFile(t *testing.T) {
	dir := t.TempDir()
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 6,
		Response:     testDownloadResponse(http.StatusOK, io.NopCloser(strings.NewReader("abc")), 6, ""),
	}
	if err := downloadResponseToStage(object, pb.New64(6)); err == nil {
		t.Fatal("short response was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "archive.zip")); !os.IsNotExist(err) {
		t.Fatalf("short response appeared at final filename: %v", err)
	}
}

func TestBadChecksumPreservesExistingFinalFile(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(finalPath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 3,
		ExpectedMD5:  testMD5("good"),
		Response:     testDownloadResponse(http.StatusOK, io.NopCloser(strings.NewReader("bad")), 3, ""),
	}
	if err := downloadResponseToStage(object, pb.New64(3)); err == nil || !strings.Contains(err.Error(), "MD5 mismatch") {
		t.Fatalf("expected an MD5 error, got %v", err)
	}
	final, err := os.ReadFile(finalPath)
	if err != nil || string(final) != "old" {
		t.Fatalf("existing final file changed after failed verification: %q, %v", final, err)
	}
	part, err := os.ReadFile(stagingPath(finalPath))
	if err != nil || string(part) != "bad" {
		t.Fatalf("expected failed bytes in part file, got %q, %v", part, err)
	}
}

func TestResumeRejectsWrongContentRange(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(stagingPath(finalPath), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 6,
		Range:        3,
		Response:     testDownloadResponse(http.StatusPartialContent, io.NopCloser(strings.NewReader("def")), 3, "bytes 0-2/6"),
	}
	if err := downloadResponseToStage(object, pb.New64(6)); err == nil {
		t.Fatal("expected incorrect range to fail")
	}
	part, err := os.ReadFile(stagingPath(finalPath))
	if err != nil || string(part) != "abc" {
		t.Fatalf("part file changed despite incorrect range: %q, %v", part, err)
	}
}

func TestIgnoredRangeRestartsStagedDownload(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(stagingPath(finalPath), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	object := commonUtils.FileDownloadResponseObject{
		DownloadPath: dir,
		Filename:     "archive.zip",
		GUID:         "test-guid",
		ExpectedSize: 6,
		Range:        3,
		Response:     testDownloadResponse(http.StatusOK, io.NopCloser(strings.NewReader("abcdef")), 6, ""),
	}
	if err := downloadResponseToStage(object, pb.New64(6)); err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(finalPath)
	if err != nil || string(final) != "abcdef" {
		t.Fatalf("server ignored Range and file was not restarted: %q, %v", final, err)
	}
}

func TestSkipCompletedChecksProvidedChecksum(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(finalPath, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := validateLocalFileStat(dir, "archive.zip", 3, testMD5("good"), true); got.Skip {
		t.Fatal("same-size file with incorrect MD5 was skipped")
	}
	if got := validateLocalFileStat(dir, "archive.zip", 3, testMD5("bad"), true); !got.Skip {
		t.Fatal("matching file was not skipped")
	}
}

func TestSignedDownloadURLDetectionIgnoresQueryParameterCase(t *testing.T) {
	for _, downloadURL := range []string{
		"https://storage.googleapis.com/bucket/file?x-goog-signature=abc",
		"https://s3.amazonaws.com/bucket/file?x-amz-signature=abc",
	} {
		if !isSignedDownloadURL(downloadURL) {
			t.Fatalf("signed URL was not recognized: %s", downloadURL)
		}
	}
}

func TestDuplicateManifestDestinationsFailBeforeConcurrentDownload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "same.zip"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	objects := []ManifestObject{
		{ObjectID: "guid-one", Filename: "same.zip", Filesize: 3},
		{ObjectID: "guid-two", Filename: "./same.zip", Filesize: 3},
	}
	err := downloadFile(objects, dir, "original", false, true, "", 2, true, false)
	if err == nil || !strings.Contains(err.Error(), "failed to download") {
		t.Fatalf("duplicate destinations were not reported as a failure: %v", err)
	}
}
