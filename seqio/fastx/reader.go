package fastx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"github.com/brentp/xopen"
	"github.com/shenwei356/bio/seq"
)

// RecordChunk  is
type RecordChunk struct {
	ID   uint64
	Data []*Record
	Err  error
}

// Reader asynchronously parse FASTX file with buffer,
// each buffer contain a chunk of multiple fastx records (RecordChunk).
// Reader also support safe cancellation.
type Reader struct {
	t  *seq.Alphabet // alphabet
	fh *xopen.Reader // file handle, xopen is such a wonderful package

	BufferSize int              // buffer size
	ChunkSize  int              // chunk size
	Ch         chan RecordChunk // chanel for output of records chunks
	IDRegexp   *regexp.Regexp   // regexp ofr parsing record id
	IsFastq    bool

	firstseq  bool          // for guess alphabet by the first seq
	done      chan struct{} // for cancellation
	finished  bool          // for cancellation
	cancelled bool          // for cancellation
}

// regexp for checking idRegexp string.
// The regular expression must contain "(" and ")" to capture matched ID
var reCheckIDregexpStr = regexp.MustCompile(`\(.+\)`)

// DefaultIDRegexp is the default ID parsing regular expression
var DefaultIDRegexp = `^([^\s]+)\s?`

// NewReader is constructor of FASTX Reader.
//
// Parameters:
//
//        t            sequence alphabet
//                     if nil is given, it will guess alphabet by the first record
//        file         file name, "-" for stdin
//        bufferSize   buffer size
//        chunkSize    chunk size
//        idRegexp     id parsing regular expression string, must contains "(" and ")" to capture matched ID
//                     "" for default value: `^([^\s]+)\s?`
//                     if record head does not match the idRegxp, whole name will be the id
//
func NewReader(t *seq.Alphabet, file string, bufferSize int, chunkSize int, idRegexp string) (*Reader, error) {
	if bufferSize <= 0 {
		bufferSize = 0
	}
	if chunkSize < 1 {
		chunkSize = 1
	}

	var r *regexp.Regexp
	if idRegexp == "" {
		r = regexp.MustCompile(DefaultIDRegexp)
	} else {
		if !reCheckIDregexpStr.MatchString(idRegexp) {
			return nil, fmt.Errorf(`bio.seqio.fastx: regular expression must contain "(" and ")" to capture matched ID. default: %s`, DefaultIDRegexp)
		}
		var err error
		r, err = regexp.Compile(idRegexp)
		if err != nil {
			return nil, fmt.Errorf("bio.seqio.fastx: fail to compile regexp: %s", idRegexp)
		}
	}

	fh, err := xopen.Ropen(file)
	if err != nil {
		return nil, fmt.Errorf("bio.seqio.fastx: %s", err)
	}

	fastxReader := &Reader{
		t:          t,
		fh:         fh,
		BufferSize: bufferSize,
		ChunkSize:  chunkSize,
		Ch:         make(chan RecordChunk, bufferSize),
		IDRegexp:   r,
		IsFastq:    false,
		firstseq:   true,
		done:       make(chan struct{}),
		finished:   false,
		cancelled:  false,
	}

	fastxReader.read()

	return fastxReader, nil
}

// ErrCanceled means that the reading process is canceled
var ErrCanceled = errors.New("bio.seqio.fastx: reading canceled")

// ErrBadFASTQFormat means bad fastq format
var ErrBadFASTQFormat = errors.New("bio.seqio.fastx: bad fastq format")

