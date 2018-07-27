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
	"io/ioutil"
	"compress/gzip"
	"github.com/pkg/errors"
	"encoding/json"
	"encoding/binary"

	"sync/atomic"
	"github.com/yourfin/transcodebot/common"
)

// Type:
//  BinAppendExtractor
// Purpose:
//  To provide an concurrent interface for reading
//  files tacked on the end of a binary
type BinAppendExtractor struct {
	filename string
	metadata appendedMetadata
}

// Procedure:
//  MakeAppendReader
// Purpose:
//  To create a BinAppendExtractor for a given file
// Parameters:
//  The file to open: filename string
// Produces:
//  A pointer to a BinAppendExtractor: reader *BinAppendExtractor
//  Any errors that occur: err error
// Preconditions:
//  filename exists on the filesystem
//  filename was appended to with by a BinAppender
// Postconditions:
//  reader is initialized to grab the files from files
func MakeAppendExtractor(filename string) (reader *BinAppendExtractor, err error) {
	reader = &BinAppendExtractor{}
	reader.filename = filename
	fileHandle, err := os.Open(filename)
	if err != nil {
		_ = fileHandle.Close()
		return nil, errors.Wrapf(err, "Open file \"%s\"", filename)
	}

	//Read in metadata pointer
	_, err = fileHandle.Seek(-8, io.SeekEnd)
	if err != nil {
		_ = fileHandle.Close()
		return nil, errors.Wrapf(err, "Seek in file \"%s\" to location %d before end", filename, 8)
	}

	//Read in pointer to metadata
	metadataPtrBytes := make([]byte, 8)
	count, err := fileHandle.Read(metadataPtrBytes)
	if err != nil {
		_ = fileHandle.Close()
		return nil, errors.Wrap(err, "Read metadata pointer")
	}
	if count != 8 {
		_ = fileHandle.Close()
		return nil, errors.Errorf("Read %d bytes instead of 8 for metadata pointer location", count)
	}
	metadataPtr := int64(binary.LittleEndian.Uint64(metadataPtrBytes))

	//Read in metadata
	_, err = fileHandle.Seek(metadataPtr, io.SeekStart)
	if err != nil {
		_ = fileHandle.Close()
		return nil, errors.Wrapf(err, "Seek in file \"%s\" to location %d before end (metadata location)", filename, 8)
	}

	err = json.NewDecoder(fileHandle).Decode(&(reader.metadata))
	if err != nil {
		_ = fileHandle.Close()
		return nil, errors.Wrap(err, "Json Decode")
	}
	if reader.metadata.Version != METADATA_VERSION {
		_ = fileHandle.Close()
		return nil, errors.Errorf(
			"BinAppender reader version \"%s\" does not match version \"%s\" on file \"%s\" ",
			METADATA_VERSION,
			reader.metadata.Version,
			filename,
		)
	}

	err = fileHandle.Close()
	if err != nil {
		return nil, errors.Wrapf(err, "Closing %s", filename)
	}

	return reader, nil
}

// Procedure:
//  *BinAppendExtractor.GetDataReader
// Purpose:
//  To return provide a reader matching a data name appended to
//  reader's file
// Parameters:
//  The *BinAppendExtractor being called: reader
//  The name of the data given: dataName string
// Produces:
//  A reader for the data by the same name: reader *BinAppendReader
//  Errors produced: err error
// Preconditions:
//  dataName is a name that exists and has data associated with it
// Postconditions:
//  The returned reader will decompress and read back the data with $dataName
//  err will only exist:
//   - When any filesystem errors in opening and seeking in the underlying binary
//   - When $dataName does not match any names in the file
func (extractor *BinAppendExtractor) GetReader(dataName string) (reader *BinAppendReader, err error) {
	if _, exists := extractor.metadata.Data[dataName]; !exists {
		return nil, errors.Errorf("Could not find name %s", dataName)
	}
	reader = &BinAppendReader{}
	reader.fileHandle, err = os.Open(extractor.filename)
	if err != nil {
		return nil, errors.Wrap(err, "opening reader filehandle")
	}
	_, err = reader.fileHandle.Seek(extractor.metadata.Data[dataName].StartFilePtr, io.SeekStart)
	if err != nil {
		return nil, errors.Wrap(err, "seeking in file")
	}
	limitReader := io.LimitReader(reader.fileHandle, extractor.metadata.Data[dataName].ZippedSize)
	reader.gzReader, err = gzip.NewReader(limitReader)
	if err != nil {
		return nil, errors.Wrap(err, "creating gzip reader")
	}
	return reader, nil
}

