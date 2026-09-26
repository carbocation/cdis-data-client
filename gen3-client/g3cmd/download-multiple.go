package g3cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uc-cdis/gen3-client/gen3-client/commonUtils"
	"github.com/uc-cdis/gen3-client/gen3-client/logs"
	pb "gopkg.in/cheggaaa/pb.v1"

	"github.com/spf13/cobra"
)

// mockgen -destination=../mocks/mock_gen3interface.go -package=mocks . Gen3Interface

func AskGen3ForFileInfo(gen3Interface Gen3Interface, guid string, protocol string, downloadPath string, filenameFormat string, rename bool, renamedFiles *[]RenamedOrSkippedFileInfo) (string, int64) {
	var fileName string
	var fileSize int64

	// If the commons has the newer Shepherd API deployed, get the filename and file size from the Shepherd API.
	// Otherwise, fall back on Indexd and Fence.
	hasShepherd, err := gen3Interface.CheckForShepherdAPI(&profileConfig)
	if err != nil {
		log.Println("Error occurred when checking for Shepherd API: " + err.Error())
		log.Println("Falling back to Indexd...")
	}
	if hasShepherd {
		endPointPostfix := commonUtils.ShepherdEndpoint + "/objects/" + guid
		_, res, err := gen3Interface.GetResponse(&profileConfig, endPointPostfix, "GET", "", nil)
		if err != nil {
			log.Println("Error occurred when querying filename from Shepherd: " + err.Error())
			log.Println("Using GUID for filename instead.")
			if filenameFormat != "guid" {
				*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: "N/A", NewFilename: guid})
			}
			return guid, 0
		}

		decoded := struct {
			Record struct {
				FileName string `json:"file_name"`
				Size     int64  `json:"size"`
			}
		}{}
		err = json.NewDecoder(res.Body).Decode(&decoded)
		if err != nil {
			log.Println("Error occurred when reading response from Shepherd: " + err.Error())
			log.Println("Using GUID for filename instead.")
			if filenameFormat != "guid" {
				*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: "N/A", NewFilename: guid})
			}
			return guid, 0
		}
		defer res.Body.Close()

		fileName = decoded.Record.FileName
		fileSize = decoded.Record.Size

	} else {
		// Attempt to get the filename from Indexd
		endPointPostfix := commonUtils.IndexdIndexEndpoint + "/" + guid
		indexdMsg, err := gen3Interface.DoRequestWithSignedHeader(&profileConfig, endPointPostfix, "", nil)
		if err != nil {
			log.Println("Error occurred when querying filename from IndexD: " + err.Error())
			log.Println("Using GUID for filename instead.")
			if filenameFormat != "guid" {
				*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: "N/A", NewFilename: guid})
			}
			return guid, 0
		}

		if filenameFormat == "guid" {
			return guid, indexdMsg.Size
		}

		actualFilename := indexdMsg.FileName
		if actualFilename == "" {
			if len(indexdMsg.URLs) > 0 {
				// Indexd record has no file name but does have URLs, try to guess file name from URL
				var indexdURL = indexdMsg.URLs[0]
				if protocol != "" {
					for _, url := range indexdMsg.URLs {
						if strings.HasPrefix(url, protocol) {
							indexdURL = url
						}
					}
				}

				actualFilename = guessFilenameFromURL(indexdURL)
				if actualFilename == "" {
					log.Println("Error occurred when guessing filename for object " + guid)
					log.Println("Using GUID for filename instead.")
					*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: "N/A", NewFilename: guid})
					return guid, indexdMsg.Size
				}
			} else {
				// Neither file name nor URLs exist in the Indexd record
				// Indexd record is busted for that file, just return as we are renaming the file for now
				// The download logic will handle the errors
				log.Println("Neither file name nor URLs exist in the Indexd record of " + guid)
				log.Println("The attempt of downloading file is likely to fail! Check Indexd record!")
				log.Println("Using GUID for filename instead.")
				*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: "N/A", NewFilename: guid})
				return guid, indexdMsg.Size
			}
		}

		fileName = actualFilename
		fileSize = indexdMsg.Size
	}

	if filenameFormat == "original" {
		if !rename { // no renaming in original mode
			return fileName, fileSize
		}
		newFilename := processOriginalFilename(downloadPath, fileName)
		if fileName != newFilename {
			*renamedFiles = append(*renamedFiles, RenamedOrSkippedFileInfo{GUID: guid, OldFilename: fileName, NewFilename: newFilename})
		}
		return newFilename, fileSize
	}
	// filenameFormat == "combined"
	combinedFilename := guid + "_" + fileName
	return combinedFilename, fileSize
}

