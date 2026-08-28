// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func archiveTree(root, output string, timestamp time.Time) (returnErr error) {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); returnErr == nil {
			returnErr = closeErr
		}
		if returnErr != nil {
			_ = os.Remove(output)
		}
	}()
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = timestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	parent := filepath.Dir(root)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			var readlinkErr error
			link, readlinkErr = os.Readlink(path)
			if readlinkErr != nil {
				return readlinkErr
			}
		}
		header, headerErr := tar.FileInfoHeader(info, link)
		if headerErr != nil {
			return headerErr
		}
		relative, relativeErr := filepath.Rel(parent, path)
		if relativeErr != nil {
			return relativeErr
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
			header.Mode = 0o755
		} else if info.Mode().IsRegular() {
			header.Mode = normalizedFileMode(info.Mode())
		}
		header.ModTime = timestamp
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "root", "root"
		if headerErr := tarWriter.WriteHeader(header); headerErr != nil {
			return headerErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		return errors.Join(copyErr, closeErr)
	})
	if err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func normalizedFileMode(mode os.FileMode) int64 {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}