// Procedure:
//  *BinAppendExtractor.ByteArray
// Purpose:
//  To read all of a block of appended data to a byte array
// Parameters:
//  The parent *BinAppendExtractor: extractor
//  The name of the data to retrieve: dataName string
// Produces:
//  The data named dataName: data []byte
//  Any errors raised:       err error
// Preconditions:
//  The extractor is has some data named $dataName
// Postconditions:
//  data contains all the data named $dataName in the extractor
//  err will be a file system error, gzip error, or due to $dataName not existing
func (extractor *BinAppendExtractor) ByteArray(dataName string) ([]byte, error) {
	reader, err := extractor.GetReader(dataName)
	defer func() { _ = reader.Close() }()
	if err != nil {
		return nil, errors.Wrap(err, "Generating reader for reading ByteArray")
	}

	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, errors.Wrap(err, "Reading all data in")
	}

	err = reader.Close()
	if err != nil {
		return nil, err
	}
	return data, nil
}

func ReadStuff(start, size int64, path string) error {
	readFile, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = readFile.Seek(start, io.SeekStart)
	if err != nil {
		return err
	}

	gzReader, err := gzip.NewReader(io.LimitReader(readFile, size))

	if err != nil {
		return err
	}

	num, err := io.Copy(os.Stdout, gzReader)
	common.Println()
	common.Println(num)
	return err
}

// Type:
//  writeCounter
// Purpose:
//  To count the number of bytes written to an io.writer
//
//  As it turns out, gzip.Writer().Write() returns the
//  number of bytes in, not out. This shim goes between
//  gzip and the filesystem so we can figure out how many
//  bytes were actually written out to pasture
type writeCounter struct {
	Counter int64
	child io.Writer
}
func (w *writeCounter) Write(p []byte) (n int, err error) {
	n, err = w.child.Write(p)
	atomic.AddInt64(&w.Counter, int64(n))
	return
}
func newWriteCounter(child io.Writer) (*writeCounter) {
	return &writeCounter{0, child}
}

// Type:
//  BinAppendReader
// Purpose:
//  Generated by BinAppendGrabber to provide an interface to read
//  appended data
// Explicitly implements:
//  io.Reader
//  io.Closer
//  io.ReadCloser
// PostConditions:
//  Must be closed so the underlying *os.File can be freed
type BinAppendReader struct {
	//The name of the data as inputed by the BinAppender
	Name string

	// gzReader wraps the limitReader which wraps the underlying fileHandle

	fileHandle *os.File
	gzReader *gzip.Reader
}

// Procedure:
//  *BinAppendReader.Read
// Purpose:
//  To read bytes out of a BinAppendReader
// Parameters:
//  The *BinAppendReader being acted upon: reader
//  The byte array to place read bytes into: p []byte
// Produces:
//  The number of bytes read: n int
//  Any errors in reading: err error
// Preconditions:
//  No additional
// Postconditions:
//  See the documentation for io.Reader
func (reader *BinAppendReader) Read(p []byte) (n int, err error) {
	return reader.gzReader.Read(p)
}

// Procedure:
//  *BinAppendReader.Close
// Purpose:
//  To clean up the file handle for a BinAppendReader
// Parameters:
//  The *BinAppendReader being acted upon: reader
//  None
// Produces:
//  Any errors in closing the underlying filesystem handle: err error
// Preconditions:
//  No additonal
// Postconditions:
//  All resources for the binAppendReader have been closed
func (reader *BinAppendReader) Close() error {
	return reader.fileHandle.Close()
}