func guessFilenameFromURL(URL string) string {
	splittedURLWithFilename := strings.Split(URL, "/")
	actualFilename := splittedURLWithFilename[len(splittedURLWithFilename)-1]
	return actualFilename
}

func processOriginalFilename(downloadPath string, actualFilename string) string {
	_, err := os.Stat(downloadPath + actualFilename)
	if os.IsNotExist(err) {
		return actualFilename
	}
	extension := filepath.Ext(actualFilename)
	filename := strings.TrimSuffix(actualFilename, extension)
	counter := 2
	for {
		newFilename := filename + "_" + strconv.Itoa(counter) + extension
		_, err := os.Stat(downloadPath + newFilename)
		if os.IsNotExist(err) {
			return newFilename
		}
		counter++
	}
}

func validateFilenameFormat(downloadPath string, filenameFormat string, rename bool, noPrompt bool) {
	if filenameFormat != "original" && filenameFormat != "guid" && filenameFormat != "combined" {
		log.Fatalln("Invalid option found! Option \"filename-format\" can either be \"original\", \"guid\" or \"combined\" only")
	}
	if filenameFormat == "guid" || filenameFormat == "combined" {
		fmt.Printf("WARNING: in \"guid\" or \"combined\" mode, duplicated files under \"%s\" will be overwritten\n", downloadPath)
		if !noPrompt && !commonUtils.AskForConfirmation("Proceed?") {
			log.Println("Aborted by user")
			os.Exit(0)
		}
	} else if !rename {
		fmt.Printf("WARNING: flag \"rename\" was set to false in \"original\" mode, duplicated files under \"%s\" will be overwritten\n", downloadPath)
		if !noPrompt && !commonUtils.AskForConfirmation("Proceed?") {
			log.Println("Aborted by user")
			os.Exit(0)
		}
	} else {
		fmt.Printf("NOTICE: flag \"rename\" was set to true in \"original\" mode, duplicated files under \"%s\" will be renamed by appending a counter value to the original filenames\n", downloadPath)
	}
}

func validateLocalFileStat(downloadPath string, filename string, filesize int64, md5sum string, skipCompleted bool) commonUtils.FileDownloadResponseObject {
	result := commonUtils.FileDownloadResponseObject{
		DownloadPath: downloadPath,
		Filename:     filename,
		ExpectedSize: filesize,
		ExpectedMD5:  md5sum,
	}
	if !skipCompleted {
		return result
	}

	finalPath := filepath.Join(downloadPath, filename)
	if fi, err := os.Stat(finalPath); err == nil && filesize > 0 && fi.Size() == filesize {
		if md5sum == "" || verifyFileMD5(finalPath, md5sum) == nil {
			result.Skip = true
			return result
		}
	}

	// Only staged bytes are eligible for resumption. An older client may have
	// left an incomplete file at the final path; replace it only after a new
	// staged download has passed validation.
	if fi, err := os.Stat(stagingPath(finalPath)); err == nil && fi.Size() > 0 && (filesize == 0 || fi.Size() < filesize) {
		result.Range = fi.Size()
	}
	return result
}

