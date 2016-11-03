package commands

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/github/git-lfs/config"
	"github.com/github/git-lfs/errors"
	"github.com/github/git-lfs/git"
	"github.com/github/git-lfs/lfs"
	"github.com/github/git-lfs/progress"
	"github.com/spf13/cobra"
)

var (
	filterSmudgeSkip = false
)

func clean(to io.Writer, reader io.Reader, fileName string) error {
	var cb progress.CopyCallback
	var file *os.File
	var fileSize int64
	if len(fileName) > 0 {
		stat, err := os.Stat(fileName)
		if err == nil && stat != nil {
			fileSize = stat.Size()

			localCb, localFile, err := lfs.CopyCallbackFile("clean", fileName, 1, 1)
			if err != nil {
				Error(err.Error())
			} else {
				cb = localCb
				file = localFile
			}
		}
	}

	cleaned, err := lfs.PointerClean(reader, fileName, fileSize, cb)
	if file != nil {
		file.Close()
	}

	if cleaned != nil {
		defer cleaned.Teardown()
	}

	if errors.IsCleanPointerError(err) {
		// TODO: report errors differently!
		// os.Stdout.Write(errors.GetContext(err, "bytes").([]byte))
		// return errors.GetContext(err, "bytes").([]byte), nil

		// TODO(taylor): what does this mean?
		return nil
	}

	if err != nil {
		Panic(err, "Error cleaning asset.")
	}

	tmpfile := cleaned.Filename
	mediafile, err := lfs.LocalMediaPath(cleaned.Oid)
	if err != nil {
		Panic(err, "Unable to get local media path.")
	}

	if stat, _ := os.Stat(mediafile); stat != nil {
		if stat.Size() != cleaned.Size && len(cleaned.Pointer.Extensions) == 0 {
			Exit("Files don't match:\n%s\n%s", mediafile, tmpfile)
		}
		Debug("%s exists", mediafile)
	} else {
		if err := os.Rename(tmpfile, mediafile); err != nil {
			Panic(err, "Unable to move %s to %s\n", tmpfile, mediafile)
		}

		Debug("Writing %s", mediafile)
	}

	_, err = cleaned.Pointer.Encode(to)
	return err
}

func smudge(to io.Writer, reader io.Reader, filename string) error {
	var buf bytes.Buffer
	reader = io.TeeReader(reader, &buf)

	ptr, err := lfs.DecodePointer(reader)
	if err != nil {
		if _, cerr := io.Copy(to, &buf); cerr != nil {
			Panic(cerr, "Error writing data to stdout:")
		}

		return err
	}

	lfs.LinkOrCopyFromReference(ptr.Oid, ptr.Size)

	cb, file, err := lfs.CopyCallbackFile("smudge", filename, 1, 1)
	if err != nil {
		Error(err.Error())
	}

	cfg := config.Config
	download := lfs.FilenamePassesIncludeExcludeFilter(filename, cfg.FetchIncludePaths(), cfg.FetchExcludePaths())

	if filterSmudgeSkip || cfg.Os.Bool("GIT_LFS_SKIP_SMUDGE", false) {
		download = false
	}

	sbuf := new(bytes.Buffer)
	err = ptr.Smudge(sbuf, filename, download, TransferManifest(), cb)
	if file != nil {
		file.Close()
	}

	if err != nil {
		// Download declined error is ok to skip if we weren't requesting download
		if !(errors.IsDownloadDeclinedError(err) && !download) {
			LoggedError(err, "Error downloading object: %s (%s)", filename, ptr.Oid)
			if !cfg.SkipDownloadErrors() {
				// TODO: What to do best here?
				os.Exit(2)
			}
		}

		_, err = ptr.Encode(to)
		return err
	}

	_, err = ptr.Encode(to)
	return err
}

func filterCommand(cmd *cobra.Command, args []string) {
	requireStdin("This command should be run by the Git filter process")
	lfs.InstallHooks(false)

	s := git.NewObjectScanner(os.Stdin, os.Stdout)
	w := git.NewPacketWriter(os.Stdout)

	if err := s.Init(); err != nil {
		ExitWithError(err)
	}
	if err := s.NegotiateCapabilities(); err != nil {
		ExitWithError(err)
	}

Scan:
	for s.Scan() {
		var err error

		switch req := s.Request(); req.Header["command"] {
		case "clean":
			err = clean(w, req.Payload, req.Header["pathname"])
		case "smudge":
			err = smudge(w, req.Payload, req.Header["pathname"])
		default:
			fmt.Errorf("Unknown command %s", cmd)
			break Scan
		}

		if err == nil {
			_, err = w.Write(nil)
		}

		var status string
		if err != nil {
			status = "error"
		} else {
			status = "success"
		}
		s.WriteStatus(status)
	}

	if err := s.Err(); err != nil {
		ExitWithError(err)
	}
}

func init() {
	RegisterCommand("filter", filterCommand, func(cmd *cobra.Command) {
		cmd.Flags().BoolVarP(&filterSmudgeSkip, "skip", "s", false, "")
	})
}
