package ldvclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A local durable outbox, and the reason it exists.
//
// Withholding a response is not the same as preventing a processing. By the
// time a record can fail to reach the logbook, the source has been queried,
// the BSN resolved, the consent written — the Dataverwerking happened. Failing
// the request afterwards denies the caller its answer; it does not un-process
// anything, and it leaves LDV's actual requirement unmet, because the
// processing is now unlogged.
//
// So the record is made durable locally first, and delivered afterwards. What
// the producer needs guaranteed is "this will reach the logbook", not "the
// logbook has it already". That is a weaker promise about timing and a
// stronger one about completeness, which is the trade LDV asks for: no
// sampling, nothing dropped.
//
// It also removes the logbook from the critical path. A logbook outage no
// longer stops the chain, which matters because fail-closed made every
// component's availability depend on it.
//
// The format is JSON lines with an fsync per append, and a cursor file holding
// the offset of the first undelivered record. A crash between delivery and
// cursor advance replays the record; the logbook keys on (trace_id, span_id)
// and reports a replay as a duplicate, so replaying is safe by construction.
type Outbox struct {
	mutex  sync.Mutex
	file   *os.File
	path   string
	cursor string
	client *Client
}

// OpenOutbox prepares the spool at path. The directory is created if needed,
// so a fresh volume needs no init step.
func OpenOutbox(path string, client *Client) (*Outbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create outbox directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open outbox: %w", err)
	}
	return &Outbox{file: file, path: path, cursor: path + ".cursor", client: client}, nil
}

func (o *Outbox) Close() error { return o.file.Close() }

// Append makes one record durable. It returns only after the bytes are on the
// disk, so a caller that gets nil may proceed knowing the record cannot now be
// lost — which is the whole point.
func (o *Outbox) Append(record Record) error {
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if _, err := o.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append to outbox: %w", err)
	}
	// Without the sync this is a buffer, not a spool: a power loss would take
	// records the producer was told were safe.
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("sync outbox: %w", err)
	}
	return nil
}

// Pending reports how many records are waiting. Used at startup to say so, and
// by tests.
func (o *Outbox) Pending(ctx context.Context) (int, error) {
	records, _, err := o.undelivered()
	return len(records), err
}

// undelivered reads the records after the cursor, with the offset each one
// ends at, so the cursor can advance per record rather than per batch.
func (o *Outbox) undelivered() ([]Record, []int64, error) {
	offset := o.readCursor()
	file, err := os.Open(o.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, nil, err
	}

	var records []Record
	var offsets []int64
	position := offset
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxRecordBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		position += int64(len(line)) + 1
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			// A line we cannot parse can never be delivered, so skipping it is
			// the only way forward — but it is a lost Dataverwerking and says
			// so loudly rather than disappearing.
			slog.Error("undeliverable record in outbox; skipping", "offset", position, "err", err.Error())
			offsets = append(offsets, position)
			records = append(records, Record{})
			continue
		}
		records = append(records, record)
		offsets = append(offsets, position)
	}
	return records, offsets, scanner.Err()
}

// maxRecordBytes bounds one spooled line.
const maxRecordBytes = 256 << 10

// Deliver sends everything waiting, oldest first, and advances the cursor per
// record. It stops at the first failure and keeps the cursor where it was, so
// order is preserved and nothing is skipped.
func (o *Outbox) Deliver(ctx context.Context) error {
	records, offsets, err := o.undelivered()
	if err != nil {
		return fmt.Errorf("read outbox: %w", err)
	}
	for index, record := range records {
		if record.SpanID == "" {
			// The unparseable line logged above; advance past it.
			if err := o.writeCursor(offsets[index]); err != nil {
				return err
			}
			continue
		}
		if err := o.client.post(ctx, record); err != nil {
			return err
		}
		if err := o.writeCursor(offsets[index]); err != nil {
			return err
		}
	}
	return nil
}

// Run delivers on an interval until the context ends, then makes a final
// attempt so a clean shutdown does not leave records waiting needlessly.
func (o *Outbox) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := o.Deliver(drainCtx); err != nil {
				slog.Warn("outbox not empty at shutdown; records will be delivered on restart", "err", err.Error())
			}
			return
		case <-ticker.C:
			if err := o.Deliver(ctx); err != nil {
				slog.Warn("outbox delivery failed; will retry", "err", err.Error())
			}
		}
	}
}

func (o *Outbox) readCursor() int64 {
	raw, err := os.ReadFile(o.cursor)
	if err != nil {
		return 0
	}
	var offset int64
	if _, err := fmt.Sscanf(string(raw), "%d", &offset); err != nil {
		return 0
	}
	return offset
}

// writeCursor advances the delivered mark. Written through a temporary file
// and renamed, so a crash mid-write leaves the old cursor rather than a
// truncated one — which would replay the whole spool.
func (o *Outbox) writeCursor(offset int64) error {
	temporary := o.cursor + ".tmp"
	if err := os.WriteFile(temporary, []byte(fmt.Sprintf("%d", offset)), 0o600); err != nil {
		return fmt.Errorf("write outbox cursor: %w", err)
	}
	if err := os.Rename(temporary, o.cursor); err != nil {
		return fmt.Errorf("advance outbox cursor: %w", err)
	}
	return nil
}