func batchDownload(g3 Gen3Interface, batchFDRSlice []commonUtils.FileDownloadResponseObject, protocolText string, workers int, errCh chan error) int {
	type downloadJob struct {
		object commonUtils.FileDownloadResponseObject
		bar    *pb.ProgressBar
	}
	bars := make([]*pb.ProgressBar, 0)
	jobs := make([]downloadJob, 0)
	for _, fdrObject := range batchFDRSlice {
		err := GetDownloadResponse(g3, &fdrObject, protocolText)
		if err != nil {
			errCh <- err
			continue
		}
		if fdrObject.Range > 0 && fdrObject.Response.StatusCode == http.StatusOK {
			// The server ignored Range. Start this transfer from byte zero.
			fdrObject.Range = 0
		}
		progressSize := fdrObject.ExpectedSize
		if progressSize <= 0 {
			progressSize = fdrObject.Response.ContentLength + fdrObject.Range
		}
		if progressSize < 0 {
			progressSize = 0
		}
		bar := pb.New64(progressSize).SetUnits(pb.U_BYTES).SetRefreshRate(time.Millisecond * 10).Prefix(fdrObject.Filename + " ")
		bar.Set64(fdrObject.Range)
		bars = append(bars, bar)
		jobs = append(jobs, downloadJob{object: fdrObject, bar: bar})
	}
	if len(jobs) == 0 {
		return 0
	}

	jobCh := make(chan downloadJob, len(jobs))
	pool, err := pb.StartPool(bars...)
	if err != nil {
		for _, job := range jobs {
			job.object.Response.Body.Close()
		}
		errCh <- errors.New("Error occurred during initializing progress bars: " + err.Error())
		return 0
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(jobs))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				results <- downloadResponseToStage(job.object, job.bar)
			}
		}()
	}

	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)

	wg.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result != nil {
			errCh <- result
		} else {
			succeeded++
		}
	}
	err = pool.Stop()
	if err != nil {
		errCh <- errors.New("Error occurred during stopping progress bars: " + err.Error())
		return succeeded
	}
	return succeeded
}