func (fastxReader *Reader) read() {
	go func() {
		reader := bufio.NewReader(fastxReader.fh)
		// buffer := bytes.Buffer{}
		buffer := bytes.NewBuffer(make([]byte, 0, defaultBytesBufferSize))
		// var buffer bbuffer.Buffer
		var i int
		var id uint64
		checkSeqType := true
		var hasSeq, isReadQual bool
		var lastName, lastSeq, thisName []byte
		chunkData := make([]*Record, fastxReader.ChunkSize)

		for {
			select {
			case <-fastxReader.done:
				if !fastxReader.finished {
					fastxReader.Ch <- RecordChunk{id, chunkData[0:i], ErrCanceled}

					fastxReader.finished = true
					fastxReader.fh.Close()
					close(fastxReader.Ch)
					return
				}
			default:

			}

			line, err := reader.ReadBytes('\n')

			if checkSeqType {
				if len(line) == 0 {
					fastxReader.Ch <- RecordChunk{id, chunkData[0:i], nil}
					fastxReader.fh.Close()
					close(fastxReader.Ch)
					return
				}
				if line[0] == '@' {
					fastxReader.IsFastq = true
				}
				checkSeqType = false
			}

			// FASTQ
			if fastxReader.IsFastq {
				if err != nil { // end of file
					fastxReader.finished = true
					fastxReader.fh.Close()

					buffer.Write(dropCR(line))

					// sequence := []byte(string(lastSeq))

					sequence := make([]byte, len(lastSeq))
					copy(sequence, lastSeq)

					// sequence := lastSeq

					// qual := []byte(string(buffer.Bytes()))

					qual := make([]byte, buffer.Len())
					copy(qual, buffer.Bytes())

					// qual := buffer.Bytes()

					buffer.Reset()

					if !(len(sequence) == 0 && len(lastName) == 0) {
						if fastxReader.firstseq {
							if fastxReader.t == nil {
								fastxReader.t = seq.DNAredundant
							}
							fastxReader.firstseq = false
						}
						var fastxRecord *Record
						fastxRecord, err = NewRecordWithQual(fastxReader.t, fastxReader.parseHeadID(lastName), lastName, sequence, qual)
						if err != nil {
							fastxReader.Ch <- RecordChunk{id, chunkData[0:i], err}
							fastxReader.fh.Close()
							close(fastxReader.Ch)
							return
						}

						chunkData[i] = fastxRecord
						i++
					}
					fastxReader.Ch <- RecordChunk{id, chunkData[0:i], nil}
					close(fastxReader.Ch)

					return
				}
				if line[0] == '@' {
					if isReadQual && len(lastSeq) > buffer.Len() { // still quality
						buffer.Write(dropCR(line[0 : len(line)-1]))
					} else {
						hasSeq = true
						isReadQual = false
						thisName = dropCR(line[1 : len(line)-1])

						if lastName != nil { // no-first seq head
							// sequence := []byte(string(lastSeq))

							sequence := make([]byte, len(lastSeq))
							copy(sequence, lastSeq)

							// sequence := lastSeq

							// qual := []byte(string(buffer.Bytes()))

							qual := make([]byte, buffer.Len())
							copy(qual, buffer.Bytes())

							// qual := buffer.Bytes()

							buffer.Reset()

							if !(len(sequence) == 0 && len(lastName) == 0) {
								if fastxReader.firstseq {
									if fastxReader.t == nil {
										fastxReader.t = seq.DNAredundant
									}
									fastxReader.firstseq = false
								}

								var fastxRecord *Record
								fastxRecord, err = NewRecordWithQual(fastxReader.t, fastxReader.parseHeadID(lastName), lastName, sequence, qual)
								if err != nil {
									fastxReader.Ch <- RecordChunk{id, chunkData[0:i], err}
									fastxReader.fh.Close()
									close(fastxReader.Ch)
									return
								}

								chunkData[i] = fastxRecord
								i++
							}
							if i == fastxReader.ChunkSize {
								fastxReader.Ch <- RecordChunk{id, chunkData[0:i], nil}
								id++
								i = 0
								chunkData = make([]*Record, fastxReader.ChunkSize)
							}

							lastName = thisName
						} else { // first sequence name
							lastName = thisName
						}
					}
				} else if line[0] == '+' && len(dropCR(line[0:len(line)-1])) == 1 {
					//lastSeq = []byte(string(buffer.Bytes()))

					lastSeq = make([]byte, buffer.Len())
					copy(lastSeq, buffer.Bytes())

					lastSeq = buffer.Bytes()

					buffer.Reset()
					isReadQual = true
				} else if hasSeq { // append sequence / qual
					buffer.Write(dropCR(line[0 : len(line)-1]))
				} else {
					// some line before the first "^>"
				}

				continue
			}

			// FASTA
			if err != nil { // end of file
				fastxReader.finished = true
				fastxReader.fh.Close()

				buffer.Write(dropCR(line))

				// sequence := []byte(string(buffer.Bytes()))

				sequence := make([]byte, buffer.Len())
				copy(sequence, buffer.Bytes())

				// sequence := buffer.Bytes()

				buffer.Reset()

				if !(len(sequence) == 0 && len(lastName) == 0) {
					if fastxReader.firstseq {
						if fastxReader.t == nil {
							fastxReader.t = seq.GuessAlphabetLessConservatively(sequence)
						}
						fastxReader.firstseq = false
					}
					fastxRecord, err := NewRecord(fastxReader.t, fastxReader.parseHeadID(lastName), lastName, sequence)
					if err != nil {
						fastxReader.Ch <- RecordChunk{id, chunkData[0:i], err}
						fastxReader.fh.Close()
						close(fastxReader.Ch)
						return
					}
					chunkData[i] = fastxRecord
					i++
				}
				fastxReader.Ch <- RecordChunk{id, chunkData[0:i], nil}
				close(fastxReader.Ch)

				return
			}

			if line[0] == '>' {
				hasSeq = true
				thisName = dropCR(line[1 : len(line)-1])
				if lastName != nil { // no-first seq head
					// sequence := []byte(string(buffer.Bytes()))

					sequence := make([]byte, buffer.Len())
					copy(sequence, buffer.Bytes())

					// sequence := buffer.Bytes()

					buffer.Reset()

					if !(len(sequence) == 0 && len(lastName) == 0) {
						if fastxReader.firstseq {
							if fastxReader.t == nil {
								fastxReader.t = seq.GuessAlphabetLessConservatively(sequence)
							}
							fastxReader.firstseq = false
						}
						fastxRecord, err := NewRecord(fastxReader.t, fastxReader.parseHeadID(lastName), lastName, sequence)
						if err != nil {
							fastxReader.Ch <- RecordChunk{id, chunkData[0:i], err}
							fastxReader.fh.Close()
							close(fastxReader.Ch)
							return
						}

						chunkData[i] = fastxRecord
						i++
					}
					if i == fastxReader.ChunkSize {
						fastxReader.Ch <- RecordChunk{id, chunkData[0:i], nil}
						id++
						i = 0
						chunkData = make([]*Record, fastxReader.ChunkSize)
					}

					lastName = thisName
				} else { // first sequence name
					lastName = thisName
				}
			} else if hasSeq { // append sequence
				buffer.Write(dropCR(line[0 : len(line)-1]))
			} else {
				// some line before the first "^>"
			}
		}
	}()
}

