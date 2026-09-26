package g3cmd

import (
	"github.com/spf13/cobra"
	"github.com/uc-cdis/gen3-client/gen3-client/logs"
)

func newDownloadSingleCommand(use string, debug bool) *cobra.Command {
	var guid string
	var downloadPath string
	var protocol string
	var filenameFormat string
	var rename bool
	var noPrompt bool
	var skipCompleted bool

	short := "Download a single file from a GUID"
	long := "Gets a presigned URL for a file from a GUID and downloads it to a .part file. The final filename appears after the download completes and its size is checked."
	if debug {
		short = "Download one file and show safe routing diagnostics"
		long = "Downloads one file through a .part file and reports the signer, requested protocol, URL host, and HTTP status without logging the signed URL."
	}

	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: "./gen3-client " + use + " --profile=<profile-name> --guid=206dfaa6-bcf1-4bc9-b2d0-77179f0f48fc",
		RunE: func(cmd *cobra.Command, args []string) error {
			// don't initialize transmission logs for non-uploading related commands
			logs.SetToBoth()
			profileConfig = conf.ParseConfig(profile)

			objects := []ManifestObject{{ObjectID: guid}}
			downloadErr := downloadFile(objects, downloadPath, filenameFormat, rename, noPrompt, protocol, 1, skipCompleted, debug)
			closeErr := logs.CloseMessageLog()
			if downloadErr != nil {
				return downloadErr
			}
			return closeErr
		},
	}

	cmd.Flags().StringVar(&profile, "profile", "", "Specify profile to use")
	cmd.MarkFlagRequired("profile") //nolint:errcheck
	cmd.Flags().StringVar(&guid, "guid", "", "Specify the guid for the data you would like to work with")
	cmd.MarkFlagRequired("guid") //nolint:errcheck
	cmd.Flags().StringVar(&downloadPath, "download-path", ".", "The directory in which to store the downloaded files")
	cmd.Flags().StringVar(&filenameFormat, "filename-format", "original", "The format of filename to be used, including \"original\", \"guid\" and \"combined\"")
	cmd.Flags().BoolVar(&rename, "rename", false, "Only useful when \"--filename-format=original\", will rename file by appending a counter value to its filename if set to true, otherwise the same filename will be used")
	cmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "If set to true, will not display user prompt message for confirmation")
	cmd.Flags().StringVar(&protocol, "protocol", "", "Specify the preferred protocol with --protocol=s3")
	cmd.Flags().BoolVar(&skipCompleted, "skip-completed", false, "Skip finished files with matching size; resume partial .part files")
	return cmd
}

func init() {
	RootCmd.AddCommand(newDownloadSingleCommand("download-single", false))
	RootCmd.AddCommand(newDownloadSingleCommand("single-download-debug", true))
}
