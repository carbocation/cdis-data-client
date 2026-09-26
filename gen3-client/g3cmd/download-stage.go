package g3cmd

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/uc-cdis/gen3-client/gen3-client/commonUtils"
	pb "gopkg.in/cheggaaa/pb.v1"
)

// Keep unfinished bytes beside the destination so the final rename is atomic.
func stagingPath(finalPath string) string {
	return finalPath + ".part"
}

func verifyFileMD5(path string, expected string) error {
	want, err := hex.DecodeString(strings.TrimSpace(expected))
	if err != nil || len(want) != md5.Size {
		return fmt.Errorf("invalid expected MD5 checksum %q", expected)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hash.Sum(nil); !bytes.Equal(got, want) {
		return fmt.Errorf("MD5 mismatch: expected %x, got %x", want, got)
	}
	return nil
}

func downloadResponseToStage(fdr commonUtils.FileDownloadResponseObject, bar *pb.ProgressBar) error {
	defer fdr.Response.Body.Close()
	defer bar.Finish()
	if fdr.Range > 0 && fdr.Response.StatusCode == http.StatusOK {
		// A server may ignore Range and return the entire object. Replace the
		// staged bytes instead of appending a second copy of the file.
		fdr.Range = 0
		bar.Set64(0)
	}

	finalPath := filepath.Join(fdr.DownloadPath, fdr.Filename)
	partPath := stagingPath(finalPath)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return fmt.Errorf("create download directory for %s: %w", fdr.GUID, err)
	}

	fileFlags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	if fdr.Range > 0 {
		if fdr.Response.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("resume %s: server did not return partial content", fdr.GUID)
		}
		wantPrefix := fmt.Sprintf("bytes %d-", fdr.Range)
		if !strings.HasPrefix(fdr.Response.Header.Get("Content-Range"), wantPrefix) {
			return fmt.Errorf("resume %s: unexpected Content-Range", fdr.GUID)
		}
		fileFlags = os.O_APPEND | os.O_WRONLY
	} else if fdr.Response.StatusCode == http.StatusPartialContent {
		if !strings.HasPrefix(fdr.Response.Header.Get("Content-Range"), "bytes 0-") {
			return fmt.Errorf("download %s: unexpected partial response", fdr.GUID)
		}
	}

	file, err := os.OpenFile(partPath, fileFlags, 0666)
	if err != nil {
		return fmt.Errorf("open staged download for %s: %w", fdr.GUID, err)
	}
	if fdr.Range > 0 {
		fi, statErr := file.Stat()
		if statErr != nil || fi.Size() != fdr.Range {
			file.Close()
			return fmt.Errorf("resume %s: staged file changed during preparation", fdr.GUID)
		}
	}

	copied, copyErr := io.Copy(io.MultiWriter(file, bar), fdr.Response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", fdr.GUID, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged download for %s: %w", fdr.GUID, closeErr)
	}
	if fdr.Response.ContentLength >= 0 && copied != fdr.Response.ContentLength {
		return fmt.Errorf("download %s: received %d bytes, expected %d in this response", fdr.GUID, copied, fdr.Response.ContentLength)
	}

	fi, err := os.Stat(partPath)
	if err != nil {
		return fmt.Errorf("stat staged download for %s: %w", fdr.GUID, err)
	}
	if fdr.ExpectedSize > 0 && fi.Size() != fdr.ExpectedSize {
		return fmt.Errorf("download %s: staged file has %d bytes, expected %d", fdr.GUID, fi.Size(), fdr.ExpectedSize)
	}
	if fdr.ExpectedMD5 != "" {
		if err := verifyFileMD5(partPath, fdr.ExpectedMD5); err != nil {
			return fmt.Errorf("verify download %s: %w", fdr.GUID, err)
		}
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		return fmt.Errorf("publish download %s: %w", fdr.GUID, err)
	}
	return nil
}