func downloadFile(objects []ManifestObject, downloadPath string, filenameFormat string, rename bool, noPrompt bool, protocol string, numParallel int, skipCompleted bool, debug bool) error {
	if numParallel < 1 {
		log.Fatalln("Invalid value for option \"numparallel\": must be a positive integer! Please check your input.")
	}

	downloadPath = commonUtils.ParseRootPath(downloadPath)
	if !strings.HasSuffix(downloadPath, "/") {
		downloadPath += "/"
	}
	filenameFormat = strings.ToLower(strings.TrimSpace(filenameFormat))
	if (filenameFormat == "guid" || filenameFormat == "combined") && rename {
		fmt.Println("NOTICE: flag \"rename\" only works if flag \"filename-format\" is \"original\"")
		rename = false
	}
	validateFilenameFormat(downloadPath, filenameFormat, rename, noPrompt)

	protocolText := ""
	if protocol != "" {
		protocolText = "?protocol=" + protocol
	}

	err := os.MkdirAll(downloadPath, 0766)
	if err != nil {
		log.Fatalln("Cannot create folder \"" + downloadPath + "\"")
	}

	renamedFiles := make([]RenamedOrSkippedFileInfo, 0)
	skippedFiles := make([]RenamedOrSkippedFileInfo, 0)
	fdrObjects := make([]commonUtils.FileDownloadResponseObject, 0)
	preparationErrors := make([]error, 0)
	seenFilenames := make(map[string]string)

	gen3Interface := NewGen3Interface()

	log.Printf("Total number of objects in manifest: %d", len(objects))
	log.Println("Preparing file info for each file, please wait...")
	fileInfoBar := pb.New(len(objects)).SetRefreshRate(time.Millisecond * 10)
	if !debug {
		fileInfoBar.Start()
	}
	for _, obj := range objects {
		if obj.ObjectID == "" {
			log.Println("Found empty object_id (GUID), skipping this entry")
			continue
		}
		var fdrObject commonUtils.FileDownloadResponseObject
		filename := obj.Filename
		filesize := obj.Filesize
		md5sum := obj.MD5Sum
		if md5sum == "" {
			md5sum = obj.FileMD5Sum
		}
		// only queries Gen3 services if any of these 2 values doesn't exists in manifest
		if filename == "" || filesize == 0 {
			filename, filesize = AskGen3ForFileInfo(gen3Interface, obj.ObjectID, protocol, downloadPath, filenameFormat, rename, &renamedFiles)
		}
		destination := filepath.Clean(filepath.Join(downloadPath, filename))
		if previousGUID, exists := seenFilenames[destination]; exists {
			preparationErrors = append(preparationErrors, fmt.Errorf("duplicate output filename %q for GUIDs %s and %s; use unique file_name values in the manifest", filename, previousGUID, obj.ObjectID))
			if !debug {
				fileInfoBar.Increment()
			}
			continue
		}
		seenFilenames[destination] = obj.ObjectID
		fdrObject = commonUtils.FileDownloadResponseObject{DownloadPath: downloadPath, Filename: filename, ExpectedSize: filesize, ExpectedMD5: md5sum}
		if !rename {
			fdrObject = validateLocalFileStat(downloadPath, filename, filesize, md5sum, skipCompleted)
		}
		fdrObject.GUID = obj.ObjectID
		fdrObject.Debug = debug
		fdrObjects = append(fdrObjects, fdrObject)
		if !debug {
			fileInfoBar.Increment()
		}
	}
	if !debug {
		fileInfoBar.Finish()
	}
	log.Println("File info prepared successfully")

	totalCompeleted := 0
	workers := getNumberOfWorkers(numParallel, len(fdrObjects))
	errCh := make(chan error, len(objects)+numParallel+1)
	for _, preparationError := range preparationErrors {
		errCh <- preparationError
	}
	batchFDRSlice := make([]commonUtils.FileDownloadResponseObject, 0)
	for _, fdrObject := range fdrObjects {
		if fdrObject.Skip {
			log.Printf("File \"%s\" (GUID: %s) has been skipped because there is a complete local copy\n", fdrObject.Filename, fdrObject.GUID)
			skippedFiles = append(skippedFiles, RenamedOrSkippedFileInfo{GUID: fdrObject.GUID, OldFilename: fdrObject.Filename})
			continue
		}

		if len(batchFDRSlice) < workers {
			batchFDRSlice = append(batchFDRSlice, fdrObject)
		} else {
			totalCompeleted += batchDownload(gen3Interface, batchFDRSlice, protocolText, workers, errCh)
			batchFDRSlice = make([]commonUtils.FileDownloadResponseObject, 0)
			batchFDRSlice = append(batchFDRSlice, fdrObject)
		}
	}
	totalCompeleted += batchDownload(gen3Interface, batchFDRSlice, protocolText, workers, errCh) // download remainders

	log.Printf("%d files downloaded.\n", totalCompeleted)

	if len(renamedFiles) > 0 {
		log.Printf("%d files have been renamed as the following:\n", len(renamedFiles))
		for _, rfi := range renamedFiles {
			log.Printf("File \"%s\" (GUID: %s) has been renamed as: %s\n", rfi.OldFilename, rfi.GUID, rfi.NewFilename)
		}
	}
	if len(skippedFiles) > 0 {
		log.Printf("%d files have been skipped\n", len(skippedFiles))
	}
	if len(errCh) > 0 {
		errorCount := len(errCh)
		close(errCh)
		log.Printf("%d files have encountered an error during downloading, detailed error messages are:\n", errorCount)
		for err := range errCh {
			log.Println(err.Error())
		}
		return fmt.Errorf("%d files failed to download", errorCount)
	}
	return nil
}