func (fastxReader *Reader) parseHeadID(head []byte) []byte {
	return ParseHeadID(fastxReader.IDRegexp, head)
}

// ParseHeadID parse ID from head by IDRegexp
func ParseHeadID(idRegexp *regexp.Regexp, head []byte) []byte {
	found := idRegexp.FindAllSubmatch(head, -1)
	if found == nil { // not match
		return head
	}
	return found[0][1]
}

// Cancel method cancel the reading process
func (fastxReader *Reader) Cancel() {
	if !fastxReader.finished && !fastxReader.cancelled {
		close(fastxReader.done)
		fastxReader.cancelled = true
	}
}

// Alphabet returns Alphabet of the file
func (fastxReader *Reader) Alphabet() *seq.Alphabet {
	return fastxReader.t
}

func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

func cleanSpace(slice []byte) []byte {
	newSlice := []byte{}
	for _, b := range slice {
		switch b {
		case '\r', '\n', ' ', '\t':
		default:
			newSlice = append(newSlice, b)
		}
	}
	return newSlice
}

func cleanEndSpace(slice []byte) []byte {
	l := len(slice)
	// newSlice := []byte(string(slice))
	newSlice := make([]byte, len(slice))
	copy(newSlice, slice)

	if slice[l-1] == '\n' {
		newSlice = slice[0 : l-1]
	}
	l = len(newSlice)
	if newSlice[l-1] == '\r' {
		newSlice = newSlice[0 : l-1]
	}
	return newSlice
}