// Copyright © 2018 Patrick Nuckolls <nuckollsp at gmail>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package build

import (
	"os"
	"io"
	"compress/gzip"
	"sync"
	"errors"
	"fmt"
	"encoding/json"
	"encoding/binary"
)

type appendedData struct {
	//TODO: Copy to temp file before opening a reader
	//TODO: CopyToTmp bool
	StartFilePtr int64 `json:"start_file_pointer"`
	ZippedSize   int64 `json:"zipped_block_size"`
}

const METADATA_VERSION string = "0.1"
type appendedMetadata struct {
	Version string
	Data    map[string]appendedData
}

type BinAppender struct {
	fileHandle *os.File
	metadata   appendedMetadata
	mux        *sync.Mutex
}

// Procedure:
//  MakeAppender
// Purpose:
//  To create a BinAppender
// Parameters:
//  The name of the file to append to: filename string
//  A function that wraps an io.Writer: writeWrapper
//    This can be used to pre-process data before insertion
//    Note: this function will be called every time a file/stream is added
// Produces:
//  A pointer to a new BinAppender: output *BinAppender
//  Any filesystem errors that occur in opening $filename: err error
// Preconditions:
//  The file at filename exists and can be written to
// Postconditions:
//  An appender is created that will append to filename through writeWrapper
//  The caller of this function closes the created BinAppender
func MakeAppender(filename string) (*BinAppender, error) {
	var err error
	output := BinAppender{}
	output.fileHandle, err = os.OpenFile(filename, os.O_RDWR, 0755)
	if err != nil {
		return nil, err
	}
	output.mux = &sync.Mutex{}
	output.metadata = appendedMetadata{}
	output.metadata.Data = make(map[string]appendedData)
	output.metadata.Version = METADATA_VERSION
	return &output, nil
}

// Procedure:
//  BinAppender.AppendStreamReader
// Purpose:
//  To append the entirety of a stream in an appended file block
// Parameters:
//  The parent *BinAppender: appender
//  The unique name of the stream: name string
//  The reader to pull data out of: source io.Reader
// Produces:
//  Side effects
//  Any errors in writing to the filesystem: err error
// Preconditions:
//  reader has a finite amount of data to read
//  $appender.Close() has not been called
// Postconditions:
//  All of the data that reader can read has been written to
//    appender's internal writer at the end of its file
//  appender's internal metadata has been updated to reflect the addition
//  Errors will be filesystem related
//
//  bash equivalent is executed:
//    $source | gzip >> $appender.file
//
//  $appender.file.ByteArray()[$appender.metadata[$name].StartFilePtr:$appender.metadata[$name].ZippedSize].gunzip() == $source.ByteArray[]
func (appender *BinAppender) AppendStreamReader(name string, source io.Reader) error {
	appender.mux.Lock()
	defer appender.mux.Unlock()

	startPtr, err := appender.fileHandle.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	gzWriter := gzip.NewWriter(appender.fileHandle)
	_, err = io.Copy(gzWriter, source)
	if err != nil {
		return err
	}
	gzWriter.Close()

	endPtr, err := appender.fileHandle.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	fileMetadata := appendedData{}
	fileMetadata.StartFilePtr = startPtr
	fileMetadata.ZippedSize = endPtr - startPtr

	appender.metadata.Data[name] = fileMetadata
	return nil
}

// Procedure:
//  BinAppender.AppendFile
// Purpose:
//  To gzip and pack a file onto the end of the BinAppender's file
// Parameters:
//  The calling BinAppender: appender BinAppender
//  The file to append: source string
// Produces:
//  Side effects:
//    filesystem
//    internal state changes
//  Any errors in writing to the filesystem: err error
// Preconditions:
//  $source exists and is readable in the file system
//  $source has not been appended already nor has $appender.AppendStreamReader(name, _)
//    been called with name == $source
//  $appender.Close() has not been called
// Postconditions:
//  A reader stream from $source will be passed to $appender.AppendStreamReader,
//    with the name parameter as source
func (appender *BinAppender) AppendFile(source string) error {
	sourceHandle, err := os.Open(source)
	if err != nil {
		return err
	}

	appender.mux.Lock()
	if _, exists := appender.metadata.Data[source]; exists {
		appender.mux.Unlock()
		return errors.New(fmt.Sprintf("file %s has already been added to appender", source))
	}
	appender.mux.Unlock()

	err = appender.AppendStreamReader(source, sourceHandle)
	if err != nil {
		return err
	}
	return sourceHandle.Close()
}

// Procedure:
//  BinAppender.Close()
// Purpose:
//  To finish writing to the file being appended to
//    and free it for use elsewhere
// Parameters:
//   The BinAppender being acted upon: appender
// Produces:
//   Any filesystem errors: err error
// Preconditions:
//  $appender.Close() has not been called
// Postconditions:
//  The json-encoded metadata about the appended files has been
//    written out to the end of file being appended to
//  The start of said json block is encoded in the final 8 bytes of
//    the file being appended to as a little endian int64
//  The internal file handle for the file being appended to has been closed
func (appender *BinAppender) Close() error {
	appender.mux.Lock()
	defer appender.mux.Unlock()

	jsonPtr, err := appender.fileHandle.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	jsonPtrBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(jsonPtrBytes, uint64(jsonPtr))

	jsonBytes, err := json.Marshal(appender.metadata)
	//Should not happen
	if err != nil {
		return err
	}
	_, err = appender.fileHandle.Write(jsonBytes)
	if err != nil {
		return err
	}
	_, err = appender.fileHandle.Write(jsonPtrBytes)
	if err != nil {
		return err
	}
	return appender.fileHandle.Close()
}