func init() {
	var manifestPath string
	var downloadPath string
	var filenameFormat string
	var rename bool
	var noPrompt bool
	var protocol string
	var numParallel int
	var skipCompleted bool

	var downloadMultipleCmd = &cobra.Command{
		Use:     "download-multiple",
		Short:   "Download multiple of files from a specified manifest",
		Long:    `Get presigned URLs for files in a manifest. Each file is staged as .part and published under its final name only after size and optional MD5 verification. Include md5sum or file_md5sum in manifest entries to enable checksum verification.`,
		Example: `./gen3-client download-multiple --profile=<profile-name> --manifest=<path-to-manifest/manifest.json> --download-path=<path-to-file-dir/>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// don't initialize transmission logs for non-uploading related commands
			logs.SetToBoth()
			profileConfig = conf.ParseConfig(profile)

			manifestPath, _ = commonUtils.GetAbsolutePath(manifestPath)
			manifestFile, err := os.Open(manifestPath)
			if err != nil {
				log.Fatalf("Failed to open manifest file %s, %v\n", manifestPath, err)
			}
			defer manifestFile.Close()
			manifestFileStat, err := manifestFile.Stat()
			if err != nil {
				log.Fatalf("Failed to get manifest file stats %s, %v\n", manifestPath, err)
			}
			log.Println("Reading manifest...")
			manifestFileSize := manifestFileStat.Size()
			manifestFileBar := pb.New(int(manifestFileSize)).SetUnits(pb.U_BYTES).SetRefreshRate(time.Millisecond * 10)
			manifestFileBar.Start()

			manifestFileReader := manifestFileBar.NewProxyReader(manifestFile)

			manifestBytes, err := ioutil.ReadAll(manifestFileReader)
			manifestFileBar.Finish()

			if err != nil {
				log.Printf("Failed reading manifest %s, %v\n", manifestPath, err)
				log.Fatalln("A valid manifest can be acquired by using the \"Download Manifest\" button in Data Explorer from a data common's portal")
			}
			var objects []ManifestObject
			err = json.Unmarshal(manifestBytes, &objects)
			if err != nil {
				log.Fatalf("Error has occurred during unmarshalling manifest object: %v\n", err)
			}

			downloadErr := downloadFile(objects, downloadPath, filenameFormat, rename, noPrompt, protocol, numParallel, skipCompleted, false)
			closeErr := logs.CloseMessageLog()
			if downloadErr != nil {
				return downloadErr
			}
			return closeErr
		},
	}

	downloadMultipleCmd.Flags().StringVar(&profile, "profile", "", "Specify profile to use")
	downloadMultipleCmd.MarkFlagRequired("profile") //nolint:errcheck
	downloadMultipleCmd.Flags().StringVar(&manifestPath, "manifest", "", "The manifest file to read from. A valid manifest can be acquired by using the \"Download Manifest\" button in Data Explorer from a data common's portal")
	downloadMultipleCmd.MarkFlagRequired("manifest") //nolint:errcheck
	downloadMultipleCmd.Flags().StringVar(&downloadPath, "download-path", ".", "The directory in which to store the downloaded files")
	downloadMultipleCmd.Flags().StringVar(&filenameFormat, "filename-format", "original", "The format of filename to be used, including \"original\", \"guid\" and \"combined\"")
	downloadMultipleCmd.Flags().BoolVar(&rename, "rename", false, "Only useful when \"--filename-format=original\", will rename file by appending a counter value to its filename if set to true, otherwise the same filename will be used")
	downloadMultipleCmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "If set to true, will not display user prompt message for confirmation")
	downloadMultipleCmd.Flags().StringVar(&protocol, "protocol", "", "Specify the preferred protocol with --protocol=s3")
	downloadMultipleCmd.Flags().IntVar(&numParallel, "numparallel", 1, "Number of downloads to run in parallel")
	downloadMultipleCmd.Flags().BoolVar(&skipCompleted, "skip-completed", false, "Skip finished files with matching size (and MD5 when provided); resume partial .part files")
	RootCmd.AddCommand(downloadMultipleCmd)
}
